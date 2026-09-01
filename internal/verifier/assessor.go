package verifier

import (
	"fmt"
	"strings"

	"github.com/tiktoken-go/tokenizer"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

const (
	SemanticAssessmentSchemaName = assessor.SemanticAssessmentSchemaName

	// The assessor owns the complete response contract and turn bound. Verifier
	// aliases these values so both provider entry points share one policy.
	SemanticAssessmentMaxProviderTurns    = assessor.SemanticAssessmentMaxProviderTurns
	SemanticAssessmentMaxCorrectionErrors = assessor.SemanticAssessmentMaxCorrectionErrors

	SemanticAssessmentMaxEntityCandidatesPerSurface = assessor.SemanticAssessmentMaxEntityCandidatesPerSurface
	SemanticAssessmentMaxPredicateOptions           = assessor.SemanticAssessmentMaxPredicateOptions
	SemanticAssessmentMaxEntityResults              = assessor.SemanticAssessmentMaxEntityResults
	SemanticAssessmentMaxRelationshipResults        = assessor.SemanticAssessmentMaxRelationshipResults
	SemanticAssessmentMaxRelationshipSplits         = assessor.SemanticAssessmentMaxRelationshipSplits
	SemanticAssessmentMaxEvidenceSpans              = assessor.SemanticAssessmentMaxEvidenceSpans
	SemanticAssessmentMaxEntityGroundings           = assessor.SemanticAssessmentMaxEntityGroundings

	semanticAssessmentSystemPrompt          = assessor.SemanticAssessmentSystemPrompt
	semanticAssessmentCorrectionInstruction = assessor.SemanticAssessmentCorrectionInstruction
)

type SemanticAssessmentLimits = assessor.SemanticAssessmentLimits

func DefaultSemanticAssessmentLimits() SemanticAssessmentLimits {
	return assessor.DefaultSemanticAssessmentLimits()
}

func normalizeSemanticAssessmentLimits(limits SemanticAssessmentLimits) SemanticAssessmentLimits {
	return assessor.NormalizeSemanticAssessmentLimits(limits)
}

// CountTokens preserves the verifier package's historical helper while using
// the same tokenizer implementation as assessor.
func CountTokens(text string, tokenizerName string) (int, error) {
	codec, err := tokenizer.Get(tokenizer.Encoding(strings.TrimSpace(tokenizerName)))
	if err != nil {
		return 0, fmt.Errorf("tokenizer %q: %w", tokenizerName, err)
	}
	count, err := codec.Count(text)
	if err != nil {
		return 0, fmt.Errorf("count tokens with %q: %w", tokenizerName, err)
	}
	return count, nil
}

func PrepareSemanticAssessmentRequest(
	req SemanticAssessmentRequest,
	limits SemanticAssessmentLimits,
) (SemanticAssessmentRequest, []SemanticValidationError) {
	return assessor.PrepareSemanticAssessmentRequest(req, limits)
}

func DecodeSemanticAssessmentResponseJSON(
	raw []byte,
	limits SemanticAssessmentLimits,
) (SemanticAssessmentResponse, error) {
	return assessor.DecodeSemanticAssessmentResponseJSON(raw, limits)
}

func PrepareSemanticAssessmentResponse(
	req SemanticAssessmentRequest,
	response SemanticAssessmentResponse,
	limits SemanticAssessmentLimits,
) (SemanticAssessmentResponse, []SemanticValidationError) {
	return assessor.PrepareSemanticAssessmentResponse(req, response, limits)
}
