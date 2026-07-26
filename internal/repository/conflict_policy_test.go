package repository

import (
	"testing"
	"time"
)

func TestEvaluateRelationshipConflictResolvesEarlyQuorum(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now.Add(24 * time.Hour),
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 4},
			{PositionID: "pos-b", SupportGroupCount: 1},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeResolve {
		t.Fatalf("Outcome = %q, want resolve", evaluation.Outcome)
	}
	if evaluation.Stage != ConflictReviewStageEarlyQuorum || evaluation.PreferredPositionID != "pos-a" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}

func TestEvaluateRelationshipConflictDoesNotUseTTLLoneAsWinner(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now.Add(-time.Minute),
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 1},
			{PositionID: "pos-b", SupportGroupCount: 1},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeOverdue {
		t.Fatalf("Outcome = %q, want overdue", evaluation.Outcome)
	}
	if evaluation.PreferredPositionID != "" {
		t.Fatalf("PreferredPositionID = %q, want empty", evaluation.PreferredPositionID)
	}
}

func TestEvaluateRelationshipConflictResolvesDueUniqueAuthoritative(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 1, AuthoritativeGroupCount: 1},
			{PositionID: "pos-b", SupportGroupCount: 3},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeResolve {
		t.Fatalf("Outcome = %q, want resolve", evaluation.Outcome)
	}
	if evaluation.Stage != ConflictReviewStageDueUniqueAuthoritative || evaluation.PreferredPositionID != "pos-a" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}

func TestEvaluateRelationshipConflictRecordedFallbackDoesNotOverrideAuthority(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 3, AuthoritativeGroupCount: 1, RecordedFallback: true},
			{PositionID: "pos-b", SupportGroupCount: 1, AuthoritativeGroupCount: 1},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeResolve {
		t.Fatalf("Outcome = %q, want resolve", evaluation.Outcome)
	}
	if evaluation.PreferredPositionID != "pos-b" {
		t.Fatalf("PreferredPositionID = %q, want pos-b", evaluation.PreferredPositionID)
	}
}

func TestEvaluateRelationshipConflictAuthoritativeOppositionBlocksMajority(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 3, AuthoritativeGroupCount: 1},
			{PositionID: "pos-b", SupportGroupCount: 1, AuthoritativeGroupCount: 1},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeOverdue {
		t.Fatalf("Outcome = %q, want overdue", evaluation.Outcome)
	}
}

func TestEvaluateRelationshipConflictLaterEffectiveTimeCanOverrideDueMajorityOpposition(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	oldEffective := now.Add(-48 * time.Hour)
	newEffective := now.Add(-24 * time.Hour)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 2, AuthoritativeGroupCount: 1, EffectiveAt: &newEffective, EffectiveTimeBasis: "valid_from"},
			{PositionID: "pos-b", SupportGroupCount: 1, AuthoritativeGroupCount: 1, EffectiveAt: &oldEffective},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeResolve {
		t.Fatalf("Outcome = %q, want resolve", evaluation.Outcome)
	}
	if evaluation.Stage != ConflictReviewStageDueMajority || evaluation.PreferredPositionID != "pos-a" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
	if evaluation.EffectiveAt == nil || !evaluation.EffectiveAt.Equal(newEffective) {
		t.Fatalf("EffectiveAt = %v, want %v", evaluation.EffectiveAt, newEffective)
	}
}

func TestEvaluateRelationshipConflictLaterEffectiveTimeCanOverrideAuthoritativeOpposition(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	oldEffective := now.Add(-48 * time.Hour)
	newEffective := now.Add(-24 * time.Hour)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now.Add(24 * time.Hour),
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupportGroupCount: 4, EffectiveAt: &newEffective, EffectiveTimeBasis: "valid_from"},
			{PositionID: "pos-b", SupportGroupCount: 1, AuthoritativeGroupCount: 1, EffectiveAt: &oldEffective},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeResolve {
		t.Fatalf("Outcome = %q, want resolve", evaluation.Outcome)
	}
	if evaluation.EffectiveAt == nil || !evaluation.EffectiveAt.Equal(newEffective) {
		t.Fatalf("EffectiveAt = %v, want %v", evaluation.EffectiveAt, newEffective)
	}
}

func TestEvaluateRelationshipConflictBreaksTiesByPositionID(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-b", SupportGroupCount: 2},
			{PositionID: "pos-a", SupportGroupCount: 2},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeOverdue {
		t.Fatalf("Outcome = %q, want overdue", evaluation.Outcome)
	}
	if evaluation.Stage != ConflictReviewStageDueNoWinner {
		t.Fatalf("Stage = %q, want %q", evaluation.Stage, ConflictReviewStageDueNoWinner)
	}
}
