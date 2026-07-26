package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestApplyConflictKnownAtClearsFutureDismissal(t *testing.T) {
	knownAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	dismissedAt := knownAt.Add(time.Hour)
	record := RelationshipConflictCaseRecord{
		Status:       string(domain.RelationshipConflictDismissed),
		ReviewDueAt:  knownAt.Add(time.Hour),
		NextReviewAt: dismissedAt,
		DismissedAt:  &dismissedAt,
		UpdatedAt:    dismissedAt,
	}

	applyConflictKnownAt(&record, &knownAt)

	assert.Equal(t, string(domain.RelationshipConflictOpen), record.Status)
	assert.True(t, record.NextReviewAt.IsZero())
	assert.Nil(t, record.DismissedAt)
}

func TestApplyConflictKnownAtPreservesResolvedStateBeforeFutureDismissal(t *testing.T) {
	knownAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	resolvedAt := knownAt.Add(-time.Hour)
	effectiveAt := resolvedAt.Add(-time.Hour)
	dismissedAt := knownAt.Add(time.Hour)
	record := RelationshipConflictCaseRecord{
		Status:              string(domain.RelationshipConflictDismissed),
		ReviewDueAt:         knownAt.Add(-2 * time.Hour),
		PreferredPositionID: "position-a",
		ResolvedAt:          &resolvedAt,
		EffectiveAt:         &effectiveAt,
		EffectiveTimeBasis:  "valid_time",
		ResolutionReason:    "deterministic winner",
		NextReviewAt:        dismissedAt,
		DismissedAt:         &dismissedAt,
		UpdatedAt:           dismissedAt,
	}

	applyConflictKnownAt(&record, &knownAt)

	assert.Equal(t, string(domain.RelationshipConflictResolved), record.Status)
	assert.Equal(t, "position-a", record.PreferredPositionID)
	assert.Equal(t, resolvedAt, *record.ResolvedAt)
	assert.Equal(t, effectiveAt, *record.EffectiveAt)
	assert.Equal(t, "valid_time", record.EffectiveTimeBasis)
	assert.Equal(t, "deterministic winner", record.ResolutionReason)
	assert.True(t, record.NextReviewAt.IsZero())
	assert.Nil(t, record.DismissedAt)
}

func TestApplyConflictKnownAtClearsDismissalWhenFutureResolutionIsRewound(t *testing.T) {
	knownAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	resolvedAt := knownAt.Add(time.Hour)
	effectiveAt := resolvedAt
	dismissedAt := knownAt.Add(2 * time.Hour)
	record := RelationshipConflictCaseRecord{
		Status:              string(domain.RelationshipConflictDismissed),
		ReviewDueAt:         knownAt.Add(time.Hour),
		PreferredPositionID: "position-a",
		ResolvedAt:          &resolvedAt,
		EffectiveAt:         &effectiveAt,
		EffectiveTimeBasis:  "recorded_at",
		ResolutionReason:    "future resolution",
		NextReviewAt:        dismissedAt,
		DismissedAt:         &dismissedAt,
		UpdatedAt:           dismissedAt,
	}

	applyConflictKnownAt(&record, &knownAt)

	assert.Equal(t, string(domain.RelationshipConflictOpen), record.Status)
	assert.Empty(t, record.PreferredPositionID)
	assert.Nil(t, record.ResolvedAt)
	assert.Nil(t, record.EffectiveAt)
	assert.Empty(t, record.EffectiveTimeBasis)
	assert.Empty(t, record.ResolutionReason)
	assert.Nil(t, record.DismissedAt)
	assert.True(t, record.NextReviewAt.IsZero())
}

func TestApplyConflictKnownAtRewindsOverdueBeforeReviewDueAt(t *testing.T) {
	knownAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	nextReviewAt := knownAt.Add(24 * time.Hour)
	record := RelationshipConflictCaseRecord{
		Status:       string(domain.RelationshipConflictOverdue),
		ReviewDueAt:  knownAt.Add(time.Hour),
		NextReviewAt: nextReviewAt,
	}

	applyConflictKnownAt(&record, &knownAt)

	assert.Equal(t, string(domain.RelationshipConflictOpen), record.Status)
	assert.True(t, record.NextReviewAt.IsZero())
}

func TestApplyConflictPositionKnownAtDispositions(t *testing.T) {
	knownAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	record := RelationshipConflictCaseRecord{
		Status: string(domain.RelationshipConflictOpen),
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "position-a", Disposition: string(domain.RelationshipConflictPositionPreferred)},
			{PositionID: "position-b", Disposition: string(domain.RelationshipConflictPositionSuppressedCurrent)},
		},
	}

	applyConflictPositionKnownAtDispositions(&record, &knownAt)

	assert.Equal(t, string(domain.RelationshipConflictPositionCandidate), record.Positions[0].Disposition)
	assert.Equal(t, string(domain.RelationshipConflictPositionCandidate), record.Positions[1].Disposition)

	record.Status = string(domain.RelationshipConflictResolved)
	record.PreferredPositionID = "position-b"

	applyConflictPositionKnownAtDispositions(&record, &knownAt)

	assert.Equal(t, string(domain.RelationshipConflictPositionSuppressedCurrent), record.Positions[0].Disposition)
	assert.Equal(t, string(domain.RelationshipConflictPositionPreferred), record.Positions[1].Disposition)
}

func TestConflictNextReviewAtDoesNotReturnPastDue(t *testing.T) {
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)

	assert.Equal(t, now.Add(24*time.Hour), conflictNextReviewAt(now, now.Add(-time.Hour)))
	assert.Equal(t, now.Add(24*time.Hour), conflictNextReviewAt(now, now))
}

func TestConflictNextReviewAtUsesFutureDueInsideDailyInterval(t *testing.T) {
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	due := now.Add(time.Hour)

	assert.Equal(t, due, conflictNextReviewAt(now, due))
	assert.Equal(t, now.Add(24*time.Hour), conflictNextReviewAt(now, now.Add(48*time.Hour)))
}

func TestNormalizeConflictReviewRunInputRejectsInvalidTimezone(t *testing.T) {
	_, err := normalizeConflictReviewRunInput(ConflictReviewRunInput{
		TeamID:   "00000000-0000-0000-0000-000000000001",
		WorkerID: "worker-a",
		Timezone: "Mars/Base",
	})

	assert.ErrorContains(t, err, "timezone is invalid")
}
