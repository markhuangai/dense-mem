package dreamservice

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestStatusAndHelperEdgeCases(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	repo := &dreamRepositoryStub{}
	svc := New(Dependencies{
		Store:     repo,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})
	ctx := dreamTestContext(teamID, ownerID)

	runs, err := svc.ListRuns(ctx, "ignored-profile", 5)
	require.NoError(t, err)
	assert.Empty(t, runs)

	status, err := svc.Status(ctx, "ignored-profile")
	require.NoError(t, err)
	assert.Nil(t, status.LatestRun)
	assert.Equal(t, 0, status.PendingCount)

	assert.Equal(t, "relationship", dreamSourceType(repository.DreamInput{Status: "active"}))
	assert.Equal(t, "candidate_relationship", dreamSourceType(repository.DreamInput{Status: "pending_evidence"}))
	assert.Equal(t, "relationship", dreamSourceType(repository.DreamInput{}))
	assert.Equal(t, "from stringer", anyString(testStringer("from stringer")))
	require.Nil(t, optionalProbability(0))
	require.NotNil(t, optionalProbability(2))
	assert.Equal(t, 1.0, *optionalProbability(2))
}
