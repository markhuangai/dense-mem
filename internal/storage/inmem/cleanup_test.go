package inmem

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/storage/redis"
)

func TestNoopCleanupRepository_ReturnsNilForBothCleanupCalls(t *testing.T) {
	repo := NewNoopCleanupRepository()

	err := repo.PurgeProfileState(context.Background(), "profile-1")
	assert.NoError(t, err)

	err = repo.InvalidateKeySessions(context.Background(), "profile-1", "key-1")
	assert.NoError(t, err)

	streamRepo := NewNoopStreamCleanupRepository()
	err = streamRepo.PurgeProfileStreamState(context.Background(), "profile-1")
	assert.NoError(t, err)
}

func TestNoopCleanupImplementations_SatisfyRequiredInterfaces(t *testing.T) {
	var _ redis.CleanupRepositoryInterface = (*NoopCleanupRepository)(nil)
	var _ service.KeySessionInvalidator = (*NoopCleanupRepository)(nil)
	var _ service.ProfileStatePurger = (*NoopCleanupRepository)(nil)
	var _ sse.StreamCleanupRepository = (*NoopStreamCleanupRepository)(nil)

	assert.True(t, true)
}
