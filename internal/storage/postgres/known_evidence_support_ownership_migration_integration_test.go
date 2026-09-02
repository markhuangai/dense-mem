//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	knownEvidenceSupportOwnershipMigrationVersion int64 = 20260902010002
	knownEvidenceSupportOwnershipMigrationBase    int64 = 20260901020001
)

func TestKnownEvidenceSupportOwnershipMigrationBackfillsForeignKeys(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, knownEvidenceSupportOwnershipMigrationBase)
	teamID, ownerID := insertMigrationTeamProfile(t, ctx, db)
	fixture := insertKnownEvidenceSupportOwnershipFixture(t, ctx, db, teamID, ownerID, ownerID)
	var updateTimeBefore string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT value
		FROM app_config
		WHERE key = 'update_time'
	`).Scan(&updateTimeBefore))

	runGooseUpTo(t, ctx, db, knownEvidenceSupportOwnershipMigrationVersion)
	var evidenceOwner string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT evidence_owner_profile_id::text
		FROM relationship_evidence_supports
		WHERE team_id = $1::uuid AND support_id = $2::uuid
	`, teamID, fixture.supportID).Scan(&evidenceOwner))
	assert.Equal(t, ownerID, evidenceOwner)
	var nullable string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'relationship_evidence_supports'
		  AND column_name = 'evidence_owner_profile_id'
	`).Scan(&nullable))
	assert.Equal(t, "NO", nullable)
	assert.False(t, indexExists(t, ctx, db, "relationship_supports_evidence_owner_backfill_null_idx"))
	var updateTimeAfter string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT value
		FROM app_config
		WHERE key = 'update_time'
	`).Scan(&updateTimeAfter))
	assert.NotEqual(t, updateTimeBefore, updateTimeAfter)
	assert.True(t, relationshipSupportConstraintExists(t, ctx, db, "relationship_supports_fragment_evidence_owner_fkey"))
	assert.True(t, relationshipSupportConstraintExists(t, ctx, db, "relationship_supports_source_evidence_owner_fkey"))
	assert.False(t, relationshipSupportConstraintDefinitionExists(t, ctx, db, "FOREIGN KEY (team_id, fragment_id, owner_profile_id) REFERENCES evidence_fragments%"))
	assert.False(t, relationshipSupportConstraintDefinitionExists(t, ctx, db, "FOREIGN KEY (team_id, source_id, owner_profile_id) REFERENCES evidence_sources%"))
	assert.False(t, relationshipSupportConstraintDefinitionExists(t, ctx, db, "FOREIGN KEY (team_id, source_revision_id, owner_profile_id) REFERENCES evidence_source_revisions%"))
	assert.False(t, relationshipSupportConstraintDefinitionExists(t, ctx, db, "FOREIGN KEY (team_id, source_id, source_revision_id, owner_profile_id) REFERENCES evidence_source_revisions%"))

	require.NoError(t, migrationDownTo(ctx, db, knownEvidenceSupportOwnershipMigrationBase))
	assert.False(t, columnExists(t, ctx, db, "relationship_evidence_supports", "evidence_owner_profile_id"))
}

func TestKnownEvidenceSupportOwnershipMigrationDownRejectsCrossOwnerRows(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, knownEvidenceSupportOwnershipMigrationBase)
	teamID, ownerA := insertMigrationTeamProfile(t, ctx, db)
	ownerB := insertKnownEvidenceMigrationProfile(t, ctx, db, teamID)
	fixture := insertKnownEvidenceSupportOwnershipFixture(t, ctx, db, teamID, ownerA, ownerA)
	runGooseUpTo(t, ctx, db, knownEvidenceSupportOwnershipMigrationVersion)
	otherEvidence := insertKnownEvidenceSupportOwnershipEvidence(t, ctx, db, teamID, ownerB, "cross-owner evidence")
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_evidence_supports (
				team_id, support_id, relationship_id, observation_id, verification_event_id,
				fragment_id, owner_profile_id, evidence_owner_profile_id, source_group_key,
				span_start, span_end, quote, authority, metadata, space_id, space_generation
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid,
				$6::uuid, $7::uuid, $8::uuid, 'known-cross-owner', 0, 5,
				'cross', 'primary', '{}'::jsonb, $9::uuid, $10
			)
		`, teamID, uuid.NewString(), fixture.relationshipID, fixture.observationID, fixture.verificationID,
			otherEvidence.fragmentID, ownerA, ownerB, fixture.spaceID, fixture.spaceGeneration)
		return err
	}))

	err := migrationDownTo(ctx, db, knownEvidenceSupportOwnershipMigrationBase)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cross-owner support rows")
}

func insertKnownEvidenceMigrationProfile(t *testing.T, ctx context.Context, db *sql.DB, teamID string) string {
	t.Helper()
	profileID := uuid.NewString()
	keyPrefix := strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{
				query: `
					INSERT INTO actor_identities (id, kind, team_id, display_name)
					VALUES ($1::uuid, 'api_client', $2::uuid, $3)
				`,
				args: []any{profileID, teamID, "known-evidence-migration-profile-" + profileID},
			},
			{
				query: `
					INSERT INTO team_memberships (actor_identity_id, team_id, status, team_admin, maximum_grants)
					VALUES ($1::uuid, $2::uuid, 'active', false, ARRAY['read', 'write']::text[])
				`,
				args: []any{profileID, teamID},
			},
			{
				query: `
					INSERT INTO credentials (
						id, actor_identity_id, owner_identity_id, team_id, kind, key_hash,
						key_prefix, key_suffix, name, scopes, rate_limit, status
					) VALUES (
						$1::uuid, $1::uuid, $1::uuid, $2::uuid, 'api_key', $3,
						$4, $5, $6, ARRAY['read', 'write']::text[], 100, 'active'
					)
				`,
				args: []any{profileID, teamID, "hash-" + profileID, keyPrefix, keyPrefix[:6], "known-evidence-migration-profile-" + profileID},
			},
			{
				query: `
					INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, credential_id)
					VALUES ($1::uuid, $2::uuid, $2::uuid, $2::uuid)
				`,
				args: []any{teamID, profileID},
			},
		} {
			if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	}))
	return profileID
}

type knownEvidenceSupportOwnershipFixture struct {
	supportID       string
	relationshipID  string
	observationID   string
	verificationID  string
	spaceID         string
	spaceGeneration int64
}

func insertKnownEvidenceSupportOwnershipFixture(t *testing.T, ctx context.Context, db *sql.DB, teamID, relationshipOwnerID, evidenceOwnerID string) knownEvidenceSupportOwnershipFixture {
	t.Helper()
	spaceID := uuid.NewString()
	var generation int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT id::text, generation
		FROM memory_spaces
		WHERE team_id = $1::uuid AND kind = 'team_shared'
		LIMIT 1
	`, teamID).Scan(&spaceID, &generation))
	entityA, entityB := uuid.NewString(), uuid.NewString()
	ingestID, fragmentID := uuid.NewString(), uuid.NewString()
	relationshipID, observationID, verificationID, supportID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	content := "Known evidence supports this relationship."
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO knowledge_ingests (team_id, ingest_id, owner_profile_id, space_id, space_generation, idempotency_key, request_hash, status, proposal, metadata) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, 'known-support-fixture', 'known-support-fixture', 'completed', '{}'::jsonb, '{}'::jsonb)`, []any{teamID, ingestID, evidenceOwnerID, spaceID, generation}},
			{`INSERT INTO evidence_fragments (team_id, fragment_id, ingest_id, owner_profile_id, space_id, space_generation, evidence_index, content, content_hash, source_type, authority, source_ref, labels, metadata) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 0, $7, 'known-support-hash', 'manual', 'primary', '', ARRAY[]::text[], '{}'::jsonb)`, []any{teamID, fragmentID, ingestID, evidenceOwnerID, spaceID, generation, content}},
			{`INSERT INTO entity_records (team_id, entity_id, entity_kind, identity_context, metadata, space_id, space_generation) VALUES ($1::uuid, $2::uuid, 'concept', '{}'::jsonb, '{}'::jsonb, $3::uuid, $4), ($1::uuid, $5::uuid, 'concept', '{}'::jsonb, '{}'::jsonb, $3::uuid, $4)`, []any{teamID, entityA, spaceID, generation, entityB}},
			{`INSERT INTO team_predicate_definitions (team_id, predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds, relationship_kind, current_cardinality, lifecycle_state, origin, metadata) VALUES ($1::uuid, 'uses', 1, ARRAY[]::text[], ARRAY[]::text[], ARRAY[]::text[], 'state', 'many', 'active', 'built_in', '{}'::jsonb) ON CONFLICT DO NOTHING`, []any{teamID}},
			{`INSERT INTO relationship_records (team_id, relationship_id, owner_profile_id, semantic_group_key, subject_entity_id, predicate_key, predicate_version, object_entity_id, relationship_kind, current_cardinality, status, polarity, support_count, source_group_count, version, metadata, space_id, space_generation) VALUES ($1::uuid, $2::uuid, $3::uuid, 'known-support-group', $4::uuid, 'uses', 1, $5::uuid, 'state', 'many', 'active', '+', 1, 1, 1, '{}'::jsonb, $6::uuid, $7)`, []any{teamID, relationshipID, relationshipOwnerID, entityA, entityB, spaceID, generation}},
			{`INSERT INTO relationship_observations (team_id, observation_id, relationship_id, ingest_id, owner_profile_id, subject_ref, original_predicate, object_ref, subject_entity_id, predicate_key, predicate_version, object_entity_id, polarity, evidence, metadata, space_id, space_generation) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'subject', 'uses', 'object', $6::uuid, 'uses', 1, $7::uuid, '+', '[]'::jsonb, '{}'::jsonb, $8::uuid, $9)`, []any{teamID, observationID, relationshipID, ingestID, relationshipOwnerID, entityA, entityB, spaceID, generation}},
			{`INSERT INTO verification_events (team_id, verification_event_id, observation_id, owner_profile_id, evidence_verdict, rationale, metadata, space_id, space_generation) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'entailed', 'fixture', '{}'::jsonb, $5::uuid, $6)`, []any{teamID, verificationID, observationID, relationshipOwnerID, spaceID, generation}},
			{`INSERT INTO relationship_evidence_supports (team_id, support_id, relationship_id, observation_id, verification_event_id, fragment_id, owner_profile_id, source_group_key, span_start, span_end, quote, authority, metadata, space_id, space_generation) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, 'known-support-fixture', 0, 5, 'Known', 'primary', '{}'::jsonb, $8::uuid, $9)`, []any{teamID, supportID, relationshipID, observationID, verificationID, fragmentID, relationshipOwnerID, spaceID, generation}},
		} {
			if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	}))
	return knownEvidenceSupportOwnershipFixture{supportID: supportID, relationshipID: relationshipID, observationID: observationID, verificationID: verificationID, spaceID: spaceID, spaceGeneration: generation}
}

func insertKnownEvidenceSupportOwnershipEvidence(t *testing.T, ctx context.Context, db *sql.DB, teamID, ownerID, content string) struct{ fragmentID string } {
	t.Helper()
	spaceID, ingestID, fragmentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	var generation int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id::text, generation FROM memory_spaces WHERE team_id = $1::uuid AND kind = 'team_shared' LIMIT 1`, teamID).Scan(&spaceID, &generation))
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_ingests (team_id, ingest_id, owner_profile_id, space_id, space_generation, idempotency_key, request_hash, status, proposal, metadata) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $6, 'completed', '{}'::jsonb, '{}'::jsonb)`, teamID, ingestID, ownerID, spaceID, generation, "cross-owner-"+fragmentID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO evidence_fragments (team_id, fragment_id, ingest_id, owner_profile_id, space_id, space_generation, evidence_index, content, content_hash, source_type, authority, source_ref, labels, metadata) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 0, $7, $8, 'manual', 'primary', '', ARRAY[]::text[], '{}'::jsonb)`, teamID, fragmentID, ingestID, ownerID, spaceID, generation, content, "hash-"+fragmentID)
		return err
	}))
	return struct{ fragmentID string }{fragmentID: fragmentID}
}

func relationshipSupportConstraintExists(t *testing.T, ctx context.Context, db *sql.DB, constraintName string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conrelid = 'relationship_evidence_supports'::regclass
			  AND conname = $1
		)
	`, constraintName).Scan(&exists))
	return exists
}

func relationshipSupportConstraintDefinitionExists(t *testing.T, ctx context.Context, db *sql.DB, definitionPattern string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conrelid = 'relationship_evidence_supports'::regclass
			  AND contype = 'f'
			  AND pg_get_constraintdef(oid) LIKE $1
		)
	`, definitionPattern).Scan(&exists))
	return exists
}
