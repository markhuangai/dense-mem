package postgres

import knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"

var (
	ErrIdempotencyConflict                       = knowledgecontract.ErrIdempotencyConflict
	ErrSourceRevisionConflict                    = knowledgecontract.ErrSourceRevisionConflict
	ErrEvidenceLifecycleNotFound                 = knowledgecontract.ErrEvidenceLifecycleNotFound
	ErrEvidenceLifecycleConflict                 = knowledgecontract.ErrEvidenceLifecycleConflict
	ErrEvidenceLifecycleIDInvalid                = knowledgecontract.ErrEvidenceLifecycleIDInvalid
	ErrTeamInactive                              = knowledgecontract.ErrTeamInactive
	ErrRememberReplay                            = knowledgecontract.ErrRememberReplay
	ErrRememberAttemptNotFound                   = knowledgecontract.ErrRememberAttemptNotFound
	ErrRememberFailureRetentionDegraded          = knowledgecontract.ErrRememberFailureRetentionDegraded
	ErrRememberDuplicateCandidateStale           = knowledgecontract.ErrRememberDuplicateCandidateStale
	ErrSemanticOwnerMismatch                     = knowledgecontract.ErrSemanticOwnerMismatch
	ErrSemanticIdempotencyConflict               = knowledgecontract.ErrSemanticIdempotencyConflict
	ErrSemanticIdentityAlias                     = knowledgecontract.ErrSemanticIdentityAlias
	ErrSemanticStaleSource                       = knowledgecontract.ErrSemanticStaleSource
	ErrSearchContractMismatch                    = knowledgecontract.ErrSearchContractMismatch
	ErrInlineEmbeddingPlanMismatch               = knowledgecontract.ErrInlineEmbeddingPlanMismatch
	ErrInlineEmbeddingPlanTooLarge               = knowledgecontract.ErrInlineEmbeddingPlanTooLarge
	ErrSearchEmbeddingRequired                   = knowledgecontract.ErrSearchEmbeddingRequired
	ErrSearchStaleVersion                        = knowledgecontract.ErrSearchStaleVersion
	ErrSubmissionAssessmentNotFound              = knowledgecontract.ErrSubmissionAssessmentNotFound
	ErrSubmissionAssessorAttemptConsumed         = knowledgecontract.ErrSubmissionAssessorAttemptConsumed
	ErrSubmissionAssessmentScopeMismatch         = knowledgecontract.ErrSubmissionAssessmentScopeMismatch
	ErrSubmissionPredicateRegistrationHeld       = knowledgecontract.ErrSubmissionPredicateRegistrationHeld
	ErrSubmissionAssessmentNonPromotable         = knowledgecontract.ErrSubmissionAssessmentNonPromotable
	ErrSubmissionAssessmentKnownEvidenceStale    = knowledgecontract.ErrSubmissionAssessmentKnownEvidenceStale
	ErrRelationshipCorrectionNotFound            = knowledgecontract.ErrRelationshipCorrectionNotFound
	ErrRelationshipCorrectionConfirmation        = knowledgecontract.ErrRelationshipCorrectionConfirmation
	ErrRelationshipCorrectionConfirmationExpired = knowledgecontract.ErrRelationshipCorrectionConfirmationExpired
	ErrRelationshipCorrectionStateConflict       = knowledgecontract.ErrRelationshipCorrectionStateConflict
	ErrEvidenceConflictNotFound                  = knowledgecontract.ErrEvidenceConflictNotFound
	ErrEvidenceConflictVersionStale              = knowledgecontract.ErrEvidenceConflictVersionStale
	ErrEvidenceConflictNotOpen                   = knowledgecontract.ErrEvidenceConflictNotOpen
	ErrEvidenceConflictInvalidCommand            = knowledgecontract.ErrEvidenceConflictInvalidCommand
	ErrEvidenceConflictStaleInput                = knowledgecontract.ErrEvidenceConflictStaleInput
	ErrConflictAssessmentUnavailable             = knowledgecontract.ErrConflictAssessmentUnavailable
	ErrConflictAssessmentStale                   = knowledgecontract.ErrConflictAssessmentStale
	ErrConflictAssessmentReserved                = knowledgecontract.ErrConflictAssessmentReserved
)
