package service

import (
	"context"
)

// CleanupServiceInterface is the companion interface for cleanup service operations.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type CleanupServiceInterface interface {
	PurgeProfileState(ctx context.Context, profileID string) error
	InvalidateKeySessions(ctx context.Context, profileID, keyID string) error
}

// cleanupRepository is an internal interface for the cleanup repository.
type cleanupRepository interface {
	PurgeProfileState(ctx context.Context, profileID string) error
	InvalidateKeySessions(ctx context.Context, profileID, keyID string) error
}

// CleanupService implements the cleanup service that wraps the repository.
type CleanupService struct {
	repo cleanupRepository
}

// Ensure CleanupService implements CleanupServiceInterface
var _ CleanupServiceInterface = (*CleanupService)(nil)

// NewCleanupService creates a new cleanup service instance.
func NewCleanupService(repo cleanupRepository) *CleanupService {
	return &CleanupService{
		repo: repo,
	}
}

// PurgeProfileState deletes all cache, session, and stream keys for a profile.
func (s *CleanupService) PurgeProfileState(ctx context.Context, profileID string) error {
	return s.repo.PurgeProfileState(ctx, profileID)
}

// InvalidateKeySessions deletes sessions that belong to a specific API key.
func (s *CleanupService) InvalidateKeySessions(ctx context.Context, profileID, keyID string) error {
	return s.repo.InvalidateKeySessions(ctx, profileID, keyID)
}
