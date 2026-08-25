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

// rememberSynchronousProcessor adapts the existing semantic assessment
// engine to the request-scoped Remember application boundary. The worker
// implementation is invoked for one exact submission; no unrelated queued
// submission can be consumed by the originating request.
type rememberSynchronousProcessor struct {
	ledger   *repository.LedgerRepositoryImpl
	catalog  memoryservice.SubmissionAssessmentCatalog
	provider assessor.Provider
	inline   repository.InlineEmbeddingRepository
	embedder embedding.EmbeddingProviderInterface
	limits   assessor.SemanticAssessmentLimits
	metrics  observability.DiscoverabilityMetrics
	logger   observability.LogProvider
}

var _ rememberapp.Processor = (*rememberSynchronousProcessor)(nil)
var _ rememberapp.SynchronousProcessor = (*rememberSynchronousProcessor)(nil)

func newRememberSynchronousProcessor(
	ledger *repository.LedgerRepositoryImpl,
	catalog memoryservice.SubmissionAssessmentCatalog,
	provider assessor.Provider,
	search repository.SearchRepository,
	embedder embedding.EmbeddingProviderInterface,
	limits assessor.SemanticAssessmentLimits,
	metrics observability.DiscoverabilityMetrics,
	logger observability.LogProvider,
) *rememberSynchronousProcessor {
	var inline repository.InlineEmbeddingRepository
	if candidate, ok := search.(repository.InlineEmbeddingRepository); ok {
		inline = candidate
	}
	return &rememberSynchronousProcessor{
		ledger: ledger, catalog: catalog, provider: provider, inline: inline, embedder: embedder, limits: limits,
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
		if attempt.RequestHash != input.RequestHash {
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
	ingestID := uuid.NewString()
	placementRunID := uuid.NewString()
	snapshot, run := rememberAssessmentSnapshot(input, ingestID, placementRunID)
	prepared, err := memoryservice.AssessSynchronousRemember(ctx, memoryservice.SynchronousAssessmentDependencies{
		Catalog: p.catalog, Provider: p.provider, Limits: p.limits, Metrics: p.metrics, Logger: p.logger,
	}, memoryservice.SynchronousAssessmentInput{Run: run, Placement: snapshot})
	if err != nil {
		return nil, err
	}
	// Do not create an intake, evidence, or placement row when the required
	// write-embedding boundary is unavailable. The assessor has already
	// succeeded, so the caller can retry the same idempotency key safely.
	if p.inline == nil || p.embedder == nil || !p.embedder.IsAvailable() {
		return nil, fmt.Errorf("%w: inline embedding is not configured", rememberapp.ErrRememberEmbeddingUnavailable)
	}
	created, err := p.ledger.CreateIngest(ctx, repository.CreateIngestInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, PlacementRunID: placementRunID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
		RequestHash: input.RequestHash, SourceSummary: input.SourceSummary,
		Status: string(domain.PlacementRunQueued), TelemetryRemember: true,
		Proposal: input.Proposal, Metadata: input.Metadata, Evidence: rememberEvidenceInputs(input.Evidence),
	})
	if err != nil {
		return nil, normalizeRememberProcessError(err)
	}
	if created == nil {
		return nil, errors.New("remember processor: nil intake result")
	}
	if created.Existing {
		// Another request won the idempotency race. Wait on its owner-scoped
		// terminal placement instead of starting a second assessor/commit.
		status, waitErr := p.waitForExistingRemember(ctx, input.TeamID, input.OwnerProfileID, created.IngestID)
		if waitErr != nil {
			return nil, waitErr
		}
		if status != nil && status.ProcessingState == "failed" {
			return status, &rememberapp.RememberProcessError{Status: status, Err: rememberFailureCause(status)}
		}
		return status, nil
	}
	started := time.Now()
	status, processErr := p.processPrepared(ctx, rememberapp.ProcessRequest{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, SubmissionID: created.IngestID,
	}, prepared)
	if errors.Is(processErr, repository.ErrSearchStaleVersion) {
		processErr = fmt.Errorf("%w: search document fence changed", rememberapp.ErrRememberCommitConflict)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cleanupCancel()
	failedPhase, failedCode, failedMessage := classifyRememberFailure(processErr, status)
	if processErr != nil || status == nil || status.ProcessingState == "queued" || status.ProcessingState == "processing" {
		if processErr == nil {
			processErr = rememberapp.ErrRememberPersistence
			failedPhase, failedCode, failedMessage = classifyRememberFailure(processErr, status)
		}
		terminalizeErr := p.ledger.TerminalizeRememberFailure(cleanupCtx, repository.RememberTerminalizeFailureInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: created.IngestID,
			FailedPhase: failedPhase, ErrorCode: failedCode, Message: failedMessage,
		})
		if terminalizeErr == nil {
			if placement, loadErr := p.ledger.GetPlacementRun(cleanupCtx, repository.GetPlacementRunInput{
				TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: created.IngestID,
			}); loadErr == nil {
				status = rememberapp.ProjectSubmissionStatus(rememberStageResult(placement))
			}
		}
	}
	if processErr == nil && status != nil && status.ProcessingState == "failed" {
		processErr = rememberFailureCause(status)
		failedPhase, failedCode, failedMessage = classifyRememberFailure(processErr, status)
	}
	if status == nil || status.ProcessingState == "queued" || status.ProcessingState == "processing" {
		status = rememberFailureStatus(created.IngestID, failedCode, failedMessage)
	}
	outcome := "failed"
	if status != nil {
		switch status.ProcessingState {
		case "completed", "rejected", "quarantined":
			outcome = status.ProcessingState
		}
	}
	publicResult := map[string]any{}
	if status != nil {
		if encoded, marshalErr := json.Marshal(status); marshalErr == nil {
			_ = json.Unmarshal(encoded, &publicResult)
		}
	}
	if err := p.ledger.RecordRememberAttempt(cleanupCtx, repository.RememberAttemptRecordInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, AttemptID: created.IngestID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
		RequestHash: input.RequestHash, ContractVersion: domain.ContractVersion, SubmissionKind: "remember",
		Outcome: outcome, FailedPhase: func() string {
			if processErr != nil {
				return failedPhase
			}
			return ""
		}(),
		ErrorCode: func() string {
			if processErr != nil {
				return failedCode
			}
			return ""
		}(),
		CorrelationID: stringValueFromMetadata(input.Metadata, "correlation_id"), PublicResult: publicResult,
		EvidenceCount: len(input.Evidence), RelationshipCount: lenMapSlice(input.Proposal, "relationship_hints"),
		Duration: time.Since(started),
	}); err != nil && processErr == nil {
		// Canonical semantic state is already terminal. Do not turn a successful
		// Remember into an operational failure merely because its replay index
		// could not be recorded; the next call can still read the placement.
		if status == nil || status.ProcessingState == "failed" {
			processErr = err
		}
	}
	if processErr != nil {
		if status == nil {
			status = rememberFailureStatus(created.IngestID, failedCode, failedMessage)
		}
		return status, &rememberapp.RememberProcessError{Status: status, Err: processErr}
	}
	return status, nil
}

func (p *rememberSynchronousProcessor) waitForExistingRemember(
	ctx context.Context,
	teamID, ownerID, ingestID string,
) (*rememberapp.SubmissionStatusResult, error) {
	if p == nil || p.ledger == nil {
		return nil, errors.New("remember processor: ledger is required")
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		placement, err := p.ledger.GetPlacementRun(ctx, repository.GetPlacementRunInput{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
		})
		if err != nil {
			return nil, err
		}
		if placement != nil {
			state := rememberapp.ProjectSubmissionStatus(rememberStageResult(placement))
			switch state.ProcessingState {
			case "completed", "rejected", "quarantined", "failed":
				return state, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
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
	if replay.Degradations == nil {
		replay.Degradations = []rememberapp.SubmissionStatusDegradation{}
	}
	return &replay, nil
}

func rememberFailureCause(status *rememberapp.SubmissionStatusResult) error {
	if status != nil {
		for _, item := range status.Errors {
			switch rememberapp.SubmissionErrorCode(item.Code) {
			case rememberapp.SubmissionErrorProviderUnavailable:
				return rememberapp.ErrRememberProviderUnavailable
			case rememberapp.SubmissionErrorProviderResponseInvalid:
				return rememberapp.ErrRememberProviderResponseInvalid
			case rememberapp.SubmissionErrorInputBudgetExceeded:
				return rememberapp.ErrRememberInputBudgetExceeded
			case rememberapp.SubmissionErrorEmbeddingUnavailable:
				return rememberapp.ErrRememberEmbeddingUnavailable
			case rememberapp.SubmissionErrorEmbeddingResponseInvalid:
				return rememberapp.ErrRememberEmbeddingInvalid
			case rememberapp.SubmissionErrorCommitConflict:
				return rememberapp.ErrRememberCommitConflict
			case rememberapp.SubmissionErrorRequestTimeout:
				return rememberapp.ErrRememberRequestTimeout
			case rememberapp.SubmissionErrorRequestCancelled:
				return rememberapp.ErrRememberRequestCancelled
			case rememberapp.SubmissionErrorStaleInput:
				return rememberapp.ErrRememberStaleInput
			case rememberapp.SubmissionErrorDatabaseFailure:
				return rememberapp.ErrRememberPersistence
			}
		}
	}
	return rememberapp.ErrRememberPersistence
}

func classifyRememberFailure(err error, status *rememberapp.SubmissionStatusResult) (string, string, string) {
	if status != nil && status.ProcessingState == "failed" && len(status.Errors) > 0 {
		code := rememberapp.SubmissionErrorCode(status.Errors[0].Code)
		value := rememberapp.StatusError(code)
		return rememberFailurePhase(code), string(value.Code), value.Message
	}
	code := rememberapp.SubmissionErrorInternalFailure
	switch {
	case errors.Is(err, rememberapp.ErrRememberProviderUnavailable):
		code = rememberapp.SubmissionErrorProviderUnavailable
	case errors.Is(err, rememberapp.ErrRememberProviderResponseInvalid):
		code = rememberapp.SubmissionErrorProviderResponseInvalid
	case errors.Is(err, rememberapp.ErrRememberInputBudgetExceeded):
		code = rememberapp.SubmissionErrorInputBudgetExceeded
	case errors.Is(err, rememberapp.ErrRememberEmbeddingUnavailable):
		code = rememberapp.SubmissionErrorEmbeddingUnavailable
	case errors.Is(err, rememberapp.ErrRememberEmbeddingInvalid):
		code = rememberapp.SubmissionErrorEmbeddingResponseInvalid
	case errors.Is(err, rememberapp.ErrRememberCommitConflict), errors.Is(err, repository.ErrSearchStaleVersion):
		code = rememberapp.SubmissionErrorCommitConflict
	case errors.Is(err, rememberapp.ErrRememberRequestTimeout), errors.Is(err, context.DeadlineExceeded):
		code = rememberapp.SubmissionErrorRequestTimeout
	case errors.Is(err, rememberapp.ErrRememberRequestCancelled), errors.Is(err, context.Canceled):
		code = rememberapp.SubmissionErrorRequestCancelled
	case errors.Is(err, rememberapp.ErrRememberStaleInput), errors.Is(err, repository.ErrPlacementStaleSource):
		code = rememberapp.SubmissionErrorStaleInput
	case errors.Is(err, rememberapp.ErrRememberPersistence):
		code = rememberapp.SubmissionErrorDatabaseFailure
	}
	value := rememberapp.StatusError(code)
	return rememberFailurePhase(code), string(value.Code), value.Message
}

func rememberFailurePhase(code rememberapp.SubmissionErrorCode) string {
	switch code {
	case rememberapp.SubmissionErrorProviderUnavailable,
		rememberapp.SubmissionErrorProviderResponseInvalid,
		rememberapp.SubmissionErrorInputBudgetExceeded,
		rememberapp.SubmissionErrorConfigurationInvalid:
		return "assessment"
	case rememberapp.SubmissionErrorEmbeddingUnavailable,
		rememberapp.SubmissionErrorEmbeddingResponseInvalid:
		return "embedding"
	case rememberapp.SubmissionErrorCommitConflict,
		rememberapp.SubmissionErrorDatabaseFailure:
		return "semantic_commit"
	default:
		return "execution"
	}
}

func rememberFailureStatus(submissionID, code, message string) *rememberapp.SubmissionStatusResult {
	statusCode := rememberapp.SubmissionErrorCode(code)
	if statusCode == "" {
		statusCode = rememberapp.SubmissionErrorInternalFailure
	}
	value := rememberapp.StatusError(statusCode)
	if message != "" {
		value.Message = message
	}
	return &rememberapp.SubmissionStatusResult{
		ContractVersion: domain.ContractVersion, SubmissionID: submissionID, SubmissionKind: "remember",
		ProcessingState: "failed", SearchState: string(domain.SearchProjectionNotRequired),
		Evidence: []rememberapp.SubmissionEvidenceStatus{}, RelationshipResults: []rememberapp.SubmissionRelationshipResult{},
		Errors: []rememberapp.SubmissionStatusError{value}, Degradations: []rememberapp.SubmissionStatusDegradation{},
	}
}

func normalizeRememberProcessError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, repository.ErrIdempotencyConflict) {
		return fmt.Errorf("%w: idempotency key is bound to a different request", rememberapp.ErrRememberConflict)
	}
	if errors.Is(err, repository.ErrSourceRevisionConflict) {
		return fmt.Errorf("%w: source revision changed", rememberapp.ErrRememberStaleInput)
	}
	var preflight *repository.RememberPreflightError
	if errors.As(err, &preflight) {
		for _, issue := range preflight.Issues {
			if strings.EqualFold(strings.TrimSpace(issue.Code), "stale") {
				return fmt.Errorf("%w: source or lifecycle input changed", rememberapp.ErrRememberStaleInput)
			}
		}
		return fmt.Errorf("%w: request preflight failed", rememberapp.ErrRememberInputBudgetExceeded)
	}
	return err
}

func rememberAssessmentSnapshot(
	input rememberapp.RememberProcessRequest,
	ingestID, placementRunID string,
) (*repository.CreateIngestResult, repository.PlacementRun) {
	evidence := make([]repository.EvidenceFragment, 0, len(input.Evidence))
	items := make([]repository.PlacementItem, 0, len(input.Evidence))
	for index, item := range input.Evidence {
		fragmentID := uuid.NewString()
		evidence = append(evidence, repository.EvidenceFragment{
			FragmentID: fragmentID, EvidenceIndex: index, Content: item.Content,
			ContentHash: item.ContentHash, Authority: item.Authority,
			SourceID: item.SourceKey, SourceRevisionID: item.SourceRevisionToken,
		})
		items = append(items, repository.PlacementItem{
			PlacementItemID: uuid.NewString(), FragmentID: fragmentID,
			EvidenceIndex: index, Status: string(domain.PlacementRunQueued), Category: "pending", Version: 1,
		})
	}
	return &repository.CreateIngestResult{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID,
			PlacementRunID: placementRunID, Status: string(domain.PlacementRunQueued), Proposal: input.Proposal,
			Evidence: evidence, Items: items,
		}, repository.PlacementRun{
			TeamID: input.TeamID, IngestID: ingestID, PlacementRunID: placementRunID,
			OwnerProfileID: input.OwnerProfileID, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
			Status: string(domain.PlacementRunQueued), Attempts: 0, MaxAttempts: 3,
		}
}

func stringValueFromMetadata(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return strings.TrimSpace(value)
	}
	if actor, ok := metadata["actor"].(map[string]any); ok {
		if value, ok := actor[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func lenMapSlice(metadata map[string]any, key string) int {
	if metadata == nil {
		return 0
	}
	switch values := metadata[key].(type) {
	case []any:
		return len(values)
	case []map[string]any:
		return len(values)
	default:
		return 0
	}
}

func (p *rememberSynchronousProcessor) Process(ctx context.Context, req rememberapp.ProcessRequest) (*rememberapp.SubmissionStatusResult, error) {
	return p.processPrepared(ctx, req, nil)
}

func (p *rememberSynchronousProcessor) processPrepared(
	ctx context.Context,
	req rememberapp.ProcessRequest,
	prepared *memoryservice.SynchronousAssessmentResult,
) (*rememberapp.SubmissionStatusResult, error) {
	if p == nil || p.ledger == nil {
		return nil, errors.New("remember processor: ledger is required")
	}
	teamID := strings.TrimSpace(req.TeamID)
	ownerID := strings.TrimSpace(req.OwnerProfileID)
	submissionID := strings.TrimSpace(req.SubmissionID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("remember processor: team_id: %w", err)
	}
	if _, err := uuid.Parse(ownerID); err != nil {
		return nil, fmt.Errorf("remember processor: owner_profile_id: %w", err)
	}
	if _, err := uuid.Parse(submissionID); err != nil {
		return nil, fmt.Errorf("remember processor: submission_id: %w", err)
	}
	if p.inline == nil || p.embedder == nil {
		return nil, fmt.Errorf("%w: inline embedding is not configured", rememberapp.ErrRememberEmbeddingUnavailable)
	}
	if !p.embedder.IsAvailable() {
		return nil, fmt.Errorf("%w: provider is unavailable", rememberapp.ErrRememberEmbeddingUnavailable)
	}
	assessmentCtx, assessmentCancel := context.WithTimeout(ctx, 160*time.Second)
	defer assessmentCancel()
	assessmentCtx = repository.WithInlineEmbeddingWrites(assessmentCtx)
	workerDeps := memoryservice.SubmissionAssessmentPlacementWorkerDependencies{
		Ledger: p.ledger, Assessments: p.ledger, Catalog: p.catalog, Provider: p.provider,
		Limits: p.limits, TeamID: teamID, OwnerProfileID: ownerID,
		WorkerID: "remember-sync-" + uuid.NewString(), Lease: 3 * time.Minute,
		Metrics: p.metrics, Logger: p.logger, InlineEmbedder: p.embedSearchDocumentBatch,
	}
	var processErr error
	if prepared != nil {
		_, processErr = memoryservice.ProcessPreparedSynchronousRemember(assessmentCtx, workerDeps, submissionID, prepared)
	} else {
		worker := memoryservice.NewSubmissionAssessmentPlacementWorkerService(workerDeps)
		_, processErr = worker.ProcessSubmissionAssessmentPlacement(assessmentCtx, submissionID)
	}
	if processErr != nil {
		if errors.Is(assessmentCtx.Err(), context.DeadlineExceeded) {
			return nil, rememberapp.ErrRememberRequestTimeout
		}
		return nil, processErr
	}
	// Inline embeddings were validated and applied inside the semantic commit
	// transaction. Reload the terminal placement only after that transaction
	// has committed so the response reflects the durable current state.
	placement, err := p.ledger.GetPlacementRun(ctx, repository.GetPlacementRunInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: submissionID,
	})
	if err != nil {
		return nil, err
	}
	status := rememberapp.ProjectSubmissionStatus(rememberStageResult(placement))
	if status.SearchState != "current" && status.SearchState != "not_required" {
		return nil, errors.New("remember processor: synchronous embedding did not reach current state")
	}
	return status, nil
}

func (p *rememberSynchronousProcessor) embedSearchDocuments(
	ctx context.Context,
	teamID, ownerID string,
	placement *repository.CreateIngestResult,
) error {
	if placement == nil {
		return errors.New("remember processor: placement result is required")
	}
	ids := make([]string, 0, 32)
	seen := make(map[string]struct{})
	for _, item := range placement.Items {
		for _, raw := range rememberResultArray(item.Result, "search_document_ids") {
			id := strings.TrimSpace(fmt.Sprint(raw))
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > 256 {
		return fmt.Errorf("%w: more than 256 search documents", rememberapp.ErrRememberInputBudgetExceeded)
	}
	documents, err := p.inline.LoadSearchDocumentsForEmbedding(ctx, repository.LoadSearchDocumentsForEmbeddingInput{
		TeamID: teamID, OwnerProfileID: ownerID, SearchDocumentIDs: ids,
	})
	if err != nil {
		return err
	}
	embeddings, err := p.embedSearchDocumentBatch(ctx, documents)
	if err != nil {
		return err
	}
	if err := p.inline.CompleteSearchDocumentsWithEmbeddings(ctx, repository.CompleteSearchDocumentsWithEmbeddingsInput{
		TeamID: teamID, OwnerProfileID: ownerID, Documents: embeddings,
	}); err != nil {
		if errors.Is(err, repository.ErrSearchStaleVersion) {
			return fmt.Errorf("%w: search document fence changed", rememberapp.ErrRememberCommitConflict)
		}
		return err
	}
	return nil
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
	embedCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if !p.embedder.IsAvailable() {
		return nil, fmt.Errorf("%w: provider is unavailable", rememberapp.ErrRememberEmbeddingUnavailable)
	}
	vectors, model, err := p.embedder.EmbedBatch(embedCtx, texts)
	if err != nil {
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
			SearchDocumentID: document.SearchDocumentID, SourceVersion: document.SourceVersion,
			DocumentHash:     document.DocumentHash,
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

func rememberResultArray(result map[string]any, key string) []any {
	if result == nil {
		return nil
	}
	switch values := result[key].(type) {
	case []any:
		return values
	case []string:
		out := make([]any, len(values))
		for i, value := range values {
			out[i] = value
		}
		return out
	default:
		return nil
	}
}
