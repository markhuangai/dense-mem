package dream

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

func TestControlServiceReadsTeamDreamsWithoutActorContext(t *testing.T) {
	teamID := uuid.NewString()
	creatorA := uuid.NewString()
	creatorB := uuid.NewString()
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	store := &dreamControlRepositoryStub{
		records: []dreamcontract.HypothesisRecord{
			{TeamID: teamID, HypothesisID: uuid.NewString(), CreatedByProfileID: creatorA, Status: "proposed", Statement: "creator A", UpdatedAt: now},
			{TeamID: teamID, HypothesisID: uuid.NewString(), CreatedByProfileID: creatorB, Status: "reinforced", Statement: "creator B", UpdatedAt: now.Add(-time.Minute)},
		},
		runs: []dreamcontract.DreamCycleRun{
			{TeamID: teamID, RunID: uuid.NewString(), InitiatedByProfileID: creatorB, Status: "completed", StartedAt: now},
			{TeamID: teamID, RunID: uuid.NewString(), InitiatedByProfileID: creatorA, Status: "completed", StartedAt: now.Add(-time.Hour)},
		},
		pending: 1,
	}
	teams := &dreamControlTeamConfigStub{team: &domain.Team{
		ID: uuid.MustParse(teamID),
		Config: map[string]any{
			"dreaming": map[string]any{"enabled": false},
		},
	}}
	svc := NewControl(ControlDependencies{
		Store: store,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{
			Enabled:        true,
			StartTimeLocal: "03:00",
			Timezone:       "UTC",
			MaxOutputs:     5,
		}},
		Teams: teams,
	})

	dreams, _, err := svc.List(context.Background(), teamID, ListOptions{
		Limit:     20,
		Sort:      DreamSortCreatedAt,
		Direction: DreamDirectionAsc,
	})
	require.NoError(t, err)
	require.Len(t, dreams, 2)
	assert.ElementsMatch(t, []string{teamID, teamID}, []string{dreams[0].TeamID, dreams[1].TeamID})
	assert.Equal(t, dreamcontract.ListHypothesesInput{
		TeamID:    teamID,
		Limit:     20,
		Sort:      DreamSortCreatedAt,
		Direction: DreamDirectionAsc,
	}, store.listInput)

	dream, err := svc.Get(context.Background(), teamID, store.records[0].HypothesisID)
	require.NoError(t, err)
	assert.Equal(t, store.records[0].HypothesisID, dream.DreamID)
	assert.Equal(t, teamID, dream.TeamID)

	runs, err := svc.ListRuns(context.Background(), teamID, 20)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, teamID, runs[0].TeamID)

	status, err := svc.Status(context.Background(), teamID)
	require.NoError(t, err)
	assert.Equal(t, 1, status.PendingCount)
	require.NotNil(t, status.LatestRun)
	assert.Equal(t, store.runs[0].RunID, status.LatestRun.RunID)
	assert.False(t, status.EffectiveConfig.Enabled)
	assert.Equal(t, "team", status.EffectiveConfig.Source)
	assert.Equal(t, uuid.MustParse(teamID), teams.requestedID)

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

}

func TestPublicDreamListStillRequiresAuthenticatedActor(t *testing.T) {
	svc := New(Dependencies{Store: &dreamRepositoryStub{}})

	_, _, err := svc.List(context.Background(), uuid.NewString(), ListOptions{Limit: 20})

	require.ErrorIs(t, err, ErrDreamAuthContext)
}

type dreamControlRepositoryStub struct {
	records   []dreamcontract.HypothesisRecord
	runs      []dreamcontract.DreamCycleRun
	pending   int
	listInput dreamcontract.ListHypothesesInput
}

func (s *dreamControlRepositoryStub) ListHypotheses(_ context.Context, input dreamcontract.ListHypothesesInput) ([]dreamcontract.HypothesisRecord, string, error) {
	s.listInput = input
	return append([]dreamcontract.HypothesisRecord(nil), s.records...), "", nil
}

func (s *dreamControlRepositoryStub) GetHypothesis(context.Context, dreamcontract.GetHypothesisInput) (*dreamcontract.HypothesisRecord, error) {
	if len(s.records) == 0 {
		return nil, dreamcontract.ErrDreamHypothesisNotFound
	}
	record := s.records[0]
	return &record, nil
}

func (s *dreamControlRepositoryStub) CountHypotheses(context.Context, string, string) (int, error) {
	return s.pending, nil
}

func (s *dreamControlRepositoryStub) ListDreamCyclesForTeam(context.Context, string, int) ([]dreamcontract.DreamCycleRun, error) {
	return append([]dreamcontract.DreamCycleRun(nil), s.runs...), nil
}

type dreamControlTeamConfigStub struct {
	team        *domain.Team
	requestedID uuid.UUID
}

func (s *dreamControlTeamConfigStub) GetByID(_ context.Context, id uuid.UUID) (*domain.Team, error) {
	s.requestedID = id
	return s.team, nil
}

var _ ControlService = (*controlService)(nil)
var _ dreamcontract.DreamControlRepository = (*dreamControlRepositoryStub)(nil)
var _ TeamConfigService = (*dreamControlTeamConfigStub)(nil)
