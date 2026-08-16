package registry

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func authenticatedScopes(ctx context.Context) []string {
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok {
		return nil
	}
	return actor.Grants
}
