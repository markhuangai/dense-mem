// Package contract owns the dependency-safe ports and data shapes for the
// knowledge write capability. It imports only domain and capability contracts;
// adapters depend on these declarations rather than the other way around.
package contract

import (
	"context"
	"errors"
)

var (
	ErrIdempotencyConflict                       = errors.New("idempotency conflict")
	ErrSourceRevisionConflict                    = errors.New("source revision conflict")
	ErrEvidenceLifecycleNotFound                 = errors.New("evidence lifecycle target not found")
	ErrEvidenceLifecycleConflict                 = errors.New("evidence lifecycle conflict")
	ErrEvidenceLifecycleIDInvalid                = errors.New("evidence lifecycle ID invalid")
	ErrTeamInactive                              = errors.New("team is not active")
	ErrRememberReplay                            = errors.New("remember result already committed")
	ErrRememberIdempotencyBusy                   = errors.New("remember idempotency key is busy")
	ErrRememberAttemptNotFound                   = errors.New("remember attempt not found")
	ErrRememberFailureRetentionDegraded          = errors.New("remember failure retention synchronization degraded")
	ErrRememberDuplicateCandidateStale           = errors.New("remember duplicate candidate is stale")
	ErrSemanticOwnerMismatch                     = errors.New("semantic owner mismatch")
	ErrSemanticIdempotencyConflict               = errors.New("semantic idempotency conflict")
	ErrSemanticIdentityAlias                     = errors.New("semantic relationship is a legacy identity alias")
	ErrSemanticStaleSource                       = errors.New("semantic source is stale")
	ErrSearchContractMismatch                    = errors.New("search contract mismatch")
	ErrInlineEmbeddingPlanMismatch               = errors.New("inline embedding plan does not match rendered search documents")
	ErrInlineEmbeddingPlanTooLarge               = errors.New("inline embedding plan exceeds the document bound")
	ErrSearchEmbeddingRequired                   = errors.New("synchronous semantic write requires inline embeddings")
	ErrSearchStaleVersion                        = errors.New("search stale source or document version")
	ErrSubmissionAssessmentNotFound              = errors.New("submission assessment not found")
	ErrSubmissionAssessorAttemptConsumed         = errors.New("submission assessor attempt already consumed")
	ErrSubmissionAssessmentScopeMismatch         = errors.New("submission assessment scope mismatch")
	ErrSubmissionPredicateRegistrationHeld       = errors.New("submission predicate registration requires review")
	ErrSubmissionAssessmentNonPromotable         = errors.New("submission assessment is not promotable")
	ErrSubmissionAssessmentKnownEvidenceStale    = errors.New("submission known evidence snapshot is stale")
	ErrRelationshipCorrectionNotFound            = errors.New("relationship correction not found")
	ErrRelationshipCorrectionConfirmation        = errors.New("relationship correction confirmation is invalid")
	ErrRelationshipCorrectionConfirmationExpired = errors.New("relationship correction confirmation expired")
	ErrRelationshipCorrectionStateConflict       = errors.New("relationship correction state conflict")
	ErrEvidenceConflictNotFound                  = errors.New("evidence conflict not found")
	ErrEvidenceConflictVersionStale              = errors.New("evidence conflict version is stale")
	ErrEvidenceConflictNotOpen                   = errors.New("evidence conflict is not open")
	ErrEvidenceConflictInvalidCommand            = errors.New("evidence conflict command is invalid")
	ErrEvidenceConflictStaleInput                = errors.New("evidence conflict citation is stale")
	ErrConflictAssessmentUnavailable             = errors.New("conflict assessment is unavailable")
	ErrConflictAssessmentStale                   = errors.New("conflict assessment is stale")
	ErrConflictAssessmentReserved                = errors.New("conflict assessment is not reserved")
)

// RememberPort is the complete request-owned Remember persistence boundary.
// Provider calls and embedding execution remain outside this port.
type RememberPort interface {
	LoadRememberAttempt(context.Context, RememberAttemptLookupInput) (*RememberAttempt, error)
	PlanRememberDuplicateEmbeddings(context.Context, RememberDuplicateCandidateInput) (*RememberDuplicateEmbeddingPlan, error)
	ResolveRememberDuplicateCandidates(context.Context, RememberDuplicateCandidateInput, []InlineEmbeddingResult) (*RememberDuplicateResolutionResult, error)
	PlanRememberEmbeddings(context.Context, SynchronousRememberCommitInput) (*InlineEmbeddingPlan, error)
	CommitRememberWithEmbeddings(context.Context, SynchronousRememberCommitInput, []InlineEmbeddingResult) (*SynchronousRememberCommitResult, error)
	RecordRememberFailure(context.Context, RememberFailureRecordInput) error
}

type LifecyclePort interface {
	PlanRelationshipCorrectionEmbeddings(context.Context, CorrectRelationshipInput) (*RelationshipCorrectionEmbeddingPlan, error)
	CorrectRelationshipWithEmbeddings(context.Context, CorrectRelationshipInput, []RelationshipCorrectionEmbedding) (*CorrectRelationshipResult, error)
	RetractEvidence(context.Context, RetractEvidenceInput) (*EvidenceLifecycleResult, error)
}

type ConflictPort interface {
	PlanRelationshipConflictResolution(context.Context, RelationshipConflictResolutionInput) (*RelationshipConflictResolutionPlan, error)
	CommitRelationshipConflictResolution(context.Context, CommitRelationshipConflictResolutionInput) (*ApplyOverdueConflictResolutionResult, error)
}
