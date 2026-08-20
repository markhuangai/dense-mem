package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (s *SSOService) UpdateProvider(ctx context.Context, provider domain.SSOProvider) (*domain.SSOProvider, error) {
	return s.updateProvider(ctx, provider, false)
}

func (s *SSOService) UpdateProviderPreservingProtectedResource(ctx context.Context, provider domain.SSOProvider) (*domain.SSOProvider, error) {
	return s.updateProvider(ctx, provider, true)
}

func (s *SSOService) updateProvider(ctx context.Context, provider domain.SSOProvider, preserveProtectedResource bool) (*domain.SSOProvider, error) {
	if provider.ID == uuid.Nil {
		err := fmt.Errorf("sso provider ID is required")
		s.debugSSOProviderFailure("sso update provider validation failed", err, &provider)
		return nil, err
	}
	if preserveProtectedResource {
		stored, err := s.repo.GetProvider(ctx, provider.ID)
		if err != nil {
			s.debugSSOProviderFailure("sso update provider config load failed", err, &provider)
			return nil, err
		}
		if stored != nil {
			provider.ProtectedResource = stored.ProtectedResource
		}
	}
	if err := normalizeSSOProviderForWrite(&provider); err != nil {
		s.debugSSOProviderFailure("sso update provider validation failed", err, &provider)
		return nil, err
	}
	if err := s.validateProtectedResourceProviderSet(ctx, &provider); err != nil {
		s.debugSSOProviderFailure("sso update protected-resource set validation failed", err, &provider)
		return nil, err
	}
	var err error
	if preserveProtectedResource {
		err = s.repo.UpdateProviderPreservingProtectedResource(ctx, &provider)
	} else {
		err = s.repo.UpdateProvider(ctx, &provider)
	}
	if err != nil {
		err = ssoProviderWriteError(err, provider.IssuerURL)
		s.debugSSOProviderFailure("sso update provider query failed", err, &provider)
		return nil, err
	}
	updated, err := s.repo.GetProvider(ctx, provider.ID)
	if err != nil {
		s.debugSSOProviderFailure("sso update provider reload failed", err, &provider)
		return nil, err
	}
	return updated, nil
}
