// Package contract contains the dependency-safe Conflict application ports.
// It owns the bounded shapes shared by the queue, review, evidence-conflict,
// provider and PostgreSQL adapters.
package contract

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
)

type (
	ConflictRuntimeConfig                       = knowledgecontract.ConflictRuntimeConfig
	ConflictReviewRunInput                      = knowledgecontract.ConflictReviewRunInput
	ConflictReviewRunCompleteInput              = knowledgecontract.ConflictReviewRunCompleteInput
	ConflictReviewRunRecord                     = knowledgecontract.ConflictReviewRunRecord
	ClaimRelationshipConflictCasesInput         = knowledgecontract.ClaimRelationshipConflictCasesInput
	ReleaseRelationshipConflictCaseClaimInput   = knowledgecontract.ReleaseRelationshipConflictCaseClaimInput
	ReviewRelationshipConflictCaseInput         = knowledgecontract.ReviewRelationshipConflictCaseInput
	ReviewRelationshipConflictCaseResult        = knowledgecontract.ReviewRelationshipConflictCaseResult
	RelationshipConflictResolutionInput         = knowledgecontract.RelationshipConflictResolutionInput
	RelationshipConflictResolutionDocument      = knowledgecontract.RelationshipConflictResolutionDocument
	RelationshipConflictResolutionFence         = knowledgecontract.RelationshipConflictResolutionFence
	RelationshipConflictResolutionPlan          = knowledgecontract.RelationshipConflictResolutionPlan
	RelationshipConflictResolutionEmbedding     = knowledgecontract.RelationshipConflictResolutionEmbedding
	CommitRelationshipConflictResolutionInput   = knowledgecontract.CommitRelationshipConflictResolutionInput
	ValidateRelationshipConflictContextInput    = knowledgecontract.ValidateRelationshipConflictContextInput
	ReserveOverdueConflictAssessmentInput       = knowledgecontract.ReserveOverdueConflictAssessmentInput
	OverdueConflictAssessmentReservation        = knowledgecontract.OverdueConflictAssessmentReservation
	OverdueConflictAssessmentDossier            = knowledgecontract.OverdueConflictAssessmentDossier
	OverdueConflictAssessmentPosition           = knowledgecontract.OverdueConflictAssessmentPosition
	OverdueConflictAssessmentEvidence           = knowledgecontract.OverdueConflictAssessmentEvidence
	CompleteOverdueConflictAssessmentInput      = knowledgecontract.CompleteOverdueConflictAssessmentInput
	CompleteOverdueConflictAssessmentResult     = knowledgecontract.CompleteOverdueConflictAssessmentResult
	ApplyOverdueConflictResolutionInput         = knowledgecontract.ApplyOverdueConflictResolutionInput
	ApplyOverdueConflictResolutionResult        = knowledgecontract.ApplyOverdueConflictResolutionResult
	ResumePendingOverdueConflictResolutionInput = knowledgecontract.ResumePendingOverdueConflictResolutionInput
	ConflictDerivedEvidenceTarget               = knowledgecontract.ConflictDerivedEvidenceTarget
	ClaimConflictDerivedEvidenceTasksInput      = knowledgecontract.ClaimConflictDerivedEvidenceTasksInput
	StageConflictDerivedEvidenceResult          = knowledgecontract.StageConflictDerivedEvidenceResult
	RelationshipConflictCaseRecord              = tracecontract.RelationshipConflictCaseRecord
	RelationshipConflictPositionRecord          = domain.RelationshipConflictPositionRecord
	RelationshipConflictSupporterRecord         = domain.RelationshipConflictSupporterRecord
	EvidenceConflictPositionRecord              = knowledgecontract.EvidenceConflictPositionRecord
	EvidenceConflictEventRecord                 = knowledgecontract.EvidenceConflictEventRecord
	EvidenceConflictCaseRecord                  = knowledgecontract.EvidenceConflictCaseRecord
	EvidenceConflictListInput                   = knowledgecontract.EvidenceConflictListInput
	EvidenceConflictListResult                  = knowledgecontract.EvidenceConflictListResult
	EvidenceConflictCursor                      = knowledgecontract.EvidenceConflictCursor
	EvidenceConflictGetInput                    = knowledgecontract.EvidenceConflictGetInput
	EvidenceConflictEventCursor                 = knowledgecontract.EvidenceConflictEventCursor
	EvidenceConflictGetResult                   = knowledgecontract.EvidenceConflictGetResult
	EvidenceConflictResolutionInput             = knowledgecontract.EvidenceConflictResolutionInput
)

const (
	ConflictReviewOutcomeResolve                         = domain.ConflictReviewOutcomeResolve
	ConflictReviewOutcomeOverdue                         = domain.ConflictReviewOutcomeOverdue
	ConflictReviewOutcomeNoop                            = domain.ConflictReviewOutcomeNoop
	ConflictReviewStageDueMajority                       = domain.ConflictReviewStageDueMajority
	ConflictReviewStageDueNoWinner                       = domain.ConflictReviewStageDueNoWinner
	ConflictReviewStageWaitingForReviewDue               = domain.ConflictReviewStageWaitingForReviewDue
	ConflictReviewStageDismissedNoConflict               = domain.ConflictReviewStageDismissedNoConflict
	ConflictReviewReasonFewerThanTwoPositions            = domain.ConflictReviewReasonFewerThanTwoPositions
	ConflictReviewReasonDueMajoritySupport               = domain.ConflictReviewReasonDueMajoritySupport
	ConflictReviewReasonReviewDueWithoutDeterministicWin = domain.ConflictReviewReasonReviewDueWithoutDeterministicWin
	ConflictReviewReasonReviewDueNotReached              = domain.ConflictReviewReasonReviewDueNotReached
	ConflictReviewReasonActiveConflictNoLongerExists     = domain.ConflictReviewReasonActiveConflictNoLongerExists
	ConflictAssessmentMaxFailedDays                      = knowledgecontract.ConflictAssessmentMaxFailedDays
	EvidenceConflictDefaultLimit                         = knowledgecontract.EvidenceConflictDefaultLimit
	EvidenceConflictMaxLimit                             = knowledgecontract.EvidenceConflictMaxLimit
	EvidenceConflictDefaultEventLimit                    = knowledgecontract.EvidenceConflictDefaultEventLimit
	EvidenceConflictMaxEventLimit                        = knowledgecontract.EvidenceConflictMaxEventLimit
	EvidenceConflictMaxResults                           = knowledgecontract.EvidenceConflictMaxResults
	EvidenceConflictMaxPositions                         = knowledgecontract.EvidenceConflictMaxPositions
	EvidenceConflictMaxQuoteRunes                        = knowledgecontract.EvidenceConflictMaxQuoteRunes
)

var (
	ErrConflictReviewLeaseLost        = knowledgecontract.ErrConflictReviewLeaseLost
	ErrConflictAssessmentUnavailable  = knowledgecontract.ErrConflictAssessmentUnavailable
	ErrConflictAssessmentStale        = knowledgecontract.ErrConflictAssessmentStale
	ErrConflictAssessmentReserved     = knowledgecontract.ErrConflictAssessmentReserved
	ErrEvidenceConflictNotFound       = knowledgecontract.ErrEvidenceConflictNotFound
	ErrEvidenceConflictVersionStale   = knowledgecontract.ErrEvidenceConflictVersionStale
	ErrEvidenceConflictNotOpen        = knowledgecontract.ErrEvidenceConflictNotOpen
	ErrEvidenceConflictInvalidCommand = knowledgecontract.ErrEvidenceConflictInvalidCommand
	ErrEvidenceConflictStaleInput     = knowledgecontract.ErrEvidenceConflictStaleInput
)

type ConflictQueueRepository interface {
	ListConflictQueue(context.Context, domain.ConflictQueueQuery) (*domain.ConflictQueuePage, error)
	CollectConflictQueueMetrics(context.Context) (domain.ConflictQueueMetricsSnapshot, error)
}

type EvidenceConflictRepository interface {
	ListEvidenceConflicts(context.Context, EvidenceConflictListInput) (*EvidenceConflictListResult, error)
	GetEvidenceConflict(context.Context, EvidenceConflictGetInput) (*EvidenceConflictGetResult, error)
	ResolveEvidenceConflict(context.Context, EvidenceConflictResolutionInput) (*EvidenceConflictCaseRecord, error)
}

// ReviewRepository is the complete application persistence boundary for both
// relationship-conflict review and deletion-only derived evidence. It keeps
// provider execution and embedding execution outside the authoritative store.
type ReviewRepository interface {
	ReviewRelationshipConflictCase(context.Context, ReviewRelationshipConflictCaseInput) (*ReviewRelationshipConflictCaseResult, error)
	ReleaseRelationshipConflictCaseClaim(context.Context, ReleaseRelationshipConflictCaseClaimInput) error
	ResumePendingOverdueConflictResolution(context.Context, ResumePendingOverdueConflictResolutionInput) (*RelationshipConflictResolutionInput, bool, error)
	ReserveOverdueConflictAssessment(context.Context, ReserveOverdueConflictAssessmentInput) (*OverdueConflictAssessmentReservation, *OverdueConflictAssessmentDossier, bool, error)
	CompleteOverdueConflictAssessment(context.Context, CompleteOverdueConflictAssessmentInput) (*CompleteOverdueConflictAssessmentResult, error)
	PlanRelationshipConflictResolution(context.Context, RelationshipConflictResolutionInput) (*RelationshipConflictResolutionPlan, error)
	CommitRelationshipConflictResolution(context.Context, CommitRelationshipConflictResolutionInput) (*ApplyOverdueConflictResolutionResult, error)
	ClaimConflictDerivedEvidenceTasks(context.Context, ClaimConflictDerivedEvidenceTasksInput) ([]ConflictDerivedEvidenceTarget, error)
	StageConflictDerivedEvidence(context.Context, ConflictDerivedEvidenceTarget) (*StageConflictDerivedEvidenceResult, error)
	RecordConflictDerivedEvidenceFailure(context.Context, ConflictDerivedEvidenceTarget, string) error
}

type ReviewRunLedger interface {
	ReviewRepository
	ReserveRelationshipConflictReviewRun(context.Context, ConflictReviewRunInput) (*ConflictReviewRunRecord, bool, error)
	ClaimRelationshipConflictCases(context.Context, ClaimRelationshipConflictCasesInput) ([]RelationshipConflictCaseRecord, error)
	CompleteRelationshipConflictReviewRun(context.Context, ConflictReviewRunCompleteInput) error
}

func EncodeEvidenceConflictCursor(cursor EvidenceConflictCursor) (string, error) {
	return knowledgecontract.EncodeEvidenceConflictCursor(cursor)
}

func DecodeEvidenceConflictCursor(raw string) (*EvidenceConflictCursor, error) {
	return knowledgecontract.DecodeEvidenceConflictCursor(raw)
}

func EncodeEvidenceConflictEventCursor(cursor EvidenceConflictEventCursor) (string, error) {
	return knowledgecontract.EncodeEvidenceConflictEventCursor(cursor)
}

func DecodeEvidenceConflictEventCursor(raw string) (*EvidenceConflictEventCursor, error) {
	return knowledgecontract.DecodeEvidenceConflictEventCursor(raw)
}
