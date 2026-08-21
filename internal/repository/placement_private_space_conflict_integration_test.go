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
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "placement-private-space-conflict-owner-c")
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
	var spaceAGeneration, spaceBGeneration int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			INSERT INTO memory_spaces (team_id, kind, owner_profile_id)
			VALUES (?, 'profile_private', ?)
			RETURNING id::text, generation
		`, teamID, ownerA).Row().Scan(&spaceA, &spaceAGeneration); err != nil {
			return err
		}
		if err := tx.Raw(`
			INSERT INTO memory_spaces (team_id, kind, owner_profile_id)
			VALUES (?, 'profile_private', ?)
			RETURNING id::text, generation
		`, teamID, ownerB).Row().Scan(&spaceB, &spaceBGeneration); err != nil {
			return err
		}
		return tx.Exec(`
				UPDATE relationship_records
			SET space_id = ?::uuid
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
			`, spaceA, teamID, firstRelationship.RelationshipID).Error
	}))
	firstRelationship.SpaceID = spaceA
	firstRelationship.SpaceGeneration = spaceAGeneration
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
	secondRelationship.SpaceID = spaceB
	secondRelationship.SpaceGeneration = spaceBGeneration

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
	firstRelationship.SpaceID = spaceB
	firstRelationship.SpaceGeneration = spaceBGeneration
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

	sharedPostgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL shared")
	sharedGraphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB shared")
	sharedFirst := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-shared-conflict-a",
		"shared-conflict-a", "Dense-Mem shared uses PostgreSQL.", subject.EntityID, sharedPostgres.EntityID, "shared-source-group-a",
	)
	sharedSecond := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-shared-conflict-b",
		"shared-conflict-b", "Dense-Mem shared uses GraphDB.", subject.EntityID, sharedGraphdb.EntityID, "shared-source-group-b",
	)
	sharedThird := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerC, "worker-shared-conflict-c",
		"shared-conflict-c", "Dense-Mem shared also uses PostgreSQL.", subject.EntityID, sharedPostgres.EntityID, "shared-source-group-c",
	)
	sharedRelationshipIDs := []string{
		sharedFirst.RelationshipResults[0].Relationship.RelationshipID,
		sharedSecond.RelationshipResults[0].Relationship.RelationshipID,
		sharedThird.RelationshipResults[0].Relationship.RelationshipID,
	}
	var sharedSpaceID, sharedConflictID string
	var sharedConflictIDs []string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT id::text FROM memory_spaces WHERE team_id = ? AND kind = 'team_shared'`, teamID).Row().Scan(&sharedSpaceID); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT DISTINCT conflict_id::text
			FROM relationship_conflict_position_members
			WHERE team_id = ?::uuid
			  AND relationship_id IN (?::uuid, ?::uuid, ?::uuid)
			ORDER BY conflict_id::text
		`, teamID, sharedRelationshipIDs[0], sharedRelationshipIDs[1], sharedRelationshipIDs[2]).Scan(&sharedConflictIDs).Error; err != nil {
			return err
		}
		return nil
	}))
	require.Len(t, sharedConflictIDs, 1)
	sharedConflictID = sharedConflictIDs[0]

	var relationshipCount, relationshipSpaceCount int64
	var relationshipMinSpace, relationshipMaxSpace string
	var caseCount, positionCount, positionSpaceCount, memberCount, memberOwnerCount, memberSpaceCount, eventCount int64
	var caseMinSpace, caseMaxSpace, positionMinSpace, positionMaxSpace, memberMinSpace, memberMaxSpace, eventMinSpace, eventMaxSpace string
	var ownerCConflictIDs []string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT COUNT(*), COUNT(DISTINCT space_id), COALESCE(MIN(space_id::text), ''), COALESCE(MAX(space_id::text), '')
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id IN (?::uuid, ?::uuid, ?::uuid)
		`, teamID, sharedRelationshipIDs[0], sharedRelationshipIDs[1], sharedRelationshipIDs[2]).Row().Scan(&relationshipCount, &relationshipSpaceCount, &relationshipMinSpace, &relationshipMaxSpace); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*), COALESCE(MIN(space_id::text), ''), COALESCE(MAX(space_id::text), '')
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, teamID, sharedConflictID).Row().Scan(&caseCount, &caseMinSpace, &caseMaxSpace); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*), COUNT(DISTINCT space_id), COALESCE(MIN(space_id::text), ''), COALESCE(MAX(space_id::text), '')
			FROM relationship_conflict_positions
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, teamID, sharedConflictID).Row().Scan(&positionCount, &positionSpaceCount, &positionMinSpace, &positionMaxSpace); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*), COUNT(DISTINCT owner_profile_id), COUNT(DISTINCT space_id),
			       COALESCE(MIN(space_id::text), ''), COALESCE(MAX(space_id::text), '')
			FROM relationship_conflict_position_members
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, teamID, sharedConflictID).Row().Scan(&memberCount, &memberOwnerCount, &memberSpaceCount, &memberMinSpace, &memberMaxSpace); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*), COALESCE(MIN(space_id::text), ''), COALESCE(MAX(space_id::text), '')
			FROM relationship_conflict_events
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, teamID, sharedConflictID).Row().Scan(&eventCount, &eventMinSpace, &eventMaxSpace); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT DISTINCT conflict_id::text
			FROM relationship_conflict_position_members
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
			ORDER BY conflict_id::text
		`, teamID, sharedRelationshipIDs[2]).Scan(&ownerCConflictIDs).Error
	}))

	require.Equal(t, int64(3), relationshipCount)
	require.Equal(t, int64(1), relationshipSpaceCount)
	require.Equal(t, sharedSpaceID, relationshipMinSpace)
	require.Equal(t, sharedSpaceID, relationshipMaxSpace)
	require.Equal(t, []string{sharedConflictID}, sharedConflictIDs)
	require.Equal(t, []string{sharedConflictID}, ownerCConflictIDs)
	require.Equal(t, int64(1), caseCount)
	require.Equal(t, sharedSpaceID, caseMinSpace)
	require.Equal(t, sharedSpaceID, caseMaxSpace)
	require.Equal(t, int64(2), positionCount)
	require.Equal(t, int64(1), positionSpaceCount)
	require.Equal(t, sharedSpaceID, positionMinSpace)
	require.Equal(t, sharedSpaceID, positionMaxSpace)
	require.Equal(t, int64(3), memberCount)
	require.Equal(t, int64(3), memberOwnerCount)
	require.Equal(t, int64(1), memberSpaceCount)
	require.Equal(t, sharedSpaceID, memberMinSpace)
	require.Equal(t, sharedSpaceID, memberMaxSpace)
	require.Equal(t, int64(6), eventCount)
	require.Equal(t, sharedSpaceID, eventMinSpace)
	require.Equal(t, sharedSpaceID, eventMaxSpace)
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
