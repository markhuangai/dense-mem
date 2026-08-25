package memoryservice

import (
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func decodeStoredSubmissionAssessment(
	assessment *repository.SubmissionAssessment,
	request assessor.SemanticAssessmentRequest,
	limits assessor.SemanticAssessmentLimits,
) (assessor.SemanticAssessmentResponse, error) {
	if assessment == nil {
		return assessor.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(errors.New("stored submission assessment is nil"))
	}
	canonicalJSON, err := assessor.CanonicalJSON(assessment.NormalizedResponse)
	if err != nil {
		return assessor.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(fmt.Errorf("stored submission assessment response is invalid JSON: %w", err))
	}
	if semanticAssessmentHash(canonicalJSON) != assessment.ResponseHash {
		return assessor.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(errors.New("stored submission assessment hash mismatch"))
	}
	storedOutputTokens, err := assessor.CountTokens(string(canonicalJSON), limits.Tokenizer)
	if err != nil {
		return assessor.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(err)
	}
	decodeLimits := limits
	if storedOutputTokens > decodeLimits.MaxOutputTokens {
		decodeLimits.MaxOutputTokens = storedOutputTokens
	}
	response, err := assessor.DecodeSemanticAssessmentResponseJSON(assessment.NormalizedResponse, decodeLimits)
	if err != nil {
		return assessor.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(err)
	}
	prepared, validationErrors := assessor.PrepareSemanticAssessmentResponse(request, response, decodeLimits)
	if len(validationErrors) > 0 {
		return assessor.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(errors.New("stored submission assessment does not match its current contract"))
	}
	prepared.ProviderTurns = assessment.ProviderTurns
	prepared.InputTokens = assessment.InputTokens
	prepared.OutputTokens = assessment.OutputTokens
	return prepared, nil
}
