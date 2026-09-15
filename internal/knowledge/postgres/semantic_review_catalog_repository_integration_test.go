//go:build integration

package postgres

import (
	"context"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestSemanticCatalogsOmitSealedGenerationEntities(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "semantic-catalog-sealed-generation")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "semantic-catalog-sealed-owner")
	repo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	catalog := NewStore(appDB, rls, ConflictRuntimeConfig{})
	activeEntity := createSemanticEntity(t, ctx, repo, teamID, ownerID, "project", "Active Catalog Entity")
	privateSpace, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).EnsureCredentialPrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
	require.NoError(t, err)
	var privateGeneration int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, privateSpace.ID).Row().Scan(&privateGeneration)
	}))
	privateEntityID := uuid.New()
	privateNameID := uuid.New()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO entity_records (
				team_id, entity_id, entity_kind, identity_context, metadata, space_id, space_generation
			) VALUES (?, ?, 'project', '{}'::jsonb, '{}'::jsonb, ?, ?)
		`, teamID, privateEntityID, privateSpace.ID, privateGeneration).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO entity_names (
				team_id, entity_name_id, entity_id, owner_profile_id, display_name, normalized_name,
				name_kind, metadata, space_id, space_generation
			) VALUES (?, ?, ?, ?, 'Sealed Catalog Entity', 'sealed catalog entity', 'canonical', '{}'::jsonb, ?, ?)
		`, teamID, privateNameID, privateEntityID, ownerID, privateSpace.ID, privateGeneration).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET lifecycle_state = 'sealed', generation = generation + 1, sealed_at = now(), updated_at = now()
			WHERE id = ?::uuid
		`, privateSpace.ID).Error
	}))
	privateCtx := requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{{ID: privateSpace.ID, Kind: domain.MemorySpaceCredentialPrivate}})
	entityCatalog, err := catalog.ListSubmissionAssessmentEntityCatalog(privateCtx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: privateSpace.ID.String(),
		Entities: []SubmissionAssessmentEntityCatalogTarget{{
			Ref: "sealed-catalog", Surface: "Sealed Catalog Entity", EntityKind: "project", KnownEntityID: privateEntityID.String(),
		}}, CandidateLimit: 5,
	})
	require.NoError(t, err)
	require.True(t, entityCatalog.Complete)
	require.Len(t, entityCatalog.Groups, 1)
	assert.Empty(t, entityCatalog.Groups[0].Candidates, "native entity catalog must omit sealed-generation entities")

	reviewCatalog, err := catalog.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerID, Entities: []SubmissionAssessmentEntityCatalogTarget{{
			Ref: "sealed-review", Surface: "Sealed Catalog Entity", EntityKind: "project",
		}}, CandidateLimit: 5,
	})
	require.NoError(t, err)
	require.Len(t, reviewCatalog.Groups, 1)
	assert.Empty(t, reviewCatalog.Groups[0].Candidates)

	knownCatalog, err := catalog.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerID, Entities: []SubmissionAssessmentEntityCatalogTarget{
			{Ref: "active", KnownEntityID: activeEntity.EntityID},
			{Ref: "private", KnownEntityID: privateEntityID.String()},
		}, CandidateLimit: 1,
	})
	require.NoError(t, err)
	knownEntities := flattenEntityCatalogCandidates(knownCatalog)
	require.Len(t, knownEntities, 1)
	assert.Equal(t, activeEntity.EntityID, knownEntities[0].EntityID)

	matchCatalog, err := catalog.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerID, Entities: []SubmissionAssessmentEntityCatalogTarget{
			{Ref: "active-match", Surface: "Active Catalog Entity"},
			{Ref: "sealed-match", Surface: "Sealed Catalog Entity"},
		}, CandidateLimit: 5,
	})
	require.NoError(t, err)
	matches := flattenEntityCatalogCandidates(matchCatalog)
	require.Len(t, matches, 1)
	assert.Equal(t, activeEntity.EntityID, matches[0].EntityID)
}

func TestSemanticReviewCatalogListsTeamCandidatesAndPredicateAliases(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "semantic-review-catalog-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "catalog-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "catalog-owner-b")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "semantic-review-catalog-other-team")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, otherTeamID, "catalog-other-owner")
	repo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	catalog := NewStore(appDB, rls, ConflictRuntimeConfig{})
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

	candidateCatalog, err := repo.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerB,
		Entities:       []SubmissionAssessmentEntityCatalogTarget{{Ref: "candidate", Surface: " DENSE-MEM ", EntityKind: "project"}},
		CandidateLimit: 5,
	})
	require.NoError(t, err)
	candidates := flattenEntityCatalogCandidates(candidateCatalog)
	require.Len(t, candidates, 1)
	assert.Equal(t, entity.EntityID, candidates[0].EntityID)
	assert.Equal(t, "Dense-Mem", candidates[0].CanonicalName)

	hiddenCatalog, err := repo.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: otherTeamID, OwnerProfileID: otherOwnerID,
		Entities:       []SubmissionAssessmentEntityCatalogTarget{{Ref: "hidden", Surface: "Dense-Mem", EntityKind: "project"}},
		CandidateLimit: 5,
	})
	require.NoError(t, err)
	assert.Empty(t, flattenEntityCatalogCandidates(hiddenCatalog))

	knownCatalog, err := repo.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerB, CandidateLimit: 1,
		Entities: []SubmissionAssessmentEntityCatalogTarget{
			{Ref: "entity-1", KnownEntityID: entity.EntityID},
			{Ref: "entity-duplicate", KnownEntityID: entity.EntityID},
			{Ref: "retired", KnownEntityID: retiredEntity.EntityID},
			{Ref: "other", KnownEntityID: otherEntity.EntityID},
		},
	})
	require.NoError(t, err)
	known := flattenEntityCatalogCandidates(knownCatalog)
	known = uniqueEntityCandidates(known)
	require.Len(t, known, 1)
	assert.Equal(t, entity.EntityID, known[0].EntityID)
	assert.Equal(t, "Dense-Mem", known[0].CanonicalName)

	resolutions, err := repo.ResolveSemanticReviewPredicateCandidates(ctx, SemanticReviewPredicateResolutionInput{
		TeamID: teamID, OwnerProfileID: ownerB, Predicates: []string{"is_working_on"}, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, resolutions, 1)
	assert.Equal(t, "works_on", resolutions[0].Candidate.PredicateKey)
	assert.Equal(t, 1, resolutions[0].Candidate.Version)

	resolutions, err = catalog.ResolveSemanticReviewPredicateCandidates(ctx, SemanticReviewPredicateResolutionInput{
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

	optionCandidates, err := repo.ListSemanticAssessmentPredicateOptions(ctx, SemanticAssessmentPredicateOptionsInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		QueryText:      "Dense-Mem uses PostgreSQL for durable memory.",
	})
	require.NoError(t, err)
	options := flattenPredicateOptions(optionCandidates)
	require.NotEmpty(t, options)
	assert.Equal(t, "uses", options[0])
	assert.Contains(t, options, "works_on")
	assert.Contains(t, options, "is_working_on")

	assessmentOptions, err := catalog.ListSemanticAssessmentPredicateOptions(ctx, SemanticAssessmentPredicateOptionsInput{
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

	proposedKeyOptions, err := catalog.ListSemanticAssessmentPredicateOptions(ctx, SemanticAssessmentPredicateOptionsInput{
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

	retiredCandidates, err := repo.ResolveSemanticReviewPredicateCandidates(ctx, SemanticReviewPredicateResolutionInput{
		TeamID: teamID, OwnerProfileID: ownerB, Predicates: []string{"is_working_on"}, Limit: 1,
	})
	require.NoError(t, err)
	assert.Empty(t, retiredCandidates)

	retiredResolutions, err := catalog.ResolveSemanticReviewPredicateCandidates(ctx, SemanticReviewPredicateResolutionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		Predicates:     []string{"is working on"},
	})
	require.NoError(t, err)
	assert.Empty(t, retiredResolutions)

	retiredOptionCandidates, err := repo.ListSemanticAssessmentPredicateOptions(ctx, SemanticAssessmentPredicateOptionsInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		QueryText:      "Mark works on Dense-Mem.",
	})
	require.NoError(t, err)
	retiredOptions := flattenPredicateOptions(retiredOptionCandidates)
	assert.NotContains(t, retiredOptions, "works_on")
	assert.NotContains(t, retiredOptions, "is_working_on")
}

func TestSemanticAssessmentEntityMatchesUseOnlyCanonicalAndAliasNames(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "semantic-assessment-name-kinds-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewStore(appDB, rls, ConflictRuntimeConfig{})
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

	formerOnly, err := repo.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerID, CandidateLimit: 5,
		Entities: []SubmissionAssessmentEntityCatalogTarget{{Ref: "former", Surface: "Dense Memory Legacy"}},
	})
	require.NoError(t, err)
	assert.Empty(t, flattenEntityCatalogCandidates(formerOnly))

	aliasCatalog, err := repo.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: teamID, OwnerProfileID: ownerID, CandidateLimit: 5,
		Entities: []SubmissionAssessmentEntityCatalogTarget{{Ref: "alias", Surface: "DM"}},
	})
	require.NoError(t, err)
	aliasMatches := flattenEntityCatalogCandidates(aliasCatalog)
	require.Len(t, aliasMatches, 1)
	assert.Equal(t, entity.EntityID, aliasMatches[0].EntityID)
	assert.Contains(t, aliasMatches[0].ActiveNames, "DM")
}

func flattenEntityCatalogCandidates(result SubmissionAssessmentEntityCatalogResult) []SemanticReviewEntityCandidate {
	var candidates []SemanticReviewEntityCandidate
	for _, group := range result.Groups {
		candidates = append(candidates, group.Candidates...)
	}
	return candidates
}

func uniqueEntityCandidates(candidates []SemanticReviewEntityCandidate) []SemanticReviewEntityCandidate {
	seen := make(map[string]struct{}, len(candidates))
	result := make([]SemanticReviewEntityCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seen[candidate.EntityID]; ok {
			continue
		}
		seen[candidate.EntityID] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

func flattenPredicateOptions(candidates []SemanticReviewPredicateCandidate) []string {
	values := make([]string, 0, len(candidates)*2)
	seen := make(map[string]struct{}, len(candidates)*2)
	for _, candidate := range candidates {
		for _, value := range append([]string{candidate.PredicateKey}, candidate.Aliases...) {
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			values = append(values, value)
		}
	}
	return values
}
