package inmem

import (
	"context"
)

// NoopCleanupRepository is a non-nil no-op implementation of cleanup
// operations for team state and credential sessions. It returns nil
// for all cleanup calls, making it safe to inject in no-Redis mode.
type NoopCleanupRepository struct{}

// NewNoopCleanupRepository creates a new no-op cleanup repository.
func NewNoopCleanupRepository() *NoopCleanupRepository {
	return &NoopCleanupRepository{}
}

// PurgeTeamState is a no-op implementation that returns nil.
func (r *NoopCleanupRepository) PurgeTeamState(ctx context.Context, teamID string) error {
	return nil
}

// InvalidateCredentialSessions is a no-op implementation that returns nil.
func (r *NoopCleanupRepository) InvalidateCredentialSessions(ctx context.Context, teamID, credentialID string) error {
	return nil
}

// NoopStreamCleanupRepository is a non-nil no-op implementation of
// SSE stream cleanup operations. It returns nil for all cleanup calls.
type NoopStreamCleanupRepository struct{}

// NewNoopStreamCleanupRepository creates a new no-op stream cleanup repository.
func NewNoopStreamCleanupRepository() *NoopStreamCleanupRepository {
	return &NoopStreamCleanupRepository{}
}

// PurgeTeamStreamState is a no-op implementation that returns nil.
func (r *NoopStreamCleanupRepository) PurgeTeamStreamState(ctx context.Context, teamID string) error {
	return nil
}
