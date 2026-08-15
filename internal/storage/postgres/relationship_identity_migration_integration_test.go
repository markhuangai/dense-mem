//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelationshipIdentityMigrationPreservesValidToCollisionsForReview(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026072402)
	fixture := insertRelationshipIdentityCollisionFixture(t, ctx, sqlDB)

	runGooseUpTo(t, ctx, sqlDB, 2026072500)
	runGooseUpTo(t, ctx, sqlDB, 2026072500)

	type relationshipState struct {
		aliasOf          string
		semanticGroupKey string
		tier             string
		status           string
		recordedTo       sql.NullTime
	}
	loadState := func(relationshipID string) relationshipState {
		t.Helper()
		var state relationshipState
		require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT COALESCE(identity_alias_of_relationship_id::text, ''),
				       semantic_group_key, tier, status, recorded_to
				FROM relationship_records
				WHERE team_id = $1::uuid
				  AND relationship_id = $2::uuid
			`, fixture.teamID, relationshipID).Scan(
				&state.aliasOf,
				&state.semanticGroupKey,
				&state.tier,
				&state.status,
				&state.recordedTo,
			)
		}))
		return state
	}

	canonical := loadState(fixture.canonicalRelationshipID)
	assert.Empty(t, canonical.aliasOf)
	assert.Equal(t, "sg:canonical", canonical.semanticGroupKey)
	assert.Equal(t, "validated_claim", canonical.tier)
	assert.Equal(t, "active", canonical.status)
	assert.False(t, canonical.recordedTo.Valid)

	alias := loadState(fixture.aliasRelationshipID)
	assert.Equal(t, fixture.canonicalRelationshipID, alias.aliasOf)
	assert.Equal(t, canonical.semanticGroupKey, alias.semanticGroupKey)
	assert.Equal(t, "candidate", alias.tier)
	assert.Equal(t, "needs_review", alias.status)
	assert.True(t, alias.recordedTo.Valid)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		for tableName, want := range map[string]int{
			"relationship_records":                 2,
			"relationship_observations":            2,
			"verification_events":                  2,
			"relationship_evidence_supports":       2,
			"relationship_support_decision_events": 2,
			"relationship_transition_events":       3,
		} {
			var got int
			if err := tx.QueryRowContext(ctx, `
				SELECT count(*)::int
				FROM `+tableName+`
				WHERE team_id = $1::uuid
			`, fixture.teamID).Scan(&got); err != nil {
				return err
			}
			assert.Equal(t, want, got, tableName)
		}

		var reviewRelationshipID, reviewReason, canonicalPayloadID string
		if err := tx.QueryRowContext(ctx, `
			SELECT relationship_id::text,
			       reason,
			       payload->>'canonical_relationship_id'
			FROM review_tasks
			WHERE team_id = $1::uuid
			  AND status = 'open'
			  AND task_type = 'relationship_needs_review'
		`, fixture.teamID).Scan(&reviewRelationshipID, &reviewReason, &canonicalPayloadID); err != nil {
			return err
		}
		assert.Equal(t, fixture.aliasRelationshipID, reviewRelationshipID)
		assert.Equal(t, "relationship_identity_valid_to_conflict", reviewReason)
		assert.Equal(t, fixture.canonicalRelationshipID, canonicalPayloadID)

		var edgeCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)::int
			FROM semantic_edges
			WHERE team_id = $1::uuid
		`, fixture.teamID).Scan(&edgeCount); err != nil {
			return err
		}
		assert.Equal(t, 1, edgeCount)
		return nil
	}))

	err := execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_records (
				team_id, owner_profile_id, semantic_group_key, subject_entity_id,
				predicate_key, predicate_version, object_entity_id,
				relationship_kind, current_cardinality, tier, status, polarity,
				valid_to
			) VALUES (
				$1::uuid, $2::uuid, 'sg:duplicate', $3::uuid,
				'works_on', 1, $4::uuid,
				'state', 'many', 'validated_claim', 'active', '+',
				'2026-08-01T00:00:00Z'::timestamptz
			)
		`, fixture.teamID, fixture.profileID, fixture.subjectEntityID, fixture.objectEntityID)
		return err
	})
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "23505", pgErr.Code)

	err = migrationDownTo(ctx, sqlDB, 2026072402)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "relationship identity aliases require review-preserving forward migration")
}

func TestRelationshipIdentityMigrationRollsBackBeforeAliasesExist(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026072500)
	require.NoError(t, migrationDownTo(ctx, sqlDB, 2026072402))
	assert.False(t, columnExists(t, ctx, sqlDB, "relationship_records", "identity_alias_of_relationship_id"))
}

type relationshipIdentityCollisionFixture struct {
	teamID                  string
	profileID               string
	subjectEntityID         string
	objectEntityID          string
	canonicalRelationshipID string
	aliasRelationshipID     string
}

func insertRelationshipIdentityCollisionFixture(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) relationshipIdentityCollisionFixture {
	t.Helper()
	teamID, profileID := insertMigrationTeamProfile(t, ctx, db)
	fixture := relationshipIdentityCollisionFixture{
		teamID:                  teamID,
		profileID:               profileID,
		subjectEntityID:         uuid.NewString(),
		objectEntityID:          uuid.NewString(),
		canonicalRelationshipID: uuid.NewString(),
		aliasRelationshipID:     uuid.NewString(),
	}
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_team_refs (team_id)
			VALUES ($1::uuid)
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id)
			VALUES ($1::uuid, $2::uuid)
		`, teamID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_predicate_definitions (
				team_id, predicate_key, version, aliases, allowed_subject_kinds,
				allowed_object_kinds, relationship_kind, current_cardinality,
				lifecycle_state, origin
			)
			SELECT $1::uuid, predicate_key, version, aliases, allowed_subject_kinds,
			       allowed_object_kinds, relationship_kind, current_cardinality,
			       lifecycle_state, 'built_in'
			FROM predicate_definitions
			WHERE predicate_key = 'works_on'
			  AND version = 1
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO entity_records (team_id, entity_id, entity_kind)
			VALUES
				($1::uuid, $2::uuid, 'person'),
				($1::uuid, $3::uuid, 'project')
		`, teamID, fixture.subjectEntityID, fixture.objectEntityID); err != nil {
			return err
		}

		relationshipIDs := []string{fixture.canonicalRelationshipID, fixture.aliasRelationshipID}
		semanticGroupKeys := []string{"sg:canonical", "sg:bounded"}
		validTos := []any{nil, time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
		createdAts := []time.Time{
			time.Date(2026, 7, 23, 18, 27, 28, 0, time.UTC),
			time.Date(2026, 7, 23, 18, 57, 18, 0, time.UTC),
		}
		for i, relationshipID := range relationshipIDs {
			ingestID := uuid.NewString()
			fragmentID := uuid.NewString()
			observationID := uuid.NewString()
			verificationID := uuid.NewString()
			supportID := uuid.NewString()
			supportDecisionID := uuid.NewString()
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO knowledge_ingests (team_id, ingest_id, owner_profile_id, status)
				VALUES ($1::uuid, $2::uuid, $3::uuid, 'completed')
			`, teamID, ingestID, profileID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO evidence_fragments (
					team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
					content, content_hash, source_type, authority
				) VALUES (
					$1::uuid, $2::uuid, $3::uuid, $4::uuid, 0,
					'evidence', $5, 'conversation', 'primary'
				)
			`, teamID, fragmentID, ingestID, profileID, "sha256:"+fragmentID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO relationship_records (
					team_id, relationship_id, owner_profile_id, semantic_group_key,
					subject_entity_id, predicate_key, predicate_version, object_entity_id,
					relationship_kind, current_cardinality, tier, status, polarity,
					valid_to, support_count, source_group_count, version, created_at, updated_at
				) VALUES (
					$1::uuid, $2::uuid, $3::uuid, $4,
					$5::uuid, 'works_on', 1, $6::uuid,
					'state', 'many', 'validated_claim', 'active', '+',
					$7, 1, 1, 2, $8, $8
				)
			`, teamID, relationshipID, profileID, semanticGroupKeys[i],
				fixture.subjectEntityID, fixture.objectEntityID, validTos[i], createdAts[i]); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO relationship_observations (
					team_id, observation_id, relationship_id, ingest_id, owner_profile_id,
					subject_ref, original_predicate, object_ref, subject_entity_id,
					predicate_key, predicate_version, object_entity_id, polarity, valid_to
				) VALUES (
					$1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid,
					'subject', 'works_on', 'object', $6::uuid,
					'works_on', 1, $7::uuid, '+', $8
				)
			`, teamID, observationID, relationshipID, ingestID, profileID,
				fixture.subjectEntityID, fixture.objectEntityID, validTos[i]); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO verification_events (
					team_id, verification_event_id, observation_id, owner_profile_id,
					evidence_verdict
				) VALUES (
					$1::uuid, $2::uuid, $3::uuid, $4::uuid, 'entailed'
				)
			`, teamID, verificationID, observationID, profileID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO relationship_evidence_supports (
					team_id, support_id, relationship_id, observation_id,
					verification_event_id, fragment_id, owner_profile_id,
					source_group_key, span_start, span_end, authority
				) VALUES (
					$1::uuid, $2::uuid, $3::uuid, $4::uuid,
					$5::uuid, $6::uuid, $7::uuid,
					'semantic_review:evidence:0', 0, 8, 'primary'
				)
			`, teamID, supportID, relationshipID, observationID, verificationID, fragmentID, profileID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO relationship_support_decision_events (
					team_id, support_decision_id, support_id, relationship_id,
					owner_profile_id, decision
				) VALUES (
					$1::uuid, $2::uuid, $3::uuid, $4::uuid,
					$5::uuid, 'grant'
				)
			`, teamID, supportDecisionID, supportID, relationshipID, profileID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO relationship_transition_events (
					team_id, relationship_id, owner_profile_id, to_tier, to_status,
					reason, verification_event_id, support_decision_id
				) VALUES (
					$1::uuid, $2::uuid, $3::uuid, 'validated_claim', 'active',
					'verifier_decision', $4::uuid, $5::uuid
				)
			`, teamID, relationshipID, profileID, verificationID, supportDecisionID); err != nil {
				return err
			}
		}
		return nil
	}))
	return fixture
}
