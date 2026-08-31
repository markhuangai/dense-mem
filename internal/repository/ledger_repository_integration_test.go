package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLedgerValidateRejectsMixedSourceRevisionBatch(t *testing.T) {
	input := normalizeCreateIngestInput(CreateIngestInput{
		TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(),
		Evidence: []EvidenceInput{
			{Content: "first source fragment", SourceKey: "wiki://write-pipeline", SourceRevisionToken: "rev-1"},
			{Content: "second source fragment", SourceKey: "wiki://write-pipeline", SourceRevisionToken: "rev-2"},
		},
	})
	err := validateCreateIngestInput(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revision fields must match")
}
func TestLedgerSourceRevisionCompareAndSet(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-source")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "source-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "source-other-owner")
	repo := NewLedgerRepository(appDB, rls)

	first, err := repo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: "doc://policy",
		RevisionToken: "rev-1", ContentHash: "sha256:first",
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.SourceID)

	second, err := repo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: "doc://policy",
		RevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
		ContentHash: "sha256:second",
	})
	require.NoError(t, err)
	assert.Equal(t, first.SourceID, second.SourceID)
	assert.NotEqual(t, first.SourceRevisionID, second.SourceRevisionID)

	matching, err := repo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: "doc://policy",
		RevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
		ContentHash: "sha256:second",
	})
	require.NoError(t, err)
	assert.Equal(t, second.SourceRevisionID, matching.SourceRevisionID)

	_, err = repo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: otherOwnerID, SourceKey: "doc://policy",
		RevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
		ContentHash: "sha256:second",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceRevisionConflict), fmt.Sprintf("err=%v", err))

	_, err = repo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: "doc://policy",
		RevisionToken: "rev-3", ExpectedPreviousRevisionToken: "rev-1",
		ContentHash: "sha256:third",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceRevisionConflict), fmt.Sprintf("err=%v", err))
}

func TestLedgerAuthorityConstraintsUseCanonicalValues(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-authority")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-authority")

	err := rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO evidence_sources (team_id, owner_profile_id, source_key, source_kind, authority)
			VALUES (?::uuid, ?::uuid, 'doc://canonical-authority', 'document', 'unknown')
		`, teamID, ownerID).Error
	})
	require.NoError(t, err)

	err = rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO evidence_sources (team_id, owner_profile_id, source_key, source_kind, authority)
			VALUES (?::uuid, ?::uuid, 'doc://legacy-derived-authority', 'document', 'derived')
		`, teamID, ownerID).Error
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evidence_sources_authority_check")

	_ = appDB
}

func TestReferenceDefinitionGuardRequiresSystemOrMigrationMode(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-reference-guard")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-reference-guard")

	err := insertPredicateDefinitionForTest(adminDB, "test_missing_mode_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires system or migration mode")

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return insertPredicateDefinitionForTest(tx, "test_profile_mode_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires system or migration mode")

	err = rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return insertPredicateDefinitionForTest(tx, "test_system_mode_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	})
	require.NoError(t, err)
}

func insertPredicateDefinitionForTest(db *gorm.DB, predicateKey string) error {
	return db.Exec(`
		INSERT INTO predicate_definitions (
			predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
			relationship_kind, current_cardinality
		) VALUES (
			?, 1, ARRAY['project']::text[], ARRAY['product']::text[], 'state', 'many'
		)
	`, predicateKey).Error
}
