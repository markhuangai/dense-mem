package repository

import (
	"errors"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

var (
	ErrConflictReviewLeaseLost           = knowledgepostgres.ErrConflictReviewLeaseLost
	ErrSemanticStaleSource               = knowledgecontract.ErrSemanticStaleSource
	ErrConflictContextStale              = knowledgepostgres.ErrConflictContextStale
	ErrRememberExactReferenceStale       = knowledgepostgres.ErrRememberExactReferenceStale
	ErrCorrectionTargetStale             = knowledgepostgres.ErrCorrectionTargetStale
	errSemanticUnresolvedEndpoint        = errors.New("semantic relationship endpoint is unresolved")
	errSemanticPredicateUnresolved       = errors.New("semantic predicate cannot be resolved safely")
	errRelationshipDecisionNonPromotable = knowledgepostgres.ErrRelationshipDecisionNonPromotable
)

// IsRememberStaleInputError classifies stale errors owned by the PostgreSQL
// adapter so the Remember processor can preserve terminal stale-input
// semantics without importing an adapter package into application policy.
func IsRememberStaleInputError(err error) bool {
	return errors.Is(err, knowledgecontract.ErrSourceRevisionConflict) ||
		errors.Is(err, knowledgecontract.ErrEvidenceLifecycleConflict) ||
		errors.Is(err, knowledgecontract.ErrEvidenceConflictStaleInput) ||
		errors.Is(err, knowledgecontract.ErrSemanticStaleSource) ||
		errors.Is(err, knowledgecontract.ErrSubmissionAssessmentKnownEvidenceStale) ||
		errors.Is(err, knowledgecontract.ErrRememberDuplicateCandidateStale) ||
		errors.Is(err, knowledgepostgres.ErrConflictContextStale) ||
		errors.Is(err, knowledgepostgres.ErrRememberExactReferenceStale) ||
		errors.Is(err, knowledgepostgres.ErrCorrectionTargetStale)
}
