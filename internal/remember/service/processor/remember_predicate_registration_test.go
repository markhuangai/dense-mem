package processor

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestRememberPredicateRegistrationDriftRequiresValidatedRequest(t *testing.T) {
	validated := normalizeRememberCommitFailure(knowledgecontract.ErrSubmissionPredicateRegistrationHeld, true)
	require.ErrorIs(t, validated, rememberapp.ErrRememberCommitConflict)
	require.ErrorIs(t, validated, knowledgecontract.ErrSubmissionPredicateRegistrationHeld)
	unvalidated := normalizeRememberCommitFailure(knowledgecontract.ErrSubmissionPredicateRegistrationHeld, false)
	require.NotErrorIs(t, unvalidated, rememberapp.ErrRememberCommitConflict)

	planCause := errors.Join(rememberapp.ErrRememberCommitConflict, knowledgecontract.ErrSubmissionPredicateRegistrationHeld)
	class, code := rememberEmbeddingPlanFailureMetadata(planCause)
	require.Equal(t, "fence_conflict", class)
	require.Equal(t, "predicate_catalog_changed", code)
	class, code = rememberCommitFailureMetadata(validated)
	require.Equal(t, "fence_conflict", class)
	require.Equal(t, "predicate_catalog_changed", code)
	require.Equal(t, rememberapp.SubmissionErrorCommitConflict,
		rememberFailureCode("embedding", &rememberEmbeddingPlanFailure{cause: planCause}))
	require.Equal(t, rememberapp.SubmissionErrorDatabaseFailure,
		rememberFailureCode("embedding", &rememberEmbeddingPlanFailure{cause: unvalidated}))
}
