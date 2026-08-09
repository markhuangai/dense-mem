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
	assert.True(t, preferredPosition.SupportersTruncated)
	require.Len(t, preferredPosition.Supporters, relationshipConflictSupporterLimit)
	assert.Equal(t, ownerA, preferredPosition.Supporters[0].ProfileID)
	assert.Equal(t, "Profile A Renamed", preferredPosition.Supporters[0].ProfileName)
	assert.Equal(t, "authoritative", preferredPosition.Supporters[0].StrongestAuthority)
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

func TestRelationshipConflictSupporterProjectionUsesOneLatestPositionPerProfile(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-supporter-latest", 3, "exact", "")

	teamID := createLedgerTeam(t, adminDB, rls, "conflict-supporter-latest-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "Latest A")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "Latest B")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "Latest C")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Latest project")
	valueA := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Latest A value")
	valueB := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Latest B value")
	commitPlacementRelationshipForConflictTest(t, ctx, ledgerRepo, teamID, ownerA, "latest-a", "latest-a", "A", subject.EntityID, valueA.EntityID, "copied-source")
	commitPlacementRelationshipForConflictTest(t, ctx, ledgerRepo, teamID, ownerB, "latest-b", "latest-b", "B", subject.EntityID, valueB.EntityID, "copied-source")
	latestCA := commitPlacementRelationshipForConflictTest(t, ctx, ledgerRepo, teamID, ownerC, "latest-c-a", "latest-c-a", "C supports A", subject.EntityID, valueA.EntityID, "copied-source")

	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	knownAt := databaseNowForTest(t, adminDB, rls)
	latestCB := commitPlacementRelationshipForConflictTest(t, ctx, ledgerRepo, teamID, ownerC, "latest-c-b", "latest-c-b", "C supports B", subject.EntityID, valueB.EntityID, "copied-source")
	latestAt := knownAt.Add(2 * time.Minute)
	// Reactivate the superseded snapshot member so the projection test can exercise two effective positions for one profile.
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE relationship_conflict_position_members
			SET accepted_at = CASE relationship_id
				WHEN ?::uuid THEN ?
				WHEN ?::uuid THEN ?
				ELSE accepted_at
			END,
			    active = true,
			    retired_at = NULL
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND relationship_id IN (?::uuid, ?::uuid)
		`, latestCA.RelationshipResults[0].Relationship.RelationshipID, latestAt.Add(-time.Minute),
			latestCB.RelationshipResults[0].Relationship.RelationshipID, latestAt,
			teamID, conflictID, ownerC,
			latestCA.RelationshipResults[0].Relationship.RelationshipID,
			latestCB.RelationshipResults[0].Relationship.RelationshipID)
		require.Equal(t, int64(2), result.RowsAffected)
		return result.Error
	}))
	current := loadConflictSupporterTestRecords(t, ctx, appDB, rls, teamID, ownerA, conflictID, nil, nil)
	require.Len(t, current, 1)
	positionA := conflictSupporterTestPosition(t, current[0].Positions, valueA.EntityID)
	positionB := conflictSupporterTestPosition(t, current[0].Positions, valueB.EntityID)
	assert.Equal(t, 1, positionA.SupporterCount)
	assert.Equal(t, 2, positionB.SupporterCount)
	assert.NotContains(t, supporterIDs(positionA.Supporters), ownerC)
	assert.Contains(t, supporterIDs(positionB.Supporters), ownerC)

	historical := loadConflictSupporterTestRecords(t, ctx, appDB, rls, teamID, ownerA, conflictID, nil, &knownAt)
	require.Len(t, historical, 1)
	historicalPositionA := conflictSupporterTestPosition(t, historical[0].Positions, valueA.EntityID)
	historicalPositionB := conflictSupporterTestPosition(t, historical[0].Positions, valueB.EntityID)
	assert.Equal(t, 2, historicalPositionA.SupporterCount)
	assert.Equal(t, 1, historicalPositionB.SupporterCount)
	assert.Contains(t, supporterIDs(historicalPositionA.Supporters), ownerC)
	assert.NotContains(t, supporterIDs(historicalPositionB.Supporters), ownerC)

	// A bounded caller may request only position A. Hidden positions must still
	// participate in the per-profile latest-position vote calculation.
	requested := []RelationshipConflictPositionRecord{{
		ConflictID: conflictID,
		PositionID: positionA.PositionID,
	}}
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return loadRelationshipConflictSupporters(ctx, tx, teamID, []string{conflictID}, nil, requested, relationshipConflictSupporterLimit)
	}))
	assert.Equal(t, 1, requested[0].SupporterCount)
	assert.NotContains(t, supporterIDs(requested[0].Supporters), ownerC)

	// Equal newest timestamps across positions abstain the profile instead of
	// selecting a position by UUID or source-group identity.
	tieAt := latestAt.Add(time.Second)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_position_members
			SET accepted_at = ?
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND owner_profile_id = ?::uuid
		`, tieAt, teamID, conflictID, ownerC).Error
	}))
	tied := loadConflictSupporterTestRecords(t, ctx, appDB, rls, teamID, ownerA, conflictID, nil, nil)
	require.Len(t, tied, 1)
	positionA = conflictSupporterTestPosition(t, tied[0].Positions, valueA.EntityID)
	positionB = conflictSupporterTestPosition(t, tied[0].Positions, valueB.EntityID)
	assert.Equal(t, 1, positionA.SupporterCount)
	assert.Equal(t, 1, positionB.SupporterCount)
	assert.NotContains(t, supporterIDs(positionA.Supporters), ownerC)
	assert.NotContains(t, supporterIDs(positionB.Supporters), ownerC)

	requested = []RelationshipConflictPositionRecord{{
		ConflictID: conflictID,
		PositionID: positionA.PositionID,
	}}
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return loadRelationshipConflictSupporters(ctx, tx, teamID, []string{conflictID}, nil, requested, relationshipConflictSupporterLimit)
	}))
	assert.Equal(t, 1, requested[0].SupporterCount)
	assert.NotContains(t, supporterIDs(requested[0].Supporters), ownerC)
}

func TestRelationshipConflictPlacementPreservesNewestSupportTimestampForGroupRepresentative(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-supporter-timestamp", 3, "exact", "")

	teamID := createLedgerTeam(t, adminDB, rls, "conflict-supporter-timestamp-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "Timestamp A")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "Timestamp B")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Timestamp project")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Timestamp PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Timestamp GraphDB")

	content := "Dense-Mem uses PostgreSQL. Dense-Mem uses PostgreSQL."
	quote := "Dense-Mem uses PostgreSQL."
	secondStart := strings.LastIndex(content, quote)
	preferred := commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-supporter-timestamp-a",
		"supporter-timestamp-a", content, subject.EntityID, postgres.EntityID, "same-source",
		conflictTestRelationshipOptions{
			authority: "secondary",
			additionalSupports: []conflictTestAdditionalSupport{{
				sourceGroupKey: "same-source",
				spanStart:      secondStart,
				spanEnd:        secondStart + len(quote),
				quote:          quote,
				authority:      "inferred",
			}},
		},
	)
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-supporter-timestamp-b",
		"supporter-timestamp-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "other-source",
	)
	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)

	var representativeAcceptedAt, newestSupportCreatedAt time.Time
	var representativeAuthority string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT member.accepted_at,
			       member.authority,
			       max(support.created_at)
			FROM relationship_conflict_position_members AS member
			JOIN relationship_evidence_supports AS support
			  ON support.team_id = member.team_id
			 AND support.relationship_id = member.relationship_id
			 AND support.owner_profile_id = member.owner_profile_id
			 AND support.source_group_key = member.source_group_key
			WHERE member.team_id = ?::uuid
			  AND member.conflict_id = ?::uuid
			  AND member.relationship_id = ?::uuid
			  AND member.source_group_key = 'same-source'
			GROUP BY member.accepted_at, member.authority
		`, teamID, conflictID, preferred.RelationshipResults[0].Relationship.RelationshipID).
			Row().Scan(&representativeAcceptedAt, &representativeAuthority, &newestSupportCreatedAt)
	}))
	assert.Equal(t, "secondary", representativeAuthority)
	assert.WithinDuration(t, newestSupportCreatedAt, representativeAcceptedAt, time.Microsecond)
}

func supporterIDs(supporters []RelationshipConflictSupporterRecord) []string {
	ids := make([]string, 0, len(supporters))
	for _, supporter := range supporters {
		ids = append(ids, supporter.ProfileID)
	}
	return ids
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
