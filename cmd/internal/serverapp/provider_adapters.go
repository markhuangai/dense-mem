package serverapp

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/conflictassessment"
	"github.com/markhuangai/dense-mem/internal/dreamgeneration"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// legacyConflictProvider keeps the transitional Conflict protocol on its own
// application port until the conflict capability issue owns its provider.
type legacyConflictProvider struct {
	provider *verifier.OpenAIVerifier
}

func (p legacyConflictProvider) ModelName() string {
	if p.provider == nil {
		return ""
	}
	return p.provider.ModelName()
}

func (p legacyConflictProvider) AssessRelationshipConflict(
	ctx context.Context,
	request conflictassessment.ConflictAssessmentRequest,
) (conflictassessment.ConflictAssessmentResponse, error) {
	response, err := p.provider.AssessRelationshipConflict(ctx, request)
	if err != nil {
		return conflictassessment.ConflictAssessmentResponse{}, err
	}
	return response, nil
}

type legacyDreamProvider struct {
	provider *verifier.OpenAIVerifier
}

func (p legacyDreamProvider) ModelName() string {
	if p.provider == nil {
		return ""
	}
	return p.provider.ModelName()
}

func (p legacyDreamProvider) GenerateDreams(
	ctx context.Context,
	request dreamgeneration.DreamGenerationRequest,
) (dreamgeneration.DreamGenerationResponse, error) {
	response, err := p.provider.GenerateDreams(ctx, request)
	if err != nil {
		return dreamgeneration.DreamGenerationResponse{}, err
	}
	return legacyDreamGenerationResponse(response), nil
}

func legacyDreamGenerationResponse(response verifier.DreamGenerationResponse) dreamgeneration.DreamGenerationResponse {
	// The verifier response is an alias, so this bridge must not serialize or remap diagnostics.
	return response
}
