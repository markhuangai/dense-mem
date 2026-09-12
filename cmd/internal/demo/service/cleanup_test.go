package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

func TestCleanerPurgesExpiredTeamsAndStopsOnCancellation(t *testing.T) {
	teamID := uuid.New()
	repo := &expiredTeamRepositoryStub{ids: []uuid.UUID{teamID}}
	teams := &cleanerTeamServiceStub{deletedCh: make(chan uuid.UUID, 1)}
	cleaner := NewCleaner(repo, teams, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cleaner.Run(ctx) }()
	select {
	case got := <-teams.deletedCh:
		require.Equal(t, teamID, got)
	case <-time.After(time.Second):
		t.Fatal("cleaner did not delete expired team")
	}
	cancel()
	require.NoError(t, <-done)
	require.Equal(t, "demo_cleanup", teams.actorRole)
	require.Equal(t, "demo cleanup", cleaner.Name())
}

func TestCleanerRunPropagatesRepositoryAndDeletionFailures(t *testing.T) {
	rawRepositoryErr := errors.New("repository unavailable")
	repo := &expiredTeamRepositoryStub{err: rawRepositoryErr}
	cleaner := NewCleaner(repo, &cleanerTeamServiceStub{}, time.Hour)
	require.ErrorIs(t, cleaner.Run(context.Background()), rawRepositoryErr)

	teamID := uuid.New()
	rawDeletionErr := errors.New("delete unavailable")
	repo = &expiredTeamRepositoryStub{ids: []uuid.UUID{teamID}}
	teams := &cleanerTeamServiceStub{deleteErr: rawDeletionErr}
	cleaner = NewCleaner(repo, teams, time.Hour)
	err := cleaner.Run(context.Background())
	require.ErrorIs(t, err, rawDeletionErr)
	require.Contains(t, err.Error(), teamID.String())
}

type expiredTeamRepositoryStub struct {
	ids []uuid.UUID
	err error
}

func (r *expiredTeamRepositoryStub) ExpiredTeamIDs(context.Context, time.Time, int) ([]uuid.UUID, error) {
	if r.err != nil {
		return nil, r.err
	}
	ids := append([]uuid.UUID(nil), r.ids...)
	r.ids = nil
	return ids, nil
}

type cleanerTeamServiceStub struct {
	accessservice.TeamService
	deletedCh chan uuid.UUID
	actorRole string
	deleteErr error
}

func (s *cleanerTeamServiceStub) Delete(_ context.Context, id uuid.UUID, _ *string, actorRole, _, _ string) error {
	if s.deletedCh != nil {
		s.deletedCh <- id
	}
	s.actorRole = actorRole
	return s.deleteErr
}
