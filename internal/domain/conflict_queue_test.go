package domain

import (
	"encoding/base64"
	"encoding/json"
	"strings"
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

func TestDecodeConflictQueueCursorRejectsMalformedPayloads(t *testing.T) {
	valid := ConflictQueueCursor{
		Version: 1, TeamID: uuid.NewString(), Status: "open", ConflictID: uuid.NewString(),
		NextReviewAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
	}
	encode := func(value any) string {
		payload, err := json.Marshal(value)
		require.NoError(t, err)
		return base64.RawURLEncoding.EncodeToString(payload)
	}

	unsupportedVersion := valid
	unsupportedVersion.Version = 2
	zeroReview := valid
	zeroReview.NextReviewAt = time.Time{}
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "oversized", raw: strings.Repeat("a", 1025)},
		{name: "invalid base64", raw: "not valid base64"},
		{name: "unsupported version", raw: encode(unsupportedVersion)},
		{name: "zero review time", raw: encode(zeroReview)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeConflictQueueCursor(test.raw)
			require.ErrorIs(t, err, ErrInvalidConflictQueueCursor)
		})
	}
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
