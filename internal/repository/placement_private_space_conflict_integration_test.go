package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlacementConflictDoesNotCombinePrivateSpacesAndPersistsPlacementSpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "placement-private-space-conflict", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "placement-private-space-conflict-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "placement-private-space-conflict-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "placement-private-space-conflict-owner-b")
	ledgerRepo := NewLedgerRepositoryWithRuntimeConfig(appDB, rls, 20, ConflictRuntimeConfig{
		ReviewTTLDays: 2,
		Timezone:      "UTC",
	})
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem private conflict")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL private")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB private")
	first := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-private-conflict-a",
		"private-conflict-a", "Dense-Mem private uses PostgreSQL.", subject.EntityID, postgres.EntityID, "private-source-group-a",
	)
	firstRelationship := first.RelationshipResults[0].Relationship
	var spaceA, spaceB string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			INSERT INTO memory_spaces (team_id, kind, owner_profile_id)
			VALUES (?, 'profile_private', ?)
			RETURNING id::text
		`, teamID, ownerA).Row().Scan(&spaceA); err != nil {
			return err
		}
		if err := tx.Raw(`
			INSERT INTO memory_spaces (team_id, kind, owner_profile_id)
			VALUES (?, 'profile_private', ?)
			RETURNING id::text
		`, teamID, ownerB).Row().Scan(&spaceB); err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE relationship_records
			SET space_id = ?::uuid
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, spaceA, teamID, firstRelationship.RelationshipID).Error
	}))
	second := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-private-conflict-b",
		"private-conflict-b", "Dense-Mem private uses GraphDB.", subject.EntityID, graphdb.EntityID, "private-source-group-b",
	)
	secondRelationship := second.RelationshipResults[0].Relationship
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET space_id = ?::uuid
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, spaceB, teamID, secondRelationship.RelationshipID).Error
	}))

	applyPrivateSpaceConflictPlacementForTest(t, ctx, adminDB, rls, secondRelationship, teamID)
	var conflictCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT COUNT(*) FROM relationship_conflict_cases WHERE team_id = ?`, teamID).Scan(&conflictCount).Error
	}))
	require.Zero(t, conflictCount)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET space_id = ?::uuid
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, spaceB, teamID, firstRelationship.RelationshipID).Error
	}))
	applyPrivateSpaceConflictPlacementForTest(t, ctx, adminDB, rls, secondRelationship, teamID)

	var caseSpace, positionSpace, memberSpace, eventSpace string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT space_id::text FROM relationship_conflict_cases WHERE team_id = ?`, teamID).Row().Scan(&caseSpace); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT space_id::text FROM relationship_conflict_positions WHERE team_id = ? LIMIT 1`, teamID).Row().Scan(&positionSpace); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT space_id::text FROM relationship_conflict_position_members WHERE team_id = ? LIMIT 1`, teamID).Row().Scan(&memberSpace); err != nil {
			return err
		}
		return tx.Raw(`SELECT space_id::text FROM relationship_conflict_events WHERE team_id = ? LIMIT 1`, teamID).Row().Scan(&eventSpace)
	}))
	require.Equal(t, spaceB, caseSpace)
	require.Equal(t, spaceB, positionSpace)
	require.Equal(t, spaceB, memberSpace)
	require.Equal(t, spaceB, eventSpace)
}

func applyPrivateSpaceConflictPlacementForTest(t *testing.T, ctx context.Context, adminDB *gorm.DB, rls interface {
	WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
}, relationship *RelationshipRecord, teamID string) {
	t.Helper()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return applyRelationshipConflictPlacement(ctx, tx, CommitPlacementSemanticInput{TeamID: teamID}, &RelationshipDecisionResult{
			Relationship: relationship,
		}, ConflictRuntimeConfig{ReviewTTLDays: 2, Timezone: "UTC"})
	}))
}
