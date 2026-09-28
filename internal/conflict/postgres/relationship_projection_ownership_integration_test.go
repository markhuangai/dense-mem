//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRelationshipConflictProjectionUsesCallerTransactionAndTeamFence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-projection-ownership", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-projection-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "owner-c")
	otherTeam := createLedgerTeam(t, adminDB, rls, "other-projection-team")
	otherOwner := createLedgerProfile(t, adminDB, rls, otherTeam, "other-owner")
	ledger := newConflictKnowledgeFixtureStore(appDB, rls)
	subject := createSemanticEntity(t, ctx, ledger.Store, teamID, ownerA, "project", "Dense-Mem")
	firstObject := createSemanticEntity(t, ctx, ledger.Store, teamID, ownerA, "product", "PostgreSQL")
	secondObject := createSemanticEntity(t, ctx, ledger.Store, teamID, ownerA, "product", "GraphDB")
	first := commitConflictRememberFixture(t, ctx, ledger.Store, teamID, ownerA, subject.EntityID, firstObject.EntityID, "Dense-Mem uses PostgreSQL.", "owner-a-support")
	_ = commitConflictRememberFixture(t, ctx, ledger.Store, teamID, ownerB, subject.EntityID, secondObject.EntityID, "Dense-Mem uses GraphDB.", "owner-b-support")
	_ = commitConflictRememberFixture(t, ctx, ledger.Store, teamID, ownerC, subject.EntityID, firstObject.EntityID, "Dense-Mem uses PostgreSQL for the team.", "owner-c-support")
	relationshipID := first.RelationshipResults[0].Relationship.RelationshipID

	var conflictID, question, spaceID string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		byRelationship, err := LoadRelationshipConflictRecordsInSpace(ctx, tx, teamID, []string{relationshipID}, nil, "")
		if err != nil {
			return err
		}
		require.Len(t, byRelationship, 1)
		conflictID = byRelationship[0].ConflictID
		question = byRelationship[0].Question
		spaceID = byRelationship[0].SpaceID
		require.Len(t, byRelationship[0].Positions, 2)
		byID, err := LoadRelationshipConflictRecordsByID(ctx, tx, teamID, []string{conflictID}, nil)
		if err != nil {
			return err
		}
		require.Equal(t, byRelationship, byID)
		knownAt := time.Now().UTC().Add(time.Minute)
		historical, err := LoadRelationshipConflictRecordsByID(ctx, tx, teamID, []string{conflictID}, &knownAt)
		if err != nil {
			return err
		}
		require.Len(t, historical, 1)
		require.Equal(t, conflictID, historical[0].ConflictID)
		require.Len(t, historical[0].Positions, 2)
		active, err := LoadActiveRelationshipConflictRecordsByIDBounded(ctx, tx, teamID, []string{conflictID}, nil, 20, 20)
		if err != nil {
			return err
		}
		require.Len(t, active, 1)
		return nil
	}))

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, otherTeam, otherOwner, func(tx *gorm.DB) error {
		records, err := LoadRelationshipConflictRecordsByID(ctx, tx, teamID, []string{conflictID}, nil)
		if err != nil {
			return err
		}
		require.Empty(t, records)
		return nil
	}))

	rollback := errors.New("rollback projection probe")
	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE relationship_conflict_cases SET question = 'uncommitted projection' WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, teamID, conflictID).Error; err != nil {
			return err
		}
		records, err := LoadRelationshipConflictRecordsByID(ctx, tx, teamID, []string{conflictID}, nil)
		if err != nil {
			return err
		}
		require.Equal(t, "uncommitted projection", records[0].Question)
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		records, err := LoadRelationshipConflictRecordsByID(ctx, tx, teamID, []string{conflictID}, nil)
		if err != nil {
			return err
		}
		require.Equal(t, question, records[0].Question)
		return nil
	}))

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation = generation + 1 WHERE team_id = ?::uuid AND id = ?::uuid`, teamID, spaceID).Error
	}))
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		active, err := LoadActiveRelationshipConflictRecordsByIDBounded(ctx, tx, teamID, []string{conflictID}, nil, 20, 20)
		if err != nil {
			return err
		}
		require.Empty(t, active)
		knownAt := time.Now().UTC().Add(time.Minute)
		historical, err := LoadRelationshipConflictRecordsByID(ctx, tx, teamID, []string{conflictID}, &knownAt)
		if err != nil {
			return err
		}
		require.Len(t, historical, 1)
		require.Len(t, historical[0].Positions, 2)
		return nil
	}))
}

func TestRelationshipConflictProjectionCountsBeforeSupporterDisplayLimit(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-voter-limit", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-voter-limit-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "owner-c")
	ledger := newConflictKnowledgeFixtureStore(appDB, rls)
	subject := createSemanticEntity(t, ctx, ledger.Store, teamID, ownerA, "project", "Dense-Mem")
	preferredObject := createSemanticEntity(t, ctx, ledger.Store, teamID, ownerA, "product", "PostgreSQL")
	losingObject := createSemanticEntity(t, ctx, ledger.Store, teamID, ownerA, "product", "GraphDB")
	preferred := commitConflictRememberFixture(t, ctx, ledger.Store, teamID, ownerA, subject.EntityID, preferredObject.EntityID, "Dense-Mem uses PostgreSQL.", "voter-a")
	_ = commitConflictRememberFixture(t, ctx, ledger.Store, teamID, ownerB, subject.EntityID, losingObject.EntityID, "Dense-Mem uses GraphDB.", "voter-b")
	_ = commitConflictRememberFixture(t, ctx, ledger.Store, teamID, ownerC, subject.EntityID, preferredObject.EntityID, "Dense-Mem uses PostgreSQL for the team.", "voter-c")
	var revokedOwner string
	var revokedResult *knowledgepostgres.SynchronousRememberCommitResult
	for index := range 22 {
		owner := createLedgerProfile(t, adminDB, rls, teamID, fmt.Sprintf("voter-%02d", index))
		result := commitConflictRememberFixture(t, ctx, ledger.Store, teamID, owner, subject.EntityID, preferredObject.EntityID,
			fmt.Sprintf("Voter %d confirms PostgreSQL.", index), fmt.Sprintf("voter-%02d-support", index))
		if index == 21 {
			revokedOwner, revokedResult = owner, result
		}
	}
	var conflictID string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		records, err := LoadRelationshipConflictRecordsInSpace(ctx, tx, teamID,
			[]string{preferred.RelationshipResults[0].Relationship.RelationshipID}, nil, "")
		if err != nil {
			return err
		}
		require.Len(t, records, 1)
		conflictID = records[0].ConflictID
		require.Len(t, records[0].Positions, 2)
		counts := []int{records[0].Positions[0].SupporterCount, records[0].Positions[1].SupporterCount}
		require.ElementsMatch(t, []int{24, 1}, counts)
		for _, position := range records[0].Positions {
			if position.SupporterCount == 24 {
				require.Len(t, position.Supporters, 20)
				require.True(t, position.SupportersTruncated)
			}
		}
		return nil
	}))
	knownAt := time.Now().UTC()
	_, err := ledger.Store.ApplyRelationshipSupportDecision(ctx, knowledgepostgres.ApplyRelationshipSupportDecisionInput{
		TeamID: teamID, OwnerProfileID: revokedOwner,
		RelationshipID: revokedResult.RelationshipResults[0].Relationship.RelationshipID,
		SupportID:      revokedResult.RelationshipResults[0].SupportID,
		Decision:       "revoke", Reason: "voter revoked support", IdempotencyKey: "voter-limit-revoke",
	})
	require.NoError(t, err)
	checkAfterRevoke := time.Now().UTC().Add(time.Second)
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		current, err := LoadRelationshipConflictRecordsByID(ctx, tx, teamID, []string{conflictID}, nil)
		if err != nil {
			return err
		}
		require.Len(t, current, 1)
		require.ElementsMatch(t, []int{24, 1}, []int{current[0].Positions[0].SupporterCount, current[0].Positions[1].SupporterCount})
		historical, err := LoadRelationshipConflictRecordsByID(ctx, tx, teamID, []string{conflictID}, &knownAt)
		if err != nil {
			return err
		}
		require.Len(t, historical, 1)
		require.ElementsMatch(t, []int{24, 1}, []int{historical[0].Positions[0].SupporterCount, historical[0].Positions[1].SupporterCount})
		afterRevoke, err := LoadRelationshipConflictRecordsByID(ctx, tx, teamID, []string{conflictID}, &checkAfterRevoke)
		if err != nil {
			return err
		}
		require.Len(t, afterRevoke, 1)
		require.ElementsMatch(t, []int{23, 1}, []int{afterRevoke[0].Positions[0].SupporterCount, afterRevoke[0].Positions[1].SupporterCount})
		return nil
	}))
}

func TestRelationshipConflictProjectionFencesRetiredPrivateGeneration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-conflict-projection"))
	identityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credential := createOwnedCredential(t, accesspostgres.NewCredentialRepository(appDB, rls, nil),
		teamID, identityID, "private-conflict-projection", domain.CredentialBindingCredentialPrivate)
	require.NoError(t, privacypostgres.NewPrivateMemoryRepository(appDB, rls).Prepare(ctx))
	subjectID, objectID, conflictID, positionID, relationshipID, ingestID, fragmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, teamID.String()); err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO entity_records (team_id, entity_id, entity_kind, space_id, space_generation)
			VALUES (?, ?, 'project', ?, ?), (?, ?, 'product', ?, ?)`,
			teamID, subjectID, credential.MemorySpaceID, credential.MemorySpaceGeneration,
			teamID, objectID, credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO knowledge_ingests (
			team_id, ingest_id, owner_profile_id, idempotency_key, request_hash, source_summary,
			status, proposal, metadata, space_id, space_generation
		) VALUES (?, ?, ?, ?, ?, ?, 'queued', '{}'::jsonb, '{}'::jsonb, ?, ?)`,
			teamID, ingestID, credential.ID, "private-projection:"+ingestID.String(), "hash-"+ingestID.String(),
			"private projection source", credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO evidence_fragments (
			team_id, fragment_id, ingest_id, owner_profile_id, evidence_index, content, content_hash,
			source_type, authority, labels, metadata, space_id, space_generation
		) VALUES (?, ?, ?, ?, 0, ?, ?, 'observation', 'primary', ARRAY[]::text[], '{}'::jsonb, ?, ?)`,
			teamID, fragmentID, ingestID, credential.ID, "private projection evidence", "hash-"+fragmentID.String(),
			credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO relationship_conflict_cases (
			team_id, conflict_id, semantic_scope_key, status, subject_entity_id,
			predicate_key, predicate_version, relationship_kind, current_cardinality,
			polarity, question, policy_version, review_due_at, next_review_at,
			review_ttl_days, timezone, space_id, space_generation
		) VALUES (?, ?, ?, 'open', ?, 'uses', 1, 'state', 'one', '+', ?, ?, now() + interval '1 day', now(), 2, 'UTC', ?, ?)`,
			teamID, conflictID, "private-projection:"+conflictID.String(), subjectID,
			"Which private value is current?", string(domain.ConflictPolicyVersion),
			credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO relationship_conflict_positions (
			team_id, conflict_id, position_id, position_key, object_entity_id, space_id, space_generation
		) VALUES (?, ?, ?, ?, ?, ?, ?)`, teamID, conflictID, positionID,
			"entity:"+objectID.String(), objectID, credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO relationship_records (
			team_id, relationship_id, owner_profile_id, semantic_group_key, subject_entity_id,
			predicate_key, predicate_version, object_entity_id, relationship_kind, current_cardinality,
			status, space_id, space_generation
		) VALUES (?, ?, ?, ?, ?, 'uses', 1, ?, 'state', 'one', 'active', ?, ?)`,
			teamID, relationshipID, credential.ID, "private-projection:"+relationshipID.String(), subjectID,
			objectID, credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO relationship_conflict_position_members (
			team_id, conflict_id, position_id, relationship_id, owner_profile_id, fragment_id, source_group_key,
			accepted_at, effective_time_basis, space_id, space_generation
		) VALUES (?, ?, ?, ?, ?, ?, ?, now(), 'recorded_at', ?, ?)`,
			teamID, conflictID, positionID, relationshipID, credential.ID, fragmentID, "private-projection-support",
			credential.MemorySpaceID, credential.MemorySpaceGeneration).Error
	}))
	read := func(active bool) []RelationshipConflictCaseRecord {
		var records []RelationshipConflictCaseRecord
		require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID.String(), credential.ID.String(), func(tx *gorm.DB) error {
			var err error
			if active {
				records, err = LoadActiveRelationshipConflictRecordsByIDBounded(ctx, tx, teamID.String(), []string{conflictID.String()}, nil, 20, 20)
			} else {
				knownAt := time.Now().UTC().Add(time.Minute)
				records, err = LoadRelationshipConflictRecordsByID(ctx, tx, teamID.String(), []string{conflictID.String()}, &knownAt)
			}
			return err
		}))
		return records
	}
	require.Len(t, read(true), 1)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation = generation + 1 WHERE id = ? AND team_id = ?`, credential.MemorySpaceID, teamID).Error
	}))
	require.Empty(t, read(true))
	historical := read(false)
	require.Len(t, historical, 1)
	require.Equal(t, positionID.String(), historical[0].Positions[0].PositionID)
}

func TestRelationshipConflictProjectionAbstainsOnEqualNewestPositions(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-equal-newest", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-equal-newest-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "owner-c")
	ledger := newConflictKnowledgeFixtureStore(appDB, rls)
	subject := createSemanticEntity(t, ctx, ledger.Store, teamID, ownerA, "project", "Dense-Mem")
	preferredObject := createSemanticEntity(t, ctx, ledger.Store, teamID, ownerA, "product", "PostgreSQL")
	otherObject := createSemanticEntity(t, ctx, ledger.Store, teamID, ownerA, "product", "GraphDB")
	first := commitConflictRememberFixture(t, ctx, ledger.Store, teamID, ownerA, subject.EntityID, preferredObject.EntityID,
		"Dense-Mem uses PostgreSQL.", "tie-preferred-a")
	_ = commitConflictRememberFixture(t, ctx, ledger.Store, teamID, ownerB, subject.EntityID, otherObject.EntityID,
		"Dense-Mem uses GraphDB.", "tie-other-b")
	_ = commitConflictRememberFixture(t, ctx, ledger.Store, teamID, ownerC, subject.EntityID, preferredObject.EntityID,
		"Dense-Mem uses PostgreSQL for the team.", "tie-preferred-c")
	var conflictID string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT conflict_id::text FROM relationship_conflict_position_members
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid AND active LIMIT 1`,
			teamID, first.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&conflictID)
	}))
	tieAt := time.Now().UTC()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		preferredForB := uuid.New()
		if err := tx.Exec(`INSERT INTO relationship_records (
			team_id, relationship_id, owner_profile_id, semantic_group_key, subject_entity_id,
			predicate_key, predicate_version, object_entity_id, relationship_kind, current_cardinality,
			status, space_id, space_generation
		) SELECT team_id, ?, ?::uuid, ?, subject_entity_id, predicate_key, predicate_version,
			object_entity_id, relationship_kind, current_cardinality, 'needs_review', space_id, space_generation
			FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`,
			preferredForB, ownerB, "tie-preferred-b", teamID,
			first.RelationshipResults[0].Relationship.RelationshipID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO relationship_conflict_position_members (
			team_id, conflict_id, position_id, relationship_id, owner_profile_id, source_group_key,
			accepted_at, effective_time_basis, space_id, space_generation
		) SELECT position.team_id, position.conflict_id, position.position_id, ?::uuid, ?::uuid, ?,
			?, 'recorded_at', position.space_id, position.space_generation
			FROM relationship_conflict_positions AS position
			WHERE position.team_id = ?::uuid AND position.conflict_id = ?::uuid AND position.object_entity_id = ?::uuid`,
			preferredForB, ownerB, "tie-preferred-b", tieAt, teamID, conflictID, preferredObject.EntityID).Error; err != nil {
			return err
		}
		var activePositionCount int
		if err := tx.Raw(`SELECT COUNT(DISTINCT position_id)::int FROM relationship_conflict_position_members
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND owner_profile_id = ?::uuid AND active`,
			teamID, conflictID, ownerB).Row().Scan(&activePositionCount); err != nil {
			return err
		}
		require.Equal(t, 2, activePositionCount)
		return tx.Exec(`UPDATE relationship_conflict_position_members SET accepted_at = ?
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND owner_profile_id = ?::uuid AND active`,
			tieAt, teamID, conflictID, ownerB).Error
	}))
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		records, err := LoadRelationshipConflictRecordsByID(ctx, tx, teamID, []string{conflictID}, nil)
		if err != nil {
			return err
		}
		require.Len(t, records, 1)
		require.Len(t, records[0].Positions, 2)
		require.ElementsMatch(t, []int{2, 0}, []int{records[0].Positions[0].SupporterCount, records[0].Positions[1].SupporterCount})
		for _, position := range records[0].Positions {
			for _, supporter := range position.Supporters {
				require.NotEqual(t, ownerB, supporter.ProfileID)
			}
		}
		return nil
	}))
}
