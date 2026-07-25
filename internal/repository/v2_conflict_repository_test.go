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
		Status:      string(domain.V2RelationshipConflictDismissed),
		ReviewDueAt: knownAt.Add(time.Hour),
		DismissedAt: &dismissedAt,
		UpdatedAt:   dismissedAt,
	}

	applyV2ConflictKnownAt(&record, &knownAt)

	assert.Equal(t, string(domain.V2RelationshipConflictOpen), record.Status)
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
	assert.Nil(t, record.DismissedAt)
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
