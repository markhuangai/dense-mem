//go:build integration

package migrationapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"gorm.io/gorm"
)

func BenchmarkEvidenceOnlyMigrationProgressOverhead(b *testing.B) {
	baselinePath := os.Getenv("DENSE_MEM_MIGRATION_BASELINE_SQL")
	require.NotEmpty(b, baselinePath, "provide the approved baseline SQL under ignored tmp/issue-749")
	baseline, err := os.ReadFile(baselinePath)
	require.NoError(b, err)
	digest := sha256.Sum256(baseline)
	require.Equal(b, "269a4d203f7568d06818c96817883db7aa1fe722207dcb32d2547294ada4b104", hex.EncodeToString(digest[:]))
	candidate, err := os.ReadFile(filepath.Join(postgres.MigrationsDir(), "v2_8", "20261110010000_evidence_only_memory.sql"))
	require.NoError(b, err)
	template, dsn := openMigrationProgressDB(b, testcontainers.WithHostConfigModifier(func(host *container.HostConfig) { host.NanoCPUs = 1_000_000_000; host.Memory = 4 << 30 }))
	migrator, err := postgres.NewMigrator(template)
	require.NoError(b, err)
	require.NoError(b, migrator.PrepareEvidenceOnlyMigration(context.Background()))
	seedProgressMigration(b, template, 20000, 750000)
	fingerprint := progressEvidenceFingerprint(b, template)
	var volumes []map[string]any
	for _, table := range []string{"ontology_record_heads", "ontology_record_revisions", "operation_logs"} {
		var count, bytes int64
		require.NoError(b, postgres.NewRLS().WithSystemReadOnlyRepeatableTx(context.Background(), template, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT count(*),pg_total_relation_size(?::regclass) FROM `+table, table).Row().Scan(&count, &bytes)
		}))
		volumes = append(volumes, map[string]any{"table": table, "rows": count, "relation_bytes": bytes})
	}
	sqlTemplate, err := template.DB()
	require.NoError(b, err)
	require.NoError(b, sqlTemplate.Close())
	address, err := url.Parse(dsn)
	require.NoError(b, err)
	address.Path = "/postgres"
	admin, err := postgres.Open(context.Background(), migrationProgressTestConfig{address.String()})
	require.NoError(b, err)
	sqlAdmin, err := admin.DB()
	require.NoError(b, err)
	defer sqlAdmin.Close()
	var results []map[string]any
	b.StopTimer()
	for pair := 0; pair < 3; pair++ {
		for order := 0; order < 2; order++ {
			instrumented := (order+pair)%2 == 1
			label := "baseline"
			source := baseline
			if instrumented {
				label = "instrumented"
				source = candidate
			}
			name := fmt.Sprintf("progress_bench_%d_%d", pair, order)
			require.NoError(b, admin.Exec(`CREATE DATABASE `+name+` TEMPLATE testdb`).Error)
			address.Path = "/" + name
			db, err := postgres.Open(context.Background(), migrationProgressTestConfig{address.String()})
			require.NoError(b, err)
			sqlDB, err := db.DB()
			require.NoError(b, err)
			// Each clone contains the same IDs, timestamps, payloads and migration history as the closed template.
			func() {
				restore := copyProgressMigration(b, func(string) string { return string(source) })
				defer restore()
				logs := newProgressLog(b)
				started := time.Now()
				require.NoError(b, RunUp(context.Background(), db, 0, logs.logger))
				elapsed := time.Since(started)
				require.Equal(b, fingerprint, progressEvidenceFingerprint(b, db))
				body, err := os.ReadFile(logs.file.Name())
				require.NoError(b, err)
				result := map[string]any{"pair": pair + 1, "variant": label, "elapsed_ms": elapsed.Milliseconds(), "log_bytes": len(body), "fingerprint_preserved": true}
				phases := map[string]any{}
				for _, event := range logs.records(b) {
					if event["msg"] == "evidence_only_migration_completed" {
						result["sql_notices"] = event["sql_notices"]
						result["collection_queries"] = event["collection_queries"]
						result["capture_state"] = event["capture_state"]
					}
					if event["msg"] == "evidence_only_migration_progress" && event["state"] == "completed" {
						phases[fmt.Sprint(event["phase"], "/", event["work_unit"])] = event["phase_elapsed_ms"]
					}
				}
				result["phase_durations_ms"] = phases
				results = append(results, result)
				encoded, err := json.Marshal(result)
				require.NoError(b, err)
				b.Log(string(encoded))
			}()
			require.NoError(b, sqlDB.Close())
			require.NoError(b, admin.Exec(`DROP DATABASE `+name).Error)
		}
	}
	report := map[string]any{"baseline_sql_sha256": hex.EncodeToString(digest[:]), "cpus": 1, "memory_bytes": 4 << 30, "volumes": volumes, "results": results, "limitations": "Synthetic same-data comparison; original hotfix SQL and instrumented SQL run with the same unchanged data logic in the candidate binary. Not a production ETA or retrieval-quality comparison."}
	reportPath := os.Getenv("DENSE_MEM_MIGRATION_OVERHEAD_REPORT")
	require.NotEmpty(b, reportPath)
	encoded, err := json.MarshalIndent(report, "", "  ")
	require.NoError(b, err)
	require.NoError(b, os.WriteFile(reportPath, encoded, 0600))
	b.ReportMetric(float64(len(results)), "paired-runs")
}
