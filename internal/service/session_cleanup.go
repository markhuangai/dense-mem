package service

import (
	"context"
)

// CleanupServiceInterface is the companion interface for cleanup service operations.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type CleanupServiceInterface interface {
	PurgeTeamState(ctx context.Context, teamID string) error
	InvalidateCredentialSessions(ctx context.Context, teamID, credentialID string) error
}

// cleanupRepository is an internal interface for the cleanup repository.
type cleanupRepository interface {
	PurgeTeamState(ctx context.Context, teamID string) error
	InvalidateCredentialSessions(ctx context.Context, teamID, credentialID string) error
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

// PurgeTeamState deletes all cache, session, and stream keys for a team.
func (s *CleanupService) PurgeTeamState(ctx context.Context, teamID string) error {
	return s.repo.PurgeTeamState(ctx, teamID)
}

// InvalidateCredentialSessions deletes sessions that belong to one credential.
func (s *CleanupService) InvalidateCredentialSessions(ctx context.Context, teamID, credentialID string) error {
	return s.repo.InvalidateCredentialSessions(ctx, teamID, credentialID)
}
