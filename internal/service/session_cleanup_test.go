package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type recordingCleanupRepository struct {
	purgeProfileID      string
	invalidateProfileID string
	invalidateKeyID     string
	purgeErr            error
	invalidateErr       error
	purgeCalls          int
	invalidateCallCount int
}

func (r *recordingCleanupRepository) PurgeProfileState(_ context.Context, profileID string) error {
	r.purgeCalls++
	r.purgeProfileID = profileID
	return r.purgeErr
}

func (r *recordingCleanupRepository) InvalidateKeySessions(_ context.Context, profileID, keyID string) error {
	r.invalidateCallCount++
	r.invalidateProfileID = profileID
	r.invalidateKeyID = keyID
	return r.invalidateErr
}

func TestCleanupServiceForwardsPurgeProfileState(t *testing.T) {
	repo := &recordingCleanupRepository{}
	service := NewCleanupService(repo)

	err := service.PurgeProfileState(context.Background(), "profile-1")

	require.NoError(t, err)
	require.Equal(t, 1, repo.purgeCalls)
	require.Equal(t, "profile-1", repo.purgeProfileID)
}

func TestCleanupServiceForwardsInvalidateKeySessions(t *testing.T) {
	repo := &recordingCleanupRepository{}
	service := NewCleanupService(repo)

	err := service.InvalidateKeySessions(context.Background(), "profile-1", "key-1")

	require.NoError(t, err)
	require.Equal(t, 1, repo.invalidateCallCount)
	require.Equal(t, "profile-1", repo.invalidateProfileID)
	require.Equal(t, "key-1", repo.invalidateKeyID)
}

func TestCleanupServicePropagatesRepositoryErrors(t *testing.T) {
	purgeErr := errors.New("purge failed")
	invalidateErr := errors.New("invalidate failed")
	repo := &recordingCleanupRepository{
		purgeErr:      purgeErr,
		invalidateErr: invalidateErr,
	}
	service := NewCleanupService(repo)

	require.ErrorIs(t, service.PurgeProfileState(context.Background(), "profile-1"), purgeErr)
	require.ErrorIs(t, service.InvalidateKeySessions(context.Background(), "profile-1", "key-1"), invalidateErr)
}
