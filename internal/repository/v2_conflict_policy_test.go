package repository

import (
	"testing"
	"time"
)

func TestEvaluateV2RelationshipConflictResolvesEarlyQuorum(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateV2RelationshipConflict(V2RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now.Add(24 * time.Hour),
		Positions: []V2RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 4},
			{PositionID: "pos-b", SupportGroupCount: 1},
		},
	})

	if evaluation.Outcome != V2ConflictReviewOutcomeResolve {
		t.Fatalf("Outcome = %q, want resolve", evaluation.Outcome)
	}
	if evaluation.Stage != "early_quorum" || evaluation.PreferredPositionID != "pos-a" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}

func TestEvaluateV2RelationshipConflictDoesNotUseTTLLoneAsWinner(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateV2RelationshipConflict(V2RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now.Add(-time.Minute),
		Positions: []V2RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 1},
			{PositionID: "pos-b", SupportGroupCount: 1},
		},
	})

	if evaluation.Outcome != V2ConflictReviewOutcomeOverdue {
		t.Fatalf("Outcome = %q, want overdue", evaluation.Outcome)
	}
	if evaluation.PreferredPositionID != "" {
		t.Fatalf("PreferredPositionID = %q, want empty", evaluation.PreferredPositionID)
	}
}

func TestEvaluateV2RelationshipConflictResolvesDueUniqueAuthoritative(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateV2RelationshipConflict(V2RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []V2RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 1, AuthoritativeGroupCount: 1},
			{PositionID: "pos-b", SupportGroupCount: 3},
		},
	})

	if evaluation.Outcome != V2ConflictReviewOutcomeResolve {
		t.Fatalf("Outcome = %q, want resolve", evaluation.Outcome)
	}
	if evaluation.Stage != "due_unique_authoritative" || evaluation.PreferredPositionID != "pos-a" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}

func TestEvaluateV2RelationshipConflictRecordedFallbackDoesNotOverrideAuthority(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateV2RelationshipConflict(V2RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []V2RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 3, AuthoritativeGroupCount: 1, RecordedFallback: true},
			{PositionID: "pos-b", SupportGroupCount: 1, AuthoritativeGroupCount: 1},
		},
	})

	if evaluation.Outcome != V2ConflictReviewOutcomeResolve {
		t.Fatalf("Outcome = %q, want resolve", evaluation.Outcome)
	}
	if evaluation.PreferredPositionID != "pos-b" {
		t.Fatalf("PreferredPositionID = %q, want pos-b", evaluation.PreferredPositionID)
	}
}

func TestEvaluateV2RelationshipConflictAuthoritativeOppositionBlocksMajority(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateV2RelationshipConflict(V2RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []V2RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 3, AuthoritativeGroupCount: 1},
			{PositionID: "pos-b", SupportGroupCount: 1, AuthoritativeGroupCount: 1},
		},
	})

	if evaluation.Outcome != V2ConflictReviewOutcomeOverdue {
		t.Fatalf("Outcome = %q, want overdue", evaluation.Outcome)
	}
}

func TestEvaluateV2RelationshipConflictLaterEffectiveTimeDoesNotWeakenAuthoritativeOverrideThreshold(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	oldEffective := now.Add(-48 * time.Hour)
	newEffective := now.Add(-24 * time.Hour)
	evaluation := EvaluateV2RelationshipConflict(V2RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []V2RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 2, AuthoritativeGroupCount: 1, EffectiveAt: &newEffective, EffectiveTimeBasis: "valid_from"},
			{PositionID: "pos-b", SupportGroupCount: 1, AuthoritativeGroupCount: 1, EffectiveAt: &oldEffective},
		},
	})

	if evaluation.Outcome != V2ConflictReviewOutcomeOverdue {
		t.Fatalf("Outcome = %q, want overdue", evaluation.Outcome)
	}
}

func TestEvaluateV2RelationshipConflictLaterEffectiveTimeCanOverrideAuthoritativeOpposition(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	oldEffective := now.Add(-48 * time.Hour)
	newEffective := now.Add(-24 * time.Hour)
	evaluation := EvaluateV2RelationshipConflict(V2RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now.Add(24 * time.Hour),
		Positions: []V2RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 4, EffectiveAt: &newEffective, EffectiveTimeBasis: "valid_from"},
			{PositionID: "pos-b", SupportGroupCount: 1, AuthoritativeGroupCount: 1, EffectiveAt: &oldEffective},
		},
	})

	if evaluation.Outcome != V2ConflictReviewOutcomeResolve {
		t.Fatalf("Outcome = %q, want resolve", evaluation.Outcome)
	}
	if evaluation.EffectiveAt == nil || !evaluation.EffectiveAt.Equal(newEffective) {
		t.Fatalf("EffectiveAt = %v, want %v", evaluation.EffectiveAt, newEffective)
	}
}
