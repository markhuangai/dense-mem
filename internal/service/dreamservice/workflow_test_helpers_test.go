package dreamservice

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func dreamTestContext(teamID uuid.UUID, ownerID uuid.UUID) context.Context {
	return requestctx.WithActorCredential(
		requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
			TeamID:    teamID,
			ProfileID: ownerID,
		}),
		requestctx.ActorCredential{
			KeyID:      uuid.New(),
			AuthMethod: "api_key",
			Role:       "member",
		},
	)
}

type dreamRepositoryStub struct {
	inputs          []repository.DreamInput
	predicates      []repository.DreamTargetPredicate
	unassessedPaths []repository.DreamPathEvaluationInput
	pathEvaluations repository.DreamPathEvaluationRecordInput
	run             repository.DreamCycleRun
	getRecord       repository.HypothesisRecord
	listRecords     []repository.HypothesisRecord
	recallRecords   []repository.HypothesisRecord
	listInput       repository.DreamInputListInput
	claimInput      repository.DreamCycleClaimInput
	completeInput   repository.DreamCycleCompleteInput
	missedInput     repository.DreamCycleClaimInput
	upserts         []repository.UpsertHypothesisInput
	submitInput     repository.SubmitHypothesisInput
	updateInput     repository.UpdateHypothesisStatusInput
	err             error
	claimErr        error
	completeErr     error
	listInputsErr   error
	targetsErr      error
	pathAssessErr   error
	upsertErr       error
	listErr         error
	getErr          error
	recallErr       error
	updateErr       error
	submitErr       error
	latestErr       error
}

func (s *dreamRepositoryStub) ClaimDreamCycle(_ context.Context, input repository.DreamCycleClaimInput) (*repository.DreamCycleRun, error) {
	s.claimInput = input
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if s.err != nil {
		return nil, s.err
	}
	run := s.run
	if run.RunID == "" {
		run.RunID = uuid.NewString()
		run.TeamID = input.TeamID
		run.InitiatedByProfileID = input.InitiatedByProfileID
		run.RunDate = input.RunDate
		run.WindowKey = input.WindowKey
		run.Status = "running"
		run.Claimed = true
	}
	return &run, nil
}

func (s *dreamRepositoryStub) CompleteDreamCycle(_ context.Context, input repository.DreamCycleCompleteInput) error {
	s.completeInput = input
	if s.completeErr != nil {
		return s.completeErr
	}
	return s.err
}

func (s *dreamRepositoryStub) ListDreamInputs(_ context.Context, input repository.DreamInputListInput) ([]repository.DreamInput, error) {
	s.listInput = input
	if s.listInputsErr != nil {
		return nil, s.listInputsErr
	}
	return append([]repository.DreamInput(nil), s.inputs...), s.err
}

func (s *dreamRepositoryStub) ListDreamTargetPredicates(context.Context, string) ([]repository.DreamTargetPredicate, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]repository.DreamTargetPredicate(nil), s.predicates...), nil
}

func (s *dreamRepositoryStub) ListAvailableDreamTargets(_ context.Context, _ string, targets []repository.DreamTargetCandidate) ([]repository.DreamTargetCandidate, error) {
	if s.targetsErr != nil {
		return nil, s.targetsErr
	}
	if s.err != nil {
		return nil, s.err
	}
	return append([]repository.DreamTargetCandidate(nil), targets...), nil
}

func (s *dreamRepositoryStub) ListUnassessedDreamPaths(_ context.Context, _ string, paths []repository.DreamPathEvaluationInput) ([]repository.DreamPathEvaluationInput, error) {
	if s.pathAssessErr != nil {
		return nil, s.pathAssessErr
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.unassessedPaths != nil {
		return append([]repository.DreamPathEvaluationInput(nil), s.unassessedPaths...), nil
	}
	return append([]repository.DreamPathEvaluationInput(nil), paths...), nil
}

func (s *dreamRepositoryStub) RecordDreamPathEvaluations(_ context.Context, input repository.DreamPathEvaluationRecordInput) error {
	s.pathEvaluations = input
	return s.err
}

func (s *dreamRepositoryStub) PersistDreamGeneration(ctx context.Context, input repository.DreamGenerationPersistInput) (repository.DreamGenerationPersistResult, error) {
	result := repository.DreamGenerationPersistResult{}
	for _, proposal := range input.Proposals {
		_, inserted, err := s.UpsertHypothesis(ctx, proposal)
		if err != nil {
			if errors.Is(err, repository.ErrDreamExactRelationshipExists) ||
				errors.Is(err, repository.ErrDreamExactHypothesisExists) ||
				errors.Is(err, repository.ErrDreamSourceStale) {
				result.Rejected++
				continue
			}
			return repository.DreamGenerationPersistResult{}, err
		}
		if inserted {
			result.Created++
		}
	}
	if err := s.RecordDreamPathEvaluations(ctx, repository.DreamPathEvaluationRecordInput{
		TeamID:             input.TeamID,
		CreatedByProfileID: input.CreatedByProfileID,
		ProviderModel:      input.ProviderModel,
		Paths:              input.EvaluatedPaths,
	}); err != nil {
		return repository.DreamGenerationPersistResult{}, err
	}
	return result, nil
}

func (s *dreamRepositoryStub) UpsertHypothesis(_ context.Context, input repository.UpsertHypothesisInput) (*repository.HypothesisRecord, bool, error) {
	s.upserts = append(s.upserts, input)
	if s.upsertErr != nil {
		return nil, false, s.upsertErr
	}
	if s.err != nil {
		return nil, false, s.err
	}
	return &repository.HypothesisRecord{
		TeamID:             input.TeamID,
		HypothesisID:       uuid.NewString(),
		CreatedByProfileID: input.CreatedByProfileID,
		Status:             string(domain.DreamStatusProposed),
		Statement:          input.Statement,
		CycleRunID:         input.RunID,
		ContentHash:        input.ContentHash,
		SourceRefs:         input.SourceRefs,
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}, true, nil
}

func (s *dreamRepositoryStub) ListHypotheses(context.Context, repository.ListHypothesesInput) ([]repository.HypothesisRecord, string, error) {
	if s.listErr != nil {
		return nil, "", s.listErr
	}
	if s.err != nil {
		return nil, "", s.err
	}
	if len(s.listRecords) > 0 {
		return append([]repository.HypothesisRecord(nil), s.listRecords...), "", nil
	}
	if s.getRecord.HypothesisID == "" {
		return nil, "", nil
	}
	return []repository.HypothesisRecord{s.getRecord}, "", nil
}

func (s *dreamRepositoryStub) GetHypothesis(context.Context, repository.GetHypothesisInput) (*repository.HypothesisRecord, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.getRecord.HypothesisID == "" {
		return nil, repository.ErrDreamHypothesisNotFound
	}
	record := s.getRecord
	return &record, nil
}

func (s *dreamRepositoryStub) RecallHypotheses(context.Context, repository.RecallHypothesesInput) ([]repository.HypothesisRecord, error) {
	if s.recallErr != nil {
		return nil, s.recallErr
	}
	if s.err != nil {
		return nil, s.err
	}
	if len(s.recallRecords) > 0 {
		return append([]repository.HypothesisRecord(nil), s.recallRecords...), nil
	}
	if s.getRecord.HypothesisID == "" {
		return nil, nil
	}
	return []repository.HypothesisRecord{s.getRecord}, nil
}

func (s *dreamRepositoryStub) UpdateHypothesisStatus(_ context.Context, input repository.UpdateHypothesisStatusInput) (*repository.HypothesisRecord, error) {
	s.updateInput = input
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.err != nil {
		return nil, s.err
	}
	record := s.getRecord
	record.Status = input.Status
	record.InvalidatedReason = input.InvalidatedReason
	return &record, nil
}

func (s *dreamRepositoryStub) SubmitHypothesis(_ context.Context, input repository.SubmitHypothesisInput) (*repository.HypothesisRecord, error) {
	s.submitInput = input
	if s.submitErr != nil {
		return nil, s.submitErr
	}
	if s.err != nil {
		return nil, s.err
	}
	record := s.getRecord
	record.Status = string(domain.DreamStatusSubmitted)
	record.SubmittedIngestID = input.SubmittedIngestID
	record.InvalidatedReason = input.InvalidatedReason
	return &record, nil
}

func (s *dreamRepositoryStub) ListDreamCyclesForTeam(context.Context, string, int) ([]repository.DreamCycleRun, error) {
	if s.latestErr != nil {
		return nil, s.latestErr
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.run.RunID == "" {
		return nil, nil
	}
	return []repository.DreamCycleRun{s.run}, nil
}

func (s *dreamRepositoryStub) CountHypotheses(context.Context, string, string) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if s.getRecord.HypothesisID != "" && s.getRecord.Status == string(domain.DreamStatusProposed) {
		return 1, nil
	}
	return 0, nil
}

func (s *dreamRepositoryStub) ClaimScheduledDreamCycle(ctx context.Context, input repository.DreamCycleClaimInput) (*repository.DreamCycleRun, error) {
	return s.ClaimDreamCycle(ctx, input)
}

func (s *dreamRepositoryStub) ClaimRecoverableScheduledDreamCycle(context.Context, repository.DreamCycleRecoveryClaimInput) (*repository.DreamCycleRun, error) {
	return nil, nil
}

func (s *dreamRepositoryStub) CompleteScheduledDreamCycle(ctx context.Context, input repository.DreamCycleCompleteInput) error {
	return s.CompleteDreamCycle(ctx, input)
}

func (s *dreamRepositoryStub) RecordScheduledDreamPathEvaluations(ctx context.Context, input repository.DreamPathEvaluationRecordInput) error {
	return s.RecordDreamPathEvaluations(ctx, input)
}

func (s *dreamRepositoryStub) PersistScheduledDreamGeneration(ctx context.Context, input repository.DreamGenerationPersistInput) (repository.DreamGenerationPersistResult, error) {
	return s.PersistDreamGeneration(ctx, input)
}

func (s *dreamRepositoryStub) UpsertScheduledHypothesis(ctx context.Context, input repository.UpsertHypothesisInput) (*repository.HypothesisRecord, bool, error) {
	return s.UpsertHypothesis(ctx, input)
}

func (s *dreamRepositoryStub) RecordMissedScheduledDreamCycle(_ context.Context, input repository.DreamCycleClaimInput) (*repository.DreamCycleRun, error) {
	s.missedInput = input
	return &repository.DreamCycleRun{
		TeamID:    input.TeamID,
		RunID:     uuid.NewString(),
		RunDate:   input.RunDate,
		WindowKey: input.WindowKey,
		Status:    "missed",
		Claimed:   true,
	}, nil
}

type rememberServiceStub struct {
	requests []memoryservice.RememberRequest
	result   *memoryservice.RememberResult
	err      error
}

func (s *rememberServiceStub) Remember(_ context.Context, req memoryservice.RememberRequest) (*memoryservice.RememberResult, error) {
	s.requests = append(s.requests, req)
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &memoryservice.RememberResult{
		IngestID:        uuid.NewString(),
		ProcessingState: string(domain.PlacementRunQueued),
	}, nil
}
