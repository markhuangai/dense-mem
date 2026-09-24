package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	remembercontract "github.com/markhuangai/dense-mem/internal/remember/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

// rememberSynchronousProcessor owns the request-scoped Remember boundary:
// assessment, one embedding batch, and one terminal repository transaction.
type rememberSynchronousProcessor struct {
	ledger             rememberSynchronousLedger
	catalog            rememberapp.SubmissionAssessmentCatalog
	provider           assessor.Provider
	embedder           embeddingcontract.EmbeddingProviderInterface
	limits             assessor.SemanticAssessmentLimits
	metrics            observability.DiscoverabilityMetrics
	logger             observability.LogProvider
	protector          observability.DiagnosticProtector
	isStaleInput       func(error) bool
	commitFailureStage func(error) string
}

// Persistence is retained as a named alias for callers while the canonical
// Remember port lives in the contract package.
type Persistence = remembercontract.Persistence

type rememberSynchronousLedger = remembercontract.Persistence

// ProcessorDependencies contains only provider, policy, and persistence ports
// needed for one request-owned Remember execution.
type ProcessorDependencies struct {
	Ledger              Persistence
	Catalog             rememberapp.SubmissionAssessmentCatalog
	Assessor            assessor.Provider
	Embedder            embeddingcontract.EmbeddingProviderInterface
	Limits              assessor.SemanticAssessmentLimits
	Metrics             observability.DiscoverabilityMetrics
	Logger              observability.LogProvider
	DiagnosticProtector observability.DiagnosticProtector
	IsStaleInput        func(error) bool
	CommitFailureStage  func(error) string
}

type rememberSynchronousIdempotencyLocker interface {
	WithRememberAttemptLock(context.Context, string, string, string, func(waited bool) error) error
}

var _ rememberapp.SynchronousProcessor = (*rememberSynchronousProcessor)(nil)

// NewSynchronousProcessor constructs the sole request-owned Remember
// processor. It performs no storage or provider work until ProcessRemember.
func NewSynchronousProcessor(deps ProcessorDependencies) *rememberSynchronousProcessor {
	staleInput := deps.IsStaleInput
	if staleInput == nil {
		staleInput = rememberapp.IsRememberStaleInputError
	}
	return &rememberSynchronousProcessor{
		ledger: deps.Ledger, catalog: deps.Catalog, provider: deps.Assessor, embedder: deps.Embedder,
		limits: deps.Limits, metrics: deps.Metrics, logger: deps.Logger, protector: deps.DiagnosticProtector,
		isStaleInput: staleInput, commitFailureStage: deps.CommitFailureStage,
	}
}

func (p *rememberSynchronousProcessor) isRememberStaleInput(err error) bool {
	if rememberapp.IsRememberStaleInputError(err) {
		return true
	}
	if p != nil && p.isStaleInput != nil {
		return p.isStaleInput(err)
	}
	return false
}

func (p *rememberSynchronousProcessor) commitStage(err error) string {
	if p == nil || p.commitFailureStage == nil {
		return ""
	}
	return p.commitFailureStage(err)
}

func (p *rememberSynchronousProcessor) ProcessRemember(
	ctx context.Context,
	input rememberapp.RememberProcessRequest,
) (*rememberapp.SubmissionStatusResult, error) {
	if p == nil || p.ledger == nil {
		return nil, errors.New("remember processor: ledger is required")
	}
	if input.InvocationStartedAt.IsZero() {
		input.InvocationStartedAt = time.Now().UTC()
	}
	locker, hasLocker := p.ledger.(rememberSynchronousIdempotencyLocker)
	if !hasLocker {
		return p.processRememberUnlocked(ctx, input)
	}
	waiterInvocationID := uuid.NewString()
	requestctx.SetRememberInvocationID(ctx, waiterInvocationID)
	var ownerResult *rememberapp.SubmissionStatusResult
	var ownerErr error
	owner := false
	callbackEntered := false
	lockErr := locker.WithRememberAttemptLock(ctx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, func(waited bool) error {
		callbackEntered = true
		if waited {
			return nil
		}
		owner = true
		ownerResult, ownerErr = p.processRememberUnlocked(ctx, input)
		return ownerErr
	})
	if owner {
		if ownerErr != nil {
			return ownerResult, ownerErr
		}
		if lockErr != nil {
			if ownerResult != nil {
				p.logRememberIdempotencyLockCleanupFailure(input, ownerResult.SubmissionID, lockErr)
				return ownerResult, nil
			}
			return ownerResult, lockErr
		}
		return ownerResult, nil
	}
	if !callbackEntered && lockErr != nil {
		processErr := rememberPreLockProcessError(input, waiterInvocationID, lockErr)
		p.recordRememberInvocation(ctx, input, waiterInvocationID, "execution", "", "idempotency_lock", processErr, processErr.Status, nil)
		return processErr.Status, processErr
	}
	// A waiter must replay the owner's durable result, including a retryable
	// failure. It may retry only after the lock owner has returned and a later
	// call acquires the key.
	if errors.Is(lockErr, context.Canceled) || errors.Is(lockErr, context.DeadlineExceeded) {
		p.recordRememberInvocation(ctx, input, waiterInvocationID, "execution", "", "idempotency_wait", lockErr, nil, nil)
		return nil, lockErr
	}
	replay, replayErr := p.loadRememberReplay(ctx, input, waiterInvocationID)
	if replayErr == nil {
		p.recordRememberInvocation(ctx, input, waiterInvocationID, "replay", replay.SubmissionID, "idempotency_wait", nil, replay, nil)
		return replay, nil
	} else if processErr := new(rememberapp.RememberProcessError); errors.As(replayErr, &processErr) && processErr.Status != nil {
		classification := "replay"
		if errors.Is(replayErr, rememberapp.ErrRememberConflict) || errors.Is(replayErr, repository.ErrIdempotencyConflict) {
			classification = "conflict"
		}
		p.recordRememberInvocation(ctx, input, waiterInvocationID, classification, replayCanonicalAttemptID(processErr.Status, waiterInvocationID), "idempotency_wait", replayErr, processErr.Status, nil)
		return processErr.Status, replayErr
	}
	classification := "replay"
	if errors.Is(replayErr, rememberapp.ErrRememberConflict) || errors.Is(replayErr, repository.ErrIdempotencyConflict) {
		classification = "conflict"
	}
	p.recordRememberInvocation(ctx, input, waiterInvocationID, classification, "", "idempotency_wait", replayErr, nil, nil)
	return nil, replayErr
}

func (p *rememberSynchronousProcessor) processRememberUnlocked(
	ctx context.Context,
	input rememberapp.RememberProcessRequest,
) (*rememberapp.SubmissionStatusResult, error) {
	if p == nil || p.ledger == nil {
		return nil, errors.New("remember processor: ledger is required")
	}
	started := time.Now()
	exchangeRecorder := &rememberExchangeRecorder{protector: p.protector}
	ctx = modelprovider.WithExchangeRecorder(ctx, exchangeRecorder)
	ingestID := uuid.NewString()
	requestctx.SetRememberInvocationID(ctx, ingestID)
	snapshot, scope := rememberAssessmentSnapshot(input, ingestID)
	assessorTurns := 0
	recoveryAttempt := false
	fail := func(err error, phase string) (*rememberapp.SubmissionStatusResult, error) {
		status, canonicalAttemptID, processErr := p.recordRememberFailure(ctx, input, ingestID, snapshot, started, phase, assessorTurns, err)
		invocationStatus := status
		if invocationStatus == nil {
			var statusErr *rememberapp.RememberProcessError
			if errors.As(processErr, &statusErr) {
				invocationStatus = statusErr.Status
			}
		}
		classification := "execution"
		if recoveryAttempt {
			classification = "recovery"
		}
		if canonicalAttemptID != "" && canonicalAttemptID != ingestID {
			classification = "replay"
		}
		if errors.Is(processErr, rememberapp.ErrRememberConflict) || errors.Is(processErr, repository.ErrIdempotencyConflict) {
			classification = "conflict"
		}
		p.recordRememberInvocation(ctx, input, ingestID, classification, canonicalAttemptID, phase, processErrOrCause(processErr, err), invocationStatus, exchangeRecorder.Snapshot(), recoveryAttempt)
		return status, processErr
	}
	attempt, lookupErr := p.ledger.LoadRememberAttempt(ctx, repository.RememberAttemptLookupInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey,
	})
	if lookupErr == nil && attempt != nil {
		if !rememberAttemptMatchesRequest(attempt, input) {
			processErr := rememberConflictProcessError(input, ingestID, rememberapp.ErrRememberConflict)
			p.recordRememberInvocation(ctx, input, ingestID, "conflict", attempt.AttemptID, "idempotency", processErr, processErr.Status, exchangeRecorder.Snapshot())
			return nil, processErr
		}
		if version := strings.TrimSpace(attempt.ContractVersion); version != "" && !domain.ContractVersionCompatible(version) {
			processErr := rememberConflictProcessError(input, ingestID, rememberapp.ErrRememberConflict)
			p.recordRememberInvocation(ctx, input, ingestID, "conflict", attempt.AttemptID, "idempotency", processErr, processErr.Status, exchangeRecorder.Snapshot())
			return nil, processErr
		}
		if attempt.Outcome == "completed" || (attempt.Outcome == "failed" && !attempt.Retryable) {
			replay, replayErr := rememberAttemptStatusForRequest(attempt, input)
			if replayErr != nil {
				classification := "replay"
				if errors.Is(replayErr, rememberapp.ErrRememberConflict) || errors.Is(replayErr, repository.ErrIdempotencyConflict) {
					classification = "conflict"
				}
				p.recordRememberInvocation(ctx, input, ingestID, classification, attempt.AttemptID, "replay", replayErr, nil, exchangeRecorder.Snapshot())
				return nil, replayErr
			}
			if attempt.Outcome == "failed" {
				processErr := &rememberapp.RememberProcessError{Status: replay, Err: rememberapp.ErrRememberPersistence}
				p.recordRememberInvocation(ctx, input, ingestID, "replay", attempt.AttemptID, "replay", processErr, replay, exchangeRecorder.Snapshot())
				return nil, processErr
			}
			p.recordRememberInvocation(ctx, input, ingestID, "replay", attempt.AttemptID, "replay", nil, replay, exchangeRecorder.Snapshot())
			return replay, nil
		}
		if attempt.Outcome != "failed" {
			processErr := rememberConflictProcessError(input, ingestID, rememberapp.ErrRememberConflict)
			p.recordRememberInvocation(ctx, input, ingestID, "conflict", attempt.AttemptID, "idempotency", processErr, processErr.Status, exchangeRecorder.Snapshot())
			return nil, processErr
		}
		recoveryAttempt = true
		observability.RecordLogicalRecovery(p.metrics, "remember", "attempted")
	} else if lookupErr != nil && !errors.Is(lookupErr, repository.ErrRememberAttemptNotFound) {
		return fail(lookupErr, "commit")
	}
	if input.SecurityRejected {
		return fail(rememberapp.ErrRememberPolicyRejected, "assessment")
	}
	duplicateInput := repository.RememberDuplicateCandidateInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
		Evidence: rememberEvidenceInputsForCommit(input, snapshot),
	}
	embeddingStarted := time.Now()
	duplicateEmbeddingCtx, duplicateEmbeddingCancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseEmbedding)
	duplicatePlan, err := p.ledger.PlanRememberDuplicateEmbeddings(duplicateEmbeddingCtx, duplicateInput)
	if err != nil {
		duplicateEmbeddingCancel()
		observability.RecordRememberPhase(p.metrics, "embedding", rememberMetricPhaseOutcome(err), time.Since(embeddingStarted))
		return fail(&rememberEmbeddingPlanFailure{cause: err}, "embedding")
	}
	duplicateDocuments, err := p.embedSearchDocumentBatch(
		duplicateEmbeddingCtx, input.TeamID, input.OwnerProfileID,
		duplicatePlan.EmbeddingModel, duplicatePlan.Documents,
	)
	duplicateEmbeddingCancel()
	if err != nil {
		observability.RecordRememberPhase(p.metrics, "embedding", rememberMetricPhaseOutcome(err), time.Since(embeddingStarted))
		return fail(err, "embedding")
	}
	duplicateEmbeddings := inlineEmbeddingResultsFromDuplicateDocuments(duplicateDocuments, duplicatePlan)
	duplicateResolution, err := p.ledger.ResolveRememberDuplicateCandidates(ctx, duplicateInput, duplicateEmbeddings)
	observability.RecordRememberPhase(p.metrics, "embedding", rememberMetricPhaseOutcome(err), time.Since(embeddingStarted))
	if err != nil {
		return fail(&rememberEmbeddingPlanFailure{cause: err}, "embedding")
	}
	snapshot.DuplicateCandidates = append([]repository.RememberDuplicateCandidateGroup(nil), duplicateResolution.Candidates...)
	snapshot.ExactDuplicateEvidence = make(map[int]repository.RememberDuplicateResolution, len(duplicateResolution.Exact))
	for index, resolution := range duplicateResolution.Exact {
		if resolution.Disposition == "reuse" {
			snapshot.ExactDuplicateEvidence[index] = resolution
		}
	}
	assessmentStarted := time.Now()
	prepared, err := rememberapp.AssessSynchronousRemember(ctx, rememberapp.SynchronousAssessmentDependencies{
		Catalog: p.catalog, Provider: p.provider, Limits: p.limits, Metrics: p.metrics, Logger: p.logger,
	}, rememberapp.SynchronousAssessmentInput{Scope: scope, Snapshot: snapshot})
	observability.RecordRememberPhase(p.metrics, "assessment", rememberMetricPhaseOutcome(err), time.Since(assessmentStarted))
	if err != nil {
		assessorTurns = rememberapp.SynchronousAssessmentProviderTurns(err)
		return fail(err, "assessment")
	}
	assessorTurns = prepared.Assessment.ProviderTurns
	commitInput, buildErr := rememberapp.BuildSynchronousRememberCommitInput(rememberapp.SynchronousRememberCommitRequest{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
		RequestHash:   input.RequestHash,
		SourceSummary: input.SourceSummary, Proposal: input.Proposal,
		Metadata: input.Metadata, Evidence: rememberEvidenceInputsForCommit(input, snapshot), Assessment: prepared,
		Duration: time.Since(started),
	})
	commitInput.StartedAt = started
	if buildErr != nil {
		if p.isRememberStaleInput(buildErr) {
			return fail(newRememberStaleInputError(buildErr), "assessment")
		}
		return fail(buildErr, "assessment")
	}
	if input.SecurityRejected || rememberAssessmentSecurityRejected(prepared) {
		input.AssessorSecurityRejected = true
		return fail(rememberapp.ErrRememberPolicyRejected, "assessment")
	}
	embeddingStarted = time.Now()
	embeddingCtx, embeddingCancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseEmbedding)
	defer embeddingCancel()
	plan, err := p.ledger.PlanRememberEmbeddings(embeddingCtx, commitInput)
	if err != nil {
		observability.RecordRememberPhase(p.metrics, "embedding", rememberMetricPhaseOutcome(err), time.Since(embeddingStarted))
		return fail(&rememberEmbeddingPlanFailure{cause: err}, "embedding")
	}
	plannedEmbeddings, err := p.embedSearchDocumentBatch(
		embeddingCtx,
		input.TeamID,
		input.OwnerProfileID,
		plan.EmbeddingModel,
		plan.Documents,
	)
	observability.RecordRememberPhase(p.metrics, "embedding", rememberMetricPhaseOutcome(err), time.Since(embeddingStarted))
	if err != nil {
		return fail(err, "embedding")
	}
	inlineEmbeddings := inlineEmbeddingResultsFromDocuments(plannedEmbeddings, plan)
	inlineEmbeddings = mergeInlineEmbeddingResults(duplicateEmbeddings, inlineEmbeddings)
	commitStarted := time.Now()
	commitCtx, commitCancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseCommit)
	if err := commitCtx.Err(); err != nil {
		commitCancel()
		observability.RecordRememberPhase(p.metrics, "commit", rememberMetricPhaseOutcome(err), time.Since(commitStarted))
		return fail(err, "commit")
	}
	defer commitCancel()
	committed, err := p.ledger.CommitRememberWithEmbeddings(commitCtx, commitInput, inlineEmbeddings)
	commitMetricErr := err
	if errors.Is(commitMetricErr, repository.ErrRememberReplay) {
		commitMetricErr = nil
	}
	observability.RecordRememberPhase(p.metrics, "commit", rememberMetricPhaseOutcome(commitMetricErr), time.Since(commitStarted))
	if errors.Is(err, repository.ErrRememberReplay) {
		replay, replayErr := p.loadRememberReplay(ctx, input, ingestID)
		canonicalAttemptID := ""
		if replay != nil {
			canonicalAttemptID = replay.SubmissionID
		}
		invocationStatus := replay
		if invocationStatus == nil {
			var processErr *rememberapp.RememberProcessError
			if errors.As(replayErr, &processErr) {
				invocationStatus = processErr.Status
				if canonicalAttemptID == "" {
					canonicalAttemptID = replayCanonicalAttemptID(processErr.Status, ingestID)
				}
			}
		}
		classification := "replay"
		if errors.Is(replayErr, rememberapp.ErrRememberConflict) || errors.Is(replayErr, repository.ErrIdempotencyConflict) {
			classification = "conflict"
		}
		p.recordRememberInvocation(ctx, input, ingestID, classification, canonicalAttemptID, "commit", replayErr, invocationStatus, exchangeRecorder.Snapshot(), recoveryAttempt)
		return replay, replayErr
	}
	if err != nil {
		return fail(normalizeRememberCommitFailure(err), "commit")
	}
	if committed == nil {
		return fail(errors.New("remember processor: nil Remember commit result"), "commit")
	}
	result, resultErr := rememberAttemptStatusForRequest(&repository.RememberAttempt{AttemptID: committed.IngestID, Outcome: committed.Outcome, PublicResult: committed.PublicResult}, input)
	classification := "execution"
	if recoveryAttempt {
		classification = "recovery"
	}
	p.recordRememberInvocation(ctx, input, committed.IngestID, classification, committed.IngestID, "commit", resultErr, result, exchangeRecorder.Snapshot())
	return result, resultErr
}

func rememberAssessmentSecurityRejected(prepared *rememberapp.SynchronousAssessmentResult) bool {
	if prepared == nil {
		return false
	}
	for _, result := range prepared.Response.EvidenceSecurityResults {
		if strings.EqualFold(strings.TrimSpace(result.Decision), "reject") {
			return true
		}
	}
	return false
}

func rememberMetricPhaseOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, rememberapp.ErrRememberRequestCancelled) || errors.Is(err, rememberapp.ErrRememberRequestTimeout) {
		return "cancelled"
	}
	return "failed"
}

func rememberAttemptMatchesRequest(attempt *repository.RememberAttempt, input rememberapp.RememberProcessRequest) bool {
	return attempt != nil && strings.TrimSpace(attempt.RequestHash) == strings.TrimSpace(input.RequestHash)
}

func (p *rememberSynchronousProcessor) recordRememberFailure(
	ctx context.Context,
	input rememberapp.RememberProcessRequest,
	attemptID string,
	snapshot rememberapp.RememberAssessmentSnapshot,
	started time.Time,
	phase string,
	assessorTurns int,
	failure error,
) (*rememberapp.SubmissionStatusResult, string, error) {
	if failure == nil {
		failure = errors.New("remember execution failed")
	}
	failure = p.normalizeRememberFailure(failure)
	if errors.Is(failure, rememberapp.ErrRememberConflict) || errors.Is(failure, repository.ErrIdempotencyConflict) {
		return nil, "", rememberConflictProcessError(input, attemptID, failure)
	}
	code := rememberFailureCode(phase, failure)
	reasonCode, details := rememberapp.SynchronousAssessmentFailureDetails(failure)
	if reasonCode == "" {
		reasonCode = "remember_" + strings.TrimSpace(phase) + "_failed"
		details = map[string]any{"component": "remember." + strings.TrimSpace(phase), "server_owned": true}
	}
	publicError := rememberapp.TerminalStatusErrorWithDetails(rememberapp.TerminalErrorCode(code), reasonCode, details)
	correlationID := rememberProcessCorrelationID(input.Metadata)
	processingState := "failed"
	notStoredReason := rememberFailureNotStoredReason(code)
	evidence, relationshipResults := rememberFailureResults(input, notStoredReason)
	status := &rememberapp.SubmissionStatusResult{
		ContractVersion: domain.ContractVersion, SubmissionID: attemptID, SubmissionKind: "remember",
		ProcessingState: processingState, SearchState: "not_required", CorrelationID: correlationID,
		Evidence: evidence, RelationshipResults: relationshipResults,
		Errors: []rememberapp.SubmissionStatusError{publicError},
	}
	publicResult, terminalResult := terminalRememberFailureResult(status)
	var exchanges []modelprovider.ProviderExchange
	if recorder, ok := modelprovider.ExchangeRecorderFromContext(ctx).(*rememberExchangeRecorder); ok {
		exchanges = recorder.Snapshot()
	}
	var callerResponse []byte
	callerResponseCaptureAvailable := false
	if capture := rememberapp.DiagnosticCaptureFromContext(ctx); capture != nil {
		callerResponseCaptureAvailable = true
		callerResponse, _ = capture.ProjectResponse(publicResult, true)
	}
	diagnostics := rememberFailureDiagnosticsWithAuthenticationSecrets(input, publicResult, exchanges, callerResponse, rememberCallerResponseDelivered(ctx, failure), callerResponseCaptureAvailable, observability.AuthenticationSecretsFromContext(ctx), p.protector)
	assessorValidation := rememberapp.SynchronousAssessmentValidationDiagnostics(failure)
	recoveryCtx, cancel := rememberFailureRecoveryContext(ctx)
	defer cancel()
	recordErr := p.ledger.RecordRememberFailure(recoveryCtx, repository.RememberFailureRecordInput{
		Attempt: repository.RememberAttemptRecordInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, AttemptID: attemptID,
			SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
			RequestHash:     input.RequestHash,
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember",
			FailedPhase: phase, ErrorCode: publicError.Code, Retryable: publicError.Retryable, RetryabilitySet: true, CorrelationID: correlationID, PublicResult: publicResult,
			EvidenceCount: len(input.Evidence), AssessorTurns: assessorTurns, Duration: time.Since(started),
			AssessorValidation: assessorValidation,
		},
		Diagnostics: diagnostics,
	})
	if recordErr != nil {
		if errors.Is(recordErr, repository.ErrRememberFailureRetentionDegraded) {
			p.logRememberFailure(ctx, input, attemptID, started, phase, publicError.Code, correlationID, assessorTurns, failure)
			p.logRememberFailureRetentionDegraded(input, attemptID, phase, recordErr)
			return nil, attemptID, &rememberapp.RememberProcessError{Status: status, Result: terminalResult, Err: failure}
		}
		if errors.Is(recordErr, repository.ErrRememberReplay) {
			winner, loadErr := p.ledger.LoadRememberAttempt(recoveryCtx, repository.RememberAttemptLookupInput{
				TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey,
			})
			if loadErr != nil {
				return nil, "", loadErr
			}
			canonicalAttemptID := ""
			if winner != nil {
				canonicalAttemptID = winner.AttemptID
			}
			replay, replayErr := rememberAttemptReplay(winner, input)
			return replay, canonicalAttemptID, replayErr
		}
		if errors.Is(recordErr, repository.ErrIdempotencyConflict) {
			return nil, "", rememberConflictProcessError(input, attemptID, errors.Join(rememberapp.ErrRememberConflict, recordErr))
		}
		p.logRememberFailure(ctx, input, attemptID, started, phase, publicError.Code, correlationID, assessorTurns, failure)
		p.logRememberFailureRecordError(ctx, input, attemptID, phase, publicError.Code, correlationID, recordErr)
		return nil, "", rememberFailurePersistenceProcessError(input, attemptID, failure)
	}
	p.logRememberFailure(ctx, input, attemptID, started, phase, publicError.Code, correlationID, assessorTurns, failure)
	return nil, attemptID, &rememberapp.RememberProcessError{Status: status, Result: terminalResult, Err: failure}
}

func normalizeRememberFailure(failure error) error {
	return normalizeRememberFailureWithClassifier(failure, nil)
}

func (p *rememberSynchronousProcessor) normalizeRememberFailure(failure error) error {
	return normalizeRememberFailureWithClassifier(failure, p.isRememberStaleInput)
}

type rememberStaleInputError struct {
	cause error
}

func newRememberStaleInputError(cause error) error {
	if cause == nil {
		return rememberapp.ErrRememberStaleInput
	}
	return &rememberStaleInputError{cause: cause}
}

func (e *rememberStaleInputError) Error() string {
	return "remember: stale input"
}

func (e *rememberStaleInputError) Unwrap() []error {
	if e == nil || e.cause == nil {
		return []error{rememberapp.ErrRememberStaleInput}
	}
	return []error{rememberapp.ErrRememberStaleInput, e.cause}
}

func normalizeRememberFailureWithClassifier(failure error, isStaleInput func(error) bool) error {
	if failure == nil || errors.Is(failure, rememberapp.ErrRememberStaleInput) {
		return failure
	}
	if errors.Is(failure, rememberapp.ErrSourceRevisionConflict) ||
		errors.Is(failure, repository.ErrRememberDuplicateCandidateStale) ||
		errors.Is(failure, repository.ErrEvidenceConflictStaleInput) ||
		rememberapp.IsRememberStaleInputError(failure) ||
		(isStaleInput != nil && isStaleInput(failure)) {
		return newRememberStaleInputError(failure)
	}
	return failure
}

func normalizeRememberCommitFailure(failure error) error {
	if errors.Is(failure, repository.ErrSearchStaleVersion) || errors.Is(failure, repository.ErrSearchContractMismatch) {
		return fmt.Errorf("%w: search state changed before commit", rememberapp.ErrRememberCommitConflict)
	}
	return failure
}

func rememberFailurePersistenceError(failure error) error {
	if failure == nil {
		return rememberapp.ErrRememberPersistence
	}
	return fmt.Errorf("%w: terminal failure record unavailable: %w", rememberapp.ErrRememberPersistence, failure)
}

func rememberFailurePersistenceProcessError(
	input rememberapp.RememberProcessRequest,
	submissionID string,
	cause error,
) *rememberapp.RememberProcessError {
	return rememberFailureProcessErrorWithStatus(
		input, submissionID, cause, rememberapp.TerminalErrorDatabaseFailure,
		"failure_retention", "remember.failure_record",
	)
}

func rememberFailureProcessErrorWithStatus(
	input rememberapp.RememberProcessRequest,
	submissionID string,
	cause error,
	code rememberapp.TerminalErrorCode,
	reasonCode string,
	component string,
) *rememberapp.RememberProcessError {
	evidence, relationshipResults := rememberFailureResults(input, "internal_failure")
	status := &rememberapp.SubmissionStatusResult{
		ContractVersion: domain.ContractVersion, SubmissionID: submissionID, SubmissionKind: "remember",
		ProcessingState: "failed", SearchState: "not_required", CorrelationID: rememberProcessCorrelationID(input.Metadata),
		Evidence: evidence, RelationshipResults: relationshipResults,
		Errors: []rememberapp.SubmissionStatusError{rememberapp.TerminalStatusErrorWithDetails(code, reasonCode, map[string]any{"component": component, "server_owned": true})},
	}
	return &rememberapp.RememberProcessError{Status: status, Err: rememberFailurePersistenceError(cause)}
}

func (p *rememberSynchronousProcessor) logRememberFailure(
	ctx context.Context,
	input rememberapp.RememberProcessRequest,
	attemptID string,
	started time.Time,
	phase string,
	errorCode string,
	correlationID string,
	assessorTurns int,
	failure error,
) {
	if p == nil || p.logger == nil {
		return
	}
	logError := failure
	if logError == nil {
		logError = errors.New("remember processing failed")
	}
	attrs := rememberFailureLogAttrs(input, attemptID, phase, errorCode, correlationID)
	attrs = append(attrs, observability.Int("duration_ms", int(time.Since(started)/time.Millisecond)))
	attrs = append(attrs, observability.Int("assessor_turns", clampAssessorTurns(assessorTurns)))
	if validation := rememberapp.SynchronousAssessmentValidationDiagnostics(failure); validation != nil {
		attrs = append(attrs, observability.LogAttr{Key: "assessor_validation", Value: validation})
	}
	var planFailure *rememberEmbeddingPlanFailure
	var configurationFailure *rememberEmbeddingConfigurationFailure
	var providerFailure *rememberEmbeddingProviderFailure
	switch {
	case errors.As(failure, &planFailure):
		logError = planFailure.cause
		failureClass, failureCode := rememberEmbeddingPlanFailureMetadata(planFailure.cause)
		attrs = append(attrs,
			observability.String("failure_source", "embedding_plan"),
			observability.String("failure_class", failureClass),
			observability.String("failure_code", failureCode),
		)
	case errors.As(failure, &configurationFailure):
		logError = failure
		attrs = append(attrs,
			observability.String("failure_source", "provider_configuration"),
			observability.String("failure_class", "configuration"),
			observability.String("failure_code", "embedding_provider_not_configured"),
		)
	case errors.As(failure, &providerFailure) && providerFailure.cause != nil:
		logError = providerFailure.cause
		metadata := embeddingcontract.ClassifyFailure(providerFailure.cause)
		attrs = append(attrs,
			observability.String("failure_source", "provider_call"),
			observability.String("failure_class", metadata.Class),
			observability.String("failure_code", metadata.Code),
		)
		if metadata.StatusCode > 0 {
			attrs = append(attrs, observability.Int("provider_status_code", metadata.StatusCode))
		}
	case phase == "commit":
		logError = failure
		failureClass, failureCode := rememberCommitFailureMetadata(failure)
		attrs = append(attrs,
			observability.String("failure_source", "semantic_commit"),
			observability.String("failure_class", failureClass),
			observability.String("failure_code", failureCode),
		)
		if stage := p.commitStage(failure); stage != "" {
			attrs = append(attrs, observability.String("commit_stage", stage))
		}
	}
	if contextual, ok := p.logger.(observability.ContextLogProvider); ok {
		contextual.ErrorContext(ctx, "remember_processing_failed", logError, attrs...)
		return
	}
	p.logger.Error("remember_processing_failed", errors.New("[diagnostic unavailable]"), attrs...)
}

func clampAssessorTurns(value int) int {
	if value < 0 {
		return 0
	}
	if value > rememberapp.SemanticMaxAssessorTurns {
		return rememberapp.SemanticMaxAssessorTurns
	}
	return value
}

func rememberCommitOperationalLogError(err error, stageFn func(error) string) error {
	stage := ""
	if stageFn != nil {
		stage = stageFn(err)
	}
	operation := "remember semantic commit"
	if stage != "" {
		operation += " at " + stage
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s timed out: %w", operation, context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s was cancelled: %w", operation, context.Canceled)
	default:
		return errors.New(operation + " failed")
	}
}

func rememberEmbeddingPlanFailureMetadata(err error) (string, string) {
	switch {
	case errors.Is(err, repository.ErrSearchContractMismatch):
		return "configuration", "search_contract_mismatch"
	case errors.Is(err, repository.ErrInlineEmbeddingPlanMismatch):
		return "data_contract", "embedding_plan_mismatch"
	case errors.Is(err, repository.ErrInlineEmbeddingPlanTooLarge):
		return "input_budget", "embedding_plan_too_large"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "embedding_plan_timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled", "embedding_plan_cancelled"
	default:
		return "internal", "embedding_plan_failed"
	}
}

func rememberCommitFailureMetadata(err error) (string, string) {
	switch {
	case errors.Is(err, rememberapp.ErrRememberCommitConflict):
		return "fence_conflict", "search_state_changed"
	case errors.Is(err, repository.ErrInlineEmbeddingPlanMismatch):
		return "data_contract", "embedding_plan_mismatch"
	case errors.Is(err, repository.ErrSearchContractMismatch):
		return "fence_conflict", "search_contract_changed"
	case errors.Is(err, repository.ErrSearchStaleVersion):
		return "fence_conflict", "search_document_stale"
	case errors.Is(err, rememberapp.ErrRememberStaleInput),
		errors.Is(err, repository.ErrRememberDuplicateCandidateStale),
		errors.Is(err, repository.ErrSourceRevisionConflict),
		errors.Is(err, repository.ErrEvidenceConflictStaleInput):
		return "stale_input", "source_state_changed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "semantic_commit_timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled", "semantic_commit_cancelled"
	default:
		return "database", "semantic_commit_failed"
	}
}

func rememberFailureCode(phase string, err error) rememberapp.SubmissionErrorCode {
	if errors.Is(err, rememberapp.ErrRememberPolicyRejected) || errors.Is(err, rememberapp.ErrEvidenceSecurityRejected) || errors.Is(err, rememberapp.ErrEncodedEvidenceNotAllowed) {
		return rememberapp.SubmissionErrorPolicyRejected
	}
	if errors.Is(err, rememberapp.ErrRememberRequestTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return rememberapp.SubmissionErrorRequestTimeout
	}
	if errors.Is(err, rememberapp.ErrRememberRequestCancelled) || errors.Is(err, context.Canceled) {
		return rememberapp.SubmissionErrorRequestCancelled
	}
	if errors.Is(err, rememberapp.ErrRememberDatabaseFailure) {
		return rememberapp.SubmissionErrorDatabaseFailure
	}
	var planFailure *rememberEmbeddingPlanFailure
	if errors.As(err, &planFailure) {
		switch {
		case errors.Is(planFailure.cause, repository.ErrInlineEmbeddingPlanTooLarge):
			return rememberapp.SubmissionErrorInputBudgetExceeded
		case errors.Is(planFailure.cause, repository.ErrSearchContractMismatch):
			return rememberapp.SubmissionErrorConfigurationInvalid
		case errors.Is(planFailure.cause, repository.ErrInlineEmbeddingPlanMismatch):
			return rememberapp.SubmissionErrorInternalFailure
		default:
			return rememberapp.SubmissionErrorDatabaseFailure
		}
	}
	var configurationFailure *rememberEmbeddingConfigurationFailure
	if errors.As(err, &configurationFailure) {
		return rememberapp.SubmissionErrorConfigurationInvalid
	}
	var providerFailure *rememberEmbeddingProviderFailure
	if errors.As(err, &providerFailure) {
		if metadata := embeddingcontract.ClassifyFailure(providerFailure.cause); metadata.Code == "provider_response_invalid" {
			return rememberapp.SubmissionErrorEmbeddingResponseInvalid
		}
	}
	if errors.Is(err, rememberapp.ErrRememberEmbeddingUnavailable) {
		return rememberapp.SubmissionErrorEmbeddingUnavailable
	}
	if errors.Is(err, rememberapp.ErrRememberEmbeddingInvalid) {
		return rememberapp.SubmissionErrorEmbeddingResponseInvalid
	}
	if errors.Is(err, rememberapp.ErrRememberProviderUnavailable) {
		return rememberapp.SubmissionErrorProviderUnavailable
	}
	if errors.Is(err, rememberapp.ErrRememberProviderResponseInvalid) {
		return rememberapp.SubmissionErrorProviderResponseInvalid
	}
	if errors.Is(err, rememberapp.ErrRememberInputBudgetExceeded) {
		return rememberapp.SubmissionErrorInputBudgetExceeded
	}
	if errors.Is(err, rememberapp.ErrRememberCommitConflict) || errors.Is(err, repository.ErrSearchStaleVersion) {
		return rememberapp.SubmissionErrorCommitConflict
	}
	if errors.Is(err, rememberapp.ErrRememberStaleInput) ||
		errors.Is(err, repository.ErrRememberDuplicateCandidateStale) ||
		errors.Is(err, repository.ErrSourceRevisionConflict) ||
		errors.Is(err, repository.ErrEvidenceConflictStaleInput) ||
		errors.Is(err, rememberapp.ErrSourceRevisionConflict) {
		return rememberapp.SubmissionErrorStaleInput
	}
	if phase == "assessment" {
		return rememberapp.SubmissionErrorProviderUnavailable
	}
	if phase == "embedding" {
		return rememberapp.SubmissionErrorEmbeddingUnavailable
	}
	return rememberapp.SubmissionErrorDatabaseFailure
}

func rememberAttemptStatus(attempt *repository.RememberAttempt) (*rememberapp.SubmissionStatusResult, error) {
	if attempt == nil {
		return nil, errors.New("remember processor: attempt is required")
	}
	encoded, err := json.Marshal(attempt.PublicResult)
	if err != nil {
		return nil, err
	}
	var replay rememberapp.SubmissionStatusResult
	if err := json.Unmarshal(encoded, &replay); err != nil {
		return nil, err
	}
	replay.SubmissionID = firstNonEmptyString(replay.SubmissionID, attempt.AttemptID)
	if replay.ContractVersion == "" {
		replay.ContractVersion = domain.ContractVersion
	}
	if replay.SubmissionKind == "" {
		replay.SubmissionKind = "remember"
	}
	if replay.Evidence == nil {
		replay.Evidence = []rememberapp.SubmissionEvidenceStatus{}
	}
	if replay.RelationshipResults == nil {
		replay.RelationshipResults = []rememberapp.SubmissionRelationshipResult{}
	}
	if replay.Errors == nil {
		replay.Errors = []rememberapp.SubmissionStatusError{}
	}
	return &replay, nil
}

func (p *rememberSynchronousProcessor) loadRememberReplay(
	ctx context.Context,
	input rememberapp.RememberProcessRequest,
	submissionID string,
) (*rememberapp.SubmissionStatusResult, error) {
	recoveryCtx, cancel := rememberFailureRecoveryContext(ctx)
	defer cancel()
	attempt, err := p.ledger.LoadRememberAttempt(recoveryCtx, repository.RememberAttemptLookupInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey,
	})
	if err == nil && attempt != nil {
		replayed, replayErr := rememberAttemptReplay(attempt, input)
		if replayErr == nil {
			return replayed, nil
		}
		var processErr *rememberapp.RememberProcessError
		if errors.As(replayErr, &processErr) && processErr.Status != nil {
			return replayed, replayErr
		}
		err = replayErr
	}
	if err == nil {
		err = repository.ErrRememberAttemptNotFound
	}
	return rememberReplayLoadFailure(input, submissionID, err)
}

func rememberReplayLoadFailure(
	input rememberapp.RememberProcessRequest,
	submissionID string,
	cause error,
) (*rememberapp.SubmissionStatusResult, error) {
	return nil, rememberFailurePersistenceProcessError(input, submissionID, cause)
}

func replayCanonicalAttemptID(status *rememberapp.SubmissionStatusResult, syntheticSubmissionID string) string {
	if status == nil || status.SubmissionID == "" || status.SubmissionID == syntheticSubmissionID {
		return ""
	}
	return status.SubmissionID
}

func rememberAssessmentSnapshot(
	input rememberapp.RememberProcessRequest,
	ingestID string,
) (rememberapp.RememberAssessmentSnapshot, rememberapp.RememberAssessmentScope) {
	evidence := make([]repository.EvidenceFragment, 0, len(input.Evidence))
	items := make([]rememberapp.RememberAssessmentItem, 0, len(input.Evidence))
	for index, item := range input.Evidence {
		fragmentID := uuid.NewString()
		exactReuseEligible := len(item.SupersedesEvidenceIDs) == 0 && strings.TrimSpace(item.SourceKey) == "" &&
			strings.TrimSpace(item.SourceRevisionToken) == "" && strings.TrimSpace(item.ExpectedPreviousRevisionToken) == ""
		evidence = append(evidence, repository.EvidenceFragment{
			FragmentID: fragmentID, EvidenceIndex: index, Content: item.Content,
			ContentHash: item.ContentHash, Authority: item.Authority,
		})
		items = append(items, rememberapp.RememberAssessmentItem{
			ItemID: uuid.NewString(), Fragment: evidence[len(evidence)-1],
			EvidenceID: fmt.Sprintf("evidence:%d", index), DuplicateAssessmentRequired: exactReuseEligible && !item.ForceInsert,
			ExactReuseEligible: exactReuseEligible,
		})
	}
	return rememberapp.RememberAssessmentSnapshot{
		Scope:                  rememberapp.RememberAssessmentScope{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration},
		Proposal:               input.Proposal,
		Evidence:               evidence,
		Items:                  items,
		ExactDuplicateEvidence: make(map[int]repository.RememberDuplicateResolution),
	}, rememberapp.RememberAssessmentScope{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration}
}

func rememberEvidenceInputsForCommit(input rememberapp.RememberProcessRequest, snapshot rememberapp.RememberAssessmentSnapshot) []repository.EvidenceInput {
	items := rememberEvidenceInputs(input.Evidence)
	for index := range items {
		if index < len(snapshot.Evidence) {
			items[index].FragmentID = snapshot.Evidence[index].FragmentID
		}
		if strings.TrimSpace(items[index].IdempotencyKey) == "" {
			items[index].IdempotencyKey = fmt.Sprintf("%s:evidence:%d", strings.TrimSpace(input.IdempotencyKey), index)
		}
	}
	return items
}

func rememberEvidenceInputs(items []rememberapp.EvidenceInput) []repository.EvidenceInput {
	if len(items) == 0 {
		return nil
	}
	result := make([]repository.EvidenceInput, 0, len(items))
	for _, item := range items {
		var event *repository.SecurityEventDraft
		if item.InitialEvent != nil {
			event = &repository.SecurityEventDraft{
				EventKind: item.InitialEvent.EventKind,
				Decision:  item.InitialEvent.Decision,
				Reason:    item.InitialEvent.Reason,
				Metadata:  item.InitialEvent.Metadata,
			}
			for _, signal := range item.InitialEvent.Signals {
				event.Signals = append(event.Signals, repository.SecuritySignalInput{
					Kind: signal.Kind, Severity: signal.Severity, SpanStart: signal.SpanStart,
					SpanEnd: signal.SpanEnd, Metadata: signal.Metadata,
				})
			}
		}
		result = append(result, repository.EvidenceInput{
			Content: item.Content, ForceInsert: item.ForceInsert, ContentHash: item.ContentHash, SourceType: item.SourceType,
			Authority: item.Authority, SourceRef: item.SourceRef, SourceKey: item.SourceKey,
			SourceRevisionToken: item.SourceRevisionToken, ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken,
			SourceRevisionContentHash: item.SourceRevisionContentHash, SourceRevisionEnvelope: item.SourceRevisionEnvelope,
			SupersedesEvidenceIDs: append([]string(nil), item.SupersedesEvidenceIDs...),
			Labels:                append([]string(nil), item.Labels...), Metadata: item.Metadata, InitialEvent: event,
		})
	}
	return result
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
