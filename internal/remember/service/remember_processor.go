package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	"github.com/markhuangai/dense-mem/internal/topics"
)

// rememberSynchronousProcessor owns the request-scoped Remember boundary:
// assessment, one embedding batch, and one terminal repository transaction.
type rememberSynchronousProcessor struct {
	topicAssignment    *topics.AssignmentService
	ledger             remembercontract.Persistence
	provider           assessor.Provider
	decisions          modelprovider.DecisionProvider
	decisionSettings   modelprovider.DecisionSettings
	embeddingExecutor  *semanticwrite.Executor
	limits             assessor.SemanticAssessmentLimits
	metrics            observability.DiscoverabilityMetrics
	logger             observability.LogProvider
	protector          observability.DiagnosticProtector
	isStaleInput       func(error) bool
	commitFailureStage func(error) string
}

// ProcessorDependencies contains only provider, policy, and persistence ports
// needed for one request-owned Remember execution.
type ProcessorDependencies struct {
	TopicAssignment     *topics.AssignmentService
	Ledger              remembercontract.Persistence
	Assessor            assessor.Provider
	Decisions           modelprovider.DecisionProvider
	DecisionSettings    modelprovider.DecisionSettings
	EmbeddingExecutor   *semanticwrite.Executor
	Limits              assessor.SemanticAssessmentLimits
	Metrics             observability.DiscoverabilityMetrics
	Logger              observability.LogProvider
	DiagnosticProtector observability.DiagnosticProtector
	IsStaleInput        func(error) bool
	CommitFailureStage  func(error) string
}

var _ SynchronousProcessor = (*rememberSynchronousProcessor)(nil)

// NewSynchronousProcessor constructs the sole request-owned Remember
// processor. It performs no storage or provider work until ProcessRemember.
func NewSynchronousProcessor(deps ProcessorDependencies) *rememberSynchronousProcessor {
	staleInput := deps.IsStaleInput
	if staleInput == nil {
		staleInput = IsRememberStaleInputError
	}
	return &rememberSynchronousProcessor{
		topicAssignment: deps.TopicAssignment, ledger: deps.Ledger, provider: deps.Assessor, embeddingExecutor: deps.EmbeddingExecutor,
		limits: deps.Limits, metrics: deps.Metrics, logger: deps.Logger, protector: deps.DiagnosticProtector,
		isStaleInput: staleInput, commitFailureStage: deps.CommitFailureStage,
		decisions: deps.Decisions, decisionSettings: deps.DecisionSettings,
	}
}

func (p *rememberSynchronousProcessor) isRememberStaleInput(err error) bool {
	if IsRememberStaleInputError(err) {
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
	input RememberProcessRequest,
) (*SubmissionStatusResult, error) {
	if p == nil || p.ledger == nil {
		return nil, errors.New("remember processor: ledger is required")
	}
	if input.InvocationStartedAt.IsZero() {
		input.InvocationStartedAt = time.Now().UTC()
	}
	p.logAdmittedRememberRequest(ctx, input)
	ReportProgress(ctx, ProgressCheckingReplay)
	waiterInvocationID := uuid.NewString()
	requestctx.SetRememberInvocationID(ctx, waiterInvocationID)
	var ownerResult *SubmissionStatusResult
	var ownerErr error
	owner := false
	callbackEntered := false
	lockErr := p.ledger.WithRememberAttemptLock(ctx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, func(waited bool) error {
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
	} else if processErr := new(RememberProcessError); errors.As(replayErr, &processErr) && processErr.Status != nil {
		classification := "replay"
		if errors.Is(replayErr, ErrRememberConflict) || errors.Is(replayErr, repository.ErrIdempotencyConflict) {
			classification = "conflict"
		}
		p.recordRememberInvocation(ctx, input, waiterInvocationID, classification, replayCanonicalAttemptID(processErr.Status, waiterInvocationID), "idempotency_wait", replayErr, processErr.Status, nil)
		return processErr.Status, replayErr
	}
	classification := "replay"
	if errors.Is(replayErr, ErrRememberConflict) || errors.Is(replayErr, repository.ErrIdempotencyConflict) {
		classification = "conflict"
	}
	p.recordRememberInvocation(ctx, input, waiterInvocationID, classification, "", "idempotency_wait", replayErr, nil, nil)
	return nil, replayErr
}

func (p *rememberSynchronousProcessor) processRememberUnlocked(
	ctx context.Context,
	input RememberProcessRequest,
) (*SubmissionStatusResult, error) {
	if p == nil || p.ledger == nil {
		return nil, errors.New("remember processor: ledger is required")
	}
	started := time.Now()
	exchangeRecorder := observability.NewProviderExchangeRecorder(p.protector)
	ctx = modelprovider.WithExchangeRecorder(ctx, exchangeRecorder)
	ingestID := uuid.NewString()
	requestctx.SetRememberInvocationID(ctx, ingestID)
	snapshot, scope := rememberAssessmentSnapshot(input, ingestID)
	assessorTurns := 0
	var assessorDiagnostics map[string]any
	recoveryAttempt := false
	fail := func(err error, phase string) (*SubmissionStatusResult, error) {
		err = WithSynchronousAssessmentDiagnostics(err, assessorDiagnostics)
		status, canonicalAttemptID, processErr := p.recordRememberFailure(ctx, input, ingestID, snapshot, started, phase, assessorTurns, err)
		invocationStatus := status
		if invocationStatus == nil {
			var statusErr *RememberProcessError
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
		if errors.Is(processErr, ErrRememberConflict) || errors.Is(processErr, repository.ErrIdempotencyConflict) {
			classification = "conflict"
		}
		p.recordRememberInvocation(ctx, input, ingestID, classification, canonicalAttemptID, phase, processErrOrCause(processErr, err), invocationStatus, exchangeRecorder.Snapshot(), recoveryAttempt)
		return status, processErr
	}
	attempt, lookupErr := p.ledger.LoadRememberAttempt(ctx, repository.RememberAttemptLookupInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
	})
	if lookupErr == nil && attempt != nil {
		if !domain.ContractVersionCompatible(strings.TrimSpace(attempt.ContractVersion)) {
			processErr := p.retiredRememberContract(ctx, input, attempt)
			p.recordRememberInvocation(ctx, input, ingestID, "conflict", attempt.AttemptID, "idempotency", processErr, processErr.Status, exchangeRecorder.Snapshot())
			return nil, processErr
		}
		if !rememberAttemptMatchesRequest(attempt, input) {
			processErr := rememberConflictProcessError(input, ingestID, ErrRememberConflict)
			p.recordRememberInvocation(ctx, input, ingestID, "conflict", attempt.AttemptID, "idempotency", processErr, processErr.Status, exchangeRecorder.Snapshot())
			return nil, processErr
		}
		if attempt.Outcome == "completed" || (attempt.Outcome == "failed" && !attempt.Retryable) {
			ReportProgress(ctx, ProgressReturningReplay)
			replay, replayErr := rememberAttemptStatusForRequest(attempt, input)
			if replayErr != nil {
				classification := "replay"
				if errors.Is(replayErr, ErrRememberConflict) || errors.Is(replayErr, repository.ErrIdempotencyConflict) {
					classification = "conflict"
				}
				p.recordRememberInvocation(ctx, input, ingestID, classification, attempt.AttemptID, "replay", replayErr, nil, exchangeRecorder.Snapshot())
				return nil, replayErr
			}
			if attempt.Outcome == "failed" {
				processErr := &RememberProcessError{Status: replay, Err: ErrRememberPersistence}
				p.recordRememberInvocation(ctx, input, ingestID, "replay", attempt.AttemptID, "replay", processErr, replay, exchangeRecorder.Snapshot())
				return nil, processErr
			}
			p.recordRememberInvocation(ctx, input, ingestID, "replay", attempt.AttemptID, "replay", nil, replay, exchangeRecorder.Snapshot())
			return replay, nil
		}
		if attempt.Outcome != "failed" {
			processErr := rememberConflictProcessError(input, ingestID, ErrRememberConflict)
			p.recordRememberInvocation(ctx, input, ingestID, "conflict", attempt.AttemptID, "idempotency", processErr, processErr.Status, exchangeRecorder.Snapshot())
			return nil, processErr
		}
		recoveryAttempt = true
		observability.RecordLogicalRecovery(p.metrics, "remember", "attempted")
	} else if lookupErr != nil && !errors.Is(lookupErr, repository.ErrRememberAttemptNotFound) {
		if errors.Is(lookupErr, domain.ErrMemoryWriteDenied) || errors.Is(lookupErr, domain.ErrMemoryOperationErased) {
			return nil, lookupErr
		}
		return fail(lookupErr, "commit")
	}
	if input.SecurityRejected {
		return fail(SecurityRejectionFailure(input.SecuritySignals, input.SecuritySignalsTruncated, "initial_scan"), "assessment")
	}
	ReportProgress(ctx, ProgressPreparingIntake)
	decisionSnapshot, err := p.rememberDecisionSnapshot(ctx)
	if err != nil {
		return fail(err, "assessment")
	}
	partitioned, intakeSecurity, partitionErr := p.partitionRemember(ctx, input, ingestID, decisionSnapshot)
	if partitionErr != nil {
		return fail(partitionErr, "assessment")
	}
	partitionSnapshot, _ := rememberAssessmentSnapshot(partitioned, ingestID)
	partitionSnapshot.MaxEvidenceItems = repository.MaxRememberPartitions
	preparedWrite, prepareErr := p.prepareRemember(ctx, partitioned, partitionSnapshot, scope, started, intakeSecurity)
	if prepareErr != nil {
		var preparation *rememberPreparationError
		if errors.As(prepareErr, &preparation) {
			assessorTurns = preparation.AssessorTurns
			input.AssessorSecurityRejected = preparation.AssessorSecurityRejected
			return fail(preparation.Cause, preparation.Phase)
		}
		return fail(prepareErr, "assessment")
	}
	assessorDiagnostics = preparedWrite.Commit.AssessorDiagnostics
	assessorTurns = preparedWrite.AssessorTurns
	commitInput, inlineEmbeddings := preparedWrite.Commit, preparedWrite.Embeddings
	commitStarted := time.Now()
	commitCtx, commitCancel := ContextForPhase(ctx, RememberPhaseCommit)
	if err := commitCtx.Err(); err != nil {
		commitCancel()
		p.observeRememberPhase(ctx, input, ingestID, "commit", err, commitStarted, nil)
		return fail(err, "commit")
	}
	defer commitCancel()
	ReportProgress(commitCtx, ProgressCommitting)
	if root, ok := p.logger.(interface{ ContextSlog() *slog.Logger }); ok {
		body, captureErr := json.Marshal(commitInput)
		if captureErr != nil {
			root.ContextSlog().WarnContext(commitCtx, "remember_commit_capture_unavailable", "attempt_id", ingestID, "reason", "serialization_failed")
		} else {
			observability.LogDiagnosticBody(commitCtx, root.ContextSlog(), p.protector, "remember_commit_input", body, modelprovider.MaxProviderDiagnosticBodyBytes, slog.String("attempt_id", ingestID), slog.String("team_id", input.TeamID), slog.String("profile_id", input.OwnerProfileID))
		}
	}
	committed, err := p.ledger.CommitRememberWithEmbeddings(commitCtx, commitInput, inlineEmbeddings)
	commitMetricErr := err
	if errors.Is(commitMetricErr, repository.ErrRememberReplay) {
		commitMetricErr = nil
	}
	p.observeRememberPhase(ctx, input, ingestID, "commit", commitMetricErr, commitStarted, nil)
	if errors.Is(err, repository.ErrRememberReplay) {
		replay, replayErr := p.loadRememberReplay(ctx, input, ingestID)
		canonicalAttemptID := ""
		if replay != nil {
			canonicalAttemptID = replay.SubmissionID
		}
		invocationStatus := replay
		if invocationStatus == nil {
			var processErr *RememberProcessError
			if errors.As(replayErr, &processErr) {
				invocationStatus = processErr.Status
				if canonicalAttemptID == "" {
					canonicalAttemptID = replayCanonicalAttemptID(processErr.Status, ingestID)
				}
			}
		}
		classification := "replay"
		if errors.Is(replayErr, ErrRememberConflict) || errors.Is(replayErr, repository.ErrIdempotencyConflict) {
			classification = "conflict"
		}
		p.recordRememberInvocation(ctx, input, ingestID, classification, canonicalAttemptID, "commit", replayErr, invocationStatus, exchangeRecorder.Snapshot(), recoveryAttempt)
		return replay, replayErr
	}
	if errors.Is(err, domain.ErrMemoryWriteDenied) || errors.Is(err, domain.ErrMemoryOperationErased) {
		return nil, err
	}
	if err != nil {
		return fail(normalizeRememberCommitFailure(err), "commit")
	}
	if committed == nil {
		return fail(errors.New("remember processor: nil Remember commit result"), "commit")
	}
	ReportProgress(ctx, ProgressPreparingResult)
	result := committed.Receipt
	var resultErr error
	if result == nil {
		resultErr = errors.New("remember processor: committed receipt is required")
	} else {
		normalizeRememberReplay(result, committed.IngestID)
	}
	classification := "execution"
	if recoveryAttempt {
		classification = "recovery"
	}
	p.recordRememberInvocation(ctx, input, committed.IngestID, classification, committed.IngestID, "commit", resultErr, result, exchangeRecorder.Snapshot())
	return result, resultErr
}

func rememberAssessmentSecurityRejected(prepared *SynchronousAssessmentResult) bool {
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
		errors.Is(err, ErrRememberRequestCancelled) || errors.Is(err, ErrRememberRequestTimeout) {
		return "cancelled"
	}
	return "failed"
}

func rememberAttemptMatchesRequest(attempt *repository.RememberAttempt, input RememberProcessRequest) bool {
	return attempt != nil && strings.TrimSpace(attempt.RequestHash) == strings.TrimSpace(input.RequestHash)
}

func (p *rememberSynchronousProcessor) recordRememberFailure(
	ctx context.Context,
	input RememberProcessRequest,
	attemptID string,
	snapshot RememberAssessmentSnapshot,
	started time.Time,
	phase string,
	assessorTurns int,
	failure error,
) (*SubmissionStatusResult, string, error) {
	ReportProgress(ctx, ProgressRecordingFailure)
	if failure == nil {
		failure = errors.New("remember execution failed")
	}
	failure = p.normalizeRememberFailure(failure)
	if errors.Is(failure, ErrRememberConflict) || errors.Is(failure, repository.ErrIdempotencyConflict) {
		return nil, "", rememberConflictProcessError(input, attemptID, failure)
	}
	code := rememberFailureCode(phase, failure)
	reasonCode, details := RememberFailureDetails(failure, phase)
	if reasonCode == "" {
		reasonCode = "remember_" + strings.TrimSpace(phase) + "_failed"
		details = map[string]any{"component": "remember." + strings.TrimSpace(phase), "server_owned": true}
	}
	publicError := TerminalStatusErrorWithDetails(TerminalErrorCode(code), reasonCode, details)
	correlationID := rememberProcessCorrelationID(input.Metadata)
	processingState := "failed"
	notStoredReason := TerminalNotStoredReasonForError(TerminalErrorCode(code))
	evidence := rememberFailureResults(input, notStoredReason)
	status := &SubmissionStatusResult{
		SpaceKind:       input.SpaceKind,
		ContractVersion: domain.ContractVersion, SubmissionID: attemptID, SubmissionKind: "remember",
		ProcessingState: processingState, SearchState: "not_required", CorrelationID: correlationID,
		Evidence: evidence,
		Errors:   []SubmissionStatusError{publicError},
	}
	publicResult, terminalResult := terminalRememberFailureResult(status)
	var exchanges []modelprovider.ProviderExchange
	if recorder, ok := modelprovider.ExchangeRecorderFromContext(ctx).(modelprovider.SnapshotRecorder); ok {
		exchanges = recorder.Snapshot()
	}
	var callerResponse []byte
	callerResponseCaptureAvailable := false
	if capture := DiagnosticCaptureFromContext(ctx); capture != nil {
		callerResponseCaptureAvailable = true
		callerResponse, _ = capture.ProjectResponse(publicResult, true)
	}
	diagnostics := rememberFailureDiagnosticsWithAuthenticationSecrets(input, publicResult, exchanges, callerResponse, rememberCallerResponseDelivered(ctx, failure), callerResponseCaptureAvailable, observability.AuthenticationSecretsFromContext(ctx), p.protector)
	assessorValidation := SynchronousAssessmentValidationDiagnostics(failure)
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
			AssessorValidation: assessorValidation, AssessorDiagnostics: SynchronousAssessmentDiagnostics(failure),
		},
		Diagnostics: diagnostics,
	})
	if recordErr != nil {
		if errors.Is(recordErr, repository.ErrRememberFailureRetentionDegraded) {
			p.logRememberFailure(ctx, input, attemptID, started, phase, publicError.Code, correlationID, assessorTurns, failure)
			p.logRememberFailureRetentionDegraded(input, attemptID, phase, recordErr)
			return nil, attemptID, &RememberProcessError{Status: status, Result: terminalResult, Err: failure}
		}
		if errors.Is(recordErr, repository.ErrRememberReplay) {
			winner, loadErr := p.ledger.LoadRememberAttempt(recoveryCtx, repository.RememberAttemptLookupInput{
				TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey,
				SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
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
			return nil, "", rememberConflictProcessError(input, attemptID, errors.Join(ErrRememberConflict, recordErr))
		}
		p.logRememberFailure(ctx, input, attemptID, started, phase, publicError.Code, correlationID, assessorTurns, failure)
		p.logRememberFailureRecordError(ctx, input, attemptID, phase, publicError.Code, correlationID, recordErr)
		return nil, "", rememberFailurePersistenceProcessError(input, attemptID, failure)
	}
	p.logRememberFailure(ctx, input, attemptID, started, phase, publicError.Code, correlationID, assessorTurns, failure)
	return nil, attemptID, &RememberProcessError{Status: status, Result: terminalResult, Err: failure}
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
		return ErrRememberStaleInput
	}
	return &rememberStaleInputError{cause: cause}
}

func (e *rememberStaleInputError) Error() string {
	return "remember: stale input"
}

func (e *rememberStaleInputError) Unwrap() []error {
	if e == nil || e.cause == nil {
		return []error{ErrRememberStaleInput}
	}
	return []error{ErrRememberStaleInput, e.cause}
}

func normalizeRememberFailureWithClassifier(failure error, isStaleInput func(error) bool) error {
	if failure == nil || errors.Is(failure, ErrRememberStaleInput) {
		return failure
	}
	if errors.Is(failure, ErrSourceRevisionConflict) ||
		errors.Is(failure, repository.ErrRememberDuplicateCandidateStale) ||
		errors.Is(failure, repository.ErrEvidenceConflictStaleInput) ||
		IsRememberStaleInputError(failure) ||
		(isStaleInput != nil && isStaleInput(failure)) {
		return newRememberStaleInputError(failure)
	}
	return failure
}

func normalizeRememberCommitFailure(failure error) error {
	if errors.Is(failure, repository.ErrSearchStaleVersion) || errors.Is(failure, repository.ErrSearchContractMismatch) {
		return fmt.Errorf("%w: search state changed before commit", ErrRememberCommitConflict)
	}
	return failure
}

func rememberFailurePersistenceError(failure error) error {
	if failure == nil {
		return ErrRememberPersistence
	}
	return fmt.Errorf("%w: terminal failure record unavailable: %w", ErrRememberPersistence, failure)
}

func rememberFailurePersistenceProcessError(
	input RememberProcessRequest,
	submissionID string,
	cause error,
) *RememberProcessError {
	return rememberFailureProcessErrorWithStatus(
		input, submissionID, cause, TerminalErrorDatabaseFailure,
		"failure_retention", "remember.failure_record",
	)
}

func rememberFailureProcessErrorWithStatus(
	input RememberProcessRequest,
	submissionID string,
	cause error,
	code TerminalErrorCode,
	reasonCode string,
	component string,
) *RememberProcessError {
	evidence := rememberFailureResults(input, "internal_failure")
	status := &SubmissionStatusResult{
		SpaceKind:       input.SpaceKind,
		ContractVersion: domain.ContractVersion, SubmissionID: submissionID, SubmissionKind: "remember",
		ProcessingState: "failed", SearchState: "not_required", CorrelationID: rememberProcessCorrelationID(input.Metadata),
		Evidence: evidence,
		Errors:   []SubmissionStatusError{TerminalStatusErrorWithDetails(code, reasonCode, map[string]any{"component": component, "server_owned": true})},
	}
	return &RememberProcessError{Status: status, Err: rememberFailurePersistenceError(cause)}
}

func (p *rememberSynchronousProcessor) logRememberFailure(
	ctx context.Context,
	input RememberProcessRequest,
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
	if validation := SynchronousAssessmentValidationDiagnostics(failure); validation != nil {
		attrs = append(attrs, observability.LogAttr{Key: "assessor_validation", Value: validation})
	}
	if diagnostics := SynchronousAssessmentDiagnostics(failure); diagnostics != nil {
		attrs = append(attrs, observability.LogAttr{Key: "assessor_diagnostics", Value: diagnostics})
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
	case phase == "assessment" && errors.Is(failure, ErrRememberDatabaseFailure):
		attrs = append(attrs,
			observability.String("failure_source", "assessment_catalog"),
			observability.String("failure_class", "database"),
			observability.String("failure_code", "assessment_catalog_failed"),
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
	if value > SemanticMaxAssessorTurns {
		return SemanticMaxAssessorTurns
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
	case errors.Is(err, ErrRememberCommitConflict):
		return "fence_conflict", "search_state_changed"
	case errors.Is(err, repository.ErrInlineEmbeddingPlanMismatch):
		return "data_contract", "embedding_plan_mismatch"
	case errors.Is(err, repository.ErrSearchContractMismatch):
		return "fence_conflict", "search_contract_changed"
	case errors.Is(err, repository.ErrSearchStaleVersion):
		return "fence_conflict", "search_document_stale"
	case errors.Is(err, ErrRememberStaleInput),
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

func rememberFailureCode(phase string, err error) SubmissionErrorCode {
	if errors.Is(err, ErrRememberPolicyRejected) || errors.Is(err, ErrEvidenceSecurityRejected) || errors.Is(err, ErrEncodedEvidenceNotAllowed) {
		return SubmissionErrorPolicyRejected
	}
	if errors.Is(err, ErrRememberRequestTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return SubmissionErrorRequestTimeout
	}
	if errors.Is(err, ErrRememberRequestCancelled) || errors.Is(err, context.Canceled) {
		return SubmissionErrorRequestCancelled
	}
	if errors.Is(err, ErrRememberDatabaseFailure) {
		return SubmissionErrorDatabaseFailure
	}
	var planFailure *rememberEmbeddingPlanFailure
	if errors.As(err, &planFailure) {
		switch {
		case errors.Is(planFailure.cause, repository.ErrInlineEmbeddingPlanTooLarge):
			return SubmissionErrorInputBudgetExceeded
		case errors.Is(planFailure.cause, repository.ErrSearchContractMismatch):
			return SubmissionErrorConfigurationInvalid
		case errors.Is(planFailure.cause, repository.ErrInlineEmbeddingPlanMismatch):
			return SubmissionErrorInternalFailure
		default:
			return SubmissionErrorDatabaseFailure
		}
	}
	var configurationFailure *rememberEmbeddingConfigurationFailure
	if errors.As(err, &configurationFailure) {
		return SubmissionErrorConfigurationInvalid
	}
	var providerFailure *rememberEmbeddingProviderFailure
	if errors.As(err, &providerFailure) {
		if metadata := embeddingcontract.ClassifyFailure(providerFailure.cause); metadata.Code == "provider_response_invalid" {
			return SubmissionErrorEmbeddingResponseInvalid
		}
	}
	if errors.Is(err, ErrRememberEmbeddingUnavailable) {
		return SubmissionErrorEmbeddingUnavailable
	}
	if errors.Is(err, ErrRememberEmbeddingInvalid) {
		return SubmissionErrorEmbeddingResponseInvalid
	}
	if errors.Is(err, ErrRememberProviderUnavailable) {
		return SubmissionErrorProviderUnavailable
	}
	if errors.Is(err, ErrRememberProviderResponseInvalid) {
		return SubmissionErrorProviderResponseInvalid
	}
	if errors.Is(err, ErrRememberInputBudgetExceeded) {
		return SubmissionErrorInputBudgetExceeded
	}
	if errors.Is(err, ErrRememberCommitConflict) || errors.Is(err, repository.ErrSearchStaleVersion) {
		return SubmissionErrorCommitConflict
	}
	if errors.Is(err, ErrRememberStaleInput) ||
		errors.Is(err, repository.ErrRememberDuplicateCandidateStale) ||
		errors.Is(err, repository.ErrSourceRevisionConflict) ||
		errors.Is(err, repository.ErrEvidenceConflictStaleInput) ||
		errors.Is(err, ErrSourceRevisionConflict) {
		return SubmissionErrorStaleInput
	}
	if phase == "assessment" {
		return SubmissionErrorProviderUnavailable
	}
	if phase == "embedding" {
		return SubmissionErrorEmbeddingUnavailable
	}
	return SubmissionErrorDatabaseFailure
}

func rememberAttemptStatus(attempt *repository.RememberAttempt) (*SubmissionStatusResult, error) {
	if attempt == nil {
		return nil, errors.New("remember processor: attempt is required")
	}
	encoded, err := json.Marshal(attempt.PublicResult)
	if err != nil {
		return nil, err
	}
	var replay SubmissionStatusResult
	if err := json.Unmarshal(encoded, &replay); err != nil {
		return nil, err
	}
	normalizeRememberReplay(&replay, attempt.AttemptID)
	return &replay, nil
}

func normalizeRememberReplay(replay *SubmissionStatusResult, attemptID string) {
	replay.SubmissionID = firstNonEmptyString(replay.SubmissionID, attemptID)
	if replay.ContractVersion == "" {
		replay.ContractVersion = domain.ContractVersion
	}
	if replay.SubmissionKind == "" {
		replay.SubmissionKind = "remember"
	}
	if replay.Evidence == nil {
		replay.Evidence = []SubmissionEvidenceStatus{}
	}
	if replay.Errors == nil {
		replay.Errors = []SubmissionStatusError{}
	}
	UpgradeRememberResultDiagnostics(replay)
}

func (p *rememberSynchronousProcessor) loadRememberReplay(
	ctx context.Context,
	input RememberProcessRequest,
	submissionID string,
) (*SubmissionStatusResult, error) {
	recoveryCtx, cancel := rememberFailureRecoveryContext(ctx)
	defer cancel()
	attempt, err := p.ledger.LoadRememberAttempt(recoveryCtx, repository.RememberAttemptLookupInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
	})
	if err == nil && attempt != nil {
		replayed, replayErr := rememberAttemptReplay(attempt, input)
		if replayErr == nil {
			ReportProgress(ctx, ProgressReturningReplay)
			return replayed, nil
		}
		var processErr *RememberProcessError
		if errors.As(replayErr, &processErr) && processErr.Status != nil {
			if !errors.Is(replayErr, ErrRememberConflict) && !errors.Is(replayErr, repository.ErrIdempotencyConflict) {
				ReportProgress(ctx, ProgressReturningReplay)
			}
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
	input RememberProcessRequest,
	submissionID string,
	cause error,
) (*SubmissionStatusResult, error) {
	return nil, rememberFailurePersistenceProcessError(input, submissionID, cause)
}

func replayCanonicalAttemptID(status *SubmissionStatusResult, syntheticSubmissionID string) string {
	if status == nil || status.SubmissionID == "" || status.SubmissionID == syntheticSubmissionID {
		return ""
	}
	return status.SubmissionID
}

func rememberAssessmentSnapshot(
	input RememberProcessRequest,
	ingestID string,
) (RememberAssessmentSnapshot, RememberAssessmentScope) {
	evidence := make([]repository.EvidenceFragment, 0, len(input.Evidence))
	items := make([]RememberAssessmentItem, 0, len(input.Evidence))
	for index, item := range input.Evidence {
		fragmentID := uuid.NewString()
		exactReuseEligible := len(item.SupersedesEvidenceIDs) == 0 && strings.TrimSpace(item.SourceKey) == "" &&
			strings.TrimSpace(item.SourceRevisionToken) == "" && strings.TrimSpace(item.ExpectedPreviousRevisionToken) == ""
		evidence = append(evidence, repository.EvidenceFragment{
			Partition:  item.Partition,
			FragmentID: fragmentID, EvidenceIndex: index, Content: item.Content,
			ContentHash: item.ContentHash, Authority: item.Authority,
		})
		items = append(items, RememberAssessmentItem{
			ItemID: uuid.NewString(), Fragment: evidence[len(evidence)-1],
			EvidenceID: fmt.Sprintf("evidence:%d", index), DuplicateAssessmentRequired: exactReuseEligible && !item.ForceInsert,
			ExactReuseEligible: exactReuseEligible,
		})
	}
	return RememberAssessmentSnapshot{
		Scope:                  RememberAssessmentScope{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration},
		Evidence:               evidence,
		Items:                  items,
		ExactDuplicateEvidence: make(map[int]repository.RememberDuplicateResolution),
	}, RememberAssessmentScope{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration}
}

func rememberEvidenceInputsForCommit(input RememberProcessRequest, snapshot RememberAssessmentSnapshot) []repository.EvidenceInput {
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

func rememberEvidenceInputs(items []EvidenceInput) []repository.EvidenceInput {
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
			Partition: item.Partition,
			Content:   item.Content, ForceInsert: item.ForceInsert, ContentHash: item.ContentHash, SourceType: item.SourceType,
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
