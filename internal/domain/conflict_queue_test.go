package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestConflictQueueCursorRoundTripAndScope(t *testing.T) {
	teamID := uuid.NewString()
	cursor := ConflictQueueCursor{
		Version: 1, TeamID: teamID, StatusFilter: "overdue", Status: "overdue",
		NextReviewAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), ConflictID: uuid.NewString(),
	}
	raw, err := EncodeConflictQueueCursor(cursor)
	require.NoError(t, err)
	decoded, err := DecodeConflictQueueCursor(raw)
	require.NoError(t, err)
	require.Equal(t, cursor, *decoded)
	require.ErrorIs(t, decoded.ValidateScope(uuid.NewString(), "overdue"), ErrConflictQueueCursorScope)
	require.ErrorIs(t, decoded.ValidateScope(teamID, "open"), ErrConflictQueueCursorScope)
}

func TestConflictQueueAllowListsBoundLabels(t *testing.T) {
	require.Equal(t, "none", NormalizeConflictQueueFailureClass(""))
	require.Equal(t, "timeout", NormalizeConflictQueueFailureClass("TIMEOUT"))
	require.Equal(t, "unknown", NormalizeConflictQueueFailureClass("database-detail"))
	require.Equal(t, "selected", NormalizeConflictQueueAssessmentDecision("selected"))
	require.Equal(t, "unknown", NormalizeConflictQueueAssessmentDecision("superseded"))
	require.Equal(t, "last_write_wins", NormalizeConflictQueueResolutionMethod("last_write_wins"))
	require.Equal(t, "unknown", NormalizeConflictQueueResolutionOutcome("applied"))
}
