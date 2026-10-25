//go:build integration

package migrationapp

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func openOntologyUpgradeRuntimeDB(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	admin := openMigrationIntegrationDB(t)
	require.NoError(t, admin.Exec(`CREATE EXTENSION IF NOT EXISTS pgcrypto;
	 CREATE EXTENSION IF NOT EXISTS vector;
	 CREATE ROLE ontology_upgrade_runtime LOGIN PASSWORD 'ontology-upgrade-fixture'
	 NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
	 GRANT CONNECT,CREATE,TEMPORARY ON DATABASE testdb TO ontology_upgrade_runtime;
	 GRANT USAGE,CREATE ON SCHEMA public TO ontology_upgrade_runtime`).Error)
	dsn, err := url.Parse(admin.Dialector.(*gormpostgres.Dialector).DSN)
	require.NoError(t, err)
	dsn.User = url.UserPassword("ontology_upgrade_runtime", "ontology-upgrade-fixture")
	db, err := gorm.Open(gormpostgres.Open(dsn.String()), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	var restricted bool
	require.NoError(t, db.Raw(`SELECT NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)
	 AND pg_get_userbyid(database.datdba)<>current_user FROM pg_roles AS role
	 JOIN pg_database AS database ON database.datname=current_database() WHERE role.rolname=current_user`).Row().Scan(&restricted))
	require.True(t, restricted)
	return db, admin
}

func seedOntologyUpgrade(t *testing.T, db *gorm.DB) string {
	t.Helper()
	source, root := storage.MigrationsDir(), t.TempDir()
	files, err := filepath.Glob(filepath.Join(source, "*", "*.sql"))
	require.NoError(t, err)
	for _, file := range files {
		if strings.SplitN(filepath.Base(file), "_", 2)[0] >= "20261023020000" {
			continue
		}
		relative, err := filepath.Rel(source, file)
		require.NoError(t, err)
		destination := filepath.Join(root, "migrations", "postgres", relative)
		require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o755))
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(destination, body, 0o600))
	}
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Chdir(root)
	require.NoError(t, storage.RunUp(context.Background(), db))
	t.Chdir(cwd)

	ctx, rls := context.Background(), storage.NewRLS()
	team := &domain.Team{Name: "ontology-upgrade-fixture"}
	require.NoError(t, access.NewTeamRepository(db, rls).Create(ctx, team))
	credential := &domain.Credential{ID: uuid.New(), TeamID: team.ID, Name: "historical-owner", KeyHash: "synthetic-upgrade-key", KeyPrefix: "synthetic-upgrade", KeySuffix: "test", Scopes: []string{"read", "write"}}
	require.NoError(t, access.NewCredentialRepository(db, rls, nil).CreateCredential(ctx, credential))
	var space string
	var generation int64
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, team.ID).Row().Scan(&space, &generation)
	}))
	store := knowledgepostgres.NewStore(db, rls, knowledge.ConflictRuntimeConfig{})
	fragment := uuid.NewString()
	_, err = store.CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: team.ID.String(), OwnerProfileID: credential.ID.String(), IdempotencyKey: "ontology-upgrade-history", RequestHash: "ontology-upgrade-history", Evidence: []knowledge.EvidenceInput{{FragmentID: fragment, Content: "Atlas uses PostgreSQL."}}})
	require.NoError(t, err)
	window, run, complete, interrupted, assessment := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO ontology_maintenance_windows(window_id,starts_at,ends_at,policy,charged_input,charged_output,reserved_input,reserved_output)
		 VALUES(?::uuid,?,?,?::jsonb,70,100,70,100)`, window, now.Add(-time.Hour), now.Add(time.Hour),
			`{"enabled":true,"cadence_hours":12,"start_time_local":"03:00","timezone":"UTC","model":"fixture-model","max_concurrency":1,"input_tokens":1000,"output_tokens":1000,"settings_version":"legacy"}`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ontology_maintenance_runs(run_id,window_id,operation_key,request_hash,command,kind,status,max_batches,created_at,updated_at)
		 VALUES(?::uuid,?::uuid,'ontology-upgrade-fixture','legacy','{}','scheduled','pending',0,?,?)`, run, window, now, now).Error; err != nil {
			return err
		}
		for _, batch := range []string{complete, interrupted} {
			status := "running"
			var completed any
			if batch == complete {
				status, completed = "completed", now.Add(-time.Minute)
			}
			if err := tx.Exec(`INSERT INTO ontology_maintenance_batches(batch_id,run_id,window_id,team_id,shared_space_id,space_generation,lease_token,lease_until,sources,revisions,status,created_at,completed_at)
			 VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?::uuid,?,?::uuid,?,jsonb_build_array(jsonb_build_object('kind','evidence','id',?::text,'version',1)),'{}',?,?,?)`,
				batch, run, window, team.ID, space, generation, uuid.NewString(), now.Add(-time.Minute), fragment, status, now.Add(-2*time.Minute), completed).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`INSERT INTO ontology_maintenance_attempts(batch_id,assessment_id,attempt,window_id,reserved_input,reserved_output,reported_input,reported_output,reported_total,admitted_at,reconciled_at)
		 VALUES(?::uuid,?::uuid,1,?::uuid,70,100,0,0,0,?,?)`, complete, assessment, window, now.Add(-2*time.Minute), now.Add(-time.Minute)).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ontology_maintenance_sources(team_id,shared_space_id,space_generation,source_kind,source_id,source_version,status,last_run_id)
		 VALUES(?::uuid,?::uuid,?,'evidence',?,1,'budget_deferred',?::uuid)`, team.ID, space, generation, fragment, run).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO app_config(key,value) VALUES('ONTOLOGY_MAINTENANCE_ENABLED','true'),('ONTOLOGY_MAINTENANCE_MODEL','fixture-model')
		 ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`).Error
	}))
	return interrupted
}

func ontologyUpgradeSnapshot(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	result := map[string]string{}
	require.NoError(t, storage.NewRLS().WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		for _, table := range []string{"entity_records", "value_records", "relationship_records", "evidence_fragments", "relationship_evidence_supports", "ontology_maintenance_windows", "ontology_maintenance_attempts", "ontology_assessments"} {
			var value string
			if err := tx.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(row)-'estimated_input_tokens'-'max_output_tokens' ORDER BY to_jsonb(row)::text)::text,'[]') FROM ` + table + ` AS row`).Row().Scan(&value); err != nil {
				return err
			}
			result[table] = value
		}
		return nil
	}))
	return result
}
