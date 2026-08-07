package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSemanticReviewCatalogListsTeamCandidatesAndPredicateAliases(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "semantic-review-catalog-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "catalog-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "catalog-owner-b")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "semantic-review-catalog-other-team")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, otherTeamID, "catalog-other-owner")
	repo := NewSemanticRepository(appDB, rls)
	entity := createSemanticEntity(t, ctx, repo, teamID, ownerA, "project", "Dense-Mem")
	retiredEntity := createSemanticEntity(t, ctx, repo, teamID, ownerA, "project", "Retired Entity")
	otherEntity := createSemanticEntity(t, ctx, repo, otherTeamID, otherOwnerID, "project", "Other Team")
	var retiredRows int64
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE entity_records
			SET status = 'retired',
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND entity_id = ?::uuid
		`, teamID, retiredEntity.EntityID)
		retiredRows = result.RowsAffected
		return result.Error
	}))
	require.Equal(t, int64(1), retiredRows)

	candidates, err := repo.ListSemanticReviewEntityCandidates(ctx, SemanticReviewEntityCandidateInput{
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

	hidden, err := repo.ListSemanticReviewEntityCandidates(ctx, SemanticReviewEntityCandidateInput{
		TeamID:         otherTeamID,
		OwnerProfileID: otherOwnerID,
		Name:           "Dense-Mem",
		EntityKind:     "project",
	})
	require.NoError(t, err)
	assert.Empty(t, hidden)

	known, err := repo.ListSemanticAssessmentKnownEntities(ctx, SemanticAssessmentKnownEntityInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		EntityIDs:      []string{entity.EntityID, entity.EntityID, retiredEntity.EntityID, otherEntity.EntityID},
	})
	require.NoError(t, err)
	require.Len(t, known, 1)
	assert.Equal(t, entity.EntityID, known[0].EntityID)
	assert.Equal(t, "Dense-Mem", known[0].CanonicalName)

	predicates, err := repo.ListSemanticReviewPredicateCandidates(ctx, SemanticReviewPredicateCandidateInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		Predicate:      "is_working_on",
	})
	require.NoError(t, err)
	require.Len(t, predicates, 1)
	assert.Equal(t, "works_on", predicates[0].PredicateKey)
	assert.Equal(t, 1, predicates[0].Version)

	resolutions, err := repo.ResolveSemanticReviewPredicateCandidates(ctx, SemanticReviewPredicateResolutionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		Predicates:     []string{"is working on", "novel relation"},
	})
	require.NoError(t, err)
	require.Len(t, resolutions, 1)
	assert.Equal(t, "is working on", resolutions[0].RequestedPredicate)
	assert.Equal(t, "alias", resolutions[0].MatchKind)
	assert.Equal(t, "works_on", resolutions[0].Candidate.PredicateKey)
	assert.Contains(t, resolutions[0].Candidate.Aliases, "is_working_on")

	options, err := repo.ListSemanticReviewPredicateOptions(ctx, SemanticReviewPredicateOptionsInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		QueryText:      "Dense-Mem uses PostgreSQL for durable memory.",
	})
	require.NoError(t, err)
	require.NotEmpty(t, options)
	assert.Equal(t, "uses", options[0])
	assert.Contains(t, options, "works_on")
	assert.Contains(t, options, "is_working_on")

	assessmentOptions, err := repo.ListSemanticAssessmentPredicateOptions(ctx, SemanticAssessmentPredicateOptionsInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		QueryText:      "Mark is working on Dense-Mem.",
		Limit:          100,
	})
	require.NoError(t, err)
	require.NotEmpty(t, assessmentOptions)
	assert.Equal(t, "works_on", assessmentOptions[0].PredicateKey)
	assert.Equal(t, 1, assessmentOptions[0].Version)
	assert.Contains(t, assessmentOptions[0].Aliases, "is_working_on")
	assert.NotEmpty(t, assessmentOptions[0].AllowedSubjectKinds)
	assert.NotEmpty(t, assessmentOptions[0].AllowedObjectKinds)
	assert.Equal(t, "active", assessmentOptions[0].LifecycleState)

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_predicate_definitions (
			    team_id, predicate_key, version, aliases, allowed_subject_kinds,
			    allowed_object_kinds, relationship_kind, current_cardinality,
			    lifecycle_state, origin, metadata
			) VALUES (
			    ?::uuid, 'working_on_project', 1, ARRAY['working on project']::text[],
			    ARRAY['person','organization','project','product','other']::text[],
			    ARRAY['project','product','organization','concept','other']::text[],
			    'state', 'many', 'active', 'built_in', '{"source":"test"}'::jsonb
			)
		`, teamID).Error
	}))

	proposedKeyOptions, err := repo.ListSemanticAssessmentPredicateOptions(ctx, SemanticAssessmentPredicateOptionsInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		QueryText:      "Mark is working on project Dense-Mem.",
		ProposedKeys:   []string{"is working on"},
		Limit:          100,
	})
	require.NoError(t, err)
	require.NotEmpty(t, proposedKeyOptions)
	assert.Equal(t, "works_on", proposedKeyOptions[0].PredicateKey)
	competingIndex := -1
	for index, option := range proposedKeyOptions {
		if option.PredicateKey == "working_on_project" {
			competingIndex = index
			break
		}
	}
	assert.Greater(t, competingIndex, 0)

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

	retiredCandidates, err := repo.ListSemanticReviewPredicateCandidates(ctx, SemanticReviewPredicateCandidateInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		Predicate:      "is_working_on",
	})
	require.NoError(t, err)
	assert.Empty(t, retiredCandidates)

	retiredResolutions, err := repo.ResolveSemanticReviewPredicateCandidates(ctx, SemanticReviewPredicateResolutionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		Predicates:     []string{"is working on"},
	})
	require.NoError(t, err)
	assert.Empty(t, retiredResolutions)

	retiredOptions, err := repo.ListSemanticReviewPredicateOptions(ctx, SemanticReviewPredicateOptionsInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		QueryText:      "Mark works on Dense-Mem.",
	})
	require.NoError(t, err)
	assert.NotContains(t, retiredOptions, "works_on")
	assert.NotContains(t, retiredOptions, "is_working_on")
}

func TestSemanticAssessmentEntityMatchesUseOnlyCanonicalAndAliasNames(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "semantic-assessment-name-kinds-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewSemanticRepository(appDB, rls)
	entity := createSemanticEntity(t, ctx, repo, teamID, ownerID, "project", "Dense-Mem")

	_, err := repo.AddEntityName(ctx, AddEntityNameInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EntityID:       entity.EntityID,
		DisplayName:    "DM",
		NameKind:       "alias",
	})
	require.NoError(t, err)
	_, err = repo.AddEntityName(ctx, AddEntityNameInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EntityID:       entity.EntityID,
		DisplayName:    "Dense Memory Legacy",
		NameKind:       "former",
	})
	require.NoError(t, err)

	formerOnly, err := repo.ListSemanticAssessmentEntityMatches(ctx, SemanticAssessmentEntityMatchInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EvidenceText:   "Dense Memory Legacy is no longer the current name.",
		Limit:          5,
	})
	require.NoError(t, err)
	assert.Empty(t, formerOnly.Matches)

	aliasMatches, err := repo.ListSemanticAssessmentEntityMatches(ctx, SemanticAssessmentEntityMatchInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EvidenceText:   "DM retains its canonical project identity.",
		Limit:          5,
	})
	require.NoError(t, err)
	require.Len(t, aliasMatches.Matches, 1)
	assert.Equal(t, entity.EntityID, aliasMatches.Matches[0].Candidate.EntityID)
	assert.Equal(t, "DM", aliasMatches.Matches[0].MatchedName)
}
