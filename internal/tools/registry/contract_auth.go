package registry

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func authenticatedScopes(ctx context.Context) []string {
	credential, ok := requestctx.ActorCredentialFromContext(ctx)
	if !ok {
		return nil
	}
	return credential.Scopes
}
