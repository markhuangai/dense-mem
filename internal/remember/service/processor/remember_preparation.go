package processor

import (
	"context"
	"errors"
	"fmt"
	"time"

	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
)

type preparedRemember struct {
	Commit        repository.SynchronousRememberCommitInput
	Embeddings    []repository.InlineEmbeddingResult
	AssessorTurns int
}

type rememberPreparationError struct {
	Phase                    string
	AssessorTurns            int
	AssessorSecurityRejected bool
	Cause                    error
}

func (e *rememberPreparationError) Error() string { return "remember preparation failed" }
func (e *rememberPreparationError) Unwrap() error { return e.Cause }

func (p *rememberSynchronousProcessor) prepareRemember(ctx context.Context, input rememberapp.RememberProcessRequest, snapshot rememberapp.RememberAssessmentSnapshot, scope rememberapp.RememberAssessmentScope, started time.Time) (*preparedRemember, error) {
	ingestID := scope.IngestID
	assessorTurns := 0
	fail := func(err error, phase string) (*preparedRemember, error) {
		return nil, &rememberPreparationError{Phase: phase, AssessorTurns: assessorTurns, AssessorSecurityRejected: input.AssessorSecurityRejected, Cause: err}
	}
	duplicateInput := repository.RememberDuplicateCandidateInput{
		MaxEvidenceItems: snapshot.MaxEvidenceItems,
		TeamID:           input.TeamID, OwnerProfileID: input.OwnerProfileID,
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
		return fail(rememberapp.AssessmentSecurityRejectionFailure(prepared), "assessment")
	}
	embeddingStarted = time.Now()
	embeddingCtx, embeddingCancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseEmbedding)
	defer embeddingCancel()
	plan, err := p.ledger.PlanRememberEmbeddings(embeddingCtx, commitInput)
	if err != nil {
		observability.RecordRememberPhase(p.metrics, "embedding", rememberMetricPhaseOutcome(err), time.Since(embeddingStarted))
		if errors.Is(err, repository.ErrSubmissionPredicateRegistrationHeld) && len(commitInput.Commit.PredicateRegistrations) > 0 {
			err = fmt.Errorf("%w: predicate catalog changed before embedding planning: %w", rememberapp.ErrRememberCommitConflict, err)
		}
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
	return &preparedRemember{Commit: commitInput, Embeddings: inlineEmbeddings, AssessorTurns: assessorTurns}, nil
}

func (p *rememberSynchronousProcessor) PrepareSession(ctx context.Context, req session.PrepareInput) (*session.Prepared, error) {
	if p == nil || p.ledger == nil {
		return nil, errors.New("session: preparation dependencies are required")
	}
	started := time.Now()
	input := rememberapp.RememberProcessRequest{
		InvocationStartedAt: started, TeamID: req.Scope.TeamID, OwnerProfileID: req.Scope.OwnerProfileID,
		SpaceID: req.Scope.SpaceID, SpaceGeneration: req.Scope.SpaceGeneration,
		IdempotencyKey: "session:" + req.SubmissionID, RequestHash: req.RequestHash,
		SourceSummary: "private session user events", Proposal: req.Proposal, Metadata: req.Metadata, Evidence: req.Evidence,
	}
	snapshot, scope := rememberAssessmentSnapshot(input, req.SubmissionID)
	snapshot.MaxEvidenceItems = session.MaxExcerpts
	prepared, err := p.prepareRemember(ctx, input, snapshot, scope, started)
	if err != nil {
		var failure *rememberPreparationError
		phase := "assessment"
		if errors.As(err, &failure) {
			phase = failure.Phase
		}
		return nil, &session.ProcessingError{Code: string(rememberFailureCode(phase, err)), Cause: err}
	}
	return &session.Prepared{Commit: prepared.Commit, Embeddings: prepared.Embeddings}, nil
}

var _ session.Preparer = (*rememberSynchronousProcessor)(nil)
