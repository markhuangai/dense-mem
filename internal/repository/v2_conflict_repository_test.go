package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestApplyV2ConflictKnownAtClearsFutureDismissal(t *testing.T) {
	knownAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	dismissedAt := knownAt.Add(time.Hour)
	record := V2RelationshipConflictCaseRecord{
		Status:       string(domain.V2RelationshipConflictDismissed),
		ReviewDueAt:  knownAt.Add(time.Hour),
		NextReviewAt: dismissedAt,
		DismissedAt:  &dismissedAt,
		UpdatedAt:    dismissedAt,
	}

	applyV2ConflictKnownAt(&record, &knownAt)

	assert.Equal(t, string(domain.V2RelationshipConflictOpen), record.Status)
	assert.True(t, record.NextReviewAt.IsZero())
	assert.Nil(t, record.DismissedAt)
}

func TestApplyV2ConflictKnownAtPreservesResolvedStateBeforeFutureDismissal(t *testing.T) {
	knownAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	resolvedAt := knownAt.Add(-time.Hour)
	effectiveAt := resolvedAt.Add(-time.Hour)
	dismissedAt := knownAt.Add(time.Hour)
	record := V2RelationshipConflictCaseRecord{
		Status:              string(domain.V2RelationshipConflictDismissed),
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

	applyV2ConflictKnownAt(&record, &knownAt)

	assert.Equal(t, string(domain.V2RelationshipConflictResolved), record.Status)
	assert.Equal(t, "position-a", record.PreferredPositionID)
	assert.Equal(t, resolvedAt, *record.ResolvedAt)
	assert.Equal(t, effectiveAt, *record.EffectiveAt)
	assert.Equal(t, "valid_time", record.EffectiveTimeBasis)
	assert.Equal(t, "deterministic winner", record.ResolutionReason)
	assert.True(t, record.NextReviewAt.IsZero())
	assert.Nil(t, record.DismissedAt)
}

func TestApplyV2ConflictKnownAtClearsDismissalWhenFutureResolutionIsRewound(t *testing.T) {
	knownAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	resolvedAt := knownAt.Add(time.Hour)
	effectiveAt := resolvedAt
	dismissedAt := knownAt.Add(2 * time.Hour)
	record := V2RelationshipConflictCaseRecord{
		Status:              string(domain.V2RelationshipConflictDismissed),
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

	applyV2ConflictKnownAt(&record, &knownAt)

	assert.Equal(t, string(domain.V2RelationshipConflictOpen), record.Status)
	assert.Empty(t, record.PreferredPositionID)
	assert.Nil(t, record.ResolvedAt)
	assert.Nil(t, record.EffectiveAt)
	assert.Empty(t, record.EffectiveTimeBasis)
	assert.Empty(t, record.ResolutionReason)
	assert.Nil(t, record.DismissedAt)
	assert.True(t, record.NextReviewAt.IsZero())
}

func TestApplyV2ConflictKnownAtRewindsOverdueBeforeReviewDueAt(t *testing.T) {
	knownAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	nextReviewAt := knownAt.Add(24 * time.Hour)
	record := V2RelationshipConflictCaseRecord{
		Status:       string(domain.V2RelationshipConflictOverdue),
		ReviewDueAt:  knownAt.Add(time.Hour),
		NextReviewAt: nextReviewAt,
	}

	applyV2ConflictKnownAt(&record, &knownAt)

	assert.Equal(t, string(domain.V2RelationshipConflictOpen), record.Status)
	assert.True(t, record.NextReviewAt.IsZero())
}

func TestApplyV2ConflictPositionKnownAtDispositions(t *testing.T) {
	knownAt := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	record := V2RelationshipConflictCaseRecord{
		Status: string(domain.V2RelationshipConflictOpen),
		Positions: []V2RelationshipConflictPositionRecord{
			{PositionID: "position-a", Disposition: string(domain.V2RelationshipConflictPositionPreferred)},
			{PositionID: "position-b", Disposition: string(domain.V2RelationshipConflictPositionSuppressedCurrent)},
		},
	}

	applyV2ConflictPositionKnownAtDispositions(&record, &knownAt)

	assert.Equal(t, string(domain.V2RelationshipConflictPositionCandidate), record.Positions[0].Disposition)
	assert.Equal(t, string(domain.V2RelationshipConflictPositionCandidate), record.Positions[1].Disposition)

	record.Status = string(domain.V2RelationshipConflictResolved)
	record.PreferredPositionID = "position-b"

	applyV2ConflictPositionKnownAtDispositions(&record, &knownAt)

	assert.Equal(t, string(domain.V2RelationshipConflictPositionSuppressedCurrent), record.Positions[0].Disposition)
	assert.Equal(t, string(domain.V2RelationshipConflictPositionPreferred), record.Positions[1].Disposition)
}

func TestV2ConflictNextReviewAtDoesNotReturnPastDue(t *testing.T) {
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)

	assert.Equal(t, now.Add(24*time.Hour), v2ConflictNextReviewAt(now, now.Add(-time.Hour)))
	assert.Equal(t, now.Add(24*time.Hour), v2ConflictNextReviewAt(now, now))
}

func TestV2ConflictNextReviewAtUsesFutureDueInsideDailyInterval(t *testing.T) {
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	due := now.Add(time.Hour)

	assert.Equal(t, due, v2ConflictNextReviewAt(now, due))
	assert.Equal(t, now.Add(24*time.Hour), v2ConflictNextReviewAt(now, now.Add(48*time.Hour)))
}

func TestNormalizeV2ConflictReviewRunInputRejectsInvalidTimezone(t *testing.T) {
	_, err := normalizeV2ConflictReviewRunInput(V2ConflictReviewRunInput{
		TeamID:   "00000000-0000-0000-0000-000000000001",
		WorkerID: "worker-a",
		Timezone: "Mars/Base",
	})

	assert.ErrorContains(t, err, "timezone is invalid")
}
