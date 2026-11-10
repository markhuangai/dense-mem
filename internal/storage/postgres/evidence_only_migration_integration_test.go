//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

const evidenceOnlyMigrationBase int64 = 20261108010000

var evidenceOnlyRetainedTables = []string{"evidence_sources", "evidence_source_revisions", "evidence_exact_aliases", "evidence_occurrences", "evidence_fragments", "evidence_security_events", "evidence_security_signals", "evidence_quarantines", "evidence_lifecycle_operations", "evidence_lifecycle_events", "evidence_conflict_cases", "evidence_conflict_positions", "evidence_conflict_events"}

type evidenceOnlyMigrationFixture struct {
	team, owner, space, ingest, evidence, source, revision, occurrence string
	generation                                                         int64
}

func insertEvidenceOnlyMigrationFixture(t *testing.T, ctx context.Context, db *sql.DB, private bool) evidenceOnlyMigrationFixture {
	t.Helper()
	team, owner := insertMigrationTeamProfile(t, ctx, db)
	fixture := evidenceOnlyMigrationFixture{team: team, owner: owner, ingest: uuid.NewString(), evidence: uuid.NewString(), source: uuid.NewString(), revision: uuid.NewString()}
	fixture.occurrence = fixture.evidence
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		kind := "team_shared"
		if private {
			kind = "private"
		}
		if err := tx.QueryRowContext(ctx, `SELECT id::text,generation FROM memory_spaces WHERE team_id=$1::uuid AND kind=$2 ORDER BY id LIMIT 1`, team, kind).Scan(&fixture.space, &fixture.generation); err != nil {
			return err
		}
		digest := sha256.Sum256([]byte("Exact migration Evidence. 🧭"))
		hash := "sha256:" + hex.EncodeToString(digest[:])
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO knowledge_ingests(team_id,ingest_id,owner_profile_id,space_id,space_generation,idempotency_key,request_hash,status,proposal,metadata) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,'legacy-remember-key','original-request-hash','completed','{"relationship_hints":[{"ref":"retired"}]}','{"contract_version":"dense-mem.v2.7.0"}')`, []any{team, fixture.ingest, owner, fixture.space, fixture.generation}},
			{`INSERT INTO evidence_sources(team_id,source_id,owner_profile_id,space_id,space_generation,source_key,source_kind,authority,metadata) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,'document://original','document','primary','{"source":"original"}')`, []any{team, fixture.source, owner, fixture.space, fixture.generation}},
			{`INSERT INTO evidence_source_revisions(team_id,source_revision_id,source_id,owner_profile_id,space_id,space_generation,revision_token,content_hash,envelope) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6,'revision-1',$7,'{"exact":"original source envelope"}')`, []any{team, fixture.revision, fixture.source, owner, fixture.space, fixture.generation, hash}},
			{`UPDATE evidence_sources SET current_revision_id=$1::uuid,current_revision_token='revision-1' WHERE team_id=$2::uuid AND source_id=$3::uuid`, []any{fixture.revision, team, fixture.source}},
			{`INSERT INTO evidence_fragments(team_id,fragment_id,ingest_id,owner_profile_id,space_id,space_generation,source_id,source_revision_id,evidence_index,content,content_hash,source_type,authority,metadata) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6,$7::uuid,$8::uuid,0,'Exact migration Evidence. 🧭',$9,'document','primary','{"original":"provenance"}')`, []any{team, fixture.evidence, fixture.ingest, owner, fixture.space, fixture.generation, fixture.source, fixture.revision, hash}},
			{`INSERT INTO evidence_security_events(team_id,fragment_id,ingest_id,owner_profile_id,occurrence_id,evidence_owner_profile_id,event_kind,decision,reason,space_id,space_generation) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$4::uuid,'deterministic_scan','pass','original security admission',$6::uuid,$7)`, []any{team, fixture.evidence, fixture.ingest, owner, fixture.occurrence, fixture.space, fixture.generation}},
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return nil
	}))
	return fixture
}

func evidenceOnlyFingerprints(t *testing.T, ctx context.Context, db *sql.DB) map[string]string {
	t.Helper()
	result := map[string]string{}
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, table := range evidenceOnlyRetainedTables {
			var hash string
			query := fmt.Sprintf(`SELECT encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY to_jsonb(row_value)::text)::text,'[]'),'UTF8')),'hex') FROM %s row_value`, quoteMigrationIdentifier(table))
			if err := tx.QueryRowContext(ctx, query).Scan(&hash); err != nil {
				return err
			}
			result[table] = hash
		}
		return nil
	}))
	return result
}

func assertEvidenceOnlyCatalog(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT relname FROM pg_class WHERE relnamespace='public'::regnamespace AND relname ~ '^(entity_|value_|relationship_|predicate_|team_predicate_|hypothes|dream_|community_)' ORDER BY relname`)
	require.NoError(t, err)
	var remaining []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		remaining = append(remaining, name)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Empty(t, remaining)
	var receipt []byte
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT metadata FROM v2_compatibility_markers WHERE marker_kind='evidence_only' AND version='dense-mem.v2.8.0' AND status='compatible'`).Scan(&receipt)
	}))
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(receipt, &metadata))
	require.Equal(t, true, metadata["retained_fingerprints_verified"])
	require.Equal(t, "committed", metadata["transaction_outcome"])
	var temporary int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM pg_policy WHERE polname IN ('evidence_only_inventory_read','evidence_only_migration_repair','evidence_only_marker_insert')`).Scan(&temporary))
	require.Zero(t, temporary)
	var deleteAction string
	var deferrable, initiallyDeferred bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT confdeltype::text,condeferrable,condeferred FROM pg_constraint
 WHERE conrelid='evidence_sources'::regclass AND conname='evidence_sources_current_revision_fk'`).Scan(&deleteAction, &deferrable, &initiallyDeferred))
	require.Equal(t, "a", deleteAction)
	require.True(t, deferrable)
	require.False(t, initiallyDeferred)
}

func evidenceOnlyMigrationUpTo(ctx context.Context, db *sql.DB, version int64) error {
	provider, err := newRuntimeMigrationProvider(MigrationsDir(), db, false)
	if err != nil {
		return err
	}
	_, err = provider.UpTo(ctx, version)
	return err
}

func TestEvidenceOnlyPostgresFreshInstall(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	require.NoError(t, NewMigratorWithDB(db).RunUp(ctx))
	assertEvidenceOnlyCatalog(t, ctx, db)
	require.NoError(t, ValidateStartupMigrationState(ctx, db, MigrationsDir()))
	require.ErrorContains(t, migrationDown(ctx, db), "irreversible after commit")
	_, err := db.ExecContext(ctx, `CREATE ROLE evidence_only_marker_writer NOSUPERUSER NOBYPASSRLS;
 GRANT USAGE ON SCHEMA public TO evidence_only_marker_writer;
 GRANT INSERT ON v2_compatibility_markers TO evidence_only_marker_writer;
 GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO evidence_only_marker_writer`)
	require.NoError(t, err)
	err = execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SET LOCAL ROLE evidence_only_marker_writer`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO v2_compatibility_markers(marker_kind,version,status) VALUES('evidence_only','dense-mem.v2.8.0','compatible')`)
		return err
	})
	var databaseError *pgconn.PgError
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, "42501", databaseError.Code)
}

func TestEvidenceOnlyPostgresPopulatedUpgradePreservesEvidenceAndLegacyIdentity(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationBase))
	fixture := insertEvidenceOnlyMigrationFixture(t, ctx, db, false)
	before := evidenceOnlyFingerprints(t, ctx, db)
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationVersion))
	require.Equal(t, before, evidenceOnlyFingerprints(t, ctx, db))
	assertEvidenceOnlyCatalog(t, ctx, db)
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		var legacyKeys int
		if err := tx.QueryRowContext(ctx, `SELECT (metadata->>'legacy_operation_keys')::int FROM v2_compatibility_markers WHERE marker_kind='evidence_only' AND version='dense-mem.v2.8.0'`).Scan(&legacyKeys); err != nil {
			return err
		}
		require.Equal(t, 1, legacyKeys)
		var key, hash, status, retired string
		if err := tx.QueryRowContext(ctx, `SELECT idempotency_key,request_hash,status,metadata->>'retired_payload' FROM knowledge_ingests WHERE team_id=$1::uuid AND ingest_id=$2::uuid`, fixture.team, fixture.ingest).Scan(&key, &hash, &status, &retired); err != nil {
			return err
		}
		require.Equal(t, "legacy-remember-key", key)
		require.Equal(t, "original-request-hash", hash)
		require.Equal(t, "completed", status)
		require.Equal(t, "true", retired)
		return nil
	}))
	require.Error(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE evidence_fragments SET content='forbidden rewrite' WHERE team_id=$1::uuid AND fragment_id=$2::uuid`, fixture.team, fixture.evidence)
		return err
	}))
}

func TestEvidenceOnlyPostgresLegalHoldBlocksAndRollsBack(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationBase))
	fixture := insertEvidenceOnlyMigrationFixture(t, ctx, db, true)
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO private_memory_legal_holds(team_id,space_id,reason_code,actor_class) VALUES($1::uuid,$2::uuid,'evidence_only_test','control')`, fixture.team, fixture.space)
		return err
	}))
	before := evidenceOnlyFingerprints(t, ctx, db)
	require.ErrorContains(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationVersion), "legal hold")
	require.Equal(t, before, evidenceOnlyFingerprints(t, ctx, db))
	var graphExists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('public.relationship_records') IS NOT NULL`).Scan(&graphExists))
	require.True(t, graphExists)
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE private_memory_legal_holds SET released_at=clock_timestamp() WHERE team_id=$1::uuid AND space_id=$2::uuid`, fixture.team, fixture.space)
		return err
	}))
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationVersion))
	require.Equal(t, before, evidenceOnlyFingerprints(t, ctx, db))
	assertEvidenceOnlyCatalog(t, ctx, db)
}

func TestEvidenceOnlyPostgresCancellationAfterRemovalRollsBack(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationBase))
	insertEvidenceOnlyMigrationFixture(t, ctx, db, false)
	before := evidenceOnlyFingerprints(t, ctx, db)
	blocker, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer blocker.Rollback()
	_, err = blocker.ExecContext(ctx, `LOCK TABLE v2_compatibility_markers IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	migrationCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	outcome := make(chan error, 1)
	go func() { outcome <- evidenceOnlyMigrationUpTo(migrationCtx, db, evidenceOnlyMigrationVersion) }()
	require.Eventually(t, func() bool {
		select {
		case err := <-outcome:
			t.Fatalf("migration failed before final marker lock: %v", err)
			return false
		default:
		}
		var waiting bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='public.v2_compatibility_markers'::regclass AND NOT granted)`).Scan(&waiting)
		require.NoError(t, err)
		return waiting
	}, 30*time.Second, 20*time.Millisecond)
	cancel()
	select {
	case err := <-outcome:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled migration did not release transaction")
	}
	require.NoError(t, blocker.Rollback())
	require.Equal(t, before, evidenceOnlyFingerprints(t, ctx, db))
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('public.relationship_records') IS NOT NULL`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationVersion))
	require.Equal(t, before, evidenceOnlyFingerprints(t, ctx, db))
	assertEvidenceOnlyCatalog(t, ctx, db)
}

func TestEvidenceOnlyPostgresUnexpectedDependencyBlocksBeforeRemoval(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationBase))
	_, err := db.ExecContext(ctx, `CREATE TABLE evidence_only_test_dependency(team_id uuid,relationship_id uuid,FOREIGN KEY(team_id,relationship_id) REFERENCES relationship_records(team_id,relationship_id))`)
	require.NoError(t, err)
	require.ErrorContains(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationVersion), "unhandled retained dependency")
	_, err = db.ExecContext(ctx, `DROP TABLE evidence_only_test_dependency`)
	require.NoError(t, err)
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationVersion))
	assertEvidenceOnlyCatalog(t, ctx, db)
}

func TestEvidenceOnlyPostgresSessionMigrationPreservesRawIdentityAndTerminalOutcomes(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationBase))
	fixture := insertEvidenceOnlyMigrationFixture(t, ctx, db, true)
	hashes := map[string]string{}
	for index, result := range []any{nil, `{"contract_version":"dense-mem.v2.7.0","processing_state":"failed","errors":[{"code":"provider_unavailable","retryable":true}]}`, `{"contract_version":"dense-mem.v2.7.0","processing_state":"completed","events":[],"errors":[]}`, `{"contract_version":"dense-mem.v2.7.0","processing_state":"failed","errors":[{"code":"submission_policy_rejected","retryable":false}]}`} {
		id := uuid.NewString()
		hash := "sha256:" + strings.Repeat(fmt.Sprint(index), 64)
		hashes[id] = hash
		intake, err := json.Marshal(map[string]any{"ExtractionVersion": "session_factual_extraction_v1", "RequestHash": hash, "Request": map[string]any{"framework": "adk", "app_name": "migration", "user_id": "u", "session_id": id, "events": []any{map[string]any{"event_id": "event", "text": "Exact raw current event."}}}})
		require.NoError(t, err)
		require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO session_submissions(team_id,owner_profile_id,space_id,space_generation,submission_id,idempotency_key,request_hash,intake,prior_context,new_event_indices,accepted_event_count,duplicate_event_count,result) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$5::text,$6,$7::jsonb,'[]','[0]',1,0,$8::jsonb)`, fixture.team, fixture.owner, fixture.space, fixture.generation, id, hash, string(intake), result); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO session_extraction_checkpoints(team_id,owner_profile_id,space_id,space_generation,submission_id,window_index,body) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,-1,'{"groups":[],"entities":[],"relationships":[]}'),($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,0,'{"entities":[],"relationships":[],"coverage":[]}')`, fixture.team, fixture.owner, fixture.space, fixture.generation, id)
			return err
		}))
	}
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationVersion))
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT submission_id::text,request_hash,intake->>'ExtractionVersion',result IS NULL,intake->'Request'->'events'->0->>'text',COALESCE(result->>'processing_state',''),COALESCE(result->>'retired_payload','') FROM session_submissions WHERE team_id=$1::uuid`, fixture.team)
		if err != nil {
			return err
		}
		defer rows.Close()
		resumable, terminal := 0, 0
		for rows.Next() {
			var id, hash, version, text, state, retired string
			var resume bool
			if err := rows.Scan(&id, &hash, &version, &resume, &text, &state, &retired); err != nil {
				return err
			}
			require.Equal(t, hashes[id], hash)
			require.Equal(t, "Exact raw current event.", text)
			if resume {
				resumable++
				require.Equal(t, "session_evidence_excerpts_v2", version)
			} else {
				terminal++
				require.Contains(t, []string{"completed", "failed"}, state)
				require.Equal(t, "true", retired)
				require.Equal(t, "session_factual_extraction_v1", version)
			}
		}
		require.Equal(t, 2, resumable)
		require.Equal(t, 2, terminal)
		return rows.Err()
	}))
}

func TestEvidenceOnlyPostgresMixedOntologyReceiptsAndDrafts(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationBase))
	fixture := insertEvidenceOnlyMigrationFixture(t, ctx, db, false)
	assessmentID, publicationID, groupID, retiredID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	overrideID, oldOverridePublication, newOverridePublication := uuid.NewString(), uuid.NewString(), uuid.NewString()
	digest := "sha256:" + strings.Repeat("a", 64)
	evidence := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: fixture.evidence, Version: 1}
	other := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: uuid.NewString(), Version: 1}
	group := ontology.Record{ID: groupID, Version: 1, Kind: ontology.EvidenceGroup, Group: &ontology.Group{Members: []ontology.SourceHandle{evidence, other}, AssessmentID: assessmentID},
		Sources:      []ontology.SourceDependency{{SourceHandle: evidence, Fingerprint: digest}, {SourceHandle: other, Fingerprint: digest}, {SourceHandle: ontology.SourceHandle{Kind: "entity", ID: retiredID, Version: 1}, Fingerprint: digest}},
		Dependencies: []ontology.RevisionRef{{ID: retiredID, Version: 1}, {ID: overrideID, Version: 1}}, Fingerprint: digest}
	legacyOverride := ontology.Record{ID: overrideID, Version: 1, Kind: ontology.OverrideKind,
		Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: retiredID, Members: []ontology.SourceHandle{{Kind: "entity", ID: retiredID, Version: 1}}},
		Sources:  []ontology.SourceDependency{{SourceHandle: ontology.SourceHandle{Kind: "entity", ID: retiredID, Version: 1}, Fingerprint: digest}}, Dependencies: []ontology.RevisionRef{{ID: retiredID, Version: 1}}, Fingerprint: digest}
	compiled, err := ontology.CompileManagementEdits(ontology.SaveDraftInput{OperationKey: "evidence-override", ExpectedRevision: 2, Reason: "Replace old graph classification with an Evidence group", Edits: []ontology.ManagementEdit{{Action: "group_together", RecordID: overrideID, ExpectedVersion: 1, Members: []ontology.SourceHandle{evidence, other}}}},
		ontology.ManagementContext{DraftID: uuid.NewString(), Revision: 2, Records: map[string]ontology.Record{overrideID: legacyOverride}, Sources: map[string]ontology.SourceSnapshot{
			ontology.SourceKey(evidence): {SourceHandle: evidence, TeamID: fixture.team, SpaceID: fixture.space, Generation: fixture.generation, Eligible: true, MeaningKey: "migration-override-context"},
			ontology.SourceKey(other):    {SourceHandle: other, TeamID: fixture.team, SpaceID: fixture.space, Generation: fixture.generation, Eligible: true, MeaningKey: "migration-override-context"},
		}})
	require.NoError(t, err)
	require.Len(t, compiled.Changes, 1)
	retainedOverride := compiled.Changes[0].Record
	retainedOverride.Version = 2
	retainedOverride.Fingerprint, err = ontology.RecordFingerprintWithAcceptedSources(retainedOverride, map[string]ontology.Record{overrideID: retainedOverride})
	require.NoError(t, err)
	receipt := ontology.OrganizationReceipt{ID: assessmentID, OperationKey: "mixed-receipt", InputHash: digest, BatchHash: digest, ContextHash: digest, ProviderIdentity: "original-provider",
		Sources: group.Sources, Dependencies: group.Dependencies, Result: ontology.OrganizationResult{AssessmentID: assessmentID, Current: true,
			Outcomes:    []ontology.OrganizationOutcome{{Source: evidence, Status: "organized", RecordIDs: []string{groupID, retiredID}}, {Source: other, Status: "organized", RecordIDs: []string{groupID}}, {Source: group.Sources[2].SourceHandle, Status: "organized", RecordIDs: []string{retiredID}}},
			Attempts:    []ontology.AssessmentAttempt{{Number: 1, ReportedUsageAvailable: true, ReportedInputTokens: 123, ReportedOutputTokens: 17, ReportedTotalTokens: 140}},
			Publication: &ontology.PublicationResult{ID: publicationID, Revision: 1, Records: []ontology.RevisionRef{{ID: groupID, Version: 1}, {ID: retiredID, Version: 1}}}}}
	draftIDs := []string{uuid.NewString(), uuid.NewString()}
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_catalog_heads(team_id,shared_space_id,space_generation,revision) VALUES($1::uuid,$2::uuid,$3,3)`, fixture.team, fixture.space, fixture.generation); err != nil {
			return err
		}
		result, _ := json.Marshal(receipt.Result.Publication)
		if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_publications(team_id,shared_space_id,space_generation,publication_id,revision,operation_key,request_hash,origin,reason,result) VALUES($1::uuid,$2::uuid,$3,$4::uuid,1,'mixed-publication',$5,'automatic','original mixed publication',$6::jsonb)`, fixture.team, fixture.space, fixture.generation, publicationID, digest, string(result)); err != nil {
			return err
		}
		for _, record := range []ontology.Record{group, {ID: retiredID, Version: 1, Kind: "entity_class", Definition: &ontology.Definition{Key: "retired", Label: "Retired", Description: "Factual class"}, Fingerprint: digest}} {
			body, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version,kind,publication_id,retired,body,fingerprint) VALUES($1::uuid,$2::uuid,$3,$4::uuid,1,$5,$6::uuid,false,$7::jsonb,$8)`, fixture.team, fixture.space, fixture.generation, record.ID, string(record.Kind), publicationID, string(body), digest); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO ontology_record_heads(team_id,shared_space_id,space_generation,record_id,version,kind,retired) VALUES($1::uuid,$2::uuid,$3,$4::uuid,1,$5,false)`, fixture.team, fixture.space, fixture.generation, record.ID, string(record.Kind)); err != nil {
				return err
			}
		}
		for index, record := range []ontology.Record{legacyOverride, retainedOverride} {
			id := []string{oldOverridePublication, newOverridePublication}[index]
			body, err := json.Marshal(record)
			if err != nil {
				return err
			}
			result, err := json.Marshal(ontology.PublicationResult{ID: id, Revision: int64(index + 2), Records: []ontology.RevisionRef{{ID: overrideID, Version: record.Version}}})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_publications(team_id,shared_space_id,space_generation,publication_id,revision,operation_key,request_hash,origin,actor_id,reason,result) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$4::text,$6,'manager',$7::uuid,'Manager revised override',$8::jsonb)`, fixture.team, fixture.space, fixture.generation, id, index+2, digest, fixture.owner, string(result)); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version,kind,publication_id,retired,body,fingerprint) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,'override',$6::uuid,false,$7::jsonb,$8)`, fixture.team, fixture.space, fixture.generation, overrideID, record.Version, id, string(body), record.Fingerprint); err != nil {
				return err
			}
			for _, source := range record.Sources {
				if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_source_dependencies(team_id,shared_space_id,space_generation,record_id,record_version,source_kind,source_id,source_version,fingerprint) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,$8,$9)`, fixture.team, fixture.space, fixture.generation, overrideID, record.Version, string(source.Kind), source.ID, source.Version, source.Fingerprint); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_record_heads(team_id,shared_space_id,space_generation,record_id,version,kind,retired) VALUES($1::uuid,$2::uuid,$3,$4::uuid,2,'override',false)`, fixture.team, fixture.space, fixture.generation, overrideID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_revision_dependencies(team_id,shared_space_id,space_generation,record_id,record_version,dependency_id,dependency_version) VALUES($1::uuid,$2::uuid,$3,$4::uuid,1,$5::uuid,1)`, fixture.team, fixture.space, fixture.generation, groupID, overrideID); err != nil {
			return err
		}
		body, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO ontology_assessments(team_id,shared_space_id,space_generation,operation_key,assessment_id,input_hash,batch_hash,body) VALUES($1::uuid,$2::uuid,$3,'mixed-receipt',$4::uuid,$5,$5,$6::jsonb)`, fixture.team, fixture.space, fixture.generation, assessmentID, digest, string(body)); err != nil {
			return err
		}
		for index, id := range draftIDs {
			kind := "topic"
			if index == 1 {
				kind = "entity_class"
			}
			body, err := json.Marshal(map[string]any{"draft": map[string]any{"id": id, "version": 1, "state": "open", "edits": []any{map[string]any{"action": "define", "kind": kind, "definition": map[string]any{"key": "draft", "label": "Draft", "description": "Draft definition"}}}}, "prepared": map[string]any{"changes": []any{map[string]any{"record": map[string]any{"id": uuid.NewString(), "kind": kind}}}}})
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO ontology_draft_heads(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version,state,body) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,1,'open',$6::jsonb)`, fixture.team, fixture.space, fixture.generation, fixture.owner, id, string(body)); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO ontology_draft_revisions(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version,operation_key,request_hash,body) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,1,$5::text,$6,$7::jsonb)`, fixture.team, fixture.space, fixture.generation, fixture.owner, id, digest, string(body)); err != nil {
				return err
			}
		}
		return nil
	}))
	before := evidenceOnlyFingerprints(t, ctx, db)
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationVersion))
	require.Equal(t, before, evidenceOnlyFingerprints(t, ctx, db))
	assertEvidenceOnlyCatalog(t, ctx, db)
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		var body []byte
		if err := tx.QueryRowContext(ctx, `SELECT body FROM ontology_assessments WHERE assessment_id=$1::uuid`, assessmentID).Scan(&body); err != nil {
			return err
		}
		var retained ontology.OrganizationReceipt
		require.NoError(t, jsonstrict.Decode(strings.NewReader(string(body)), &retained, ontology.MaxPublicationBytes))
		require.NoError(t, ontology.ValidateOrganizationReceipt(retained))
		require.Len(t, retained.Sources, 2)
		require.Len(t, retained.Result.Outcomes, 2)
		require.Empty(t, retained.Dependencies)
		require.Equal(t, receipt.Result.Attempts, retained.Result.Attempts)
		require.Equal(t, receipt.InputHash, retained.InputHash)
		require.Equal(t, []string{groupID}, retained.Result.Outcomes[0].RecordIDs)
		require.Equal(t, []ontology.RevisionRef{{ID: groupID, Version: 1}}, retained.Result.Publication.Records)
		if err := tx.QueryRowContext(ctx, `SELECT body FROM ontology_record_revisions WHERE record_id=$1::uuid`, groupID).Scan(&body); err != nil {
			return err
		}
		var rebound ontology.Record
		require.NoError(t, json.Unmarshal(body, &rebound))
		require.NoError(t, ontology.ValidateRecord(rebound))
		expected, err := ontology.RecordFingerprintWithAcceptedSources(rebound, map[string]ontology.Record{groupID: rebound})
		require.NoError(t, err)
		require.Equal(t, expected, rebound.Fingerprint)
		require.NotEqual(t, digest, rebound.Fingerprint)
		require.Equal(t, assessmentID, rebound.Group.AssessmentID)
		if err := tx.QueryRowContext(ctx, `SELECT revision.body FROM ontology_record_heads head JOIN ontology_record_revisions revision USING(team_id,shared_space_id,space_generation,record_id,version) WHERE head.record_id=$1::uuid`, overrideID).Scan(&body); err != nil {
			return err
		}
		var preserved ontology.Record
		require.NoError(t, json.Unmarshal(body, &preserved))
		require.Equal(t, retainedOverride, preserved)
		var obsolete, currentSources int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ontology_record_revisions WHERE record_id=$1::uuid AND version=1`, overrideID).Scan(&obsolete); err != nil {
			return err
		}
		require.Zero(t, obsolete)
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ontology_source_dependencies WHERE record_id=$1::uuid AND record_version=2`, overrideID).Scan(&currentSources); err != nil {
			return err
		}
		require.Equal(t, 2, currentSources)
		for index, publication := range []string{oldOverridePublication, newOverridePublication} {
			if err := tx.QueryRowContext(ctx, `SELECT result FROM ontology_publications WHERE publication_id=$1::uuid`, publication).Scan(&body); err != nil {
				return err
			}
			var result ontology.PublicationResult
			require.NoError(t, json.Unmarshal(body, &result))
			if index == 0 {
				require.Empty(t, result.Records)
			} else {
				require.Equal(t, []ontology.RevisionRef{{ID: overrideID, Version: 2}}, result.Records)
			}
		}
		for index, id := range draftIDs {
			var state string
			var edits, prepared int
			var original sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT state,jsonb_array_length(body->'draft'->'edits'),COALESCE(jsonb_array_length(body->'prepared'->'changes'),0),body->>'original_state' FROM ontology_draft_heads WHERE draft_id=$1::uuid`, id).Scan(&state, &edits, &prepared, &original); err != nil {
				return err
			}
			if index == 0 {
				require.Equal(t, "open", state)
				require.Equal(t, 1, edits)
				require.Equal(t, 1, prepared)
				require.False(t, original.Valid)
			} else {
				require.Equal(t, "discarded", state)
				require.Zero(t, edits)
				require.Zero(t, prepared)
				require.Equal(t, "open", original.String)
			}
		}
		return nil
	}))
}
