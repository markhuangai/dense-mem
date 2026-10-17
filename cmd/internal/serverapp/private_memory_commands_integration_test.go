//go:build integration

package serverapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	privacy "github.com/markhuangai/dense-mem/internal/privacy/contract"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func privateCommandDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	withLifecyclePostgres(t, func(t *testing.T) {})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	configureLifecycleEnvironment(t, listener.Addr().String())
	db, err := gorm.Open(gormpostgres.Open(os.Getenv("POSTGRES_DSN")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func TestPrivateMemoryCommandsInspectFreshDatabaseWithoutStartingOrMigrating(t *testing.T) {
	db := privateCommandDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var output bytes.Buffer
	require.NoError(t, RunPrivateMemoryPreflight(ctx, &output))
	var report privacy.PrivateMemoryMigrationReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.Equal(t, privacy.PrivateMemoryPlannerVersion, report.PlannerVersion)
	require.Empty(t, report.Blockers)
	require.True(t, strings.HasPrefix(report.SourceFingerprint, "sha256:"))
	var count int
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_tables WHERE schemaname='public'`).Row().Scan(&count))
	require.Zero(t, count, "preflight must not install even migration metadata")
	for _, args := range [][]string{{"unexpected"}, {"--source-fingerprint", "invalid"}, {"--maintenance-confirmed", "--source-fingerprint", report.SourceFingerprint, "unexpected"}} {
		require.Error(t, RunPrivateMemoryMigration(ctx, args, &bytes.Buffer{}))
	}
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_tables WHERE schemaname='public'`).Row().Scan(&count))
	require.Zero(t, count, "invalid maintenance requests must not prepare schema")
	output.Reset()
	require.NoError(t, RunPrivateMemoryMigration(ctx, nil, &output))
	require.JSONEq(t, `{"planner_version":"private-memory.v2.7.0.1","status":"complete"}`, output.String())
	output.Reset()
	require.NoError(t, RunPrivateMemoryPreflight(ctx, &output))
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.Equal(t, "complete", report.Status)
	output.Reset()
	require.NoError(t, RunPrivateMemoryMigration(ctx, []string{"--maintenance-confirmed", "--source-fingerprint", report.SourceFingerprint}, &output))
	require.JSONEq(t, `{"planner_version":"private-memory.v2.7.0.1","status":"complete"}`, output.String())
	require.ErrorIs(t, RunPrivateMemoryMigration(ctx, []string{"--source-fingerprint", "sha256:" + strings.Repeat("0", 64)}, &bytes.Buffer{}), privacy.ErrPrivateMemoryMigrationBlocked)
	require.NoError(t, RunPrivateMemoryMigration(ctx, []string{"--source-fingerprint", report.SourceFingerprint}, &bytes.Buffer{}))
}

func TestPrivateMemoryPreflightCommandBoundsLegacyReportAndReturnsBlockers(t *testing.T) {
	db := privateCommandDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	source := storage.MigrationsDir()
	root := t.TempDir()
	files, err := filepath.Glob(filepath.Join(source, "*", "*.sql"))
	require.NoError(t, err)
	for _, file := range files {
		if strings.HasPrefix(filepath.Base(file), "2026101401000") {
			continue
		}
		relative, err := filepath.Rel(source, file)
		require.NoError(t, err)
		destination := filepath.Join(root, "migrations", "postgres", relative)
		require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0755))
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(destination, body, 0600))
	}
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Chdir(root)
	migrator, err := storage.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(ctx))
	t.Chdir(cwd)
	team, owner := uuid.New(), uuid.New()
	var space uuid.UUID
	rls := storage.NewRLS()
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO teams(id,name) VALUES(?,'preflight command')`, team).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO actor_identities(id,kind,team_id,active) VALUES(?,'human',?,true)`, owner, team).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO team_memberships(team_id,actor_identity_id,status,maximum_grants) VALUES(?,?,'active',ARRAY['read','write'])`, team, owner).Error; err != nil {
			return err
		}
		if err := tx.Raw(`INSERT INTO memory_spaces(team_id,kind,owner_profile_id) VALUES(?,'profile_private',?) RETURNING id`, team, owner).Row().Scan(&space); err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO actor_identities(id,kind,team_id,active) SELECT md5('preflight actor:'||n)::uuid,'api_client',?,true FROM generate_series(1,1001) n`, team).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO team_memberships(team_id,actor_identity_id,status,maximum_grants) SELECT ?,md5('preflight actor:'||n)::uuid,'active',ARRAY['read','write'] FROM generate_series(1,1001) n`, team).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ownership_aliases(team_id,legacy_owner_id,canonical_identity_id) SELECT ?,md5('preflight actor:'||n)::uuid,md5('preflight actor:'||n)::uuid FROM generate_series(1,1001) n`, team).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO knowledge_ingests(team_id,owner_profile_id,space_id,request_hash,source_summary,status) SELECT ?,md5('preflight actor:'||n)::uuid,?,'original-'||n,'private preflight content canary','completed' FROM generate_series(1,1001) n`, team, space).Error
	}))
	var output bytes.Buffer
	require.NoError(t, RunPrivateMemoryPreflight(ctx, &output))
	var report privacy.PrivateMemoryMigrationReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.EqualValues(t, 1001, report.MappingCount)
	require.Len(t, report.Mappings, 1000)
	require.True(t, report.MappingsTruncated)
	require.Less(t, output.Len(), 512*1024)
	require.NotContains(t, output.String(), "private preflight content canary")
	var prepared bool
	require.NoError(t, db.Raw(`SELECT to_regclass('public.private_memory_migration_state') IS NOT NULL`).Row().Scan(&prepared))
	require.False(t, prepared)
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO entity_records(team_id,entity_kind,space_id) VALUES(?,'project',?)`, team, space).Error
	}))
	output.Reset()
	require.ErrorIs(t, RunPrivateMemoryPreflight(ctx, &output), privacy.ErrPrivateMemoryMigrationBlocked)
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.NotEmpty(t, report.Blockers)
	require.NotContains(t, output.String(), "private preflight content canary")
	require.NoError(t, db.Raw(`SELECT to_regclass('public.private_memory_migration_state') IS NOT NULL`).Row().Scan(&prepared))
	require.False(t, prepared, "blocked inspection must remain read-only")
}
