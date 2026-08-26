package serverapp

import (
	"context"
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
	ledger   *repository.LedgerRepositoryImpl
	catalog  memoryservice.SubmissionAssessmentCatalog
	provider assessor.Provider
	embedder embedding.EmbeddingProviderInterface
	limits   assessor.SemanticAssessmentLimits
	metrics  observability.DiscoverabilityMetrics
	logger   observability.LogProvider
}

var _ rememberapp.SynchronousProcessor = (*rememberSynchronousProcessor)(nil)

func newRememberSynchronousProcessor(
	ledger *repository.LedgerRepositoryImpl,
	catalog memoryservice.SubmissionAssessmentCatalog,
	provider assessor.Provider,
	embedder embedding.EmbeddingProviderInterface,
	limits assessor.SemanticAssessmentLimits,
	metrics observability.DiscoverabilityMetrics,
	logger observability.LogProvider,
) *rememberSynchronousProcessor {
	return &rememberSynchronousProcessor{
		ledger: ledger, catalog: catalog, provider: provider, embedder: embedder, limits: limits,
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
	attempt, lookupErr := p.ledger.LoadRememberAttempt(ctx, repository.RememberAttemptLookupInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey,
	})
	if lookupErr == nil && attempt != nil {
		if strings.TrimSpace(attempt.RequestHash) != strings.TrimSpace(input.RequestHash) {
			return nil, rememberapp.ErrRememberConflict
		}
		if attempt.Outcome == "completed" || attempt.Outcome == "rejected" || attempt.Outcome == "quarantined" || attempt.Outcome == "replayed" {
			replay, replayErr := rememberAttemptStatus(attempt)
			if replayErr != nil {
				return nil, replayErr
			}
			return replay, nil
		}
	} else if lookupErr != nil && !errors.Is(lookupErr, repository.ErrRememberAttemptNotFound) {
		return nil, lookupErr
	}
	started := time.Now()
	ingestID := uuid.NewString()
	snapshot, scope := rememberAssessmentSnapshot(input, ingestID)
	assessorTurns := 0
	fail := func(err error, phase string) (*rememberapp.SubmissionStatusResult, error) {
		return p.recordRememberFailure(ctx, input, ingestID, snapshot, started, phase, assessorTurns, err)
	}
	if input.SecurityRejected {
		commitInput := repository.SynchronousRememberCommitInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID,
			SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
			RequestHash: input.RequestHash, SourceSummary: input.SourceSummary, Proposal: input.Proposal,
			Metadata: input.Metadata, Evidence: rememberEvidenceInputsForCommit(input, snapshot), StartedAt: started, Duration: time.Since(started),
		}
		if err := ctx.Err(); err != nil {
			return fail(err, "commit")
		}
		commitCtx, commitCancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseCommit)
		terminal, terminalErr := p.ledger.CommitRememberPreflightQuarantine(commitCtx, commitInput, string(rememberapp.SubmissionErrorQuarantined))
		commitCancel()
		if errors.Is(terminalErr, repository.ErrRememberReplay) {
			replayed, loadErr := p.ledger.LoadRememberAttempt(ctx, repository.RememberAttemptLookupInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey})
			if loadErr != nil {
				return nil, loadErr
			}
			return rememberAttemptStatus(replayed)
		}
		if terminalErr != nil {
			return nil, terminalErr
		}
		return rememberAttemptStatus(&repository.RememberAttempt{AttemptID: terminal.IngestID, Outcome: terminal.Outcome, PublicResult: terminal.PublicResult})
	}
	prepared, err := memoryservice.AssessSynchronousRemember(ctx, memoryservice.SynchronousAssessmentDependencies{
		Catalog: p.catalog, Provider: p.provider, Limits: p.limits, Metrics: p.metrics, Logger: p.logger,
	}, memoryservice.SynchronousAssessmentInput{Scope: scope, Snapshot: snapshot})
	if err != nil {
		return fail(err, "assessment")
	}
	assessorTurns = prepared.Assessment.ProviderTurns
	commitInput, buildErr := memoryservice.BuildSynchronousRememberCommitInput(memoryservice.SynchronousRememberCommitRequest{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
		RequestHash: input.RequestHash, SourceSummary: input.SourceSummary, Proposal: input.Proposal,
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
		terminal, terminalErr := p.ledger.CommitRememberTerminal(commitCtx, commitInput, "quarantined", string(rememberapp.SubmissionErrorQuarantined), quarantines)
		commitCancel()
		if errors.Is(terminalErr, repository.ErrRememberReplay) {
			replayed, loadErr := p.ledger.LoadRememberAttempt(ctx, repository.RememberAttemptLookupInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey})
			if loadErr != nil {
				return nil, loadErr
			}
			return rememberAttemptStatus(replayed)
		}
		if terminalErr != nil {
			return fail(terminalErr, "commit")
		}
		return rememberAttemptStatus(&repository.RememberAttempt{AttemptID: terminal.IngestID, Outcome: terminal.Outcome, PublicResult: terminal.PublicResult})
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
		terminal, terminalErr := p.ledger.CommitRememberTerminal(commitCtx, commitInput, "rejected", string(rememberapp.SubmissionErrorNoSupportedMemory), nil)
		commitCancel()
		if errors.Is(terminalErr, repository.ErrRememberReplay) {
			replayed, loadErr := p.ledger.LoadRememberAttempt(ctx, repository.RememberAttemptLookupInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey})
			if loadErr != nil {
				return nil, loadErr
			}
			return rememberAttemptStatus(replayed)
		}
		if terminalErr != nil {
			return fail(terminalErr, "commit")
		}
		return rememberAttemptStatus(&repository.RememberAttempt{AttemptID: terminal.IngestID, Outcome: terminal.Outcome, PublicResult: terminal.PublicResult})
	}
	embeddingCtx, embeddingCancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseEmbedding)
	defer embeddingCancel()
	plan, err := p.ledger.PlanRememberEmbeddings(embeddingCtx, commitInput)
	if err != nil {
		return fail(err, "embedding")
	}
	plannedEmbeddings, err := p.embedSearchDocumentBatch(embeddingCtx, plan.Documents)
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
		replayed, loadErr := p.ledger.LoadRememberAttempt(ctx, repository.RememberAttemptLookupInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey,
		})
		if loadErr != nil {
			return nil, loadErr
		}
		return rememberAttemptStatus(replayed)
	}
	if err != nil {
		if errors.Is(err, repository.ErrSearchStaleVersion) {
			err = fmt.Errorf("%w: search document fence changed", rememberapp.ErrRememberCommitConflict)
		}
		return fail(err, "commit")
	}
	if committed == nil {
		return nil, errors.New("remember processor: nil Remember commit result")
	}
	return rememberAttemptStatus(&repository.RememberAttempt{AttemptID: committed.IngestID, Outcome: committed.Outcome, PublicResult: committed.PublicResult})
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
	if errors.Is(failure, rememberapp.ErrRememberConflict) || errors.Is(failure, repository.ErrIdempotencyConflict) {
		return nil, failure
	}
	if errors.Is(failure, repository.ErrSourceRevisionConflict) || errors.Is(failure, rememberapp.ErrSourceRevisionConflict) {
		failure = fmt.Errorf("%w: %v", rememberapp.ErrRememberStaleInput, failure)
	}
	code := rememberFailureCode(phase, failure)
	publicError := rememberapp.StatusError(code)
	correlationID := ""
	if input.Metadata != nil {
		if actor, ok := input.Metadata["actor"].(map[string]any); ok {
			correlationID, _ = actor["correlation_id"].(string)
		}
	}
	status := &rememberapp.SubmissionStatusResult{
		ContractVersion: domain.ContractVersion, SubmissionID: attemptID, SubmissionKind: "remember",
		ProcessingState: "failed", SearchState: "not_required", CorrelationID: correlationID,
		Evidence: []rememberapp.SubmissionEvidenceStatus{}, RelationshipResults: []rememberapp.SubmissionRelationshipResult{},
		Errors: []rememberapp.SubmissionStatusError{publicError},
	}
	publicResult := map[string]any{
		"contract_version": domain.ContractVersion, "submission_id": attemptID, "submission_kind": "remember",
		"processing_state": "failed", "search_state": "not_required", "correlation_id": correlationID,
		"evidence": []any{}, "relationship_results": []any{}, "errors": []any{map[string]any{
			"code": publicError.Code, "message": publicError.Message, "retryable": publicError.Retryable,
			"next_action": publicError.NextAction, "remediation": publicError.Remediation,
		}},
	}
	artifacts := []repository.RememberFailureArtifactInput{
		{ArtifactKind: "failure", ContentType: "application/json", Content: []byte(fmt.Sprintf(`{"phase":%q,"code":%q}`, phase, publicError.Code))},
	}
	if len(snapshot.Evidence) > 0 {
		requestPayload := map[string]any{"submission_id": attemptID, "idempotency_key": input.IdempotencyKey, "evidence": make([]map[string]any, 0, len(snapshot.Evidence))}
		for _, evidence := range snapshot.Evidence {
			requestPayload["evidence"] = append(requestPayload["evidence"].([]map[string]any), map[string]any{"index": evidence.EvidenceIndex, "content": evidence.Content})
		}
		if encoded, err := json.Marshal(requestPayload); err == nil && len(encoded) <= 256*1024 {
			artifacts = append(artifacts, repository.RememberFailureArtifactInput{ArtifactKind: "request", ContentType: "application/json", Content: encoded})
		}
	}
	recoveryCtx, cancel := rememberFailureRecoveryContext(ctx)
	defer cancel()
	if err := p.ledger.RecordRememberFailure(recoveryCtx, repository.RememberFailureRecordInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, AttemptID: attemptID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
		RequestHash: input.RequestHash, ContractVersion: domain.ContractVersion, SubmissionKind: "remember",
		FailedPhase: phase, ErrorCode: publicError.Code, CorrelationID: correlationID, PublicResult: publicResult,
		EvidenceCount: len(input.Evidence), AssessorTurns: assessorTurns, Duration: time.Since(started), Artifacts: artifacts,
	}); err != nil {
		if errors.Is(err, repository.ErrRememberReplay) {
			winner, loadErr := p.ledger.LoadRememberAttempt(ctx, repository.RememberAttemptLookupInput{
				TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey,
			})
			if loadErr != nil {
				return nil, loadErr
			}
			return rememberAttemptReplay(winner)
		}
		if errors.Is(err, repository.ErrIdempotencyConflict) {
			return nil, rememberapp.ErrRememberConflict
		}
		return nil, failure
	}
	return nil, &rememberapp.RememberProcessError{Status: status, Err: failure}
}

func rememberFailureRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

func rememberFailureCode(phase string, err error) rememberapp.SubmissionErrorCode {
	if errors.Is(err, rememberapp.ErrRememberRequestTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return rememberapp.SubmissionErrorRequestTimeout
	}
	if errors.Is(err, rememberapp.ErrRememberRequestCancelled) || errors.Is(err, context.Canceled) {
		return rememberapp.SubmissionErrorRequestCancelled
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

func rememberAttemptReplay(attempt *repository.RememberAttempt) (*rememberapp.SubmissionStatusResult, error) {
	status, err := rememberAttemptStatus(attempt)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(attempt.Outcome) != "failed" && strings.TrimSpace(status.ProcessingState) != "failed" {
		return status, nil
	}
	return nil, &rememberapp.RememberProcessError{Status: status, Err: rememberapp.ErrRememberPersistence}
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
			SourceID: item.SourceKey, SourceRevisionID: item.SourceRevisionToken,
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
	if p.embedder == nil || !p.embedder.IsAvailable() {
		return nil, fmt.Errorf("%w: provider is unavailable", rememberapp.ErrRememberEmbeddingUnavailable)
	}
	vectors, model, err := p.embedder.EmbedBatch(embedCtx, texts)
	if err != nil {
		if errors.Is(embedCtx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("%w: embedding phase canceled", rememberapp.ErrRememberRequestCancelled)
		}
		if errors.Is(embedCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: embedding phase exceeded 10 seconds", rememberapp.ErrRememberRequestTimeout)
		}
		return nil, fmt.Errorf("%w: %v", rememberapp.ErrRememberEmbeddingUnavailable, safeEmbeddingError(err))
	}
	if len(vectors) != len(documents) || strings.TrimSpace(model) == "" || model != strings.TrimSpace(p.embedder.ModelName()) {
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

func safeEmbeddingError(err error) string {
	if err == nil {
		return "provider request failed"
	}
	return "provider request failed"
}
