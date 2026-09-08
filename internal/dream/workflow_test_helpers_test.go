package dream

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func dreamTestContext(teamID uuid.UUID, ownerID uuid.UUID) context.Context {
	credentialID := uuid.New()
	return requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: credentialID, MembershipID: credentialID,
		OwnerID: ownerID, CredentialID: &credentialID,
		AuthMethod: "api_key", Role: "member", Grants: []string{"read", "write"},
	})
}

type dreamRepositoryStub struct {
	confirmationLock    sync.Mutex
	inputs              []dreamcontract.DreamInput
	predicates          []dreamcontract.DreamTargetPredicate
	unassessedPaths     []dreamcontract.DreamPathEvaluationInput
	pathEvaluations     dreamcontract.DreamPathEvaluationRecordInput
	run                 dreamcontract.DreamCycleRun
	getRecord           dreamcontract.HypothesisRecord
	listRecords         []dreamcontract.HypothesisRecord
	recallRecords       []dreamcontract.HypothesisRecord
	listInput           dreamcontract.DreamInputListInput
	claimInput          dreamcontract.DreamCycleClaimInput
	claimNil            bool
	completeInput       dreamcontract.DreamCycleCompleteInput
	missedInput         dreamcontract.DreamCycleClaimInput
	upserts             []dreamcontract.UpsertHypothesisInput
	submitInput         dreamcontract.SubmitHypothesisInput
	updateInput         dreamcontract.UpdateHypothesisStatusInput
	err                 error
	claimErr            error
	completeErr         error
	listInputsErr       error
	targetsErr          error
	pathAssessErr       error
	upsertErr           error
	listErr             error
	reinforcedErr       error
	listHypothesesCalls int
	getErr              error
	recallErr           error
	updateErr           error
	submitErr           error
	submitErrs          []error
	submitCalls         int
	latestErr           error
	confirmationLockErr error
	recoveryRun         *dreamcontract.DreamCycleRun
	recoveryErr         error

	confirmationLockCalls int
}

func (s *dreamRepositoryStub) ClaimDreamCycle(_ context.Context, input dreamcontract.DreamCycleClaimInput) (*dreamcontract.DreamCycleRun, error) {
	s.claimInput = input
	if s.claimNil {
		return nil, nil
	}
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

func (s *dreamRepositoryStub) CompleteDreamCycle(_ context.Context, input dreamcontract.DreamCycleCompleteInput) error {
	s.completeInput = input
	if s.completeErr != nil {
		return s.completeErr
	}
	return s.err
}

func (s *dreamRepositoryStub) ListDreamInputs(_ context.Context, input dreamcontract.DreamInputListInput) ([]dreamcontract.DreamInput, error) {
	s.listInput = input
	if s.listInputsErr != nil {
		return nil, s.listInputsErr
	}
	return append([]dreamcontract.DreamInput(nil), s.inputs...), s.err
}

func (s *dreamRepositoryStub) ListDreamTargetPredicates(context.Context, string) ([]dreamcontract.DreamTargetPredicate, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]dreamcontract.DreamTargetPredicate(nil), s.predicates...), nil
}

func (s *dreamRepositoryStub) ListAvailableDreamTargets(_ context.Context, _ string, targets []dreamcontract.DreamTargetCandidate) ([]dreamcontract.DreamTargetCandidate, error) {
	if s.targetsErr != nil {
		return nil, s.targetsErr
	}
	if s.err != nil {
		return nil, s.err
	}
	return append([]dreamcontract.DreamTargetCandidate(nil), targets...), nil
}

func (s *dreamRepositoryStub) ListUnassessedDreamPaths(_ context.Context, _ string, paths []dreamcontract.DreamPathEvaluationInput) ([]dreamcontract.DreamPathEvaluationInput, error) {
	if s.pathAssessErr != nil {
		return nil, s.pathAssessErr
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.unassessedPaths != nil {
		return append([]dreamcontract.DreamPathEvaluationInput(nil), s.unassessedPaths...), nil
	}
	return append([]dreamcontract.DreamPathEvaluationInput(nil), paths...), nil
}

func (s *dreamRepositoryStub) RecordDreamPathEvaluations(_ context.Context, input dreamcontract.DreamPathEvaluationRecordInput) error {
	s.pathEvaluations = input
	return s.err
}

func (s *dreamRepositoryStub) PersistDreamGeneration(ctx context.Context, input dreamcontract.DreamGenerationPersistInput) (dreamcontract.DreamGenerationPersistResult, error) {
	result := dreamcontract.DreamGenerationPersistResult{}
	for _, proposal := range input.Proposals {
		_, inserted, err := s.UpsertHypothesis(ctx, proposal)
		if err != nil {
			if errors.Is(err, dreamcontract.ErrDreamExactRelationshipExists) ||
				errors.Is(err, dreamcontract.ErrDreamExactHypothesisExists) ||
				errors.Is(err, dreamcontract.ErrDreamSourceStale) {
				result.Rejected++
				continue
			}
			return dreamcontract.DreamGenerationPersistResult{}, err
		}
		if inserted {
			result.Created++
		}
	}
	if err := s.RecordDreamPathEvaluations(ctx, dreamcontract.DreamPathEvaluationRecordInput{
		TeamID:             input.TeamID,
		CreatedByProfileID: input.CreatedByProfileID,
		ProviderModel:      input.ProviderModel,
		Paths:              input.EvaluatedPaths,
	}); err != nil {
		return dreamcontract.DreamGenerationPersistResult{}, err
	}
	return result, nil
}

func (s *dreamRepositoryStub) UpsertHypothesis(_ context.Context, input dreamcontract.UpsertHypothesisInput) (*dreamcontract.HypothesisRecord, bool, error) {
	s.upserts = append(s.upserts, input)
	if s.upsertErr != nil {
		return nil, false, s.upsertErr
	}
	if s.err != nil {
		return nil, false, s.err
	}
	return &dreamcontract.HypothesisRecord{
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

func (s *dreamRepositoryStub) ListHypotheses(_ context.Context, input dreamcontract.ListHypothesesInput) ([]dreamcontract.HypothesisRecord, string, error) {
	s.listHypothesesCalls++
	if input.Status == string(domain.DreamStatusReinforced) && s.reinforcedErr != nil {
		return nil, "", s.reinforcedErr
	}
	if s.listErr != nil {
		return nil, "", s.listErr
	}
	if s.err != nil {
		return nil, "", s.err
	}
	if len(s.listRecords) > 0 {
		return append([]dreamcontract.HypothesisRecord(nil), s.listRecords...), "", nil
	}
	if s.getRecord.HypothesisID == "" {
		return nil, "", nil
	}
	return []dreamcontract.HypothesisRecord{s.getRecord}, "", nil
}

func (s *dreamRepositoryStub) GetHypothesis(context.Context, dreamcontract.GetHypothesisInput) (*dreamcontract.HypothesisRecord, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.getRecord.HypothesisID == "" {
		return nil, dreamcontract.ErrDreamHypothesisNotFound
	}
	record := s.getRecord
	return &record, nil
}

func (s *dreamRepositoryStub) WithHypothesisConfirmationLock(_ context.Context, _, _ string, fn func(dreamcontract.DreamRepository) error) error {
	s.confirmationLockCalls++
	if s.confirmationLockErr != nil {
		return s.confirmationLockErr
	}
	s.confirmationLock.Lock()
	defer s.confirmationLock.Unlock()
	return fn(s)
}

func (s *dreamRepositoryStub) RecallHypotheses(context.Context, dreamcontract.RecallHypothesesInput) ([]dreamcontract.HypothesisRecord, error) {
	if s.recallErr != nil {
		return nil, s.recallErr
	}
	if s.err != nil {
		return nil, s.err
	}
	if len(s.recallRecords) > 0 {
		return append([]dreamcontract.HypothesisRecord(nil), s.recallRecords...), nil
	}
	if s.getRecord.HypothesisID == "" {
		return nil, nil
	}
	return []dreamcontract.HypothesisRecord{s.getRecord}, nil
}

func (s *dreamRepositoryStub) UpdateHypothesisStatus(_ context.Context, input dreamcontract.UpdateHypothesisStatusInput) (*dreamcontract.HypothesisRecord, error) {
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

func (s *dreamRepositoryStub) SubmitHypothesis(_ context.Context, input dreamcontract.SubmitHypothesisInput) (*dreamcontract.HypothesisRecord, error) {
	s.submitInput = input
	s.submitCalls++
	if len(s.submitErrs) > 0 {
		err := s.submitErrs[0]
		s.submitErrs = s.submitErrs[1:]
		if err != nil {
			return nil, err
		}
	}
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

func (s *dreamRepositoryStub) ListDreamCyclesForTeam(context.Context, string, int) ([]dreamcontract.DreamCycleRun, error) {
	if s.latestErr != nil {
		return nil, s.latestErr
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.run.RunID == "" {
		return nil, nil
	}
	return []dreamcontract.DreamCycleRun{s.run}, nil
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

func (s *dreamRepositoryStub) ClaimScheduledDreamCycle(ctx context.Context, input dreamcontract.DreamCycleClaimInput) (*dreamcontract.DreamCycleRun, error) {
	return s.ClaimDreamCycle(ctx, input)
}

func (s *dreamRepositoryStub) ClaimRecoverableScheduledDreamCycle(context.Context, dreamcontract.DreamCycleRecoveryClaimInput) (*dreamcontract.DreamCycleRun, error) {
	if s.recoveryErr != nil {
		return nil, s.recoveryErr
	}
	return s.recoveryRun, nil
}

func (s *dreamRepositoryStub) CompleteScheduledDreamCycle(ctx context.Context, input dreamcontract.DreamCycleCompleteInput) error {
	return s.CompleteDreamCycle(ctx, input)
}

func (s *dreamRepositoryStub) RecordScheduledDreamPathEvaluations(ctx context.Context, input dreamcontract.DreamPathEvaluationRecordInput) error {
	return s.RecordDreamPathEvaluations(ctx, input)
}

func (s *dreamRepositoryStub) PersistScheduledDreamGeneration(ctx context.Context, input dreamcontract.DreamGenerationPersistInput) (dreamcontract.DreamGenerationPersistResult, error) {
	return s.PersistDreamGeneration(ctx, input)
}

func (s *dreamRepositoryStub) UpsertScheduledHypothesis(ctx context.Context, input dreamcontract.UpsertHypothesisInput) (*dreamcontract.HypothesisRecord, bool, error) {
	return s.UpsertHypothesis(ctx, input)
}

func (s *dreamRepositoryStub) RecordMissedScheduledDreamCycle(_ context.Context, input dreamcontract.DreamCycleClaimInput) (*dreamcontract.DreamCycleRun, error) {
	s.missedInput = input
	return &dreamcontract.DreamCycleRun{
		TeamID:    input.TeamID,
		RunID:     uuid.NewString(),
		RunDate:   input.RunDate,
		WindowKey: input.WindowKey,
		Status:    "missed",
		Claimed:   true,
	}, nil
}

type rememberServiceStub struct {
	requests []rememberapp.RememberRequest
	result   *rememberapp.RememberResult
	err      error
	after    func()
}

func (s *rememberServiceStub) Remember(_ context.Context, req rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
	s.requests = append(s.requests, req)
	if s.after != nil {
		s.after()
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	ingestID := uuid.NewString()
	return dreamTerminalRememberResult(string(rememberapp.TerminalProcessingCompleted), ingestID), nil
}
