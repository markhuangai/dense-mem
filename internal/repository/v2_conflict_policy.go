package repository

import "github.com/markhuangai/dense-mem/internal/domain"

const (
	V2ConflictReviewOutcomeResolve = domain.V2ConflictReviewOutcomeResolve
	V2ConflictReviewOutcomeOverdue = domain.V2ConflictReviewOutcomeOverdue
	V2ConflictReviewOutcomeNoop    = domain.V2ConflictReviewOutcomeNoop

	V2ConflictReviewStageEarlyQuorum            = domain.V2ConflictReviewStageEarlyQuorum
	V2ConflictReviewStageDueUniqueAuthoritative = domain.V2ConflictReviewStageDueUniqueAuthoritative
	V2ConflictReviewStageDueMajority            = domain.V2ConflictReviewStageDueMajority
	V2ConflictReviewStageDueNoWinner            = domain.V2ConflictReviewStageDueNoWinner
	V2ConflictReviewStageWaitingForReviewDue    = domain.V2ConflictReviewStageWaitingForReviewDue
	V2ConflictReviewStageDismissedNoConflict    = domain.V2ConflictReviewStageDismissedNoConflict

	V2ConflictReviewReasonFewerThanTwoPositions            = domain.V2ConflictReviewReasonFewerThanTwoPositions
	V2ConflictReviewReasonNoSupportGroups                  = domain.V2ConflictReviewReasonNoSupportGroups
	V2ConflictReviewReasonEarlyQuorumSupport               = domain.V2ConflictReviewReasonEarlyQuorumSupport
	V2ConflictReviewReasonUniqueAuthoritativeSource        = domain.V2ConflictReviewReasonUniqueAuthoritativeSource
	V2ConflictReviewReasonDueMajoritySupport               = domain.V2ConflictReviewReasonDueMajoritySupport
	V2ConflictReviewReasonReviewDueWithoutDeterministicWin = domain.V2ConflictReviewReasonReviewDueWithoutDeterministicWin
	V2ConflictReviewReasonReviewDueNotReached              = domain.V2ConflictReviewReasonReviewDueNotReached
	V2ConflictReviewReasonActiveConflictNoLongerExists     = domain.V2ConflictReviewReasonActiveConflictNoLongerExists
)

type V2RelationshipConflictPositionRecord = domain.V2RelationshipConflictPositionRecord
type V2RelationshipConflictEvaluationInput = domain.V2RelationshipConflictEvaluationInput
type V2RelationshipConflictEvaluation = domain.V2RelationshipConflictEvaluation

func EvaluateV2RelationshipConflict(input V2RelationshipConflictEvaluationInput) V2RelationshipConflictEvaluation {
	return domain.EvaluateV2RelationshipConflict(input)
}
