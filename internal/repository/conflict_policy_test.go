package repository

import (
	"testing"
	"time"
)

func TestEvaluateRelationshipConflictUsesSupporterCountsAfterTTL(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupporterCount: 5},
			{PositionID: "pos-b", SupporterCount: 1},
		},
	})
	if evaluation.Outcome != ConflictReviewOutcomeResolve || evaluation.PreferredPositionID != "pos-a" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}

func TestEvaluateRelationshipConflictWaitsBeforeTTL(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now.Add(time.Hour),
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupporterCount: 5},
			{PositionID: "pos-b", SupporterCount: 1},
		},
	})
	if evaluation.Outcome != ConflictReviewOutcomeNoop || evaluation.Stage != ConflictReviewStageWaitingForReviewDue {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}

func TestEvaluateRelationshipConflictTieRemainsOverdue(t *testing.T) {
	now := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
		Now:         now,
		ReviewDueAt: now,
		Positions: []RelationshipConflictPositionRecord{
			{PositionID: "pos-a", SupporterCount: 1},
			{PositionID: "pos-b", SupporterCount: 1},
		},
	})
	if evaluation.Outcome != ConflictReviewOutcomeOverdue || evaluation.Stage != ConflictReviewStageDueNoWinner {
		t.Fatalf("evaluation = %+v", evaluation)
	}
}
