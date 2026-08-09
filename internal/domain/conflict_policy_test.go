package domain

import (
	"testing"
	"time"
)

func TestEvaluateRelationshipConflictWaitsForTTLDespiteSupporterMajority(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now.Add(24 * time.Hour),
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupporterCount: 5},
			{PositionID: "pos-b", SupporterCount: 1},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeNoop || evaluation.Stage != ConflictReviewStageWaitingForReviewDue {
		t.Fatalf("evaluation = %+v, want pre-TTL no-op", evaluation)
	}
}

func TestEvaluateRelationshipConflictResolvesStrictSupporterMajorityAfterTTL(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupporterCount: 5},
			{PositionID: "pos-b", SupporterCount: 1},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeResolve || evaluation.Stage != ConflictReviewStageDueMajority || evaluation.PreferredPositionID != "pos-a" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
	if evaluation.TotalSupporterCount != 6 {
		t.Fatalf("TotalSupporterCount = %d, want 6", evaluation.TotalSupporterCount)
	}
}

func TestEvaluateRelationshipConflictDoesNotResolveTieAfterTTL(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupporterCount: 1},
			{PositionID: "pos-b", SupporterCount: 1},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeOverdue || evaluation.PreferredPositionID != "" {
		t.Fatalf("evaluation = %+v, want overdue without winner", evaluation)
	}
}

func TestEvaluateRelationshipConflictPluralityWithoutMajorityStaysOverdue(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupporterCount: 3},
			{PositionID: "pos-b", SupporterCount: 2},
			{PositionID: "pos-c", SupporterCount: 2},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeOverdue ||
		evaluation.Stage != ConflictReviewStageDueNoWinner ||
		evaluation.PreferredPositionID != "" {
		t.Fatalf("evaluation = %+v, want overdue without winner", evaluation)
	}
}

func TestEvaluateRelationshipConflictAuthorityDoesNotVetoSupporterMajority(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupporterCount: 3, RecordedFallback: true},
			{PositionID: "pos-b", SupporterCount: 1},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeResolve || evaluation.PreferredPositionID != "pos-a" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}

func TestEvaluateRelationshipConflictZeroSupportersWaitsThenOverdues(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	before := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now.Add(time.Hour),
		Positions:   []RelationshipConflictPositionRecord{{PositionID: "pos-a"}, {PositionID: "pos-b"}},
	})
	if before.Outcome != ConflictReviewOutcomeNoop {
		t.Fatalf("before TTL evaluation = %+v", before)
	}
	after := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now.Add(time.Hour),
		ReviewDueAt: now,
		Positions:   []RelationshipConflictPositionRecord{{PositionID: "pos-a"}, {PositionID: "pos-b"}},
	})
	if after.Outcome != ConflictReviewOutcomeOverdue {
		t.Fatalf("after TTL evaluation = %+v", after)
	}
}

func TestEvaluateRelationshipConflictIgnoresInvalidPositions(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: " "},
			{PositionID: "pos-a", SupporterCount: 1},
		},
	})

	if evaluation.Outcome != ConflictReviewOutcomeNoop ||
		evaluation.Stage != ConflictReviewStageWaitingForReviewDue ||
		evaluation.Reason != ConflictReviewReasonFewerThanTwoPositions {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}
