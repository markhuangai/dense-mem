package serverapp

// This compatibility facade keeps the serverapp health helper stable while
// readiness policy lives in internal/operations.

import (
	"context"

	operations "github.com/markhuangai/dense-mem/internal/operations"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

func checkSearchReadiness(ctx context.Context, search interface {
	CheckSearchReadiness(context.Context) (*searchcontract.SearchReadiness, error)
}) error {
	return operations.CheckSearchReadiness(ctx, search)
}
