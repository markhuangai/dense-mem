package registry

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type internalSubmissionStatusLookupContextKey struct{}

func authenticatedScopes(ctx context.Context) []string {
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok {
		return nil
	}
	return actor.Grants
}

func withInternalSubmissionStatusLookup(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalSubmissionStatusLookupContextKey{}, true)
}

func internalSubmissionStatusLookup(ctx context.Context) bool {
	value, _ := ctx.Value(internalSubmissionStatusLookupContextKey{}).(bool)
	return value
}
