package service

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

var ErrIdentityCleanupPreflightUnavailable = errors.New("identity cleanup preflight unavailable")

type IdentityCleanupPreflightReader interface {
	Preflight(ctx context.Context) (domain.IdentityCleanupPreflight, error)
}

type IdentityCleanupPreflightService struct {
	repo repository.IdentityCleanupPreflightRepository
}

var _ IdentityCleanupPreflightReader = (*IdentityCleanupPreflightService)(nil)

func NewIdentityCleanupPreflightService(repo repository.IdentityCleanupPreflightRepository) *IdentityCleanupPreflightService {
	return &IdentityCleanupPreflightService{repo: repo}
}

func (s *IdentityCleanupPreflightService) Preflight(ctx context.Context) (domain.IdentityCleanupPreflight, error) {
	if s == nil || s.repo == nil {
		return domain.IdentityCleanupPreflight{}, ErrIdentityCleanupPreflightUnavailable
	}
	return s.repo.ReadIdentityCleanupPreflight(ctx)
}
