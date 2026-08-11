package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCoalesceLastUsedUpdatesKeepsNewestTimestampPerKey(t *testing.T) {
	id := uuid.New()
	otherID := uuid.New()
	older := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)

	got := coalesceLastUsedUpdates([]LastUsedUpdate{
		{ID: id, At: older},
		{ID: id, At: newer},
		{ID: id, At: older},
		{ID: otherID, At: newer},
		{ID: uuid.Nil, At: newer},
		{ID: id, At: time.Time{}},
	})

	byID := make(map[uuid.UUID]time.Time, len(got))
	for _, update := range got {
		byID[update.ID] = update.At
	}
	require.Equal(t, map[uuid.UUID]time.Time{id: newer, otherID: newer}, byID)
}
