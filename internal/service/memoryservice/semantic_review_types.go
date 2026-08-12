package memoryservice

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/repository"
)

type SemanticPlacementReviewSource interface {
	BuildSemanticReviewJob(ctx context.Context, run repository.PlacementRun) (SemanticReviewJob, error)
}
