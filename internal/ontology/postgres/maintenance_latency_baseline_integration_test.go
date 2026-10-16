//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

//go:embed testdata/maintenance_write_latency_baseline.json
var maintenanceLatencyBaselineJSON []byte

func copyMaintenanceLatencyMigrations(t *testing.T, source string, through int64) (string, string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(source, "*", "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	root := t.TempDir()
	digest := sha256.New()
	for _, file := range files {
		version, err := strconv.ParseInt(strings.SplitN(filepath.Base(file), "_", 2)[0], 10, 64)
		require.NoError(t, err)
		if version > through {
			continue
		}
		relative, err := filepath.Rel(source, file)
		require.NoError(t, err)
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		fmt.Fprintf(digest, "%s\x00%s\x00", filepath.ToSlash(relative), body)
		destination := filepath.Join(root, "migrations", "postgres", relative)
		require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0755))
		require.NoError(t, os.WriteFile(destination, body, 0600))
	}
	return root, fmt.Sprintf("sha256:%x", digest.Sum(nil))
}

func newMaintenanceLatencyFixture(t *testing.T) *ontologyFixture {
	t.Helper()
	var baseline struct {
		ReferenceCommit        string `json:"reference_commit"`
		MigrationVersion       int64  `json:"migration_version"`
		MigrationSourcesSHA256 string `json:"migration_sources_sha256"`
	}
	require.NoError(t, json.Unmarshal(maintenanceLatencyBaselineJSON, &baseline))
	root, digest := copyMaintenanceLatencyMigrations(t, storage.MigrationsDir(), baseline.MigrationVersion)
	require.Equal(t, baseline.MigrationSourcesSHA256, digest, "the accepted reference cannot change without explicit approval")
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Chdir(root)
	f := newOntologyFixture(t)
	t.Chdir(cwd)
	var version int64
	require.NoError(t, f.admin.Raw(`SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version).Error)
	require.Equal(t, baseline.MigrationVersion, version)
	t.Logf("accepted write-latency reference commit=%s migration=%d sources=%s", baseline.ReferenceCommit, version, digest)
	return f
}

func TestOntologyMaintenanceWriteLatencyBaseline(t *testing.T) {
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		t.Skip("set DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 for disposable ontology PostgreSQL cases")
	}
	candidate, _ := copyMaintenanceLatencyMigrations(t, storage.MigrationsDir(), 99999999999999)
	probe := filepath.Join(candidate, "migrations", "postgres", "v2_6", "99990101000000_latency_baseline_probe.sql")
	require.NoError(t, os.WriteFile(probe, []byte(`-- +goose Up
ALTER TABLE entity_records ADD COLUMN latency_baseline_probe BOOLEAN NOT NULL DEFAULT true;
-- +goose Down
ALTER TABLE entity_records DROP COLUMN latency_baseline_probe;
`), 0600))
	t.Chdir(candidate)
	f := newMaintenanceLatencyFixture(t)
	var columns int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='entity_records' AND column_name='latency_baseline_probe'`).Scan(&columns).Error)
	require.Zero(t, columns, "a later candidate migration must not enter the accepted reference")
	source := maintenanceEntity(t, f, "Accepted latency reference")
	read := func() int64 {
		var version int64
		require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT version FROM community_topic_versions WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND dependency_key=?`, f.team, f.space, f.generation, "entity:"+source.ID).Row().Scan(&version)
		}))
		return version
	}
	before := read()
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE entity_records SET version=version+1 WHERE team_id=?::uuid AND entity_id=?::uuid`, f.team, source.ID).Error
	}))
	after := read()
	require.Equal(t, before+1, after, "the accepted reference must include community counter write overhead")
	migrator, err := storage.NewMigrator(f.admin)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(context.Background()))
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='entity_records' AND column_name='latency_baseline_probe'`).Scan(&columns).Error)
	require.Equal(t, 1, columns, "the measured candidate must include later migrations")
	var preserved bool
	require.NoError(t, f.admin.Raw(`SELECT latency_baseline_probe FROM entity_records WHERE entity_id=?::uuid`, source.ID).Row().Scan(&preserved))
	require.True(t, preserved)
	require.Equal(t, after, read())
}
