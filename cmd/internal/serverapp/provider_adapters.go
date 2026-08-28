package serverapp

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/conflictassessment"
	"github.com/markhuangai/dense-mem/internal/dreamgeneration"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// semanticWriteProvider adapts the existing ordered embedding port to T01's
// index-explicit batch executor at the composition boundary.
type semanticWriteProvider struct {
	provider embedding.EmbeddingProviderInterface
}

func (p semanticWriteProvider) EmbedBatch(ctx context.Context, texts []string) ([]semanticwrite.IndexedEmbedding, string, error) {
	vectors, model, err := p.provider.EmbedBatch(ctx, texts)
	if err != nil {
		return nil, model, err
	}
	result := make([]semanticwrite.IndexedEmbedding, len(vectors))
	for index, vector := range vectors {
		result[index] = semanticwrite.IndexedEmbedding{Index: index, Vector: append([]float32(nil), vector...)}
	}
	return result, model, nil
}

func (p semanticWriteProvider) ModelName() string {
	if p.provider == nil {
		return ""
	}
	return p.provider.ModelName()
}
func (p semanticWriteProvider) Dimensions() int {
	if p.provider == nil {
		return 0
	}
	return p.provider.Dimensions()
}
func (p semanticWriteProvider) IsAvailable() bool {
	return p.provider != nil && p.provider.IsAvailable()
}

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
