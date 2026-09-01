package serverapp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

// rememberSynchronousProcessor owns the request-scoped Remember boundary:
// assessment, one embedding batch, and one terminal repository transaction.
type rememberSynchronousProcessor struct {
	ledger   rememberSynchronousLedger
	catalog  memoryservice.SubmissionAssessmentCatalog
	provider assessor.Provider
	embedder embedding.EmbeddingProviderInterface
	limits   assessor.SemanticAssessmentLimits
	metrics  observability.DiscoverabilityMetrics
	logger   observability.LogProvider
}

type rememberSynchronousLedger interface {
	LoadRememberAttempt(context.Context, repository.RememberAttemptLookupInput) (*repository.RememberAttempt, error)
	CommitRememberPreflightQuarantine(context.Context, repository.SynchronousRememberCommitInput, repository.RememberTerminalErrorInput) (*repository.SynchronousRememberCommitResult, error)
	CommitRememberTerminal(context.Context, repository.SynchronousRememberCommitInput, string, repository.RememberTerminalErrorInput, []repository.SubmissionAssessmentSecurityQuarantineInput) (*repository.SynchronousRememberCommitResult, error)
	PlanRememberEmbeddings(context.Context, repository.SynchronousRememberCommitInput) (*repository.InlineEmbeddingPlan, error)
	CommitRememberWithEmbeddings(context.Context, repository.SynchronousRememberCommitInput, []repository.InlineEmbeddingResult) (*repository.SynchronousRememberCommitResult, error)
	RecordRememberFailure(context.Context, repository.RememberFailureRecordInput) error
}

var _ rememberapp.SynchronousProcessor = (*rememberSynchronousProcessor)(nil)
var _ rememberSynchronousLedger = (*repository.LedgerRepositoryImpl)(nil)

func newRememberSynchronousProcessor(
	ledger *repository.LedgerRepositoryImpl,
	catalog memoryservice.SubmissionAssessmentCatalog,
	provider assessor.Provider,
	embedder embedding.EmbeddingProviderInterface,
	limits assessor.SemanticAssessmentLimits,
	metrics observability.DiscoverabilityMetrics,
	logger observability.LogProvider,
) *rememberSynchronousProcessor {
	var ledgerPort rememberSynchronousLedger
	if ledger != nil {
		ledgerPort = ledger
	}
	return &rememberSynchronousProcessor{
		ledger: ledgerPort, catalog: catalog, provider: provider, embedder: embedder, limits: limits,
		metrics: metrics, logger: logger,
	}
}

func (p *rememberSynchronousProcessor) ProcessRemember(
	ctx context.Context,
	input rememberapp.RememberProcessRequest,
) (*rememberapp.SubmissionStatusResult, error) {
	if p == nil || p.ledger == nil {
		return nil, errors.New("remember processor: ledger is required")
	}
	started := time.Now()
	ingestID := uuid.NewString()
	snapshot, scope := rememberAssessmentSnapshot(input, ingestID)
	assessorTurns := 0
	fail := func(err error, phase string) (*rememberapp.SubmissionStatusResult, error) {
		return p.recordRememberFailure(ctx, input, ingestID, snapshot, started, phase, assessorTurns, err)
	}
	attempt, lookupErr := p.ledger.LoadRememberAttempt(ctx, repository.RememberAttemptLookupInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey,
	})
	if lookupErr == nil && attempt != nil {
		if !rememberAttemptMatchesRequest(attempt, input) {
			return nil, rememberConflictProcessError(input, ingestID, rememberapp.ErrRememberConflict)
		}
		if attempt.Outcome == "completed" || attempt.Outcome == "rejected" || attempt.Outcome == "quarantined" || attempt.Outcome == "replayed" {
			replay, replayErr := rememberAttemptStatusForRequest(attempt, input)
			if replayErr != nil {
				return nil, replayErr
			}
			return replay, nil
		}
	} else if lookupErr != nil && !errors.Is(lookupErr, repository.ErrRememberAttemptNotFound) {
		return fail(lookupErr, "commit")
	}
	if input.SecurityRejected {
		commitInput := repository.SynchronousRememberCommitInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID,
			SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
			RequestHash:   input.RequestHash,
			SourceSummary: input.SourceSummary, Proposal: input.Proposal,
			Metadata: input.Metadata, Evidence: rememberEvidenceInputsForCommit(input, snapshot), StartedAt: started, Duration: time.Since(started),
		}
		if err := ctx.Err(); err != nil {
			return fail(err, "commit")
		}
		commitCtx, commitCancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseCommit)
		terminal, terminalErr := p.ledger.CommitRememberPreflightQuarantine(commitCtx, commitInput, rememberTerminalErrorInput(rememberapp.TerminalErrorQuarantined))
		commitCancel()
		if errors.Is(terminalErr, repository.ErrRememberReplay) {
			return p.loadRememberReplay(ctx, input, ingestID)
		}
		if terminalErr != nil {
			return fail(terminalErr, "commit")
		}
		return rememberAttemptStatusForRequest(&repository.RememberAttempt{AttemptID: terminal.IngestID, Outcome: terminal.Outcome, PublicResult: terminal.PublicResult}, input)
	}
	prepared, err := memoryservice.AssessSynchronousRemember(ctx, memoryservice.SynchronousAssessmentDependencies{
		Catalog: p.catalog, Provider: p.provider, Limits: p.limits, Metrics: p.metrics, Logger: p.logger,
	}, memoryservice.SynchronousAssessmentInput{Scope: scope, Snapshot: snapshot})
	if err != nil {
		assessorTurns = memoryservice.SynchronousAssessmentProviderTurns(err)
		return fail(err, "assessment")
	}
	assessorTurns = prepared.Assessment.ProviderTurns
	commitInput, buildErr := memoryservice.BuildSynchronousRememberCommitInput(memoryservice.SynchronousRememberCommitRequest{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
		RequestHash:   input.RequestHash,
		SourceSummary: input.SourceSummary, Proposal: input.Proposal,
		Metadata: input.Metadata, Evidence: rememberEvidenceInputsForCommit(input, snapshot), Assessment: prepared,
		Duration: time.Since(started),
	})
	commitInput.StartedAt = started
	var noSupported *memoryservice.NoSupportedMemoryError
	if buildErr != nil {
		if !errors.As(buildErr, &noSupported) {
			if memoryservice.IsRememberStaleInputError(buildErr) {
				return fail(fmt.Errorf("%w: %v", rememberapp.ErrRememberStaleInput, buildErr), "assessment")
			}
			return fail(buildErr, "assessment")
		}
	}
	terminalOutcome := rememberAssessmentTerminalOutcome(prepared, noSupported != nil)
	if terminalOutcome == "quarantined" {
		quarantines, quarantineErr := memoryservice.BuildSynchronousRememberSecurityQuarantines(prepared)
		if quarantineErr != nil {
			return fail(quarantineErr, "assessment")
		}
		if err := ctx.Err(); err != nil {
			return fail(err, "commit")
		}
		commitCtx, commitCancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseCommit)
		if err := commitCtx.Err(); err != nil {
			commitCancel()
			return fail(err, "commit")
		}
		terminal, terminalErr := p.ledger.CommitRememberTerminal(commitCtx, commitInput, "quarantined", rememberTerminalErrorInput(rememberapp.TerminalErrorQuarantined), quarantines)
		commitCancel()
		if errors.Is(terminalErr, repository.ErrRememberReplay) {
			return p.loadRememberReplay(ctx, input, ingestID)
		}
		if terminalErr != nil {
			return fail(terminalErr, "commit")
		}
		return rememberAttemptStatusForRequest(&repository.RememberAttempt{AttemptID: terminal.IngestID, Outcome: terminal.Outcome, PublicResult: terminal.PublicResult}, input)
	}
	if terminalOutcome == "rejected" {
		if err := ctx.Err(); err != nil {
			return fail(err, "commit")
		}
		commitCtx, commitCancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseCommit)
		if err := commitCtx.Err(); err != nil {
			commitCancel()
			return fail(err, "commit")
		}
		terminal, terminalErr := p.ledger.CommitRememberTerminal(commitCtx, commitInput, "rejected", rememberTerminalErrorInput(rememberapp.TerminalErrorNoSupportedMemory), nil)
		commitCancel()
		if errors.Is(terminalErr, repository.ErrRememberReplay) {
			return p.loadRememberReplay(ctx, input, ingestID)
		}
		if terminalErr != nil {
			return fail(terminalErr, "commit")
		}
		return rememberAttemptStatusForRequest(&repository.RememberAttempt{AttemptID: terminal.IngestID, Outcome: terminal.Outcome, PublicResult: terminal.PublicResult}, input)
	}
	embeddingCtx, embeddingCancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseEmbedding)
	defer embeddingCancel()
	plan, err := p.ledger.PlanRememberEmbeddings(embeddingCtx, commitInput)
	if err != nil {
		return fail(&rememberEmbeddingPlanFailure{cause: err}, "embedding")
	}
	plannedEmbeddings, err := p.embedSearchDocumentBatch(
		embeddingCtx,
		input.TeamID,
		input.OwnerProfileID,
		plan.EmbeddingModel,
		plan.Documents,
	)
	if err != nil {
		return fail(err, "embedding")
	}
	inlineEmbeddings := make([]repository.InlineEmbeddingResult, 0, len(plannedEmbeddings))
	for _, embedding := range plannedEmbeddings {
		inlineEmbeddings = append(inlineEmbeddings, repository.InlineEmbeddingResult{
			DocumentHash:            embedding.DocumentHash,
			Embedding:               embedding.Embedding,
			EmbeddingContractID:     plan.EmbeddingContractID,
			EmbeddingDimensions:     plan.EmbeddingDimensions,
			EmbeddingModel:          plan.EmbeddingModel,
			SearchIndexGenerationID: plan.SearchIndexGenerationID,
			IndexGeneration:         plan.IndexGeneration,
		})
	}
	commitCtx, commitCancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseCommit)
	if err := commitCtx.Err(); err != nil {
		commitCancel()
		return fail(err, "commit")
	}
	defer commitCancel()
	committed, err := p.ledger.CommitRememberWithEmbeddings(commitCtx, commitInput, inlineEmbeddings)
	if errors.Is(err, repository.ErrRememberReplay) {
		return p.loadRememberReplay(ctx, input, ingestID)
	}
	if err != nil {
		return fail(normalizeRememberCommitFailure(err), "commit")
	}
	if committed == nil {
		return nil, errors.New("remember processor: nil Remember commit result")
	}
	return rememberAttemptStatusForRequest(&repository.RememberAttempt{AttemptID: committed.IngestID, Outcome: committed.Outcome, PublicResult: committed.PublicResult}, input)
}

func rememberAssessmentTerminalOutcome(prepared *memoryservice.SynchronousAssessmentResult, noSupported bool) string {
	if prepared != nil && len(prepared.Response.SecuritySignals) > 0 {
		return "quarantined"
	}
	if noSupported {
		return "rejected"
	}
	return ""
}

func rememberAttemptMatchesRequest(attempt *repository.RememberAttempt, input rememberapp.RememberProcessRequest) bool {
	return attempt != nil && strings.TrimSpace(attempt.RequestHash) == strings.TrimSpace(input.RequestHash)
}

func (p *rememberSynchronousProcessor) recordRememberFailure(
	ctx context.Context,
	input rememberapp.RememberProcessRequest,
	attemptID string,
	snapshot memoryservice.RememberAssessmentSnapshot,
	started time.Time,
	phase string,
	assessorTurns int,
	failure error,
) (*rememberapp.SubmissionStatusResult, error) {
	if failure == nil {
		failure = errors.New("remember execution failed")
	}
	failure = normalizeRememberFailure(failure)
	if errors.Is(failure, rememberapp.ErrRememberConflict) || errors.Is(failure, repository.ErrIdempotencyConflict) {
		return nil, rememberConflictProcessError(input, attemptID, failure)
	}
	code := rememberFailureCode(phase, failure)
	publicError := rememberapp.TerminalStatusError(rememberapp.TerminalErrorCode(code))
	correlationID := rememberProcessCorrelationID(input.Metadata)
	processingState := "failed"
	switch code {
	case rememberapp.SubmissionErrorNoSupportedMemory, rememberapp.SubmissionErrorStaleInput:
		processingState = "rejected"
	case rememberapp.SubmissionErrorQuarantined:
		processingState = "quarantined"
	}
	notStoredReason := rememberFailureNotStoredReason(code)
	evidence, relationshipResults := rememberFailureResults(input, notStoredReason)
	status := &rememberapp.SubmissionStatusResult{
		ContractVersion: domain.ContractVersion, SubmissionID: attemptID, SubmissionKind: "remember",
		ProcessingState: processingState, SearchState: "not_required", CorrelationID: correlationID,
		Evidence: evidence, RelationshipResults: relationshipResults,
		Errors: []rememberapp.SubmissionStatusError{publicError},
	}
	publicEvidence := make([]any, 0, len(evidence))
	for _, item := range evidence {
		publicEvidence = append(publicEvidence, map[string]any{
			"disposition": item.Disposition, "evidence_index": item.EvidenceIndex,
			"superseded_evidence_ids": item.SupersededEvidenceIDs, "search_state": item.SearchState,
			"reason": item.Reason,
		})
	}
	publicRelationships := make([]any, 0, len(relationshipResults))
	for _, item := range relationshipResults {
		publicRelationships = append(publicRelationships, map[string]any{
			"ref": item.RelationshipRef, "disposition": item.Disposition,
			"reason": item.Reason, "splits": item.Splits,
		})
	}
	publicResult := map[string]any{
		"contract_version": domain.ContractVersion, "submission_id": attemptID, "submission_kind": "remember",
		"processing_state": processingState, "search_state": "not_required", "correlation_id": correlationID,
		"evidence": publicEvidence, "relationship_results": publicRelationships, "errors": []any{map[string]any{
			"code": publicError.Code, "message": publicError.Message, "retryable": publicError.Retryable,
			"next_action": publicError.NextAction, "remediation": publicError.Remediation,
		}},
	}
	artifacts := []repository.RememberFailureArtifactInput{
		{ArtifactKind: "failure", ContentType: "application/json", Content: []byte(fmt.Sprintf(`{"phase":%q,"code":%q}`, phase, publicError.Code))},
	}
	if artifact, ok := rememberFailureRequestArtifact(attemptID, snapshot.Evidence); ok {
		artifacts = append(artifacts, artifact)
	}
	recoveryCtx, cancel := rememberFailureRecoveryContext(ctx)
	defer cancel()
	if err := p.ledger.RecordRememberFailure(recoveryCtx, repository.RememberFailureRecordInput{
		Attempt: repository.RememberAttemptRecordInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, AttemptID: attemptID,
			SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
			RequestHash:     input.RequestHash,
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember",
			FailedPhase: phase, ErrorCode: publicError.Code, Retryable: publicError.Retryable, RetryabilitySet: true, CorrelationID: correlationID, PublicResult: publicResult,
			EvidenceCount: len(input.Evidence), AssessorTurns: assessorTurns, Duration: time.Since(started),
		},
		Artifacts: artifacts,
	}); err != nil {
		if errors.Is(err, repository.ErrRememberReplay) {
			winner, loadErr := p.ledger.LoadRememberAttempt(recoveryCtx, repository.RememberAttemptLookupInput{
				TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey,
			})
			if loadErr != nil {
				return nil, loadErr
			}
			return rememberAttemptReplay(winner, input)
		}
		if errors.Is(err, repository.ErrIdempotencyConflict) {
			return nil, rememberConflictProcessError(input, attemptID, errors.Join(rememberapp.ErrRememberConflict, err))
		}
		p.logRememberFailure(input, attemptID, started, phase, publicError.Code, correlationID, failure)
		p.logRememberFailureRecordError(input, attemptID, phase, publicError.Code, correlationID, err)
		return nil, rememberFailurePersistenceProcessError(input, attemptID, failure)
	}
	p.logRememberFailure(input, attemptID, started, phase, publicError.Code, correlationID, failure)
	return nil, &rememberapp.RememberProcessError{Status: status, Err: failure}
}

func rememberConflictProcessError(
	input rememberapp.RememberProcessRequest,
	submissionID string,
	cause error,
) *rememberapp.RememberProcessError {
	if cause == nil {
		cause = rememberapp.ErrRememberConflict
	}
	evidence, relationshipResults := rememberFailureResults(input, "internal_failure")
	status := &rememberapp.SubmissionStatusResult{
		ContractVersion: domain.ContractVersion, SubmissionID: submissionID, SubmissionKind: "remember",
		ProcessingState: "failed", SearchState: "not_required", CorrelationID: rememberProcessCorrelationID(input.Metadata),
		Evidence: evidence, RelationshipResults: relationshipResults,
		Errors: []rememberapp.SubmissionStatusError{rememberapp.StatusError(rememberapp.SubmissionErrorIdempotencyConflict)},
	}
	return &rememberapp.RememberProcessError{Status: status, Err: cause}
}

func rememberFailureNotStoredReason(code rememberapp.SubmissionErrorCode) string {
	switch code {
	case rememberapp.SubmissionErrorNoSupportedMemory:
		return "not_supported_by_evidence"
	case rememberapp.SubmissionErrorStaleInput:
		return "stale_input"
	case rememberapp.SubmissionErrorQuarantined:
		return "security_quarantine"
	default:
		return "internal_failure"
	}
}

func rememberFailureResults(
	input rememberapp.RememberProcessRequest,
	notStoredReason string,
) ([]rememberapp.SubmissionEvidenceStatus, []rememberapp.SubmissionRelationshipResult) {
	evidence := make([]rememberapp.SubmissionEvidenceStatus, len(input.Evidence))
	for index := range evidence {
		evidence[index] = rememberapp.SubmissionEvidenceStatus{
			Disposition:           "not_stored",
			EvidenceIndex:         index,
			SupersededEvidenceIDs: []string{},
			SearchState:           "not_required",
			Reason:                notStoredReason,
		}
	}
	refs := rememberFailureRelationshipRefs(input.Proposal)
	relationships := make([]rememberapp.SubmissionRelationshipResult, len(refs))
	for index, ref := range refs {
		relationships[index] = rememberapp.SubmissionRelationshipResult{
			RelationshipRef: ref,
			Disposition:     "not_stored",
			Reason:          notStoredReason,
			Splits:          []rememberapp.SubmissionRelationshipSplit{},
		}
	}
	return evidence, relationships
}

func rememberFailureRelationshipRefs(proposal map[string]any) []string {
	if proposal == nil {
		return []string{}
	}
	raw := proposal["relationship_hints"]
	if raw == nil {
		raw = proposal["relationships"]
	}
	var values []any
	switch typed := raw.(type) {
	case []any:
		values = typed
	case []map[string]any:
		values = make([]any, 0, len(typed))
		for _, value := range typed {
			values = append(values, value)
		}
	default:
		return []string{}
	}
	refs := make([]string, 0, len(values))
	for _, value := range values {
		fields, ok := value.(map[string]any)
		if !ok {
			refs = append(refs, "")
			continue
		}
		ref, _ := fields["ref"].(string)
		refs = append(refs, strings.TrimSpace(ref))
	}
	return refs
}

func normalizeRememberFailure(failure error) error {
	if failure == nil || errors.Is(failure, rememberapp.ErrRememberStaleInput) {
		return failure
	}
	if errors.Is(failure, rememberapp.ErrSourceRevisionConflict) || memoryservice.IsRememberStaleInputError(failure) {
		return fmt.Errorf("%w: %v", rememberapp.ErrRememberStaleInput, failure)
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
	evidence, relationshipResults := rememberFailureResults(input, "internal_failure")
	status := &rememberapp.SubmissionStatusResult{
		ContractVersion: domain.ContractVersion, SubmissionID: submissionID, SubmissionKind: "remember",
		ProcessingState: "failed", SearchState: "not_required", CorrelationID: rememberProcessCorrelationID(input.Metadata),
		Evidence: evidence, RelationshipResults: relationshipResults,
		Errors: []rememberapp.SubmissionStatusError{rememberapp.TerminalStatusError(rememberapp.TerminalErrorDatabaseFailure)},
	}
	return &rememberapp.RememberProcessError{Status: status, Err: rememberFailurePersistenceError(cause)}
}

func (p *rememberSynchronousProcessor) logRememberFailure(
	input rememberapp.RememberProcessRequest,
	attemptID string,
	started time.Time,
	phase string,
	errorCode string,
	correlationID string,
	failure error,
) {
	if p == nil || p.logger == nil {
		return
	}
	logError := errors.New("remember processing failed")
	attrs := rememberFailureLogAttrs(input, attemptID, phase, errorCode, correlationID)
	attrs = append(attrs, observability.Int("duration_ms", int(time.Since(started)/time.Millisecond)))
	var planFailure *rememberEmbeddingPlanFailure
	var configurationFailure *rememberEmbeddingConfigurationFailure
	var providerFailure *rememberEmbeddingProviderFailure
	switch {
	case errors.As(failure, &planFailure):
		logError = errors.New("remember embedding plan failed")
		failureClass, failureCode := rememberEmbeddingPlanFailureMetadata(planFailure.cause)
		attrs = append(attrs,
			observability.String("failure_source", "embedding_plan"),
			observability.String("failure_class", failureClass),
			observability.String("failure_code", failureCode),
		)
	case errors.As(failure, &configurationFailure):
		logError = errors.New("remember embedding provider configuration is invalid")
		attrs = append(attrs,
			observability.String("failure_source", "provider_configuration"),
			observability.String("failure_class", "configuration"),
			observability.String("failure_code", "embedding_provider_not_configured"),
		)
	case errors.As(failure, &providerFailure) && providerFailure.cause != nil:
		logError = providerFailure.cause
		metadata := embedding.ClassifyFailure(providerFailure.cause)
		attrs = append(attrs,
			observability.String("failure_source", "provider_call"),
			observability.String("failure_class", metadata.Class),
			observability.String("failure_code", metadata.Code),
		)
		if metadata.StatusCode > 0 {
			attrs = append(attrs, observability.Int("provider_status_code", metadata.StatusCode))
		}
	case phase == "commit":
		logError = rememberCommitOperationalLogError(failure)
		failureClass, failureCode := rememberCommitFailureMetadata(failure)
		attrs = append(attrs,
			observability.String("failure_source", "semantic_commit"),
			observability.String("failure_class", failureClass),
			observability.String("failure_code", failureCode),
		)
		if stage := repository.RememberCommitFailureStage(failure); stage != "" {
			attrs = append(attrs, observability.String("commit_stage", stage))
		}
	}
	p.logger.Error("remember_processing_failed", logError, attrs...)
}

func rememberCommitOperationalLogError(err error) error {
	stage := repository.RememberCommitFailureStage(err)
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
	case errors.Is(err, rememberapp.ErrRememberStaleInput), errors.Is(err, repository.ErrSourceRevisionConflict):
		return "stale_input", "source_state_changed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "semantic_commit_timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled", "semantic_commit_cancelled"
	default:
		return "database", "semantic_commit_failed"
	}
}

func (p *rememberSynchronousProcessor) logRememberFailureRecordError(
	input rememberapp.RememberProcessRequest,
	attemptID string,
	phase string,
	errorCode string,
	correlationID string,
	failure error,
) {
	if p == nil || p.logger == nil {
		return
	}
	attrs := rememberFailureLogAttrs(input, attemptID, phase, errorCode, correlationID)
	attrs = append(attrs, observability.String("recovery_error_code", rememberFailureRecoveryErrorCode(failure)))
	p.logger.Error("remember_failure_record_failed", rememberFailureRecoveryLogError(failure), attrs...)
}

func rememberFailureRecoveryLogError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("remember failure record persistence timed out: %w", context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("remember failure record persistence was cancelled: %w", context.Canceled)
	default:
		return errors.New("remember failure record persistence failed")
	}
}

func rememberFailureLogAttrs(
	input rememberapp.RememberProcessRequest,
	attemptID string,
	phase string,
	errorCode string,
	correlationID string,
) []observability.LogAttr {
	return []observability.LogAttr{
		observability.String("team_id", input.TeamID),
		observability.String("profile_id", input.OwnerProfileID),
		observability.CorrelationID(correlationID),
		observability.String("reference_type", "remember_attempt"),
		observability.String("reference_id", attemptID),
		observability.String("submission_id", attemptID),
		observability.String("failed_phase", phase),
		observability.String("error_code", errorCode),
	}
}

func rememberProcessCorrelationID(metadata map[string]any) string {
	if actor, ok := metadata["actor"].(map[string]any); ok {
		correlationID, _ := actor["correlation_id"].(string)
		return strings.TrimSpace(correlationID)
	}
	return ""
}

func rememberFailureRecoveryErrorCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "request_cancelled"
	default:
		return "persistence_failed"
	}
}

func rememberFailureRequestArtifact(attemptID string, evidence []repository.EvidenceFragment) (repository.RememberFailureArtifactInput, bool) {
	if len(evidence) == 0 {
		return repository.RememberFailureArtifactInput{}, false
	}
	evidencePayload := make([]map[string]any, 0, len(evidence))
	for _, item := range evidence {
		digest := sha256.Sum256([]byte(item.Content))
		evidencePayload = append(evidencePayload, map[string]any{
			"index":        item.EvidenceIndex,
			"content_hash": fmt.Sprintf("sha256:%x", digest[:]),
		})
	}
	requestPayload := map[string]any{"submission_id": attemptID, "evidence": evidencePayload}
	encoded, err := json.Marshal(requestPayload)
	if err != nil || len(encoded) > 256*1024 {
		return repository.RememberFailureArtifactInput{}, false
	}
	return repository.RememberFailureArtifactInput{ArtifactKind: "request", ContentType: "application/json", Content: encoded}, true
}

func rememberFailureRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), rememberapp.RememberFailurePersistenceBudget)
}

func rememberFailureCode(phase string, err error) rememberapp.SubmissionErrorCode {
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
		if metadata := embedding.ClassifyFailure(providerFailure.cause); metadata.Code == "provider_response_invalid" {
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
		errors.Is(err, repository.ErrSourceRevisionConflict) ||
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

func rememberAssessmentSnapshot(
	input rememberapp.RememberProcessRequest,
	ingestID string,
) (memoryservice.RememberAssessmentSnapshot, memoryservice.RememberAssessmentScope) {
	evidence := make([]repository.EvidenceFragment, 0, len(input.Evidence))
	items := make([]memoryservice.RememberAssessmentItem, 0, len(input.Evidence))
	for index, item := range input.Evidence {
		fragmentID := uuid.NewString()
		evidence = append(evidence, repository.EvidenceFragment{
			FragmentID: fragmentID, EvidenceIndex: index, Content: item.Content,
			ContentHash: item.ContentHash, Authority: item.Authority,
		})
		items = append(items, memoryservice.RememberAssessmentItem{
			ItemID: uuid.NewString(), Fragment: evidence[len(evidence)-1],
			EvidenceID: fmt.Sprintf("evidence:%d", index),
		})
	}
	return memoryservice.RememberAssessmentSnapshot{
		Scope:    memoryservice.RememberAssessmentScope{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration},
		Proposal: input.Proposal, Evidence: evidence, Items: items,
	}, memoryservice.RememberAssessmentScope{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration}
}

func rememberEvidenceInputsForCommit(input rememberapp.RememberProcessRequest, snapshot memoryservice.RememberAssessmentSnapshot) []repository.EvidenceInput {
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
			Content: item.Content, ContentHash: item.ContentHash, SourceType: item.SourceType,
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

func (p *rememberSynchronousProcessor) embedSearchDocumentBatch(
	ctx context.Context,
	teamID string,
	ownerProfileID string,
	embeddingModel string,
	documents []repository.SearchDocumentForEmbedding,
) ([]repository.SearchDocumentEmbedding, error) {
	if len(documents) == 0 {
		return []repository.SearchDocumentEmbedding{}, nil
	}
	if len(documents) > 256 {
		return nil, fmt.Errorf("%w: more than 256 search documents", rememberapp.ErrRememberInputBudgetExceeded)
	}
	texts := make([]string, len(documents))
	for i := range documents {
		texts[i] = documents[i].DocumentText
	}
	embedCtx, cancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseEmbedding)
	defer cancel()
	embedCtx = observability.WithMetricIdentity(embedCtx, teamID, ownerProfileID)
	embedCtx = observability.WithAIOperation(embedCtx, observability.AIOperationSearchDocumentEmbedding, len(texts))
	if p.embedder == nil || !p.embedder.IsAvailable() {
		return nil, &rememberEmbeddingConfigurationFailure{}
	}
	embeddingModel = strings.TrimSpace(embeddingModel)
	if embeddingModel == "" || strings.TrimSpace(p.embedder.ModelName()) != embeddingModel {
		return nil, fmt.Errorf("%w: configured model does not match the embedding plan", rememberapp.ErrRememberEmbeddingInvalid)
	}
	vectors, model, err := p.embedder.EmbedBatch(embedCtx, texts)
	if err != nil {
		if errors.Is(embedCtx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("%w: embedding phase canceled", rememberapp.ErrRememberRequestCancelled)
		}
		if errors.Is(embedCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: embedding phase exceeded 10 seconds", rememberapp.ErrRememberRequestTimeout)
		}
		return nil, &rememberEmbeddingProviderFailure{cause: err}
	}
	if len(vectors) != len(documents) || strings.TrimSpace(model) != embeddingModel {
		return nil, fmt.Errorf("%w: count or model mismatch", rememberapp.ErrRememberEmbeddingInvalid)
	}
	completed := make([]repository.SearchDocumentEmbedding, len(documents))
	for i, document := range documents {
		if len(vectors[i]) != document.EmbeddingDimensions {
			return nil, fmt.Errorf("%w: dimensions mismatch", rememberapp.ErrRememberEmbeddingInvalid)
		}
		for _, value := range vectors[i] {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, fmt.Errorf("%w: non-finite vector", rememberapp.ErrRememberEmbeddingInvalid)
			}
		}
		completed[i] = repository.SearchDocumentEmbedding{
			SearchDocumentID: document.SearchDocumentID, SourceKind: document.SourceKind, SourceID: document.SourceID,
			SourceVersion: document.SourceVersion, DocumentText: document.DocumentText,
			DocumentHash: document.DocumentHash, StoredDocumentHash: document.StoredDocumentHash,
			ProjectionFormat: document.ProjectionFormat, ProjectionGenerationID: document.ProjectionGenerationID,
			DocumentVersion: document.DocumentVersion, EmbeddingContractID: document.EmbeddingContractID,
			EmbeddingDimensions: document.EmbeddingDimensions, Embedding: vectors[i], SpaceID: document.SpaceID,
			SpaceGeneration: document.SpaceGeneration,
		}
	}
	return completed, nil
}

type rememberEmbeddingPlanFailure struct {
	cause error
}

func (e *rememberEmbeddingPlanFailure) Error() string {
	return "remember embedding plan failed"
}

func (e *rememberEmbeddingPlanFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type rememberEmbeddingConfigurationFailure struct{}

func (*rememberEmbeddingConfigurationFailure) Error() string {
	return rememberapp.ErrRememberEmbeddingUnavailable.Error()
}

func (*rememberEmbeddingConfigurationFailure) Unwrap() error {
	return rememberapp.ErrRememberEmbeddingUnavailable
}

type rememberEmbeddingProviderFailure struct {
	cause error
}

func (e *rememberEmbeddingProviderFailure) Error() string {
	if e == nil || e.cause == nil {
		return rememberapp.ErrRememberEmbeddingUnavailable.Error()
	}
	return fmt.Sprintf("%v: %v", rememberapp.ErrRememberEmbeddingUnavailable, e.cause)
}

func (e *rememberEmbeddingProviderFailure) Unwrap() []error {
	if e == nil || e.cause == nil {
		return []error{rememberapp.ErrRememberEmbeddingUnavailable}
	}
	return []error{rememberapp.ErrRememberEmbeddingUnavailable, e.cause}
}
