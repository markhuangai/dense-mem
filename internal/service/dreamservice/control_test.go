package dreamservice

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestControlServiceReadsTeamDreamsWithoutActorContext(t *testing.T) {
	teamID := uuid.NewString()
	ownerA := uuid.NewString()
	ownerB := uuid.NewString()
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	store := &dreamControlRepositoryStub{
		records: []repository.HypothesisRecord{
			{TeamID: teamID, HypothesisID: uuid.NewString(), OwnerProfileID: ownerA, Status: "proposed", Statement: "owner A", UpdatedAt: now},
			{TeamID: teamID, HypothesisID: uuid.NewString(), OwnerProfileID: ownerB, Status: "reinforced", Statement: "owner B", UpdatedAt: now.Add(-time.Minute)},
		},
		runs: []repository.DreamCycleRun{
			{TeamID: teamID, RunID: uuid.NewString(), OwnerProfileID: ownerB, Status: "completed", StartedAt: now},
			{TeamID: teamID, RunID: uuid.NewString(), OwnerProfileID: ownerA, Status: "completed", StartedAt: now.Add(-time.Hour)},
		},
		pending: 1,
		updated: 2,
	}
	svc := NewControl(ControlDependencies{Store: store})

	dreams, _, err := svc.List(context.Background(), teamID, ListOptions{Limit: 20})
	require.NoError(t, err)
	require.Len(t, dreams, 2)
	assert.ElementsMatch(t, []string{ownerA, ownerB}, []string{dreams[0].ProfileID, dreams[1].ProfileID})

	dream, err := svc.Get(context.Background(), teamID, store.records[0].HypothesisID)
	require.NoError(t, err)
	assert.Equal(t, store.records[0].HypothesisID, dream.DreamID)
	assert.Equal(t, ownerA, dream.ProfileID)

	runs, err := svc.ListRuns(context.Background(), teamID, 20)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, ownerB, runs[0].ProfileID)

	status, err := svc.Status(context.Background(), teamID)
	require.NoError(t, err)
	assert.Equal(t, 1, status.PendingCount)
	require.NotNil(t, status.LatestRun)
	assert.Equal(t, store.runs[0].RunID, status.LatestRun.RunID)

	updated, err := svc.Refresh(context.Background(), teamID, ControlActor{
		Source:        "control_portal:authorization-bearer",
		ClientIP:      "192.0.2.10",
		CorrelationID: "corr-1",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, updated)
	assert.Equal(t, repository.RefreshTeamHypothesisStalenessInput{
		TeamID:        teamID,
		Limit:         controlRefreshLimit,
		ActorSource:   "control_portal:authorization-bearer",
		ActorRole:     "control",
		ClientIP:      "192.0.2.10",
		CorrelationID: "corr-1",
	}, store.refreshInput)
}

func TestControlServiceRequiresRepository(t *testing.T) {
	svc := NewControl(ControlDependencies{})
	ctx := context.Background()

	_, _, err := svc.List(ctx, uuid.NewString(), ListOptions{})
	require.ErrorContains(t, err, "repository is required")

	_, err = svc.Get(ctx, uuid.NewString(), uuid.NewString())
	require.ErrorContains(t, err, "repository is required")

	_, err = svc.ListRuns(ctx, uuid.NewString(), 1)
	require.ErrorContains(t, err, "repository is required")

	_, err = svc.Status(ctx, uuid.NewString())
	require.ErrorContains(t, err, "repository is required")

	_, err = svc.Refresh(ctx, uuid.NewString(), ControlActor{})
	require.ErrorContains(t, err, "repository is required")
}

func TestPublicDreamListStillRequiresAuthenticatedActor(t *testing.T) {
	svc := New(Dependencies{Store: &dreamRepositoryStub{}})

	_, _, err := svc.List(context.Background(), uuid.NewString(), ListOptions{Limit: 20})

	require.ErrorIs(t, err, ErrDreamAuthContext)
}

type dreamControlRepositoryStub struct {
	records      []repository.HypothesisRecord
	runs         []repository.DreamCycleRun
	pending      int
	updated      int
	refreshInput repository.RefreshTeamHypothesisStalenessInput
}

func (s *dreamControlRepositoryStub) ListHypotheses(context.Context, repository.ListHypothesesInput) ([]repository.HypothesisRecord, string, error) {
	return append([]repository.HypothesisRecord(nil), s.records...), "", nil
}

func (s *dreamControlRepositoryStub) GetHypothesis(context.Context, repository.GetHypothesisInput) (*repository.HypothesisRecord, error) {
	if len(s.records) == 0 {
		return nil, repository.ErrDreamHypothesisNotFound
	}
	record := s.records[0]
	return &record, nil
}

func (s *dreamControlRepositoryStub) CountHypotheses(context.Context, string, string) (int, error) {
	return s.pending, nil
}

func (s *dreamControlRepositoryStub) ListDreamCyclesForTeam(context.Context, string, int) ([]repository.DreamCycleRun, error) {
	return append([]repository.DreamCycleRun(nil), s.runs...), nil
}

func (s *dreamControlRepositoryStub) RefreshTeamHypothesisStaleness(_ context.Context, input repository.RefreshTeamHypothesisStalenessInput) (int, error) {
	s.refreshInput = input
	return s.updated, nil
}

var _ ControlService = (*controlService)(nil)
var _ repository.DreamControlRepository = (*dreamControlRepositoryStub)(nil)
