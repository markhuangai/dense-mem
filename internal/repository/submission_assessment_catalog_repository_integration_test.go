package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSubmissionAssessmentCatalogIsTeamScopedAndUsesBoundedPredicateResolution(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "submission-catalog-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "submission-catalog-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "submission-catalog-owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "submission-catalog-team-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "submission-catalog-owner-c")
	repo := NewSemanticRepository(appDB, rls)
	teamAEntity := createSemanticEntity(t, ctx, repo, teamA, ownerA, "project", "Dense Mem")
	teamCEntity := createSemanticEntity(t, ctx, repo, teamC, ownerC, "project", "Dense Mem")

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamA, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_predicate_definitions (
			    team_id, predicate_key, version, aliases, allowed_subject_kinds,
			    allowed_object_kinds, relationship_kind, current_cardinality,
			    lifecycle_state, origin, metadata
			) VALUES (
			    ?::uuid, 'team_a_only', 1, ARRAY[]::text[], ARRAY['concept']::text[],
			    ARRAY['concept']::text[], 'state', 'many', 'active', 'built_in', '{}'::jsonb
			)
		`, teamA).Error
	}))

	entityCatalog, err := repo.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID:         teamA,
		OwnerProfileID: ownerB,
		Entities: []SubmissionAssessmentEntityCatalogTarget{{
			Ref: "dense-mem", Surface: "  DENSE   MEM  ", EntityKind: "project", KnownEntityID: teamCEntity.EntityID,
		}},
		CandidateLimit: 20,
	})
	require.NoError(t, err)
	require.True(t, entityCatalog.Complete)
	require.Len(t, entityCatalog.Groups, 1)
	require.Len(t, entityCatalog.Groups[0].Candidates, 1)
	assert.Equal(t, teamAEntity.EntityID, entityCatalog.Groups[0].Candidates[0].EntityID)
	assert.NotEqual(t, teamCEntity.EntityID, entityCatalog.Groups[0].Candidates[0].EntityID)

	teamAPredicates, err := repo.ResolveSemanticReviewPredicateCandidates(ctx, SemanticReviewPredicateResolutionInput{
		TeamID: teamA, OwnerProfileID: ownerB, Predicates: []string{"team_a_only"}, Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, teamAPredicates, 1)
	assert.Equal(t, "team_a_only", teamAPredicates[0].Candidate.PredicateKey)

	teamCPredicates, err := repo.ResolveSemanticReviewPredicateCandidates(ctx, SemanticReviewPredicateResolutionInput{
		TeamID: teamC, OwnerProfileID: ownerC, Predicates: []string{"team_a_only"}, Limit: 2,
	})
	require.NoError(t, err)
	assert.Empty(t, teamCPredicates)

	options, err := repo.ListSemanticAssessmentPredicateOptions(ctx, SemanticAssessmentPredicateOptionsInput{
		TeamID: teamA, OwnerProfileID: ownerB, QueryText: "Alpha uses team_a_only", ProposedKeys: []string{"team_a_only"}, Limit: 100,
	})
	require.NoError(t, err)
	require.NotEmpty(t, options)
	assert.Equal(t, "team_a_only", options[0].PredicateKey)
}
