package conflictqueue

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type fakeQueueRepository struct {
	page  *domain.ConflictQueuePage
	query domain.ConflictQueueQuery
}

func (f *fakeQueueRepository) ListConflictQueue(_ context.Context, query domain.ConflictQueueQuery) (*domain.ConflictQueuePage, error) {
	f.query = query
	return f.page, nil
}

func (f *fakeQueueRepository) CollectConflictQueueMetrics(context.Context) (domain.ConflictQueueMetricsSnapshot, error) {
	return domain.ConflictQueueMetricsSnapshot{}, nil
}

func TestListDefaultsAndNormalizesQueueOptions(t *testing.T) {
	repository := &fakeQueueRepository{page: &domain.ConflictQueuePage{}}
	service := New(repository)
	teamID := "2b1f8e4b-263c-4b6f-bc58-0efb7fbf4b5e"

	_, err := service.List(context.Background(), " "+teamID+" ", ListOptions{})
	require.NoError(t, err)
	require.Equal(t, domain.ConflictQueueDefaultLimit, repository.query.Limit)
	require.Equal(t, teamID, repository.query.TeamID)
	require.Empty(t, repository.query.Status)

	_, err = service.List(context.Background(), teamID, ListOptions{Status: " OVERDUE ", Limit: 50})
	require.NoError(t, err)
	require.Equal(t, "overdue", repository.query.Status)
	require.Equal(t, 50, repository.query.Limit)
}

func TestListRejectsInvalidQueueOptionsAndCursorScope(t *testing.T) {
	service := New(&fakeQueueRepository{page: &domain.ConflictQueuePage{}})
	teamID := "2b1f8e4b-263c-4b6f-bc58-0efb7fbf4b5e"

	_, err := service.List(context.Background(), teamID, ListOptions{Status: "closed"})
	require.ErrorIs(t, err, ErrInvalidStatus)
	_, err = service.List(context.Background(), teamID, ListOptions{Limit: 101})
	require.ErrorIs(t, err, ErrInvalidLimit)
	_, err = service.List(context.Background(), teamID, ListOptions{Cursor: "not-a-cursor"})
	require.ErrorIs(t, err, ErrInvalidCursor)
	foreignCursor, err := domain.EncodeConflictQueueCursor(domain.ConflictQueueCursor{
		Version: 1, TeamID: "8b8f5d6d-6f23-4e48-9f5b-7a3ec4c39b90", Status: "open",
		NextReviewAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), ConflictID: "d97d3e7a-4c4e-4f31-bd39-f0c7ac6b8f8a",
	})
	require.NoError(t, err)
	_, err = service.List(context.Background(), teamID, ListOptions{Cursor: foreignCursor})
	require.ErrorIs(t, err, ErrInvalidCursor)
}
