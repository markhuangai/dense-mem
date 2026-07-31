package domain

import (
	"testing"
	"time"
)

func TestSelectConflictLastWriteWinnerPrefersAuthorityThenAcceptanceTime(t *testing.T) {
	older := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	winner, ok := SelectConflictLastWriteWinner([]ConflictResolutionPosition{
		{
			PositionID: "secondary-newer",
			Supports:   []ConflictResolutionSupport{{Authority: "secondary", AcceptedAt: newer}},
		},
		{
			PositionID: "primary-older",
			Supports:   []ConflictResolutionSupport{{Authority: "primary", AcceptedAt: older}},
		},
	})
	if !ok {
		t.Fatal("SelectConflictLastWriteWinner returned no winner")
	}
	if winner.PositionID != "primary-older" || winner.Authority != "primary" || !winner.AcceptedAt.Equal(older) {
		t.Fatalf("winner = %#v", winner)
	}
}

func TestSelectConflictLastWriteWinnerUsesLatestSupportAtTheSameAuthority(t *testing.T) {
	older := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	winner, ok := SelectConflictLastWriteWinner([]ConflictResolutionPosition{
		{
			PositionID: "older",
			Supports:   []ConflictResolutionSupport{{Authority: "primary", AcceptedAt: older}},
		},
		{
			PositionID: "newer",
			Supports:   []ConflictResolutionSupport{{Authority: "primary", AcceptedAt: newer}},
		},
	})
	if !ok {
		t.Fatal("SelectConflictLastWriteWinner returned no winner")
	}
	if winner.PositionID != "newer" || !winner.AcceptedAt.Equal(newer) {
		t.Fatalf("winner = %#v", winner)
	}
}

func TestSelectConflictLastWriteWinnerHasStableFallbackForUnknownAuthority(t *testing.T) {
	accepted := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	winner, ok := SelectConflictLastWriteWinner([]ConflictResolutionPosition{
		{PositionID: "position-b", Supports: []ConflictResolutionSupport{{Authority: "unknown", AcceptedAt: accepted}}},
		{PositionID: "position-a", Supports: []ConflictResolutionSupport{{Authority: "not-recognized", AcceptedAt: accepted}}},
	})
	if !ok {
		t.Fatal("SelectConflictLastWriteWinner returned no winner")
	}
	if winner.PositionID != "position-a" || winner.Authority != "unknown" {
		t.Fatalf("winner = %#v", winner)
	}
}

func TestSelectConflictLastWriteWinnerUsesBestSupportWithinPosition(t *testing.T) {
	accepted := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	winner, ok := SelectConflictLastWriteWinner([]ConflictResolutionPosition{
		{
			PositionID: "mixed-authority",
			Supports: []ConflictResolutionSupport{
				{Authority: "secondary", AcceptedAt: accepted.Add(time.Hour)},
				{Authority: "authoritative", AcceptedAt: accepted},
			},
		},
		{
			PositionID: "inferred-newer",
			Supports:   []ConflictResolutionSupport{{Authority: "derived", AcceptedAt: accepted.Add(24 * time.Hour)}},
		},
	})
	if !ok {
		t.Fatal("SelectConflictLastWriteWinner returned no winner")
	}
	if winner.PositionID != "mixed-authority" || winner.Authority != "authoritative" || !winner.AcceptedAt.Equal(accepted) {
		t.Fatalf("winner = %#v", winner)
	}
}

func TestSelectConflictLastWriteWinnerSkipsPositionsWithoutComparableSupport(t *testing.T) {
	if _, ok := SelectConflictLastWriteWinner([]ConflictResolutionPosition{{PositionID: ""}, {PositionID: "no-support"}}); ok {
		t.Fatal("SelectConflictLastWriteWinner unexpectedly selected a position")
	}
}
