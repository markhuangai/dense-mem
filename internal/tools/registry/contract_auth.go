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

type validatedContractToolKey struct{}

// WithValidatedContractInput lets the binding consume the dispatcher-validated request.
func WithValidatedContractInput(ctx context.Context, tool Tool) context.Context {
	return context.WithValue(ctx, validatedContractToolKey{}, tool.Name)
}

func validateBindingInput(ctx context.Context, tool Tool, input map[string]any) error {
	name, _ := ctx.Value(validatedContractToolKey{}).(string)
	if name != "" && name == tool.Name {
		return nil
	}
	return ValidateContractInput(tool, input, authenticatedScopes(ctx))
}
