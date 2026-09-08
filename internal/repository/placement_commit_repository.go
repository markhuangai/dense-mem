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
