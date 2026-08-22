package memoryservice

import (
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func decodeStoredSubmissionAssessment(
	assessment *repository.SubmissionAssessment,
	request verifier.SemanticAssessmentRequest,
	limits verifier.SemanticAssessmentLimits,
) (verifier.SemanticAssessmentResponse, error) {
	if assessment == nil {
		return verifier.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(errors.New("stored submission assessment is nil"))
	}
	canonicalJSON, err := verifier.CanonicalJSON(assessment.NormalizedResponse)
	if err != nil {
		return verifier.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(fmt.Errorf("stored submission assessment response is invalid JSON: %w", err))
	}
	if semanticAssessmentHash(canonicalJSON) != assessment.ResponseHash {
		return verifier.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(errors.New("stored submission assessment hash mismatch"))
	}
	storedOutputTokens, err := verifier.CountTokens(string(canonicalJSON), limits.Tokenizer)
	if err != nil {
		return verifier.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(err)
	}
	decodeLimits := limits
	if storedOutputTokens > decodeLimits.MaxOutputTokens {
		decodeLimits.MaxOutputTokens = storedOutputTokens
	}
	response, err := verifier.DecodeSemanticAssessmentResponseJSON(assessment.NormalizedResponse, decodeLimits)
	if err != nil {
		return verifier.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(err)
	}
	prepared, validationErrors := verifier.PrepareSemanticAssessmentResponse(request, response, decodeLimits)
	if len(validationErrors) > 0 {
		return verifier.SemanticAssessmentResponse{}, newStoredSubmissionAssessmentValidationError(errors.New("stored submission assessment does not match its current contract"))
	}
	return prepared, nil
}
