package repository

import "github.com/markhuangai/dense-mem/internal/domain"

const (
	ConflictReviewOutcomeResolve = domain.ConflictReviewOutcomeResolve
	ConflictReviewOutcomeOverdue = domain.ConflictReviewOutcomeOverdue
	ConflictReviewOutcomeNoop    = domain.ConflictReviewOutcomeNoop

	ConflictReviewStageEarlyQuorum            = domain.ConflictReviewStageEarlyQuorum
	ConflictReviewStageDueUniqueAuthoritative = domain.ConflictReviewStageDueUniqueAuthoritative
	ConflictReviewStageDueMajority            = domain.ConflictReviewStageDueMajority
	ConflictReviewStageDueNoWinner            = domain.ConflictReviewStageDueNoWinner
	ConflictReviewStageWaitingForReviewDue    = domain.ConflictReviewStageWaitingForReviewDue
	ConflictReviewStageDismissedNoConflict    = domain.ConflictReviewStageDismissedNoConflict

	ConflictReviewReasonFewerThanTwoPositions            = domain.ConflictReviewReasonFewerThanTwoPositions
	ConflictReviewReasonNoSupportGroups                  = domain.ConflictReviewReasonNoSupportGroups
	ConflictReviewReasonEarlyQuorumSupport               = domain.ConflictReviewReasonEarlyQuorumSupport
	ConflictReviewReasonUniqueAuthoritativeSource        = domain.ConflictReviewReasonUniqueAuthoritativeSource
	ConflictReviewReasonDueMajoritySupport               = domain.ConflictReviewReasonDueMajoritySupport
	ConflictReviewReasonReviewDueWithoutDeterministicWin = domain.ConflictReviewReasonReviewDueWithoutDeterministicWin
	ConflictReviewReasonReviewDueNotReached              = domain.ConflictReviewReasonReviewDueNotReached
	ConflictReviewReasonActiveConflictNoLongerExists     = domain.ConflictReviewReasonActiveConflictNoLongerExists
)

type RelationshipConflictPositionRecord = domain.RelationshipConflictPositionRecord
type RelationshipConflictEvaluationInput = domain.RelationshipConflictEvaluationInput
type RelationshipConflictEvaluation = domain.RelationshipConflictEvaluation

func EvaluateRelationshipConflict(input RelationshipConflictEvaluationInput) RelationshipConflictEvaluation {
	return domain.EvaluateRelationshipConflict(input)
}
