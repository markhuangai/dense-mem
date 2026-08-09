package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRelationshipConflictSupporterProjectionIsBoundedCurrentAndTeamScoped(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-supporters", 3, "exact", "")

	teamID := createLedgerTeam(t, adminDB, rls, "conflict-supporters-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "Profile A")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "Profile B")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "conflict-supporters-other-team")
	otherOwner := createLedgerProfile(t, adminDB, rls, otherTeamID, "Other Profile")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")

	preferred := commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-supporters-a",
		"supporters-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "shared-source",
		conflictTestRelationshipOptions{
			authority: "secondary",
			additionalSupports: []conflictTestAdditionalSupport{{
				sourceGroupKey: "profile-a-independent",
				spanStart:      0,
				spanEnd:        len("Dense-Mem uses PostgreSQL"),
				quote:          "Dense-Mem uses PostgreSQL",
				authority:      "authoritative",
			}},
		},
	)
	loser := commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-supporters-b",
		"supporters-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "graphdb-source",
	)
	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)

	var historicalKnownAt time.Time
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT clock_timestamp()`).Scan(&historicalKnownAt).Error
	}))

	for i := 0; i < 20; i++ {
		ownerID := createLedgerProfile(t, adminDB, rls, teamID, fmt.Sprintf("Profile %02d", i+3))
		sourceGroup := "shared-source"
		if i > 0 {
			sourceGroup = fmt.Sprintf("independent-source-%02d", i)
		}
		commitPlacementRelationshipForConflictTest(
			t, ctx, ledgerRepo, teamID, ownerID, fmt.Sprintf("worker-supporters-%02d", i+3),
			fmt.Sprintf("supporters-%02d", i+3), fmt.Sprintf("Dense-Mem uses PostgreSQL according to profile %02d.", i+3),
			subject.EntityID, postgres.EntityID, sourceGroup,
		)
	}

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE team_profiles
			SET name = 'Profile A Renamed', updated_at = now()
			WHERE team_id = ?::uuid
			  AND id = ?::uuid
		`, teamID, ownerA).Error
	}))

	current := loadConflictSupporterTestRecords(
		t, ctx, appDB, rls, teamID, ownerA, conflictID,
		[]string{
			preferred.RelationshipResults[0].Relationship.RelationshipID,
			loser.RelationshipResults[0].Relationship.RelationshipID,
		},
		nil,
	)
	require.Len(t, current, 1)
	preferredPosition := conflictSupporterTestPosition(t, current[0].Positions, postgres.EntityID)
	assert.Equal(t, 21, preferredPosition.SupporterCount)
	assert.Equal(t, 21, preferredPosition.SupportGroupCount)
	assert.Equal(t, 1, preferredPosition.AuthoritativeGroupCount)
	assert.True(t, preferredPosition.SupportersTruncated)
	require.Len(t, preferredPosition.Supporters, relationshipConflictSupporterLimit)
	assert.Equal(t, ownerA, preferredPosition.Supporters[0].ProfileID)
	assert.Equal(t, "Profile A Renamed", preferredPosition.Supporters[0].ProfileName)
	assert.Equal(t, "authoritative", preferredPosition.Supporters[0].StrongestAuthority)
	assert.Equal(t, 2, preferredPosition.Supporters[0].SourceGroupCount)
	assert.NotEmpty(t, preferredPosition.Supporters[0].EvidenceID)
	assert.False(t, preferredPosition.Supporters[0].AcceptedAt.IsZero())

	historical := loadConflictSupporterTestRecords(
		t, ctx, appDB, rls, teamID, ownerA, conflictID,
		[]string{preferred.RelationshipResults[0].Relationship.RelationshipID},
		&historicalKnownAt,
	)
	require.Len(t, historical, 1)
	historicalPosition := conflictSupporterTestPosition(t, historical[0].Positions, postgres.EntityID)
	assert.Equal(t, 1, historicalPosition.SupporterCount)
	assert.Equal(t, 2, historicalPosition.SupportGroupCount)
	assert.Equal(t, 1, historicalPosition.AuthoritativeGroupCount)
	assert.False(t, historicalPosition.SupportersTruncated)
	require.Len(t, historicalPosition.Supporters, 1)
	assert.Equal(t, "Profile A Renamed", historicalPosition.Supporters[0].ProfileName)

	foreign := loadConflictSupporterTestRecords(t, ctx, appDB, rls, otherTeamID, otherOwner, conflictID, nil, nil)
	assert.Empty(t, foreign)
	var foreignVisible int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, otherTeamID, otherOwner, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_conflict_position_members
			WHERE conflict_id = ?::uuid
		`, conflictID).Scan(&foreignVisible).Error
	}))
	assert.Zero(t, foreignVisible)

	var plan string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL enable_seqscan = off`).Error; err != nil {
			return err
		}
		return tx.Raw(
			`EXPLAIN (FORMAT JSON) `+relationshipConflictSupporterRowsSQL,
			relationshipConflictSupporterRowsArgs(teamID, []string{conflictID}, nil)...,
		).Row().Scan(&plan)
	}))
	assert.True(t,
		strings.Contains(plan, "relationship_conflict_members_case_idx") ||
			strings.Contains(plan, "relationship_conflict_position_members_pkey"),
		"supporter projection plan did not use a conflict-member index: %s", plan,
	)
}

func loadConflictSupporterTestRecords(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls rLSHelper,
	teamID string,
	profileID string,
	conflictID string,
	relationshipIDs []string,
	knownAt *time.Time,
) []RelationshipConflictCaseRecord {
	t.Helper()
	var records []RelationshipConflictCaseRecord
	require.NoError(t, rls.WithTeamProfileTx(ctx, db, teamID, profileID, func(tx *gorm.DB) error {
		var err error
		if relationshipIDs == nil {
			records, err = loadRelationshipConflictRecordsByID(ctx, tx, teamID, []string{conflictID}, knownAt)
		} else {
			records, err = loadRelationshipConflictRecords(ctx, tx, teamID, relationshipIDs, knownAt)
		}
		return err
	}))
	return records
}

func conflictSupporterTestPosition(
	t *testing.T,
	positions []RelationshipConflictPositionRecord,
	objectEntityID string,
) RelationshipConflictPositionRecord {
	t.Helper()
	for _, position := range positions {
		if position.ObjectEntityID == objectEntityID {
			return position
		}
	}
	t.Fatalf("missing conflict position for object %s", objectEntityID)
	return RelationshipConflictPositionRecord{}
}
