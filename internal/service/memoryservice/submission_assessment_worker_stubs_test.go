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
	assessment                *repository.SubmissionAssessment
	loadErr                   error
	loadCalls                 int
	loadNotFoundCall          int
	loadAfterReservation      *repository.SubmissionAssessment
	loadAfterReservationErr   error
	reserveErr                error
	persistErr                error
	persistExisting           bool
	persistNormalizedResponse json.RawMessage
	persistCalls              int
	revisionInputs            []repository.AppendSubmissionAssessmentRevisionInput
	revisionErr               error
	revisionErrors            []error
	revisionExisting          bool
	reserved                  bool
	commits                   []repository.CommitSubmissionAssessmentInput
	completions               []repository.CompleteSubmissionAssessmentInput
	requeues                  []repository.RequeueSubmissionAssessmentInput
	completeNil               bool
	completeErr               error
	completeFirstDisposition  *repository.PlacementFirstDisposition
	requeueNil                bool
	requeueErr                error
	commitErr                 error
	commitErrors              []error
	commitNil                 bool
}

func (s *submissionAssessmentWorkerAssessmentStub) LoadSubmissionAssessment(context.Context, repository.LoadSubmissionAssessmentInput) (*repository.SubmissionAssessment, error) {
	s.loadCalls++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	if s.loadNotFoundCall > 0 && s.loadCalls == s.loadNotFoundCall {
		return nil, repository.ErrSubmissionAssessmentNotFound
	}
	if s.loadCalls > 1 && s.loadAfterReservationErr != nil {
		return nil, s.loadAfterReservationErr
	}
	if s.loadCalls > 1 && s.loadAfterReservation != nil {
		return s.loadAfterReservation, nil
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
		ProviderTurns: input.ProviderTurns, InputTokens: input.InputTokens, OutputTokens: input.OutputTokens, CandidateContextTokens: input.CandidateContextTokens,
		CandidateContextTruncated: input.CandidateContextTruncated, NormalizedResponse: append(json.RawMessage(nil), input.NormalizedResponse...), ResponseHash: input.ResponseHash, ValidatedAt: input.ValidatedAt,
	}
	if s.persistNormalizedResponse != nil {
		s.assessment.NormalizedResponse = append(json.RawMessage(nil), s.persistNormalizedResponse...)
	}
	return s.assessment, s.persistExisting, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) AppendSubmissionAssessmentRevision(
	_ context.Context,
	input repository.AppendSubmissionAssessmentRevisionInput,
) (*repository.SubmissionAssessment, bool, error) {
	s.revisionInputs = append(s.revisionInputs, input)
	if len(s.revisionErrors) > 0 {
		err := s.revisionErrors[0]
		s.revisionErrors = s.revisionErrors[1:]
		if err != nil {
			return nil, false, err
		}
	}
	if s.revisionErr != nil {
		return nil, false, s.revisionErr
	}
	if s.assessment == nil {
		return nil, false, repository.ErrSubmissionAssessmentNotFound
	}
	s.assessment.RevisionNumber++
	s.assessment.ProviderTurns = input.ProviderTurns
	s.assessment.InputTokens = input.InputTokens
	s.assessment.OutputTokens = input.OutputTokens
	s.assessment.CandidateContextTokens = input.CandidateContextTokens
	s.assessment.CandidateContextTruncated = input.CandidateContextTruncated
	s.assessment.NormalizedResponse = append(json.RawMessage(nil), input.NormalizedResponse...)
	s.assessment.ResponseHash = input.ResponseHash
	s.assessment.ValidatedAt = input.ValidatedAt
	return s.assessment, s.revisionExisting, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) CommitSubmissionAssessment(_ context.Context, input repository.CommitSubmissionAssessmentInput) (*repository.CommitSubmissionAssessmentResult, error) {
	s.commits = append(s.commits, input)
	if len(s.commitErrors) > 0 {
		err := s.commitErrors[0]
		s.commitErrors = s.commitErrors[1:]
		if err != nil {
			return nil, err
		}
	}
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
	return &repository.CompleteSubmissionAssessmentResult{
		Status: input.Status, FirstDisposition: s.completeFirstDisposition,
	}, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) RequeueSubmissionAssessment(_ context.Context, input repository.RequeueSubmissionAssessmentInput) (*repository.RequeueSubmissionAssessmentResult, error) {
	s.requeues = append(s.requeues, input)
	if s.requeueErr != nil {
		return nil, s.requeueErr
	}
	if s.requeueNil {
		return nil, nil
	}
	return &repository.RequeueSubmissionAssessmentResult{Status: string(domain.SemanticReviewRetryable)}, nil
}

type submissionAssessmentWorkerProviderStub struct {
	calls           int
	request         *verifier.SemanticAssessmentRequest
	response        func(verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error)
	responseForTurn func(verifier.SemanticAssessmentRequest, int) (verifier.SemanticAssessmentResponse, error)
	startSessionID  string
	repairSessionID string
	startErr        error
	repairErr       error
}

type submissionAssessmentWorkerProviderSession struct {
	id    string
	turns int
}

func (s *submissionAssessmentWorkerProviderSession) SessionID() string { return s.id }

func (s *submissionAssessmentWorkerProviderStub) Assess(_ context.Context, req verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentSession, verifier.SemanticAssessmentTurn, error) {
	s.calls++
	if s.startErr != nil {
		return nil, verifier.SemanticAssessmentTurn{}, s.startErr
	}
	s.request = &req
	session := &submissionAssessmentWorkerProviderSession{id: "stub-session", turns: 1}
	s.startSessionID = session.SessionID()
	turn, err := s.turn(req, session.turns)
	return session, turn, err
}

func (s *submissionAssessmentWorkerProviderStub) Repair(_ context.Context, sessionRef verifier.SemanticAssessmentSession, repair verifier.SemanticAssessmentRepairRequest) (verifier.SemanticAssessmentTurn, error) {
	session, ok := sessionRef.(*submissionAssessmentWorkerProviderSession)
	if !ok || session == nil {
		return verifier.SemanticAssessmentTurn{}, errors.New("invalid stub assessor session")
	}
	s.calls++
	if s.repairErr != nil {
		return verifier.SemanticAssessmentTurn{}, s.repairErr
	}
	s.request = &repair.Request
	s.repairSessionID = session.SessionID()
	session.turns++
	return s.turn(repair.Request, session.turns)
}

func (s *submissionAssessmentWorkerProviderStub) turn(req verifier.SemanticAssessmentRequest, turns int) (verifier.SemanticAssessmentTurn, error) {
	response := submissionAssessmentValidResponse(req, false)
	if s.responseForTurn != nil {
		var err error
		response, err = s.responseForTurn(req, turns)
		if err != nil {
			return verifier.SemanticAssessmentTurn{}, err
		}
	} else if s.response != nil {
		var err error
		response, err = s.response(req)
		if err != nil {
			return verifier.SemanticAssessmentTurn{}, err
		}
	}
	normalized, validationErrors := verifier.PrepareSemanticAssessmentResponse(req, response, verifier.DefaultSemanticAssessmentLimits())
	return verifier.SemanticAssessmentTurn{
		Response:         normalized,
		ValidationErrors: validationErrors,
		ValidationStage:  "response_contract",
		Turn:             turns,
	}, nil
}

func (*submissionAssessmentWorkerProviderStub) ModelName() string {
	return "submission-assessment-model"
}
