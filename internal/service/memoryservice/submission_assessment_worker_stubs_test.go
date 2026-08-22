package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type submissionAssessmentWorkerLedgerStub struct {
	run       *repository.PlacementRun
	placement *repository.CreateIngestResult
	claimErr  error
	claimNil  bool
	getErr    error
}

func (*submissionAssessmentWorkerLedgerStub) CreateIngest(context.Context, repository.CreateIngestInput) (*repository.CreateIngestResult, error) {
	return nil, errors.New("unexpected CreateIngest")
}

func (s *submissionAssessmentWorkerLedgerStub) GetPlacementRun(context.Context, repository.GetPlacementRunInput) (*repository.CreateIngestResult, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.placement, nil
}

func (*submissionAssessmentWorkerLedgerStub) AdvanceSourceRevision(context.Context, repository.AdvanceSourceRevisionInput) (*repository.SourceRevisionResult, error) {
	return nil, errors.New("unexpected AdvanceSourceRevision")
}

func (*submissionAssessmentWorkerLedgerStub) AppendSecurityEvent(context.Context, repository.SecurityEventInput) (string, error) {
	return "", errors.New("unexpected AppendSecurityEvent")
}

func (*submissionAssessmentWorkerLedgerStub) AppendPlacementOutcome(context.Context, repository.PlacementOutcomeInput) (string, error) {
	return "", errors.New("unexpected AppendPlacementOutcome")
}

func (s *submissionAssessmentWorkerLedgerStub) ClaimNextPlacementRun(context.Context, string, string, time.Duration) (*repository.PlacementRun, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if s.claimNil {
		return nil, nil
	}
	return s.run, nil
}

func (*submissionAssessmentWorkerLedgerStub) FinishPlacementRun(context.Context, string, string, string, string, string) (*repository.PlacementFirstDisposition, error) {
	return nil, errors.New("unexpected FinishPlacementRun")
}

type submissionAssessmentWorkerAssessmentStub struct {
	assessment   *repository.SubmissionAssessment
	loadErr      error
	reserveErr   error
	persistErr   error
	policyErr    error
	persistCalls int
	reserved     bool
	policy       repository.AutoWriteConfidencePolicy
	commits      []repository.CommitSubmissionAssessmentInput
	completions  []repository.CompleteSubmissionAssessmentInput
	requeues     []repository.RequeueSubmissionAssessmentInput
	completeNil  bool
	completeErr  error
	requeueNil   bool
	commitErr    error
	commitNil    bool
}

func (s *submissionAssessmentWorkerAssessmentStub) LoadSubmissionAssessment(context.Context, repository.LoadSubmissionAssessmentInput) (*repository.SubmissionAssessment, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	if s.assessment == nil {
		return nil, repository.ErrSubmissionAssessmentNotFound
	}
	return s.assessment, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) ReserveSubmissionAssessorAttempt(context.Context, repository.ReserveSubmissionAssessorAttemptInput) (bool, error) {
	if s.reserveErr != nil {
		return false, s.reserveErr
	}
	if s.reserved {
		return false, nil
	}
	s.reserved = true
	return true, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) PersistSubmissionAssessment(_ context.Context, input repository.PersistSubmissionAssessmentInput) (*repository.SubmissionAssessment, bool, error) {
	s.persistCalls++
	if s.persistErr != nil {
		return nil, false, s.persistErr
	}
	s.assessment = &repository.SubmissionAssessment{
		TeamID: input.TeamID, AssessmentID: uuid.NewString(), OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID, PlacementRunID: input.PlacementRunID,
		RequestID: input.RequestID, AssessorContractVersion: input.AssessorContractVersion, Model: input.Model, Tokenizer: input.Tokenizer,
		InputTokens: input.InputTokens, OutputTokens: input.OutputTokens, CandidateContextTokens: input.CandidateContextTokens,
		CandidateContextTruncated: input.CandidateContextTruncated, NormalizedResponse: append(json.RawMessage(nil), input.NormalizedResponse...), ResponseHash: input.ResponseHash, ValidatedAt: input.ValidatedAt,
	}
	return s.assessment, false, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) LoadAutoWriteConfidencePolicy(context.Context, repository.LoadAutoWriteConfidencePolicyInput) (repository.AutoWriteConfidencePolicy, error) {
	if s.policyErr != nil {
		return repository.AutoWriteConfidencePolicy{}, s.policyErr
	}
	return s.policy, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) CommitSubmissionAssessment(_ context.Context, input repository.CommitSubmissionAssessmentInput) (*repository.CommitSubmissionAssessmentResult, error) {
	s.commits = append(s.commits, input)
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	if s.commitNil {
		return nil, nil
	}
	return &repository.CommitSubmissionAssessmentResult{Status: string(domain.SemanticReviewAccepted)}, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) CompleteSubmissionAssessment(_ context.Context, input repository.CompleteSubmissionAssessmentInput) (*repository.CompleteSubmissionAssessmentResult, error) {
	s.completions = append(s.completions, input)
	if s.completeErr != nil {
		return nil, s.completeErr
	}
	if s.completeNil {
		return nil, nil
	}
	return &repository.CompleteSubmissionAssessmentResult{Status: input.Status}, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) RequeueSubmissionAssessment(_ context.Context, input repository.RequeueSubmissionAssessmentInput) (*repository.RequeueSubmissionAssessmentResult, error) {
	s.requeues = append(s.requeues, input)
	if s.requeueNil {
		return nil, nil
	}
	return &repository.RequeueSubmissionAssessmentResult{Status: string(domain.SemanticReviewRetryable)}, nil
}

type submissionAssessmentWorkerProviderStub struct {
	calls    int
	request  *verifier.SemanticAssessmentRequest
	response func(verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error)
}

type submissionAssessmentWorkerNormalizerStub struct {
	response func(verifier.RememberNormalizerRequest) (verifier.RememberNormalizerResponse, error)
}

func (s submissionAssessmentWorkerNormalizerStub) NormalizeRemember(_ context.Context, req verifier.RememberNormalizerRequest) (verifier.RememberNormalizerResponse, error) {
	if s.response != nil {
		return s.response(req)
	}
	return verifier.RememberNormalizerResponse{}, nil
}

func (submissionAssessmentWorkerNormalizerStub) ModelName() string {
	return "remember-normalizer-model"
}

func (s *submissionAssessmentWorkerProviderStub) AssessSemantic(_ context.Context, req verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
	s.calls++
	s.request = &req
	if s.response != nil {
		return s.response(req)
	}
	return submissionAssessmentValidResponse(req, false), nil
}

func (*submissionAssessmentWorkerProviderStub) ModelName() string {
	return "submission-assessment-model"
}
