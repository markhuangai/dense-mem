package http

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *httpSSORepoStub) UpdateProviderPreservingProtectedResource(ctx context.Context, provider *domain.SSOProvider) error {
	if stored := r.providers[provider.ID]; stored != nil {
		provider.ProtectedResource = stored.ProtectedResource
	}
	return r.UpdateProvider(ctx, provider)
}
