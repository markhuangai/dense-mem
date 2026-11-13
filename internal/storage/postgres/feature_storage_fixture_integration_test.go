//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func featureStorageFixture(t *testing.T, count int) (*sql.DB, evidenceOnlyMigrationFixture) {
	t.Helper()
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	t.Cleanup(cleanup)
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, 20261108010000))
	fixture := insertEvidenceOnlyMigrationFixture(t, ctx, db, false)
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, evidenceOnlyMigrationVersion))
	publication := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_catalog_heads(team_id,shared_space_id,space_generation,revision) VALUES($1::uuid,$2::uuid,$3,1)`, fixture.team, fixture.space, fixture.generation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_publications(team_id,shared_space_id,space_generation,publication_id,revision,operation_key,request_hash,origin,reason,result) VALUES($1::uuid,$2::uuid,$3,$4::uuid,1,'feature-storage-fixture',$5,'automatic','exact fixture','{}')`, fixture.team, fixture.space, fixture.generation, publication, "sha256:"+strings.Repeat("a", 64)); err != nil {
			return err
		}
		var first ontology.Record
		for i := 0; i < count; i++ {
			record := ontology.Record{ID: uuid.NewString(), Version: 1, Kind: ontology.Topic, Definition: &ontology.Definition{Key: fmt.Sprintf("fixture_%d", i), Label: fmt.Sprintf("Exact café 🧭 %d", i)}}
			var err error
			record.Fingerprint, err = ontology.RecordFingerprintWithAcceptedSources(record, nil)
			if err != nil {
				return err
			}
			body, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version,kind,publication_id,retired,body,fingerprint) VALUES($1::uuid,$2::uuid,$3,$4::uuid,1,'topic',$5::uuid,false,$6::jsonb,$7)`, fixture.team, fixture.space, fixture.generation, record.ID, publication, string(body), record.Fingerprint); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_record_heads(team_id,shared_space_id,space_generation,record_id,version,kind,retired,names) VALUES($1::uuid,$2::uuid,$3,$4::uuid,1,'topic',false,ARRAY[]::text[])`, fixture.team, fixture.space, fixture.generation, record.ID); err != nil {
				return err
			}
			if i == 0 {
				first = record
			}
		}
		if count == 0 {
			return nil
		}
		draft := uuid.NewString()
		body, err := json.Marshal(map[string]any{"draft": map[string]any{"id": draft, "version": 1, "state": "open", "base_revision": 1, "reason": "exact draft", "updated_at": time.Now().UTC(), "edits": []any{
			map[string]any{"action": "define", "kind": "topic", "definition": first.Definition},
			map[string]any{"action": "pin_definition", "definition_id": first.ID},
		}}, "prepared": map[string]any{"operation_key": "prepared", "expected_revision": 1, "reason": "exact draft", "changes": []any{map[string]any{"expected_version": 0, "record": first}}}})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ontology_draft_heads(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version,state,body) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,1,'open',$6::jsonb)`, fixture.team, fixture.space, fixture.generation, fixture.owner, draft, string(body)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ontology_draft_revisions(team_id,shared_space_id,space_generation,owner_profile_id,draft_id,version,operation_key,request_hash,body) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,1,'draft-operation',$6,$7::jsonb)`, fixture.team, fixture.space, fixture.generation, fixture.owner, draft, "sha256:"+strings.Repeat("b", 64), string(body))
		return err
	}))
	return db, fixture
}

func featureStorageSnapshot(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	var body string
	require.NoError(t, execPostgresTxMode(context.Background(), db, "system", func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(source) ORDER BY to_jsonb(source)::text)::text,'[]') FROM ` + quoteMigrationIdentifier(table) + ` source`).Scan(&body)
	}))
	return body
}

func stopFeatureCopy(t *testing.T, db *sql.DB, terminate bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := false
	ctx, progress := ObserveMigration(ctx, func(s MigrationSnapshot) {
		if !stopped && s.Phase == "copy" && s.WorkUnit == "ontology_record_revisions" && s.State == "batch_committed" {
			stopped = true
			if terminate {
				_, err := db.Exec(`SELECT pg_terminate_backend($1)`, s.BackendPID)
				require.NoError(t, err)
			} else {
				cancel()
			}
		}
	})
	defer progress.Close()
	require.Error(t, NewMigratorWithDB(db).RunUp(ctx))
	require.True(t, stopped)
	var committed int64
	require.NoError(t, execPostgresTxMode(context.Background(), db, "system", func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT row_count FROM data_migration_checkpoints WHERE migration_version=$1 AND phase='copy' AND work_unit='ontology_record_revisions'`, FeatureStorageDataVersion).Scan(&committed)
	}))
	require.EqualValues(t, 100, committed)
}
