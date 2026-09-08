package contract

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEvidenceConflictCursorValidation(t *testing.T) {
	teamID, conflictID, eventID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	cursor := EvidenceConflictCursor{
		Version:      1,
		TeamID:       teamID,
		StatusFilter: "open",
		UpdatedAt:    time.Now(),
		ConflictID:   conflictID,
	}
	require.NoError(t, cursor.Validate(teamID, "open"))
	for _, status := range []string{"open", "resolved", "dismissed"} {
		cursor.StatusFilter = status
		require.NoError(t, cursor.Validate(teamID, status))
	}
	cursor.StatusFilter = ""
	require.NoError(t, cursor.Validate(teamID, ""))

	cases := []EvidenceConflictCursor{
		{TeamID: teamID, ConflictID: conflictID, UpdatedAt: cursor.UpdatedAt, StatusFilter: ""},
		{Version: 1, TeamID: teamID, ConflictID: conflictID, StatusFilter: "", UpdatedAt: time.Time{}},
		{Version: 1, TeamID: "bad", ConflictID: conflictID, StatusFilter: "", UpdatedAt: cursor.UpdatedAt},
		{Version: 1, TeamID: teamID, ConflictID: "bad", StatusFilter: "", UpdatedAt: cursor.UpdatedAt},
		{Version: 1, TeamID: teamID, ConflictID: conflictID, StatusFilter: "unknown", UpdatedAt: cursor.UpdatedAt},
		{Version: 1, TeamID: uuid.NewString(), ConflictID: conflictID, StatusFilter: "", UpdatedAt: cursor.UpdatedAt},
		{Version: 1, TeamID: teamID, ConflictID: conflictID, StatusFilter: "open", UpdatedAt: cursor.UpdatedAt},
	}
	for index, invalid := range cases {
		t.Run("invalid_cursor_"+string(rune('a'+index)), func(t *testing.T) {
			require.ErrorIs(t, invalid.Validate(teamID, ""), ErrEvidenceConflictInvalidCommand)
		})
	}

	eventCursor := EvidenceConflictEventCursor{
		Version:    1,
		TeamID:     teamID,
		ConflictID: conflictID,
		Ordinal:    1,
		EventID:    eventID,
	}
	require.NoError(t, eventCursor.Validate(teamID, conflictID))
	invalidEventCursors := []EvidenceConflictEventCursor{
		{TeamID: teamID, ConflictID: conflictID, EventID: eventID, Ordinal: 1},
		{Version: 1, TeamID: teamID, ConflictID: conflictID, EventID: eventID},
		{Version: 1, TeamID: "bad", ConflictID: conflictID, EventID: eventID, Ordinal: 1},
		{Version: 1, TeamID: teamID, ConflictID: "bad", EventID: eventID, Ordinal: 1},
		{Version: 1, TeamID: teamID, ConflictID: conflictID, EventID: "bad", Ordinal: 1},
		{Version: 1, TeamID: uuid.NewString(), ConflictID: conflictID, EventID: eventID, Ordinal: 1},
		{Version: 1, TeamID: teamID, ConflictID: uuid.NewString(), EventID: eventID, Ordinal: 1},
	}
	for index, invalid := range invalidEventCursors {
		t.Run("invalid_event_cursor_"+string(rune('a'+index)), func(t *testing.T) {
			require.ErrorIs(t, invalid.Validate(teamID, conflictID), ErrEvidenceConflictInvalidCommand)
		})
	}
}
