package domain

import (
	"sort"
	"strings"
	"time"
)

const (
	V2ConflictReviewOutcomeResolve = "resolve"
	V2ConflictReviewOutcomeOverdue = "overdue"
	V2ConflictReviewOutcomeNoop    = "no_op"

	V2ConflictReviewStageEarlyQuorum            = "early_quorum"
	V2ConflictReviewStageDueUniqueAuthoritative = "due_unique_authoritative"
	V2ConflictReviewStageDueMajority            = "due_majority"
	V2ConflictReviewStageDueNoWinner            = "due_no_winner"
	V2ConflictReviewStageWaitingForReviewDue    = "waiting_for_review_due"
	V2ConflictReviewStageDismissedNoConflict    = "dismissed_no_active_conflict"

	V2ConflictReviewReasonFewerThanTwoPositions            = "fewer_than_two_positions"
	V2ConflictReviewReasonNoSupportGroups                  = "no_support_groups"
	V2ConflictReviewReasonEarlyQuorumSupport               = "early_quorum_support"
	V2ConflictReviewReasonUniqueAuthoritativeSource        = "unique_authoritative_source"
	V2ConflictReviewReasonDueMajoritySupport               = "due_majority_support"
	V2ConflictReviewReasonReviewDueWithoutDeterministicWin = "review_due_without_deterministic_winner"
	V2ConflictReviewReasonReviewDueNotReached              = "review_due_not_reached"
	V2ConflictReviewReasonActiveConflictNoLongerExists     = "active_conflict_no_longer_exists"
)

type V2RelationshipConflictPositionRecord struct {
	ConflictID              string
	PositionID              string
	PositionKey             string
	Disposition             string
	ObjectEntityID          string
	ObjectValueID           string
	SupportGroupCount       int
	AuthoritativeGroupCount int
	RelationshipIDs         []string
	OwnerProfileIDs         []string
	EvidenceIDs             []string
	EffectiveAt             *time.Time
	EffectiveTimeBasis      string
	RecordedFallback        bool
}

type V2RelationshipConflictEvaluationInput struct {
	Now         time.Time
	ReviewDueAt time.Time
	Positions   []V2RelationshipConflictPositionRecord
}

type V2RelationshipConflictEvaluation struct {
	Outcome                string
	Stage                  string
	PreferredPositionID    string
	Reason                 string
	EffectiveAt            *time.Time
	EffectiveTimeBasis     string
	TotalSupportGroupCount int
}

func EvaluateV2RelationshipConflict(input V2RelationshipConflictEvaluationInput) V2RelationshipConflictEvaluation {
	positions := normalizedV2ConflictPolicyPositions(input.Positions)
	if len(positions) < 2 {
		return V2RelationshipConflictEvaluation{Outcome: V2ConflictReviewOutcomeNoop, Reason: V2ConflictReviewReasonFewerThanTwoPositions}
	}
	totalGroups := 0
	for _, position := range positions {
		totalGroups += position.SupportGroupCount
	}
	if totalGroups <= 0 {
		return V2RelationshipConflictEvaluation{Outcome: V2ConflictReviewOutcomeNoop, Reason: V2ConflictReviewReasonNoSupportGroups}
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
	authoritativePositions := v2ConflictAuthoritativePositionCount(positions)
	hasAuthoritativeOpposition := v2ConflictHasAuthoritativeOpposition(top, positions)
	canOverrideAuthoritativeOpposition := !hasAuthoritativeOpposition ||
		v2ConflictPreferredHasLaterEffectiveTime(top, positions)
	if top.SupportGroupCount >= 4 && topShare > 0.75 && canOverrideAuthoritativeOpposition {
		return V2RelationshipConflictEvaluation{
			Outcome:                V2ConflictReviewOutcomeResolve,
			Stage:                  V2ConflictReviewStageEarlyQuorum,
			PreferredPositionID:    top.PositionID,
			Reason:                 V2ConflictReviewReasonEarlyQuorumSupport,
			EffectiveAt:            top.EffectiveAt,
			EffectiveTimeBasis:     top.EffectiveTimeBasis,
			TotalSupportGroupCount: totalGroups,
		}
	}
	if !input.Now.Before(input.ReviewDueAt) {
		if authoritativePositions == 1 {
			for _, position := range positions {
				if position.AuthoritativeGroupCount > 0 && !position.RecordedFallback {
					return V2RelationshipConflictEvaluation{
						Outcome:                V2ConflictReviewOutcomeResolve,
						Stage:                  V2ConflictReviewStageDueUniqueAuthoritative,
						PreferredPositionID:    position.PositionID,
						Reason:                 V2ConflictReviewReasonUniqueAuthoritativeSource,
						EffectiveAt:            position.EffectiveAt,
						EffectiveTimeBasis:     position.EffectiveTimeBasis,
						TotalSupportGroupCount: totalGroups,
					}
				}
			}
		}
		if top.SupportGroupCount >= 2 && topShare > 0.5 && !hasAuthoritativeOpposition {
			return V2RelationshipConflictEvaluation{
				Outcome:                V2ConflictReviewOutcomeResolve,
				Stage:                  V2ConflictReviewStageDueMajority,
				PreferredPositionID:    top.PositionID,
				Reason:                 V2ConflictReviewReasonDueMajoritySupport,
				EffectiveAt:            top.EffectiveAt,
				EffectiveTimeBasis:     top.EffectiveTimeBasis,
				TotalSupportGroupCount: totalGroups,
			}
		}
		return V2RelationshipConflictEvaluation{
			Outcome:                V2ConflictReviewOutcomeOverdue,
			Stage:                  V2ConflictReviewStageDueNoWinner,
			Reason:                 V2ConflictReviewReasonReviewDueWithoutDeterministicWin,
			TotalSupportGroupCount: totalGroups,
		}
	}
	return V2RelationshipConflictEvaluation{
		Outcome:                V2ConflictReviewOutcomeNoop,
		Stage:                  V2ConflictReviewStageWaitingForReviewDue,
		Reason:                 V2ConflictReviewReasonReviewDueNotReached,
		TotalSupportGroupCount: totalGroups,
	}
}

func normalizedV2ConflictPolicyPositions(
	positions []V2RelationshipConflictPositionRecord,
) []V2RelationshipConflictPositionRecord {
	out := make([]V2RelationshipConflictPositionRecord, 0, len(positions))
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

func v2ConflictAuthoritativePositionCount(positions []V2RelationshipConflictPositionRecord) int {
	count := 0
	for _, position := range positions {
		if position.AuthoritativeGroupCount > 0 && !position.RecordedFallback {
			count++
		}
	}
	return count
}

func v2ConflictHasAuthoritativeOpposition(
	preferred V2RelationshipConflictPositionRecord,
	positions []V2RelationshipConflictPositionRecord,
) bool {
	for _, position := range positions {
		if position.PositionID != preferred.PositionID && position.AuthoritativeGroupCount > 0 && !position.RecordedFallback {
			return true
		}
	}
	return false
}

func v2ConflictPreferredHasLaterEffectiveTime(
	preferred V2RelationshipConflictPositionRecord,
	positions []V2RelationshipConflictPositionRecord,
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
