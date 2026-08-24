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
	emptyTeam := createLedgerTeam(t, adminDB, rls, "submission-catalog-empty-team")
	emptyOwner := createLedgerProfile(t, adminDB, rls, emptyTeam, "submission-catalog-empty-owner")
	repo := NewSemanticRepository(appDB, rls)
	teamAEntity := createSemanticEntity(t, ctx, repo, teamA, ownerA, "project", "Dense Mem")
	teamCEntity := createSemanticEntity(t, ctx, repo, teamC, ownerC, "project", "Dense Mem")
	_, err := repo.AddEntityName(ctx, AddEntityNameInput{
		TeamID: teamA, OwnerProfileID: ownerA, EntityID: teamAEntity.EntityID, DisplayName: "Dense Memory", NameKind: "alias",
	})
	require.NoError(t, err)
	_, err = repo.AddEntityName(ctx, AddEntityNameInput{
		TeamID: teamC, OwnerProfileID: ownerC, EntityID: teamCEntity.EntityID, DisplayName: "Dense Memory", NameKind: "alias",
	})
	require.NoError(t, err)
	inactiveEntity := createSemanticEntity(t, ctx, repo, teamA, ownerA, "project", "Retired Dense Mem")
	_, err = repo.AddEntityName(ctx, AddEntityNameInput{
		TeamID: teamA, OwnerProfileID: ownerA, EntityID: inactiveEntity.EntityID, DisplayName: "Old Dense Memory", NameKind: "alias",
	})
	require.NoError(t, err)
	var retiredRows int64
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamA, func(tx *gorm.DB) error {
		result := tx.Exec(`UPDATE entity_records SET status = 'retired', updated_at = now() WHERE team_id = ?::uuid AND entity_id = ?::uuid`, teamA, inactiveEntity.EntityID)
		retiredRows = result.RowsAffected
		return result.Error
	}))
	require.Equal(t, int64(1), retiredRows)

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
			Ref: "dense-mem", Surface: "  DENSE   MEMORY  ", EntityKind: "project", KnownEntityID: teamCEntity.EntityID,
		}},
		CandidateLimit: 20,
	})
	require.NoError(t, err)
	require.True(t, entityCatalog.Complete)
	require.Len(t, entityCatalog.Groups, 1)
	require.Len(t, entityCatalog.Groups[0].Candidates, 1)
	assert.Equal(t, teamAEntity.EntityID, entityCatalog.Groups[0].Candidates[0].EntityID)
	assert.NotEqual(t, teamCEntity.EntityID, entityCatalog.Groups[0].Candidates[0].EntityID)
	assert.Contains(t, entityCatalog.Groups[0].Candidates[0].ActiveNames, "Dense Memory")

	knownOnlyCatalog, err := repo.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamA, OwnerProfileID: ownerB,
		Entities: []SubmissionAssessmentEntityCatalogTarget{{
			Ref: "known-only", KnownEntityID: teamAEntity.EntityID,
		}},
		CandidateLimit: 20,
	})
	require.NoError(t, err)
	require.Len(t, knownOnlyCatalog.Groups, 1)
	require.Len(t, knownOnlyCatalog.Groups[0].Candidates, 1)
	assert.Equal(t, teamAEntity.EntityID, knownOnlyCatalog.Groups[0].Candidates[0].EntityID)

	exactEntity := createSemanticEntity(t, ctx, repo, teamA, ownerA, "person", "Duplicate Exact Name")
	for range 21 {
		createSemanticEntity(t, ctx, repo, teamA, ownerA, "person", "Duplicate Exact Name")
	}
	exactDuplicateCatalog, err := repo.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamA, OwnerProfileID: ownerB,
		Entities: []SubmissionAssessmentEntityCatalogTarget{{
			Ref: "exact-duplicate", Surface: "Duplicate Exact Name", EntityKind: "person", KnownEntityID: exactEntity.EntityID,
		}},
		CandidateLimit: 20,
	})
	require.NoError(t, err)
	require.True(t, exactDuplicateCatalog.Complete)
	require.Len(t, exactDuplicateCatalog.Groups, 1)
	require.Len(t, exactDuplicateCatalog.Groups[0].Candidates, 1)
	assert.Equal(t, exactEntity.EntityID, exactDuplicateCatalog.Groups[0].Candidates[0].EntityID)

	inactiveCatalog, err := repo.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamA, OwnerProfileID: ownerB,
		Entities: []SubmissionAssessmentEntityCatalogTarget{{
			Ref: "inactive", Surface: "Old Dense Memory", EntityKind: "project", KnownEntityID: inactiveEntity.EntityID,
		}},
		CandidateLimit: 20,
	})
	require.NoError(t, err)
	require.Len(t, inactiveCatalog.Groups, 1)
	assert.Empty(t, inactiveCatalog.Groups[0].Candidates)

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

	emptyTeamPredicates, err := repo.ResolveSemanticReviewPredicateCandidates(ctx, SemanticReviewPredicateResolutionInput{
		TeamID: emptyTeam, OwnerProfileID: emptyOwner, Predicates: []string{"uses"}, Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, emptyTeamPredicates, 1)
	assert.Equal(t, "uses", emptyTeamPredicates[0].Candidate.PredicateKey)

	emptyTeamOptions, err := repo.ListSemanticAssessmentPredicateOptions(ctx, SemanticAssessmentPredicateOptionsInput{
		TeamID: emptyTeam, OwnerProfileID: emptyOwner, QueryText: "Project uses Atlas", ProposedKeys: []string{"uses"}, Limit: 20,
	})
	require.NoError(t, err)
	require.NotEmpty(t, emptyTeamOptions)
	assert.Equal(t, "uses", emptyTeamOptions[0].PredicateKey)
}
