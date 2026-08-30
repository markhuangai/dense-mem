package synchronousremember

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	remember "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

// SynchronousRememberLedger is the durable request boundary.  Keeping the
// port here lets the E2E composition install the processor without making the
// public Remember service depend on PostgreSQL.
type SynchronousRememberLedger interface {
	LoadRememberAttempt(context.Context, repository.RememberAttemptLookupInput) (*repository.RememberAttempt, error)
	PlanSynchronousRememberEmbeddings(context.Context, repository.CreateIngestInput, repository.CommitSubmissionAssessmentInput) (*repository.SynchronousRememberEmbeddingPlan, error)
	CommitSynchronousRemember(context.Context, repository.SynchronousRememberCommitInput) (*repository.SynchronousRememberCommitResult, error)
	CommitSynchronousRememberTerminal(context.Context, repository.SynchronousRememberTerminalInput) (*repository.SynchronousRememberCommitResult, error)
	RecordSynchronousRememberPreflightQuarantine(context.Context, repository.RememberAttemptRecordInput) error
	RecordSynchronousRememberRejectedAttempt(context.Context, repository.RememberAttemptRecordInput) error
	RecordRememberFailure(context.Context, repository.RememberFailureRecordInput) error
}

type SynchronousRememberProcessorDependencies struct {
	Ledger     SynchronousRememberLedger
	Catalog    memoryservice.SubmissionAssessmentCatalog
	Provider   assessor.Provider
	Limits     assessor.SemanticAssessmentLimits
	Embeddings *semanticwrite.Executor
	Auditor    remember.SecurityRejectionAuditor
	Metrics    observability.DiscoverabilityMetrics
	Logger     observability.LogProvider
	// BeforeCommit is an optional composition hook used only by the disposable
	// E2E runtime to inject a deterministic post-embedding fence race.
	BeforeCommit func(context.Context, remember.RememberProcessRequest, *repository.SynchronousRememberEmbeddingPlan) error
}

type synchronousRememberProcessor struct {
	ledger       SynchronousRememberLedger
	catalog      memoryservice.SubmissionAssessmentCatalog
	provider     assessor.Provider
	limits       assessor.SemanticAssessmentLimits
	embeddings   *semanticwrite.Executor
	auditor      remember.SecurityRejectionAuditor
	metrics      observability.DiscoverabilityMetrics
	logger       observability.LogProvider
	beforeCommit func(context.Context, remember.RememberProcessRequest, *repository.SynchronousRememberEmbeddingPlan) error
}

var _ remember.SynchronousProcessor = (*synchronousRememberProcessor)(nil)

func NewSynchronousRememberProcessor(deps SynchronousRememberProcessorDependencies) remember.SynchronousProcessor {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &synchronousRememberProcessor{ledger: deps.Ledger, catalog: deps.Catalog, provider: deps.Provider, limits: deps.Limits, embeddings: deps.Embeddings, auditor: deps.Auditor, metrics: metrics, logger: deps.Logger, beforeCommit: deps.BeforeCommit}
}

func (p *synchronousRememberProcessor) ProcessRemember(ctx context.Context, input remember.RememberProcessRequest) (*remember.SubmissionStatusResult, error) {
	if p == nil || p.ledger == nil || p.catalog == nil || p.provider == nil || p.embeddings == nil {
		return nil, errors.New("remember: synchronous processor dependencies are required")
	}
	started := time.Now()
	attemptID := uuid.NewString()
	relationshipRefs := synchronousRelationshipRefs(input.Proposal)
	base := synchronousTerminalBase(input, attemptID, relationshipRefs)

	replay, err := p.ledger.LoadRememberAttempt(ctx, repository.RememberAttemptLookupInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey})
	if err == nil && replay != nil {
		if !domain.RememberRequestHashMatches(replay.RequestHash, replay.ContractVersion, input.RequestHash, input.MigratedRequestHash) {
			if input.SecurityRejected && input.SecurityRejectionAudit != nil {
				commitCtx, cancelCommit := remember.ContextForPhase(ctx, remember.RememberPhaseCommit)
				auditErr := remember.RecordSecurityRejectionAudit(commitCtx, p.auditor, p.logger, *input.SecurityRejectionAudit)
				cancelCommit()
				if auditErr != nil {
					return nil, remember.ErrRememberPersistence
				}
			}
			return nil, remember.ErrRememberConflict
		}
		if replay.Outcome == "completed" || replay.Outcome == "rejected" || replay.Outcome == "quarantined" || replay.Outcome == "replayed" {
			return p.replayStatus(ctx, input, base, replay)
		}
	}
	if err != nil && !errors.Is(err, repository.ErrRememberAttemptNotFound) {
		if input.SecurityRejected && input.SecurityRejectionAudit != nil {
			auditCtx, cancelAudit := remember.ContextForPhase(ctx, remember.RememberPhaseCommit)
			auditErr := remember.RecordSecurityRejectionAudit(auditCtx, p.auditor, p.logger, *input.SecurityRejectionAudit)
			cancelAudit()
			if auditErr != nil {
				return p.failure(ctx, input, attemptID, base, "commit", 0, started, remember.ErrRememberPersistence)
			}
		}
		return p.failure(ctx, input, attemptID, base, "commit", 0, started, err)
	}

	if input.SecurityRejected {
		failure := remember.TerminalResultWithError(base, remember.TerminalErrorQuarantined)
		commitCtx, cancelCommit := remember.ContextForPhase(ctx, remember.RememberPhaseCommit)
		if input.SecurityRejectionAudit != nil {
			if err := remember.RecordSecurityRejectionAudit(commitCtx, p.auditor, p.logger, *input.SecurityRejectionAudit); err != nil {
				cancelCommit()
				return p.failure(ctx, input, attemptID, base, "commit", 0, started, err)
			}
		}
		err := p.ledger.RecordSynchronousRememberPreflightQuarantine(commitCtx, synchronousAttempt(input, attemptID, failure.Result, "quarantined", "preflight", string(remember.TerminalErrorQuarantined), 0, started))
		cancelCommit()
		if errors.Is(err, repository.ErrRememberReplay) {
			return p.replayStatus(ctx, input, base, nil)
		}
		if err != nil {
			return p.failure(ctx, input, attemptID, base, "commit", 0, started, err)
		}
		return terminalStatus(failure.Result)
	}

	create := repository.CreateIngestInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey, RequestHash: input.RequestHash, SourceSummary: input.SourceSummary, Status: "queued", TelemetryRemember: true, Proposal: input.Proposal, Metadata: input.Metadata, Evidence: synchronousEvidence(input.Evidence)}
	if err := repository.ValidateCreateIngestInput(create); err != nil {
		return nil, fmt.Errorf("remember: invalid input: %w", err)
	}

	assessmentCtx, cancelAssessment := remember.ContextForPhase(ctx, remember.RememberPhaseAssessment)
	prepared, err := memoryservice.AssessSynchronousRemember(assessmentCtx, memoryservice.SynchronousRememberAssessmentDependencies{Catalog: p.catalog, Provider: p.provider, Limits: p.limits, Metrics: p.metrics, Logger: p.logger}, memoryservice.SynchronousRememberAssessmentInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: attemptID, Proposal: input.Proposal, Evidence: synchronousEvidence(input.Evidence)})
	cancelAssessment()
	if err != nil {
		return p.failure(ctx, input, attemptID, base, "assessment", synchronousAssessmentTurns(err), started, err)
	}
	if len(prepared.Response.SecuritySignals) > 0 {
		return p.commitTerminal(ctx, input, attemptID, base, create, prepared, "quarantined", nil, relationshipRefs, started)
	}
	preview, err := memoryservice.BuildSynchronousRememberPreviewCommitInput(prepared)
	if err != nil {
		var noSupported *memoryservice.NoSupportedMemoryError
		if errors.As(err, &noSupported) {
			return p.commitTerminal(ctx, input, attemptID, base, create, prepared, "rejected", noSupported.RelationshipResults, relationshipRefs, started)
		}
		return p.failure(ctx, input, attemptID, base, "assessment", prepared.Response.ProviderTurns, started, err)
	}
	embeddingCtx, cancelEmbedding := remember.ContextForPhase(ctx, remember.RememberPhaseEmbedding)
	plan, err := p.ledger.PlanSynchronousRememberEmbeddings(embeddingCtx, create, preview)
	if err != nil {
		cancelEmbedding()
		return p.failure(ctx, input, attemptID, base, "embedding", prepared.Response.ProviderTurns, started, err)
	}
	embedded, err := p.embeddings.Execute(embeddingCtx, semanticwritePlan(plan))
	cancelEmbedding()
	if err != nil {
		return p.failure(ctx, input, attemptID, base, "embedding", prepared.Response.ProviderTurns, started, err)
	}
	if p.beforeCommit != nil {
		if err := p.beforeCommit(ctx, input, plan); err != nil {
			return p.failure(ctx, input, attemptID, base, "commit", prepared.Response.ProviderTurns, started, err)
		}
	}
	inline := synchronousEmbeddingResult(embedded)
	commitCtx, cancelCommit := remember.ContextForPhase(ctx, remember.RememberPhaseCommit)
	defer cancelCommit()
	var durableResults []repository.SubmissionRelationshipResultInput
	var durableObservations []repository.SubmissionAssessmentRelationshipObservationInput
	committed, err := p.ledger.CommitSynchronousRemember(commitCtx, repository.SynchronousRememberCommitInput{
		CreateIngest:     create,
		Attempt:          synchronousAttempt(input, attemptID, nil, "completed", "", "", prepared.Response.ProviderTurns, started),
		InlineEmbeddings: &inline,
		BuildCommit: func(created *repository.CreateIngestResult, scope repository.SubmissionAssessmentRunScope) (repository.PersistSubmissionAssessmentInput, repository.CommitSubmissionAssessmentInput, error) {
			persist, err := memoryservice.BuildSynchronousRememberAssessmentPersistenceInput(created, prepared)
			if err != nil {
				return repository.PersistSubmissionAssessmentInput{}, repository.CommitSubmissionAssessmentInput{}, err
			}
			commit, err := memoryservice.BuildSynchronousRememberCommitInput(created, repository.PlacementRun{TeamID: created.TeamID, OwnerProfileID: created.OwnerProfileID, IngestID: created.IngestID, PlacementRunID: created.PlacementRunID}, scope, prepared.Response, &repository.SubmissionAssessment{AssessmentID: uuid.NewString(), RequestID: prepared.Request.RequestID, Model: prepared.Model, ResponseHash: persist.ResponseHash})
			durableResults = append([]repository.SubmissionRelationshipResultInput(nil), commit.RelationshipResults...)
			durableObservations = append([]repository.SubmissionAssessmentRelationshipObservationInput(nil), commit.RelationshipObservations...)
			return persist, commit, err
		},
		BuildPublicResult: func(created *repository.CreateIngestResult, result *repository.CommitSubmissionAssessmentResult) (map[string]any, error) {
			terminal := synchronousCompletedResult(input, created, result, durableResults, durableObservations, relationshipRefs)
			if err := remember.ValidateTerminalRememberResult(terminal, len(input.Evidence), relationshipRefs); err != nil {
				return nil, err
			}
			return terminalMap(terminal)
		},
	})
	if errors.Is(err, repository.ErrRememberReplay) {
		var winner *repository.RememberAttempt
		if committed != nil {
			winner = committed.Attempt
		}
		return p.replayStatus(ctx, input, base, winner)
	}
	if err != nil {
		return p.failure(ctx, input, attemptID, base, "commit", prepared.Response.ProviderTurns, started, err)
	}
	if committed == nil || committed.Attempt == nil {
		return p.failure(ctx, input, attemptID, base, "commit", prepared.Response.ProviderTurns, started, errors.New("remember: synchronous commit result is required"))
	}
	return synchronousAttemptStatus(committed.Attempt, input)
}

func (p *synchronousRememberProcessor) failure(ctx context.Context, input remember.RememberProcessRequest, attemptID string, base *remember.TerminalRememberResult, phase string, turns int, started time.Time, cause error) (*remember.SubmissionStatusResult, error) {
	code := synchronousFailureCode(phase, cause)
	if p.logger != nil {
		attrs := []observability.LogAttr{
			observability.String("phase", phase), observability.String("error_code", string(code)),
			observability.String("team_id", input.TeamID), observability.ProfileID(input.OwnerProfileID),
			observability.String("attempt_id", attemptID), observability.CorrelationID(synchronousCorrelation(input.Metadata)),
		}
		if stage := synchronousRememberCommitStage(cause); stage != "" {
			attrs = append(attrs, observability.String("commit_stage", stage))
		}
		if stage := synchronousRememberEmbeddingStage(cause); stage != "" {
			attrs = append(attrs, observability.String("embedding_stage", stage))
		}
		p.logger.Error("synchronous_remember_failed", errors.New("synchronous Remember failed"), attrs...)
	}
	failure := remember.TerminalResultWithError(base, code)
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), remember.RememberFailurePersistenceBudget)
	defer cancelRecord()
	attemptOutcome := "failed"
	if code == remember.TerminalErrorStaleInput {
		attemptOutcome = "rejected"
	}
	attempt := synchronousAttempt(input, attemptID, failure.Result, attemptOutcome, phase, string(code), turns, started)
	var recordErr error
	if attemptOutcome == "rejected" {
		recordErr = p.ledger.RecordSynchronousRememberRejectedAttempt(recordCtx, attempt)
	} else {
		recordErr = p.ledger.RecordRememberFailure(recordCtx, repository.RememberFailureRecordInput{
			Attempt:   attempt,
			Artifacts: []repository.RememberFailureArtifactInput{rememberFailureArtifact(phase, string(code))},
		})
	}
	if recordErr != nil {
		if errors.Is(recordErr, repository.ErrRememberReplay) {
			return p.replayStatus(recordCtx, input, base, nil)
		}
		if errors.Is(recordErr, repository.ErrIdempotencyConflict) {
			return nil, remember.ErrRememberConflict
		}
		return nil, fmt.Errorf("%w: terminal failure record unavailable", remember.ErrRememberPersistence)
	}
	status, _ := terminalStatus(failure.Result)
	return nil, &remember.RememberProcessError{Status: status, Result: failure.Result, Err: cause}
}

func rememberFailureArtifact(phase, errorCode string) repository.RememberFailureArtifactInput {
	encoded, _ := json.Marshal(struct {
		Phase     string `json:"phase"`
		ErrorCode string `json:"error_code"`
	}{Phase: phase, ErrorCode: errorCode})
	return repository.RememberFailureArtifactInput{
		ArtifactKind: "failure",
		ContentType:  "application/json",
		Content:      encoded,
	}
}

func (p *synchronousRememberProcessor) replayStatus(ctx context.Context, input remember.RememberProcessRequest, base *remember.TerminalRememberResult, winner *repository.RememberAttempt) (*remember.SubmissionStatusResult, error) {
	if winner == nil {
		var err error
		winner, err = p.ledger.LoadRememberAttempt(ctx, repository.RememberAttemptLookupInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IdempotencyKey: input.IdempotencyKey})
		if err != nil {
			return synchronousReplayFailure(base, err)
		}
	}
	status, err := synchronousAttemptStatus(winner, input)
	if err != nil {
		return synchronousReplayFailure(base, err)
	}
	return status, nil
}

func synchronousReplayFailure(base *remember.TerminalRememberResult, cause error) (*remember.SubmissionStatusResult, error) {
	code := remember.TerminalErrorDatabaseFailure
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		code = remember.TerminalErrorRequestTimeout
	case errors.Is(cause, context.Canceled):
		code = remember.TerminalErrorRequestCancelled
	}
	failure := remember.TerminalResultWithError(base, code)
	status, _ := terminalStatus(failure.Result)
	return nil, &remember.RememberProcessError{Status: status, Result: failure.Result, Err: cause}
}

type synchronousRememberCommitStager interface {
	SynchronousRememberCommitStage() string
}

func synchronousRememberCommitStage(err error) string {
	var staged synchronousRememberCommitStager
	if errors.As(err, &staged) {
		return strings.TrimSpace(staged.SynchronousRememberCommitStage())
	}
	return ""
}

type synchronousRememberEmbeddingStager interface {
	SynchronousRememberEmbeddingStage() string
}

func synchronousRememberEmbeddingStage(err error) string {
	var staged synchronousRememberEmbeddingStager
	if errors.As(err, &staged) {
		return strings.TrimSpace(staged.SynchronousRememberEmbeddingStage())
	}
	return ""
}

func (p *synchronousRememberProcessor) commitTerminal(ctx context.Context, input remember.RememberProcessRequest, attemptID string, base *remember.TerminalRememberResult, create repository.CreateIngestInput, prepared *memoryservice.SynchronousRememberAssessmentResult, outcome string, rejected []repository.SubmissionRelationshipResultInput, refs []string, started time.Time) (*remember.SubmissionStatusResult, error) {
	commitCtx, cancel := remember.ContextForPhase(ctx, remember.RememberPhaseCommit)
	defer cancel()
	terminalCode := remember.TerminalErrorQuarantined
	if outcome == "rejected" {
		terminalCode = remember.TerminalErrorNoSupportedMemory
	}
	terminal, err := p.ledger.CommitSynchronousRememberTerminal(commitCtx, repository.SynchronousRememberTerminalInput{
		CreateIngest: create,
		Attempt:      synchronousAttempt(input, attemptID, nil, outcome, "commit", string(terminalCode), prepared.Response.ProviderTurns, started),
		BuildTerminal: func(created *repository.CreateIngestResult, scope repository.SubmissionAssessmentRunScope) (*repository.PersistSubmissionAssessmentInput, repository.CompleteSubmissionAssessmentInput, error) {
			persist, err := memoryservice.BuildSynchronousRememberAssessmentPersistenceInput(created, prepared)
			if err != nil {
				return nil, repository.CompleteSubmissionAssessmentInput{}, err
			}
			if outcome == "rejected" {
				return &persist, memoryservice.BuildSynchronousRememberRejectedTerminalInput(scope, rejected), nil
			}
			complete, err := memoryservice.BuildSynchronousRememberQuarantineTerminalInput(created, prepared.Response, scope)
			return &persist, complete, err
		},
		BuildPublicResult: func(created *repository.CreateIngestResult, _ *repository.CommitSubmissionAssessmentResult) (map[string]any, error) {
			result := synchronousTerminalOutcome(input, created, refs, outcome, rejected)
			if err := remember.ValidateTerminalRememberResult(result, len(input.Evidence), refs); err != nil {
				return nil, err
			}
			return terminalMap(result)
		},
	})
	if errors.Is(err, repository.ErrRememberReplay) {
		var winner *repository.RememberAttempt
		if terminal != nil {
			winner = terminal.Attempt
		}
		return p.replayStatus(ctx, input, base, winner)
	}
	if err != nil {
		return p.failure(ctx, input, attemptID, base, "commit", prepared.Response.ProviderTurns, started, err)
	}
	if terminal == nil || terminal.Attempt == nil {
		return p.failure(ctx, input, attemptID, base, "commit", prepared.Response.ProviderTurns, started, errors.New("remember: terminal commit result is required"))
	}
	return synchronousAttemptStatus(terminal.Attempt, input)
}

func synchronousAssessmentTurns(err error) int {
	turns := memoryservice.SynchronousRememberAssessmentConsumedProviderTurns(err)
	var malformed *assessor.MalformedResponseError
	if errors.As(err, &malformed) && malformed.Attempts > turns {
		turns = malformed.Attempts
	}
	return turns
}

func synchronousFailureCode(phase string, err error) remember.TerminalErrorCode {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return remember.TerminalErrorRequestTimeout
	case errors.Is(err, context.Canceled):
		return remember.TerminalErrorRequestCancelled
	case errors.Is(err, repository.ErrIdempotencyConflict):
		return remember.TerminalErrorIdempotencyConflict
	case errors.Is(err, repository.ErrPlacementStaleSource), memoryservice.IsRememberStaleInputError(err):
		return remember.TerminalErrorStaleInput
	case errors.Is(err, repository.ErrSynchronousRememberEmbeddingInputBudget):
		return remember.TerminalErrorInputBudgetExceeded
	case memoryservice.IsSemanticAssessmentInputBudgetError(err):
		return remember.TerminalErrorInputBudgetExceeded
	case errors.Is(err, repository.ErrSearchContractMismatch):
		return remember.TerminalErrorConfigurationInvalid
	case errors.Is(err, repository.ErrSynchronousRememberEmbeddingFence) && phase == "commit":
		return remember.TerminalErrorCommitConflict
	case errors.Is(err, repository.ErrSynchronousRememberEmbeddingFence):
		return remember.TerminalErrorInternalFailure
	case errors.Is(err, semanticwrite.ErrProviderUnavailable):
		return remember.TerminalErrorEmbeddingUnavailable
	case errors.Is(err, semanticwrite.ErrProviderResponseInvalid), errors.Is(err, semanticwrite.ErrInvalidPlan):
		return remember.TerminalErrorEmbeddingResponseInvalid
	case errors.Is(err, assessor.ErrVerifierMalformedResponse):
		return remember.TerminalErrorProviderResponseInvalid
	case memoryservice.IsRepositoryDatabaseFailure(err):
		return remember.TerminalErrorDatabaseFailure
	case phase == "assessment":
		return remember.TerminalErrorProviderUnavailable
	case phase == "embedding":
		return remember.TerminalErrorEmbeddingUnavailable
	case phase == "commit":
		return remember.TerminalErrorCommitConflict
	default:
		return remember.TerminalErrorInternalFailure
	}
}

func synchronousAttempt(input remember.RememberProcessRequest, id string, result *remember.TerminalRememberResult, outcome, phase, code string, turns int, started time.Time) repository.RememberAttemptRecordInput {
	public := map[string]any{}
	if result != nil {
		public, _ = terminalMap(result)
	}
	return repository.RememberAttemptRecordInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, AttemptID: id, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey, RequestHash: input.RequestHash, MigratedRequestHash: input.MigratedRequestHash, ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: outcome, FailedPhase: phase, ErrorCode: code, CorrelationID: synchronousCorrelation(input.Metadata), PublicResult: public, EvidenceCount: len(input.Evidence), RelationshipCount: len(synchronousRelationshipRefs(input.Proposal)), AssessorTurns: turns, Duration: time.Since(started)}
}

func synchronousEvidence(input []remember.EvidenceInput) []repository.EvidenceInput {
	out := make([]repository.EvidenceInput, len(input))
	for i, item := range input {
		out[i] = repository.EvidenceInput{Content: item.Content, ContentHash: item.ContentHash, SourceType: item.SourceType, Authority: item.Authority, SourceRef: item.SourceRef, SourceKey: item.SourceKey, SourceRevisionToken: item.SourceRevisionToken, ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken, SourceRevisionContentHash: item.SourceRevisionContentHash, SourceRevisionEnvelope: item.SourceRevisionEnvelope, SupersedesEvidenceIDs: append([]string(nil), item.SupersedesEvidenceIDs...), Labels: append([]string(nil), item.Labels...), Metadata: item.Metadata, InitialEvent: synchronousSecurityEventDraft(item.InitialEvent)}
	}
	return out
}

func synchronousSecurityEventDraft(input *remember.SecurityEventDraft) *repository.SecurityEventDraft {
	if input == nil {
		return nil
	}
	event := &repository.SecurityEventDraft{EventKind: input.EventKind, Decision: input.Decision, Reason: input.Reason, Metadata: input.Metadata}
	event.Signals = make([]repository.SecuritySignalInput, 0, len(input.Signals))
	for _, signal := range input.Signals {
		event.Signals = append(event.Signals, repository.SecuritySignalInput{Kind: signal.Kind, Severity: signal.Severity, SpanStart: signal.SpanStart, SpanEnd: signal.SpanEnd, Metadata: signal.Metadata})
	}
	return event
}

func semanticwritePlan(plan *repository.SynchronousRememberEmbeddingPlan) semanticwrite.Plan {
	docs := make([]semanticwrite.Document, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		docs = append(docs, semanticwrite.Document{Hash: document.Hash, Text: document.Text})
	}
	return semanticwrite.Plan{Documents: docs, Fence: semanticwrite.Fence{Model: plan.EmbeddingModel, Dimensions: plan.EmbeddingDimensions, EmbeddingContractID: plan.EmbeddingContractID, SearchGenerationID: plan.SearchGenerationID, SearchGenerationVersion: plan.SearchGenerationVersion}, Timeout: remember.RememberEmbeddingBudget}
}
func synchronousEmbeddingResult(result semanticwrite.Result) repository.SynchronousRememberEmbeddingResult {
	items := make([]repository.SynchronousRememberEmbedding, 0, len(result.Embeddings))
	for _, item := range result.Embeddings {
		items = append(items, repository.SynchronousRememberEmbedding{DocumentHash: item.DocumentHash, Vector: item.Vector})
	}
	return repository.SynchronousRememberEmbeddingResult{EmbeddingContractID: result.Fence.EmbeddingContractID, EmbeddingDimensions: result.Fence.Dimensions, EmbeddingModel: result.Fence.Model, SearchGenerationID: result.Fence.SearchGenerationID, SearchGenerationVersion: result.Fence.SearchGenerationVersion, Embeddings: items}
}

func synchronousTerminalBase(input remember.RememberProcessRequest, attemptID string, refs []string) *remember.TerminalRememberResult {
	evidence := make([]remember.TerminalEvidenceResult, len(input.Evidence))
	for i := range evidence {
		evidence[i] = remember.TerminalEvidenceResult{EvidenceIndex: i, SupersededEvidenceIDs: []string{}}
	}
	relationships := make([]remember.SubmissionRelationshipResult, len(refs))
	for i, ref := range refs {
		relationships[i] = remember.SubmissionRelationshipResult{RelationshipRef: ref, Splits: []remember.SubmissionRelationshipSplit{}}
	}
	return &remember.TerminalRememberResult{ContractVersion: domain.ContractVersion, SubmissionID: attemptID, SubmissionKind: "remember", CorrelationID: synchronousCorrelation(input.Metadata), Evidence: evidence, RelationshipResults: relationships, Errors: []remember.SubmissionStatusError{}, Kind: remember.ResultKindTerminal}
}
func synchronousCorrelation(metadata map[string]any) string {
	actor, _ := metadata["actor"].(map[string]any)
	value, _ := actor["correlation_id"].(string)
	return strings.TrimSpace(value)
}
func synchronousRelationshipRefs(proposal map[string]any) []string {
	raw, _ := proposal["relationship_hints"].([]map[string]any)
	refs := make([]string, 0, len(raw))
	for i, item := range raw {
		ref, _ := item["ref"].(string)
		ref = strings.TrimSpace(ref)
		if ref == "" {
			ref = fmt.Sprintf("relationship:%d", i)
		}
		refs = append(refs, ref)
	}
	return refs
}
func terminalMap(result *remember.TerminalRememberResult) (map[string]any, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(raw, &out)
	return out, err
}
func terminalStatus(result *remember.TerminalRememberResult) (*remember.SubmissionStatusResult, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var out remember.SubmissionStatusResult
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out.Kind = remember.ResultKindTerminal
	out.Terminal = result
	return &out, nil
}
func synchronousAttemptStatus(attempt *repository.RememberAttempt, input remember.RememberProcessRequest) (*remember.SubmissionStatusResult, error) {
	if attempt == nil {
		return nil, errors.New("remember: replay attempt is required")
	}
	raw, err := json.Marshal(attempt.PublicResult)
	if err != nil {
		return nil, err
	}
	var status remember.SubmissionStatusResult
	if err = json.Unmarshal(raw, &status); err != nil {
		return nil, err
	}
	status.Kind = remember.ResultKindTerminal
	var terminal remember.TerminalRememberResult
	if err := json.Unmarshal(raw, &terminal); err != nil {
		return nil, err
	}
	terminal.Kind = remember.ResultKindTerminal
	terminal.RelationshipResults = reorderSynchronousRelationshipResults(terminal.RelationshipResults, synchronousRelationshipRefs(input.Proposal))
	status.RelationshipResults = terminal.RelationshipResults
	status.Terminal = &terminal
	return &status, nil
}

func reorderSynchronousRelationshipResults(results []remember.SubmissionRelationshipResult, refs []string) []remember.SubmissionRelationshipResult {
	if len(results) != len(refs) {
		return results
	}
	byRef := make(map[string]remember.SubmissionRelationshipResult, len(results))
	for _, result := range results {
		ref := strings.TrimSpace(result.RelationshipRef)
		if ref == "" {
			return results
		}
		if _, exists := byRef[ref]; exists {
			return results
		}
		byRef[ref] = result
	}
	seenRefs := make(map[string]struct{}, len(refs))
	reordered := make([]remember.SubmissionRelationshipResult, len(refs))
	for index, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return results
		}
		if _, exists := seenRefs[ref]; exists {
			return results
		}
		seenRefs[ref] = struct{}{}
		result, exists := byRef[ref]
		if !exists {
			return results
		}
		result.RelationshipRef = ref
		reordered[index] = result
	}
	return reordered
}

func synchronousCompletedResult(input remember.RememberProcessRequest, created *repository.CreateIngestResult, committed *repository.CommitSubmissionAssessmentResult, durable []repository.SubmissionRelationshipResultInput, observations []repository.SubmissionAssessmentRelationshipObservationInput, refs []string) *remember.TerminalRememberResult {
	base := synchronousTerminalBase(input, created.IngestID, refs)
	base.ProcessingState = string(remember.TerminalProcessingCompleted)
	base.SearchState = string(remember.TerminalSearchCurrent)
	for i, evidence := range created.Evidence {
		base.Evidence[i] = remember.TerminalEvidenceResult{Disposition: "stored", EvidenceID: evidence.FragmentID, EvidenceIndex: i, SupersededEvidenceIDs: append([]string{}, evidence.SupersededEvidenceIDs...), SearchState: string(remember.TerminalSearchCurrent)}
	}
	byRef := make(map[string]repository.SubmissionRelationshipResultInput, len(durable))
	for _, result := range durable {
		byRef[result.RelationshipRef] = result
	}
	byObservation := make(map[string]repository.SubmissionAssessmentRelationshipObservationInput, len(observations))
	for _, observation := range observations {
		byObservation[observation.Observation.Ref] = observation
	}
	indexByRef := make(map[string]int, len(base.RelationshipResults))
	for index, result := range base.RelationshipResults {
		indexByRef[result.RelationshipRef] = index
		result, ok := byRef[result.RelationshipRef]
		if !ok {
			base.RelationshipResults[index].Disposition = "not_stored"
			base.RelationshipResults[index].Reason = "not_supported_by_evidence"
			continue
		}
		base.RelationshipResults[index].Disposition = result.Disposition
		base.RelationshipResults[index].Reason = result.Reason
	}
	for _, result := range committed.RelationshipResults {
		observation, ok := byObservation[result.ProposalID]
		if !ok || result.Relationship == nil {
			continue
		}
		index, ok := indexByRef[observation.RelationshipRef]
		if !ok {
			continue
		}
		base.RelationshipResults[index].Splits = append(base.RelationshipResults[index].Splits, remember.SubmissionRelationshipSplit{SplitIndex: observation.SplitIndex, RelationshipID: result.Relationship.RelationshipID, RelationshipVersion: result.Relationship.Version, Status: result.Relationship.Status})
		base.RelationshipResults[index].Disposition = "stored"
	}
	for index := range base.RelationshipResults {
		sort.Slice(base.RelationshipResults[index].Splits, func(left, right int) bool {
			return base.RelationshipResults[index].Splits[left].SplitIndex < base.RelationshipResults[index].Splits[right].SplitIndex
		})
	}
	return base
}

func synchronousTerminalOutcome(input remember.RememberProcessRequest, created *repository.CreateIngestResult, refs []string, outcome string, relationshipResults []repository.SubmissionRelationshipResultInput) *remember.TerminalRememberResult {
	base := synchronousTerminalBase(input, created.IngestID, refs)
	base.ProcessingState = outcome
	base.SearchState = string(remember.TerminalSearchNotRequired)
	reason := "not_supported_by_evidence"
	if outcome == "quarantined" {
		reason = "security_quarantine"
	}
	for index := range created.Evidence {
		base.Evidence[index] = remember.TerminalEvidenceResult{Disposition: "not_stored", EvidenceIndex: index, SupersededEvidenceIDs: []string{}, SearchState: string(remember.TerminalSearchNotRequired), Reason: reason}
	}
	byRef := make(map[string]repository.SubmissionRelationshipResultInput, len(relationshipResults))
	for _, result := range relationshipResults {
		byRef[result.RelationshipRef] = result
	}
	for index, ref := range refs {
		base.RelationshipResults[index].Disposition = "not_stored"
		base.RelationshipResults[index].Reason = reason
		if stored, ok := byRef[ref]; ok {
			base.RelationshipResults[index].Disposition = stored.Disposition
			base.RelationshipResults[index].Reason = stored.Reason
		}
	}
	if outcome == "quarantined" {
		base.Errors = []remember.SubmissionStatusError{remember.TerminalStatusError(remember.TerminalErrorQuarantined)}
	} else {
		base.Errors = []remember.SubmissionStatusError{remember.TerminalStatusError(remember.TerminalErrorNoSupportedMemory)}
	}
	return base
}
