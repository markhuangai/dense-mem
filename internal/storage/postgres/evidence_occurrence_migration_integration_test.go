//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const (
	evidenceOccurrenceMigrationBase    int64 = 20260902010003
	evidenceOccurrenceMigrationVersion int64 = 20260903010001
)

func TestEvidenceOccurrenceMigrationBackfillsAliasesAndOccurrencesInBatches(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, evidenceOccurrenceMigrationBase)
	teamID, ownerID := insertMigrationTeamProfile(t, ctx, db)

	var spaceID string
	var generation int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT id::text, generation
		FROM memory_spaces
		WHERE team_id = $1::uuid AND kind = 'team_shared'
		LIMIT 1
	`, teamID).Scan(&spaceID, &generation))
	ingestID := uuid.NewString()
	const fragmentCount = 502
	firstHistoricalID := "00000000-0000-0000-0000-000000000001"
	secondHistoricalID := "00000000-0000-0000-0000-000000000002"
	collisionCanonicalID := "00000000-0000-0000-0000-000000001001"
	collisionBytesID := "00000000-0000-0000-0000-000000001002"
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, space_id, space_generation,
				idempotency_key, request_hash, status, proposal, metadata
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5,
			          'occurrence-migration-fixture', 'occurrence-migration-fixture',
			          'completed', '{}'::jsonb, '{}'::jsonb)
		`, teamID, ingestID, ownerID, spaceID, generation); err != nil {
			return err
		}
		for index := 0; index < fragmentCount; index++ {
			fragmentID := fmt.Sprintf("00000000-0000-0000-0000-%012d", index+1)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO evidence_fragments (
					team_id, fragment_id, ingest_id, owner_profile_id,
					space_id, space_generation, evidence_index, content, content_hash,
					source_type, authority, source_ref, labels, metadata
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6,
				          $7, 'same historical evidence', 'sha256:historical-duplicate',
				          'manual', 'primary', '', ARRAY[]::text[], '{}'::jsonb)
			`, teamID, fragmentID, ingestID, ownerID, spaceID, generation, index); err != nil {
				return err
			}
		}
		for index, fixture := range []struct {
			id, content string
		}{
			{collisionCanonicalID, "same hash but byte A"},
			{collisionBytesID, "same hash but byte B"},
		} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO evidence_fragments (
					team_id, fragment_id, ingest_id, owner_profile_id,
					space_id, space_generation, evidence_index, content, content_hash,
					source_type, authority, source_ref, labels, metadata
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6,
				          $7, $8, 'sha256:collision', 'manual', 'primary', '', ARRAY[]::text[], '{}'::jsonb)
			`, teamID, fixture.id, ingestID, ownerID, spaceID, generation, fragmentCount+index, fixture.content); err != nil {
				return err
			}
		}
		return nil
	}))

	runGooseUpTo(t, ctx, db, evidenceOccurrenceMigrationVersion)
	var aliases, occurrences int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)::int FROM evidence_exact_aliases WHERE team_id = $1::uuid
	`, teamID).Scan(&aliases))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)::int FROM evidence_occurrences WHERE team_id = $1::uuid
	`, teamID).Scan(&occurrences))
	require.Equal(t, fragmentCount-1, aliases)
	require.Equal(t, fragmentCount+2, occurrences)

	var canonicalCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(DISTINCT canonical_fragment_id)::int
		FROM evidence_occurrences
		WHERE team_id = $1::uuid
	`, teamID).Scan(&canonicalCount))
	require.Equal(t, 3, canonicalCount)
	var deterministicCanonical string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT canonical_fragment_id::text
		FROM evidence_exact_aliases
		WHERE team_id = $1::uuid AND alias_fragment_id = $2::uuid
	`, teamID, secondHistoricalID).Scan(&deterministicCanonical))
	require.Equal(t, firstHistoricalID, deterministicCanonical)
	var secondBatchCanonical string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT canonical_fragment_id::text
		FROM evidence_exact_aliases
		WHERE team_id = $1::uuid AND alias_fragment_id = $2::uuid
	`, teamID, fmt.Sprintf("00000000-0000-0000-0000-%012d", fragmentCount)).Scan(&secondBatchCanonical))
	require.Equal(t, firstHistoricalID, secondBatchCanonical)
	var collisionAliases int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)::int
		FROM evidence_exact_aliases
		WHERE team_id = $1::uuid AND alias_fragment_id IN ($2::uuid, $3::uuid)
	`, teamID, collisionCanonicalID, collisionBytesID).Scan(&collisionAliases))
	require.Zero(t, collisionAliases, "same hash with different bytes must remain canonical evidence")

	var policyCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)::int
		FROM pg_policy
		WHERE polrelid IN ('evidence_occurrences'::regclass, 'evidence_exact_aliases'::regclass)
		  AND polname LIKE '%private_erasure_delete'
	`).Scan(&policyCount))
	require.Equal(t, 2, policyCount)

	require.Error(t, migrationDownTo(ctx, db, evidenceOccurrenceMigrationBase))
}

func TestEvidenceOccurrenceMigrationBackfillPrefersCurrentSourceRevision(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, evidenceOccurrenceMigrationBase)
	teamID, ownerID := insertMigrationTeamProfile(t, ctx, db)

	var spaceID string
	var generation int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT id::text, generation
		FROM memory_spaces
		WHERE team_id = $1::uuid AND kind = 'team_shared'
		LIMIT 1
	`, teamID).Scan(&spaceID, &generation))

	sourceID := uuid.NewString()
	oldRevisionID := uuid.NewString()
	currentRevisionID := uuid.NewString()
	oldIngestID := uuid.NewString()
	currentIngestID := uuid.NewString()
	oldFragmentID := uuid.NewString()
	currentFragmentID := uuid.NewString()
	const content = "source revision duplicate remains visible"
	const contentHash = "sha256:source-revision-duplicate"
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, ingest := range []struct {
			id, key string
		}{
			{oldIngestID, "source-revision-old-ingest"},
			{currentIngestID, "source-revision-current-ingest"},
		} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO knowledge_ingests (
					team_id, ingest_id, owner_profile_id, space_id, space_generation,
					idempotency_key, request_hash, status, proposal, metadata
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5,
				          $6, $6, 'completed', '{}'::jsonb, '{}'::jsonb)
			`, teamID, ingest.id, ownerID, spaceID, generation, ingest.key); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_sources (
				team_id, source_id, owner_profile_id, source_key, source_kind, authority,
				space_id, space_generation
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'doc://source-revision-duplicate',
			          'document', 'primary', $4::uuid, $5)
		`, teamID, sourceID, ownerID, spaceID, generation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_source_revisions (
				team_id, source_revision_id, source_id, owner_profile_id,
				revision_token, expected_previous_revision_token, content_hash,
				envelope, space_id, space_generation
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'rev-1', '',
			          'sha256:source-revision-one', '{}'::jsonb, $5::uuid, $6)
		`, teamID, oldRevisionID, sourceID, ownerID, spaceID, generation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_source_revisions (
				team_id, source_revision_id, source_id, owner_profile_id,
				revision_token, expected_previous_revision_token, supersedes_revision_id,
				content_hash, envelope, space_id, space_generation
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'rev-2', 'rev-1',
			          $5::uuid, 'sha256:source-revision-two', '{}'::jsonb, $6::uuid, $7)
		`, teamID, currentRevisionID, sourceID, ownerID, oldRevisionID, spaceID, generation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE evidence_sources
			SET current_revision_id = $1::uuid, current_revision_token = 'rev-2'
			WHERE team_id = $2::uuid AND source_id = $3::uuid
		`, currentRevisionID, teamID, sourceID); err != nil {
			return err
		}
		for _, fragment := range []struct {
			id, ingestID, revisionID string
			createdAt                string
		}{
			{oldFragmentID, oldIngestID, oldRevisionID, "2026-01-01T00:00:00Z"},
			{currentFragmentID, currentIngestID, currentRevisionID, "2026-01-02T00:00:00Z"},
		} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO evidence_fragments (
					team_id, fragment_id, ingest_id, owner_profile_id, source_id,
					source_revision_id, space_id, space_generation, evidence_index,
					content, content_hash, source_type, authority, source_ref, labels, metadata,
					created_at
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid,
				          $7::uuid, $8, 0, $9, $10, 'document', 'primary', '',
				          ARRAY[]::text[], '{}'::jsonb, $11::timestamptz)
			`, teamID, fragment.id, fragment.ingestID, ownerID, sourceID, fragment.revisionID,
				spaceID, generation, content, contentHash, fragment.createdAt); err != nil {
				return err
			}
		}
		return nil
	}))

	runGooseUpTo(t, ctx, db, evidenceOccurrenceMigrationVersion)
	var canonicalID string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT canonical_fragment_id::text
		FROM evidence_exact_aliases
		WHERE team_id = $1::uuid AND alias_fragment_id = $2::uuid
	`, teamID, oldFragmentID).Scan(&canonicalID))
	require.Equal(t, currentFragmentID, canonicalID)
	var currentAliasCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)::int
		FROM evidence_exact_aliases
		WHERE team_id = $1::uuid AND alias_fragment_id = $2::uuid
	`, teamID, currentFragmentID).Scan(&currentAliasCount))
	require.Zero(t, currentAliasCount, "the current source revision must remain canonical")
	var canonicalOccurrences int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)::int
		FROM evidence_occurrences
		WHERE team_id = $1::uuid AND canonical_fragment_id = $2::uuid
	`, teamID, currentFragmentID).Scan(&canonicalOccurrences))
	require.Equal(t, 2, canonicalOccurrences)
}

func TestEvidenceOccurrenceMigrationKeepsIndependentSourceLineagesSeparate(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, evidenceOccurrenceMigrationBase)
	teamID, ownerID := insertMigrationTeamProfile(t, ctx, db)

	var spaceID string
	var generation int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT id::text, generation
		FROM memory_spaces
		WHERE team_id = $1::uuid AND kind = 'team_shared'
		LIMIT 1
	`, teamID).Scan(&spaceID, &generation))

	sourceOneID, sourceTwoID := uuid.NewString(), uuid.NewString()
	revisionOneID, revisionTwoID := uuid.NewString(), uuid.NewString()
	ingestOneID, ingestTwoID := uuid.NewString(), uuid.NewString()
	fragmentOneID, fragmentTwoID := uuid.NewString(), uuid.NewString()
	const content = "identical bytes from independent source lineages"
	const contentHash = "sha256:independent-source-lineages"
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, ingest := range []struct{ id, key string }{
			{ingestOneID, "independent-source-lineage-one"},
			{ingestTwoID, "independent-source-lineage-two"},
		} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO knowledge_ingests (
					team_id, ingest_id, owner_profile_id, space_id, space_generation,
					idempotency_key, request_hash, status, proposal, metadata
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5,
				          $6, $6, 'completed', '{}'::jsonb, '{}'::jsonb)
			`, teamID, ingest.id, ownerID, spaceID, generation, ingest.key); err != nil {
				return err
			}
		}
		for _, source := range []struct {
			id, key, revisionID, ingestID, fragmentID string
		}{
			{sourceOneID, "document://independent-source-one", revisionOneID, ingestOneID, fragmentOneID},
			{sourceTwoID, "document://independent-source-two", revisionTwoID, ingestTwoID, fragmentTwoID},
		} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO evidence_sources (
					team_id, source_id, owner_profile_id, source_key, source_kind, authority,
					space_id, space_generation
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4,
				          'document', 'primary', $5::uuid, $6)
			`, teamID, source.id, ownerID, source.key, spaceID, generation); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO evidence_source_revisions (
					team_id, source_revision_id, source_id, owner_profile_id,
					revision_token, expected_previous_revision_token, content_hash,
					envelope, space_id, space_generation
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'rev-1', '',
				          $5, '{}'::jsonb, $6::uuid, $7)
			`, teamID, source.revisionID, source.id, ownerID, contentHash, spaceID, generation); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE evidence_sources
				SET current_revision_id = $1::uuid, current_revision_token = 'rev-1'
				WHERE team_id = $2::uuid AND source_id = $3::uuid
			`, source.revisionID, teamID, source.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO evidence_fragments (
					team_id, fragment_id, ingest_id, owner_profile_id, source_id,
					source_revision_id, space_id, space_generation, evidence_index,
					content, content_hash, source_type, authority, source_ref, labels, metadata
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid,
				          $7::uuid, $8, 0, $9, $10, 'document', 'primary', '',
				          ARRAY[]::text[], '{}'::jsonb)
			`, teamID, source.fragmentID, source.ingestID, ownerID, source.id, source.revisionID,
				spaceID, generation, content, contentHash); err != nil {
				return err
			}
		}
		return nil
	}))

	runGooseUpTo(t, ctx, db, evidenceOccurrenceMigrationVersion)
	var aliases int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)::int
		FROM evidence_exact_aliases
		WHERE team_id = $1::uuid AND alias_fragment_id IN ($2::uuid, $3::uuid)
	`, teamID, fragmentOneID, fragmentTwoID).Scan(&aliases))
	require.Zero(t, aliases)
	var canonicalCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(DISTINCT canonical_fragment_id)::int
		FROM evidence_occurrences
		WHERE team_id = $1::uuid AND occurrence_id IN ($2::uuid, $3::uuid)
	`, teamID, fragmentOneID, fragmentTwoID).Scan(&canonicalCount))
	require.Equal(t, 2, canonicalCount)
}
