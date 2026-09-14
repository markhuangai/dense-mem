package inmem

import (
	"context"
	privacyservice "github.com/markhuangai/dense-mem/internal/privacy/service"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	"testing"

	"github.com/markhuangai/dense-mem/internal/storage/redis"
	"github.com/stretchr/testify/assert"
)

func TestNoopCleanupRepository_ReturnsNilForBothCleanupCalls(t *testing.T) {
	repo := NewNoopCleanupRepository()

	err := repo.PurgeTeamState(context.Background(), "profile-1")
	assert.NoError(t, err)

	err = repo.InvalidateCredentialSessions(context.Background(), "profile-1", "key-1")
	assert.NoError(t, err)

}

func TestNoopCleanupImplementations_SatisfyRequiredInterfaces(t *testing.T) {
	var _ redis.CleanupRepositoryInterface = (*NoopCleanupRepository)(nil)
	var _ privacyservice.CredentialSessionInvalidator = (*NoopCleanupRepository)(nil)
	var _ accessservice.TeamStatePurger = (*NoopCleanupRepository)(nil)

	assert.True(t, true)
}
