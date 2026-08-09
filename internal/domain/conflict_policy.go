package domain

import (
	"sort"
	"strings"
	"time"
)

const (
	ConflictReviewOutcomeResolve = "resolve"
	ConflictReviewOutcomeOverdue = "overdue"
	ConflictReviewOutcomeNoop    = "no_op"

	ConflictReviewStageDueMajority         = "due_supporter_majority"
	ConflictReviewStageDueNoWinner         = "due_no_winner"
	ConflictReviewStageWaitingForReviewDue = "waiting_for_review_due"
	ConflictReviewStageDismissedNoConflict = "dismissed_no_active_conflict"

	ConflictReviewReasonFewerThanTwoPositions            = "fewer_than_two_positions"
	ConflictReviewReasonDueMajoritySupport               = "due_supporter_majority"
	ConflictReviewReasonReviewDueWithoutDeterministicWin = "review_due_without_deterministic_winner"
	ConflictReviewReasonReviewDueNotReached              = "review_due_not_reached"
	ConflictReviewReasonActiveConflictNoLongerExists     = "active_conflict_no_longer_exists"
)

type RelationshipConflictPositionRecord struct {
	ConflictID          string
	PositionID          string
	PositionKey         string
	PositionCount       int
	Disposition         string
	ObjectEntityID      string
	ObjectValueID       string
	SupporterCount      int
	SupportersTruncated bool
	PositionsTruncated  bool
	Supporters          []RelationshipConflictSupporterRecord
	RelationshipIDs     []string
	OwnerProfileIDs     []string
	EvidenceIDs         []string
	EffectiveAt         *time.Time
	EffectiveTimeBasis  string
	RecordedFallback    bool
}

type RelationshipConflictSupporterRecord struct {
	ProfileID          string
	ProfileName        string
	StrongestAuthority string
	EvidenceID         string
	AcceptedAt         time.Time
}

type RelationshipConflictEvaluationInput struct {
	Now         time.Time
	ReviewDueAt time.Time
	Positions   []RelationshipConflictPositionRecord
}

type RelationshipConflictEvaluation struct {
	Outcome             string
	Stage               string
	PreferredPositionID string
	Reason              string
	EffectiveAt         *time.Time
	EffectiveTimeBasis  string
	TotalSupporterCount int
}

func EvaluateRelationshipConflict(input RelationshipConflictEvaluationInput) RelationshipConflictEvaluation {
	positions := normalizedConflictPolicyPositions(input.Positions)
	if len(positions) < 2 {
		return RelationshipConflictEvaluation{
			Outcome: ConflictReviewOutcomeNoop,
			Stage:   ConflictReviewStageWaitingForReviewDue,
			Reason:  ConflictReviewReasonFewerThanTwoPositions,
		}
	}
	totalSupporters := 0
	for _, position := range positions {
		totalSupporters += position.SupporterCount
	}
	if totalSupporters <= 0 {
		if input.Now.Before(input.ReviewDueAt) {
			return RelationshipConflictEvaluation{
				Outcome:             ConflictReviewOutcomeNoop,
				Stage:               ConflictReviewStageWaitingForReviewDue,
				Reason:              ConflictReviewReasonReviewDueNotReached,
				TotalSupporterCount: totalSupporters,
			}
		}
		return RelationshipConflictEvaluation{
			Outcome:             ConflictReviewOutcomeOverdue,
			Stage:               ConflictReviewStageDueNoWinner,
			Reason:              ConflictReviewReasonReviewDueWithoutDeterministicWin,
			TotalSupporterCount: totalSupporters,
		}
	}
	sort.SliceStable(positions, func(i, j int) bool {
		if positions[i].SupporterCount != positions[j].SupporterCount {
			return positions[i].SupporterCount > positions[j].SupporterCount
		}
		return positions[i].PositionID < positions[j].PositionID
	})
	top := positions[0]
	if !input.Now.Before(input.ReviewDueAt) {
		if top.SupporterCount > totalSupporters-top.SupporterCount {
			return RelationshipConflictEvaluation{
				Outcome:             ConflictReviewOutcomeResolve,
				Stage:               ConflictReviewStageDueMajority,
				PreferredPositionID: top.PositionID,
				Reason:              ConflictReviewReasonDueMajoritySupport,
				EffectiveAt:         top.EffectiveAt,
				EffectiveTimeBasis:  top.EffectiveTimeBasis,
				TotalSupporterCount: totalSupporters,
			}
		}
		return RelationshipConflictEvaluation{
			Outcome:             ConflictReviewOutcomeOverdue,
			Stage:               ConflictReviewStageDueNoWinner,
			Reason:              ConflictReviewReasonReviewDueWithoutDeterministicWin,
			TotalSupporterCount: totalSupporters,
		}
	}
	return RelationshipConflictEvaluation{
		Outcome:             ConflictReviewOutcomeNoop,
		Stage:               ConflictReviewStageWaitingForReviewDue,
		Reason:              ConflictReviewReasonReviewDueNotReached,
		TotalSupporterCount: totalSupporters,
	}
}

func normalizedConflictPolicyPositions(
	positions []RelationshipConflictPositionRecord,
) []RelationshipConflictPositionRecord {
	out := make([]RelationshipConflictPositionRecord, 0, len(positions))
	for _, position := range positions {
		position.PositionID = strings.TrimSpace(position.PositionID)
		position.PositionKey = strings.TrimSpace(position.PositionKey)
		position.EffectiveTimeBasis = strings.TrimSpace(position.EffectiveTimeBasis)
		if position.PositionID == "" {
			continue
		}
		if position.SupporterCount < 0 {
			position.SupporterCount = 0
		}
		out = append(out, position)
	}
	return out
}
