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

	ConflictReviewStageEarlyQuorum            = "early_quorum"
	ConflictReviewStageDueUniqueAuthoritative = "due_unique_authoritative"
	ConflictReviewStageDueMajority            = "due_majority"
	ConflictReviewStageDueNoWinner            = "due_no_winner"
	ConflictReviewStageWaitingForReviewDue    = "waiting_for_review_due"
	ConflictReviewStageDismissedNoConflict    = "dismissed_no_active_conflict"

	ConflictReviewReasonFewerThanTwoPositions            = "fewer_than_two_positions"
	ConflictReviewReasonNoSupportGroups                  = "no_support_groups"
	ConflictReviewReasonEarlyQuorumSupport               = "early_quorum_support"
	ConflictReviewReasonUniqueAuthoritativeSource        = "unique_authoritative_source"
	ConflictReviewReasonDueMajoritySupport               = "due_majority_support"
	ConflictReviewReasonReviewDueWithoutDeterministicWin = "review_due_without_deterministic_winner"
	ConflictReviewReasonReviewDueNotReached              = "review_due_not_reached"
	ConflictReviewReasonActiveConflictNoLongerExists     = "active_conflict_no_longer_exists"
)

type RelationshipConflictPositionRecord struct {
	ConflictID              string
	PositionID              string
	PositionKey             string
	PositionCount           int
	Disposition             string
	ObjectEntityID          string
	ObjectValueID           string
	SupporterCount          int
	SupportGroupCount       int
	AuthoritativeGroupCount int
	SupportersTruncated     bool
	PositionsTruncated      bool
	Supporters              []RelationshipConflictSupporterRecord
	RelationshipIDs         []string
	OwnerProfileIDs         []string
	EvidenceIDs             []string
	EffectiveAt             *time.Time
	EffectiveTimeBasis      string
	RecordedFallback        bool
}

type RelationshipConflictSupporterRecord struct {
	ProfileID          string
	ProfileName        string
	StrongestAuthority string
	EvidenceID         string
	AcceptedAt         time.Time
	SourceGroupCount   int
}

type RelationshipConflictEvaluationInput struct {
	Now         time.Time
	ReviewDueAt time.Time
	Positions   []RelationshipConflictPositionRecord
}

type RelationshipConflictEvaluation struct {
	Outcome                string
	Stage                  string
	PreferredPositionID    string
	Reason                 string
	EffectiveAt            *time.Time
	EffectiveTimeBasis     string
	TotalSupportGroupCount int
}

func EvaluateRelationshipConflict(input RelationshipConflictEvaluationInput) RelationshipConflictEvaluation {
	positions := normalizedConflictPolicyPositions(input.Positions)
	if len(positions) < 2 {
		return RelationshipConflictEvaluation{Outcome: ConflictReviewOutcomeNoop, Reason: ConflictReviewReasonFewerThanTwoPositions}
	}
	totalGroups := 0
	for _, position := range positions {
		totalGroups += position.SupportGroupCount
	}
	if totalGroups <= 0 {
		return RelationshipConflictEvaluation{Outcome: ConflictReviewOutcomeNoop, Reason: ConflictReviewReasonNoSupportGroups}
	}
	sort.SliceStable(positions, func(i, j int) bool {
		if positions[i].SupportGroupCount != positions[j].SupportGroupCount {
			return positions[i].SupportGroupCount > positions[j].SupportGroupCount
		}
		if positions[i].AuthoritativeGroupCount != positions[j].AuthoritativeGroupCount {
			return positions[i].AuthoritativeGroupCount > positions[j].AuthoritativeGroupCount
		}
		return positions[i].PositionID < positions[j].PositionID
	})
	top := positions[0]
	topShare := float64(top.SupportGroupCount) / float64(totalGroups)
	authoritativePositions := conflictAuthoritativePositionCount(positions)
	hasAuthoritativeOpposition := conflictHasAuthoritativeOpposition(top, positions)
	canOverrideAuthoritativeOpposition := !hasAuthoritativeOpposition ||
		conflictPreferredHasLaterEffectiveTime(top, positions)
	if top.SupportGroupCount >= 4 && topShare > 0.75 && canOverrideAuthoritativeOpposition {
		return RelationshipConflictEvaluation{
			Outcome:                ConflictReviewOutcomeResolve,
			Stage:                  ConflictReviewStageEarlyQuorum,
			PreferredPositionID:    top.PositionID,
			Reason:                 ConflictReviewReasonEarlyQuorumSupport,
			EffectiveAt:            top.EffectiveAt,
			EffectiveTimeBasis:     top.EffectiveTimeBasis,
			TotalSupportGroupCount: totalGroups,
		}
	}
	if !input.Now.Before(input.ReviewDueAt) {
		if authoritativePositions == 1 {
			for _, position := range positions {
				if position.AuthoritativeGroupCount > 0 && !position.RecordedFallback {
					return RelationshipConflictEvaluation{
						Outcome:                ConflictReviewOutcomeResolve,
						Stage:                  ConflictReviewStageDueUniqueAuthoritative,
						PreferredPositionID:    position.PositionID,
						Reason:                 ConflictReviewReasonUniqueAuthoritativeSource,
						EffectiveAt:            position.EffectiveAt,
						EffectiveTimeBasis:     position.EffectiveTimeBasis,
						TotalSupportGroupCount: totalGroups,
					}
				}
			}
		}
		if top.SupportGroupCount >= 2 && topShare > 0.5 && canOverrideAuthoritativeOpposition {
			return RelationshipConflictEvaluation{
				Outcome:                ConflictReviewOutcomeResolve,
				Stage:                  ConflictReviewStageDueMajority,
				PreferredPositionID:    top.PositionID,
				Reason:                 ConflictReviewReasonDueMajoritySupport,
				EffectiveAt:            top.EffectiveAt,
				EffectiveTimeBasis:     top.EffectiveTimeBasis,
				TotalSupportGroupCount: totalGroups,
			}
		}
		return RelationshipConflictEvaluation{
			Outcome:                ConflictReviewOutcomeOverdue,
			Stage:                  ConflictReviewStageDueNoWinner,
			Reason:                 ConflictReviewReasonReviewDueWithoutDeterministicWin,
			TotalSupportGroupCount: totalGroups,
		}
	}
	return RelationshipConflictEvaluation{
		Outcome:                ConflictReviewOutcomeNoop,
		Stage:                  ConflictReviewStageWaitingForReviewDue,
		Reason:                 ConflictReviewReasonReviewDueNotReached,
		TotalSupportGroupCount: totalGroups,
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
		if position.PositionID == "" || position.SupportGroupCount <= 0 {
			continue
		}
		if position.AuthoritativeGroupCount < 0 {
			position.AuthoritativeGroupCount = 0
		}
		out = append(out, position)
	}
	return out
}

func conflictAuthoritativePositionCount(positions []RelationshipConflictPositionRecord) int {
	count := 0
	for _, position := range positions {
		if position.AuthoritativeGroupCount > 0 && !position.RecordedFallback {
			count++
		}
	}
	return count
}

func conflictHasAuthoritativeOpposition(
	preferred RelationshipConflictPositionRecord,
	positions []RelationshipConflictPositionRecord,
) bool {
	for _, position := range positions {
		if position.PositionID != preferred.PositionID && position.AuthoritativeGroupCount > 0 && !position.RecordedFallback {
			return true
		}
	}
	return false
}

func conflictPreferredHasLaterEffectiveTime(
	preferred RelationshipConflictPositionRecord,
	positions []RelationshipConflictPositionRecord,
) bool {
	if preferred.EffectiveAt == nil {
		return false
	}
	for _, position := range positions {
		if position.PositionID == preferred.PositionID || position.AuthoritativeGroupCount == 0 || position.RecordedFallback {
			continue
		}
		if position.EffectiveAt == nil || !preferred.EffectiveAt.After(*position.EffectiveAt) {
			return false
		}
	}
	return true
}
