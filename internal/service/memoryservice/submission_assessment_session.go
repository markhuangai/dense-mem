package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func (s *submissionAssessmentPlacementWorkerService) loadOrAssess(
	ctx context.Context,
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	request verifier.SemanticAssessmentRequest,
	refresh func(context.Context) (verifier.SemanticAssessmentRequest, error),
) (*repository.SubmissionAssessment, verifier.SemanticAssessmentResponse, bool, bool, bool, *submissionAssessmentLiveSession, error) {
	stored, err := s.assessments.LoadSubmissionAssessment(ctx, repository.LoadSubmissionAssessmentInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		PlacementRunID: run.PlacementRunID,
	})
	if err == nil {
		response, decodeErr := decodeStoredSubmissionAssessment(stored, request, s.limits)
		if decodeErr != nil {
			observability.RecordAssessorValidationFailure(s.metrics, "stored_response")
		} else {
			observability.RecordAssessorAssessmentPersistence(s.metrics, "reused")
			observability.RecordAssessorDuplicateRequestPrevention(s.metrics, "post_persist")
		}
		return stored, response, true, false, false, nil, decodeErr
	}
	if !errors.Is(err, repository.ErrSubmissionAssessmentNotFound) {
		return nil, verifier.SemanticAssessmentResponse{}, false, false, false, nil, err
	}
	reserved, err := s.assessments.ReserveSubmissionAssessorAttempt(ctx, repository.ReserveSubmissionAssessorAttemptInput{
		SubmissionAssessmentRunScope: scope,
	})
	if err != nil {
		return nil, verifier.SemanticAssessmentResponse{}, false, false, false, nil, err
	}
	if !reserved {
		stored, err := s.assessments.LoadSubmissionAssessment(ctx, repository.LoadSubmissionAssessmentInput{
			TeamID:         run.TeamID,
			OwnerProfileID: run.OwnerProfileID,
			PlacementRunID: run.PlacementRunID,
		})
		if err == nil {
			response, decodeErr := decodeStoredSubmissionAssessment(stored, request, s.limits)
			if decodeErr != nil {
				observability.RecordAssessorValidationFailure(s.metrics, "stored_response")
			} else {
				observability.RecordAssessorAssessmentPersistence(s.metrics, "reused")
				observability.RecordAssessorDuplicateRequestPrevention(s.metrics, "post_persist")
			}
			return stored, response, true, false, false, nil, decodeErr
		}
		if !errors.Is(err, repository.ErrSubmissionAssessmentNotFound) {
			return nil, verifier.SemanticAssessmentResponse{}, false, false, false, nil, err
		}
		observability.RecordAssessorDuplicateRequestPrevention(s.metrics, "reservation")
		return nil, verifier.SemanticAssessmentResponse{}, false, false, false, nil, repository.ErrSubmissionAssessorAttemptConsumed
	}

	started := time.Now()
	providerCtx := observability.WithMetricIdentity(ctx, run.TeamID, run.OwnerProfileID)
	providerCtx = observability.WithAIOperation(providerCtx, observability.AIOperationPlacementAssessment, 1)
	modelName := s.provider.ModelName()
	response, session, finalRequest, err := s.assessRememberSession(providerCtx, request, refresh, 0)
	if err != nil {
		outcome := "provider_error"
		releaseProviderAttempt := true
		if errors.Is(err, verifier.ErrVerifierMalformedResponse) {
			outcome = "malformed_exhausted"
			releaseProviderAttempt = false
		}
		observability.RecordAssessorCall(s.metrics, request.InputTokens, 0, time.Since(started).Seconds(), outcome)
		return nil, verifier.SemanticAssessmentResponse{}, false, true, releaseProviderAttempt, nil, err
	}
	normalized := response
	inputTokens := normalized.InputTokens
	if inputTokens <= 0 {
		inputTokens = finalRequest.InputTokens
	}
	observability.RecordAssessorCall(s.metrics, inputTokens, normalized.OutputTokens, time.Since(started).Seconds(), "ok")
	normalizedJSON, err := json.Marshal(normalized)
	if err != nil {
		return nil, verifier.SemanticAssessmentResponse{}, false, true, false, nil, err
	}
	canonicalJSON, err := verifier.CanonicalJSON(normalizedJSON)
	if err != nil {
		return nil, verifier.SemanticAssessmentResponse{}, false, true, false, nil, err
	}
	persisted, existing, err := s.assessments.PersistSubmissionAssessment(ctx, repository.PersistSubmissionAssessmentInput{
		TeamID:                    run.TeamID,
		OwnerProfileID:            run.OwnerProfileID,
		IngestID:                  run.IngestID,
		PlacementRunID:            run.PlacementRunID,
		RequestID:                 finalRequest.RequestID,
		AssessorContractVersion:   domain.ContractVersion,
		Model:                     modelName,
		Tokenizer:                 assessmentTokenizer(s.limits),
		ProviderTurns:             normalized.ProviderTurns,
		InputTokens:               inputTokens,
		OutputTokens:              normalized.OutputTokens,
		CandidateContextTokens:    finalRequest.CandidateContextTokens,
		CandidateContextTruncated: finalRequest.CandidateContextTruncated,
		NormalizedResponse:        canonicalJSON,
		ResponseHash:              semanticAssessmentHash(canonicalJSON),
		ValidatedAt:               s.now().UTC(),
	})
	if err != nil {
		observability.RecordAssessorAssessmentPersistence(s.metrics, "error")
		return nil, verifier.SemanticAssessmentResponse{}, false, true, false, nil, err
	}
	if existing {
		observability.RecordAssessorAssessmentPersistence(s.metrics, "reused")
		storedResponse, decodeErr := decodeStoredSubmissionAssessment(persisted, request, s.limits)
		if decodeErr != nil {
			observability.RecordAssessorValidationFailure(s.metrics, "stored_response")
		}
		return persisted, storedResponse, true, true, false, nil, decodeErr
	}
	observability.RecordAssessorAssessmentPersistence(s.metrics, "persisted")
	return persisted, normalized, false, true, false, &submissionAssessmentLiveSession{
		session: session, request: finalRequest,
	}, nil
}

func (s *submissionAssessmentPlacementWorkerService) assessRememberSession(
	ctx context.Context,
	request verifier.SemanticAssessmentRequest,
	refresh func(context.Context) (verifier.SemanticAssessmentRequest, error),
	turnOffset int,
) (verifier.SemanticAssessmentResponse, verifier.SemanticAssessmentSession, verifier.SemanticAssessmentRequest, error) {
	if refresh == nil {
		return verifier.SemanticAssessmentResponse{}, nil, request, errors.New("submission assessment worker: assessment refresh is required")
	}
	session, turn, err := s.provider.Assess(ctx, request)
	if err != nil {
		return verifier.SemanticAssessmentResponse{}, session, request, err
	}
	response, finalRequest, err := s.completeRememberSessionTurns(ctx, session, turn, request, refresh, turnOffset)
	return response, session, finalRequest, err
}

func (s *submissionAssessmentPlacementWorkerService) completeRememberSessionTurns(
	ctx context.Context,
	session verifier.SemanticAssessmentSession,
	turn verifier.SemanticAssessmentTurn,
	request verifier.SemanticAssessmentRequest,
	refresh func(context.Context) (verifier.SemanticAssessmentRequest, error),
	turnOffset int,
) (verifier.SemanticAssessmentResponse, verifier.SemanticAssessmentRequest, error) {
	for {
		turnNumber := turn.Turn
		if turnNumber <= 0 {
			turnNumber = 1
		}
		totalTurns := turnOffset + turnNumber
		response := turn.Response
		validationErrors := append([]verifier.SemanticValidationError(nil), turn.ValidationErrors...)
		if len(validationErrors) == 0 {
			response, validationErrors = verifier.PrepareSemanticAssessmentResponse(request, response, s.limits)
		}
		if len(validationErrors) == 0 {
			response.ProviderTurns = totalTurns
			return response, request, nil
		}
		observability.RecordAssessorValidationFailure(s.metrics, assessmentValidationStage(turn.ValidationStage))
		for _, family := range semanticAssessmentValidationFieldFamiliesForService(validationErrors) {
			observability.RecordAssessorValidationFieldFailure(s.metrics, assessmentValidationStage(turn.ValidationStage), family)
		}
		if totalTurns >= SemanticPlacementMaxAssessorTurns {
			return verifier.SemanticAssessmentResponse{}, request, &verifier.MalformedResponseError{
				Provider:                "semantic_assessor",
				Message:                 "semantic assessor response remained invalid after bounded correction",
				FailureClass:            "malformed_exhausted",
				Attempts:                totalTurns,
				ValidationStage:         assessmentValidationStage(turn.ValidationStage),
				ValidationFieldFamilies: semanticAssessmentValidationFieldFamiliesForService(validationErrors),
			}
		}
		nextRequest, err := refresh(ctx)
		if err != nil {
			return verifier.SemanticAssessmentResponse{}, request, err
		}
		turn, err = s.provider.Repair(ctx, session, verifier.SemanticAssessmentRepairRequest{
			Request:          nextRequest,
			ValidationErrors: validationErrors,
		})
		if err != nil {
			return verifier.SemanticAssessmentResponse{}, request, err
		}
		request = nextRequest
	}
}
