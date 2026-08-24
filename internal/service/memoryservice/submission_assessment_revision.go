package memoryservice

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

const maxSubmissionAssessmentRevisionPersistenceAttempts = 2

var errSubmissionAssessmentRevisionPersistence = errors.New("submission assessment revision persistence exhausted")

func (s *submissionAssessmentPlacementWorkerService) persistSubmissionAssessmentRevision(
	ctx context.Context,
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	assessment *repository.SubmissionAssessment,
	response verifier.SemanticAssessmentResponse,
	request verifier.SemanticAssessmentRequest,
) (*repository.SubmissionAssessment, error) {
	if assessment == nil {
		return nil, errors.New("submission assessment revision requires a persisted assessment")
	}
	normalizedJSON, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	canonicalJSON, err := verifier.CanonicalJSON(normalizedJSON)
	if err != nil {
		return nil, err
	}
	inputTokens := response.InputTokens
	if inputTokens <= 0 {
		inputTokens = request.InputTokens
	}
	input := repository.AppendSubmissionAssessmentRevisionInput{
		SubmissionAssessmentRunScope: scope,
		AssessmentID:                 assessment.AssessmentID,
		ProviderTurns:                response.ProviderTurns,
		InputTokens:                  inputTokens,
		OutputTokens:                 response.OutputTokens,
		CandidateContextTokens:       request.CandidateContextTokens,
		CandidateContextTruncated:    request.CandidateContextTruncated,
		NormalizedResponse:           canonicalJSON,
		ResponseHash:                 semanticAssessmentHash(canonicalJSON),
		ValidatedAt:                  s.now().UTC(),
	}
	return s.appendSubmissionAssessmentRevision(ctx, input)
}

func (s *submissionAssessmentPlacementWorkerService) reserveSubmissionAssessmentProviderTurns(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	assessment *repository.SubmissionAssessment,
	providerTurns int,
) (*repository.SubmissionAssessment, error) {
	if assessment == nil || providerTurns <= assessment.ProviderTurns {
		return assessment, nil
	}
	canonicalJSON, err := verifier.CanonicalJSON(assessment.NormalizedResponse)
	if err != nil {
		return nil, errors.Join(errSubmissionAssessmentRevisionPersistence, err)
	}
	return s.appendSubmissionAssessmentRevision(ctx, repository.AppendSubmissionAssessmentRevisionInput{
		SubmissionAssessmentRunScope: scope,
		AssessmentID:                 assessment.AssessmentID,
		ProviderTurns:                providerTurns,
		InputTokens:                  assessment.InputTokens,
		OutputTokens:                 assessment.OutputTokens,
		CandidateContextTokens:       assessment.CandidateContextTokens,
		CandidateContextTruncated:    assessment.CandidateContextTruncated,
		NormalizedResponse:           canonicalJSON,
		ResponseHash:                 semanticAssessmentHash(canonicalJSON),
		ValidatedAt:                  s.now().UTC(),
	})
}

func (s *submissionAssessmentPlacementWorkerService) appendSubmissionAssessmentRevision(
	ctx context.Context,
	input repository.AppendSubmissionAssessmentRevisionInput,
) (*repository.SubmissionAssessment, error) {
	var lastErr error
	for attempt := 0; attempt < maxSubmissionAssessmentRevisionPersistenceAttempts; attempt++ {
		persisted, existing, err := s.assessments.AppendSubmissionAssessmentRevision(ctx, input)
		if err == nil {
			outcome := "persisted"
			if existing {
				outcome = "reused"
			}
			observability.RecordAssessorAssessmentPersistence(s.metrics, outcome)
			return persisted, nil
		}
		lastErr = err
	}
	observability.RecordAssessorAssessmentPersistence(s.metrics, "error")
	return nil, errors.Join(errSubmissionAssessmentRevisionPersistence, lastErr)
}
