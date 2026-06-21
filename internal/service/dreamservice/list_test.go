package dreamservice

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDreamServiceListSortsAndPaginates(t *testing.T) {
	base := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	secondCreated := base.Add(time.Hour)
	graph := &cycleRunGraphStub{
		dreamRows: []map[string]any{
			{"d": dreamTestNodeWithTimes("dream-1", "profile-1", base, base.Add(3*time.Hour), base.Add(6*time.Hour))},
			{"d": dreamTestNodeWithTimes("dream-2", "profile-1", secondCreated, base.Add(2*time.Hour), base.Add(5*time.Hour))},
			{"d": dreamTestNodeWithTimes("dream-3", "profile-1", base.Add(2*time.Hour), base.Add(time.Hour), base.Add(4*time.Hour))},
		},
	}
	svc := New(Dependencies{Graph: graph})

	_, _, err := svc.List(context.Background(), "profile-1", ListOptions{Limit: 2})
	require.NoError(t, err)
	require.Contains(t, graph.lastReadQuery, "WITH d, d.updated_at AS sort_at")
	require.Contains(t, graph.lastReadQuery, "ORDER BY sort_at DESC, d.dream_id ASC")

	dreams, next, err := svc.List(context.Background(), "profile-1", ListOptions{
		Limit:     2,
		Sort:      DreamSortCreatedAt,
		Direction: DreamDirectionAsc,
	})

	require.NoError(t, err)
	require.Len(t, dreams, 2)
	require.Equal(t, int64(3), graph.lastReadParams["limit"])
	require.Contains(t, graph.lastReadQuery, "WITH d, d.created_at AS sort_at")
	require.Contains(t, graph.lastReadQuery, "ORDER BY sort_at ASC, d.dream_id ASC")
	require.NotEmpty(t, next)

	cursorAt, cursorDreamID, err := decodeDreamCursor(next, DreamSortCreatedAt, DreamDirectionAsc)
	require.NoError(t, err)
	require.Equal(t, secondCreated, cursorAt)
	require.Equal(t, "dream-2", cursorDreamID)

	_, _, err = svc.List(context.Background(), "profile-1", ListOptions{
		Limit:     2,
		Cursor:    next,
		Sort:      DreamSortCreatedAt,
		Direction: DreamDirectionAsc,
	})
	require.NoError(t, err)
	require.Equal(t, secondCreated, graph.lastReadParams["cursorAt"])
	require.Equal(t, "dream-2", graph.lastReadParams["cursorDreamID"])
	require.Contains(t, graph.lastReadQuery, "sort_at > $cursorAt")

	_, _, err = svc.List(context.Background(), "profile-1", ListOptions{
		Limit:     2,
		Cursor:    next,
		Sort:      DreamSortUpdatedAt,
		Direction: DreamDirectionAsc,
	})
	require.ErrorIs(t, err, ErrInvalidDreamCursor)
}
