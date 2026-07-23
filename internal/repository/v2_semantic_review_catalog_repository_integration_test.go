package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV2SemanticReviewCatalogListsTeamCandidatesAndPredicateAliases(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "semantic-review-catalog-team")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamID, "catalog-owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamID, "catalog-owner-b")
	otherTeamID := createV2LedgerTeam(t, adminDB, rls, "semantic-review-catalog-other-team")
	otherOwnerID := createV2LedgerProfile(t, adminDB, rls, otherTeamID, "catalog-other-owner")
	repo := NewV2SemanticRepository(appDB, rls)
	entity := createV2SemanticEntity(t, ctx, repo, teamID, ownerA, "project", "Dense-Mem")

	candidates, err := repo.ListV2SemanticReviewEntityCandidates(ctx, V2SemanticReviewEntityCandidateInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		Name:           " DENSE-MEM ",
		EntityKind:     "project",
		Limit:          5,
	})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.Equal(t, entity.EntityID, candidates[0].EntityID)
	assert.Equal(t, "Dense-Mem", candidates[0].CanonicalName)

	hidden, err := repo.ListV2SemanticReviewEntityCandidates(ctx, V2SemanticReviewEntityCandidateInput{
		TeamID:         otherTeamID,
		OwnerProfileID: otherOwnerID,
		Name:           "Dense-Mem",
		EntityKind:     "project",
	})
	require.NoError(t, err)
	assert.Empty(t, hidden)

	predicates, err := repo.ListV2SemanticReviewPredicateCandidates(ctx, V2SemanticReviewPredicateCandidateInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		Predicate:      "is_working_on",
	})
	require.NoError(t, err)
	require.Len(t, predicates, 1)
	assert.Equal(t, "works_on", predicates[0].PredicateKey)
	assert.Equal(t, 1, predicates[0].Version)

	resolutions, err := repo.ResolveV2SemanticReviewPredicateCandidates(ctx, V2SemanticReviewPredicateResolutionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		Predicates:     []string{"is working on", "novel relation"},
	})
	require.NoError(t, err)
	require.Len(t, resolutions, 1)
	assert.Equal(t, "is working on", resolutions[0].RequestedPredicate)
	assert.Equal(t, "alias", resolutions[0].MatchKind)
	assert.Equal(t, "works_on", resolutions[0].Candidate.PredicateKey)

	options, err := repo.ListV2SemanticReviewPredicateOptions(ctx, V2SemanticReviewPredicateOptionsInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		QueryText:      "Dense-Mem uses PostgreSQL for durable memory.",
	})
	require.NoError(t, err)
	require.NotEmpty(t, options)
	assert.Equal(t, "uses", options[0])
	assert.Contains(t, options, "works_on")
	assert.Contains(t, options, "is_working_on")

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_predicate_definitions (
			    team_id, predicate_key, version, aliases, allowed_subject_kinds,
			    allowed_object_kinds, relationship_kind, current_cardinality,
			    lifecycle_state, origin, metadata
			)
			SELECT team_id, predicate_key, version + 1, aliases, allowed_subject_kinds,
			       allowed_object_kinds, relationship_kind, current_cardinality,
			       'retired', 'operator', '{"reason":"test retirement"}'::jsonb
			FROM team_predicate_definitions
			WHERE team_id = ?::uuid
			  AND predicate_key = 'works_on'
			  AND version = 1
		`, teamID).Error
	}))

	retiredCandidates, err := repo.ListV2SemanticReviewPredicateCandidates(ctx, V2SemanticReviewPredicateCandidateInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		Predicate:      "is_working_on",
	})
	require.NoError(t, err)
	assert.Empty(t, retiredCandidates)

	retiredResolutions, err := repo.ResolveV2SemanticReviewPredicateCandidates(ctx, V2SemanticReviewPredicateResolutionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		Predicates:     []string{"is working on"},
	})
	require.NoError(t, err)
	assert.Empty(t, retiredResolutions)

	retiredOptions, err := repo.ListV2SemanticReviewPredicateOptions(ctx, V2SemanticReviewPredicateOptionsInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		QueryText:      "Mark works on Dense-Mem.",
	})
	require.NoError(t, err)
	assert.NotContains(t, retiredOptions, "works_on")
	assert.NotContains(t, retiredOptions, "is_working_on")
}
