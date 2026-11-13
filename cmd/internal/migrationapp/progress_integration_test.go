//go:build integration

package migrationapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type progressLog struct {
	mu     sync.Mutex
	file   *os.File
	logger *slog.Logger
}

func newProgressLog(t testing.TB) *progressLog {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "progress.jsonl"))
	require.NoError(t, err)
	original := os.Stdout
	os.Stdout = file
	logger := observability.NewWithProtector(slog.LevelInfo, observability.NewCredentialProtector("progress-credential-canary"))
	os.Stdout = original
	logger = logger.With(observability.String("diagnostic_canary", "progress-credential-canary")).(*observability.Logger)
	t.Cleanup(func() { _ = file.Close() })
	return &progressLog{file: file, logger: logger.Slog()}
}
func (l *progressLog) records(t testing.TB) []map[string]any {
	t.Helper()
	body, err := os.ReadFile(l.file.Name())
	require.NoError(t, err)
	require.NotContains(t, string(body), "progress-credential-canary")
	require.NotContains(t, string(body), "Exact progress Evidence. café 🧭")
	var result []map[string]any
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		result = append(result, event)
	}
	return result
}
func progressHas(t testing.TB, l *progressLog, message, phase string) bool {
	for _, event := range l.records(t) {
		if event["msg"] == message && (phase == "" || event["phase"] == phase) {
			return true
		}
	}
	return false
}

func copyProgressMigration(t testing.TB, edit func(string) string) func() {
	t.Helper()
	migrationWorkingDirectoryMu.Lock()
	source := postgres.MigrationsDir()
	root := t.TempDir()
	err := filepath.WalkDir(source, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(source, file)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if filepath.Base(file) == "20261110010000_evidence_only_memory.sql" {
			body = []byte(edit(string(body)))
		}
		target := filepath.Join(root, "migrations", "postgres", relative)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, body, 0600)
	})
	require.NoError(t, err)
	original, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	var once sync.Once
	restore := func() {
		once.Do(func() { require.NoError(t, os.Chdir(original)); migrationWorkingDirectoryMu.Unlock() })
	}
	t.Cleanup(restore)
	return restore
}

func seedProgressMigration(t testing.TB, db *gorm.DB, recordCount, logCount int) {
	t.Helper()
	ctx := context.Background()
	rls := postgres.NewRLS()
	team := &domain.Team{Name: "progress-fixture"}
	require.NoError(t, access.NewTeamRepository(db, rls).Create(ctx, team))
	owner := &domain.Credential{ID: uuid.New(), TeamID: team.ID, Name: "progress owner", KeyHash: "synthetic", KeyPrefix: uuid.NewString()[:20], KeySuffix: "test", Scopes: []string{"read", "write"}}
	require.NoError(t, access.NewCredentialRepository(db, rls, nil).CreateCredential(ctx, owner))
	var shared string
	var generation int64
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`SELECT id::text,generation,kind FROM memory_spaces WHERE team_id=?::uuid ORDER BY kind`, team.ID).Rows()
		if err != nil {
			return err
		}
		type space struct {
			id   string
			gen  int64
			kind string
		}
		var spaces []space
		for rows.Next() {
			var s space
			if err := rows.Scan(&s.id, &s.gen, &s.kind); err != nil {
				return err
			}
			spaces = append(spaces, s)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, s := range spaces {
			if s.kind == "team_shared" {
				shared = s.id
				generation = s.gen
			}
		}
		return nil
	}))
	require.NotEmpty(t, shared)
	var private string
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id::text FROM memory_spaces WHERE team_id=?::uuid AND kind='private' LIMIT 1`, team.ID).Row().Scan(&private)
	}))
	for i, space := range []string{shared, private} {
		_, err := knowledgepostgres.NewStore(db, rls).CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: team.ID.String(), OwnerProfileID: owner.ID.String(), SpaceID: space, SpaceGeneration: generation, IdempotencyKey: fmt.Sprintf("progress-%d", i), RequestHash: "original", Evidence: []knowledge.EvidenceInput{{Content: "Exact progress Evidence. café 🧭"}}})
		require.NoError(t, err)
	}
	publication := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO ontology_catalog_heads(team_id,shared_space_id,space_generation,revision) VALUES(?::uuid,?::uuid,?,1)`, team.ID, shared, generation).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ontology_publications(team_id,shared_space_id,space_generation,publication_id,revision,operation_key,request_hash,origin,reason,result) VALUES(?::uuid,?::uuid,?,?::uuid,1,'progress',?,'automatic','progress fixture','{}')`, team.ID, shared, generation, publication, "sha256:"+strings.Repeat("a", 64)).Error; err != nil {
			return err
		}
		for offset := 0; offset < recordCount; offset += 500 {
			var records []ontology.Record
			for i := offset; i < recordCount && i < offset+500; i++ {
				record := ontology.Record{ID: uuid.NewString(), Version: 1, Kind: ontology.Topic, Definition: &ontology.Definition{Key: fmt.Sprintf("topic_%d", i), Label: fmt.Sprintf("Topic %d", i), Description: "synthetic volume"}}
				var err error
				record.Fingerprint, err = ontology.RecordFingerprintWithAcceptedSources(record, nil)
				if err != nil {
					return err
				}
				records = append(records, record)
			}
			data, err := json.Marshal(records)
			if err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version,kind,publication_id,retired,body,fingerprint)
    SELECT ?::uuid,?::uuid,?,(body->>'id')::uuid,1,'topic',?::uuid,false,body,body->>'fingerprint' FROM jsonb_array_elements(?::jsonb) body`, team.ID, shared, generation, publication, string(data)).Error; err != nil {
				return err
			}
		}
		root, child, grandchild := uuid.NewString(), uuid.NewString(), uuid.NewString()
		for i, id := range []string{root, child, grandchild} {
			kind, body := "entity_class", `{"kind":"entity_class","version":1}`
			if i > 0 {
				kind = "override"
				target := root
				if i == 2 {
					target = child
				}
				body = fmt.Sprintf(`{"id":%q,"kind":"override","version":1,"override":{"action":"set_classification","target_id":%q}}`, id, target)
			}
			if err := tx.Exec(`INSERT INTO ontology_record_revisions(team_id,shared_space_id,space_generation,record_id,version,kind,publication_id,retired,body,fingerprint) VALUES(?::uuid,?::uuid,?,?::uuid,1,?,?::uuid,false,?::jsonb,?)`, team.ID, shared, generation, id, kind, publication, body, "sha256:"+strings.Repeat("b", 64)).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`INSERT INTO ontology_record_heads(team_id,shared_space_id,space_generation,record_id,version,kind,retired) SELECT team_id,shared_space_id,space_generation,record_id,version,kind,retired FROM ontology_record_revisions WHERE team_id=?::uuid`, team.ID).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO operation_logs(severity,severity_rank,message,attrs) SELECT 'INFO',0,'synthetic migration volume',jsonb_build_object('component',CASE WHEN value%2=0 THEN 'graph' ELSE 'http' END,'padding',repeat('x',256)) FROM generate_series(1,?) value`, logCount).Error
	}))
}

func progressEvidenceFingerprint(t testing.TB, db *gorm.DB) string {
	t.Helper()
	digest := sha256.New()
	require.NoError(t, postgres.NewRLS().WithSystemReadOnlyRepeatableTx(context.Background(), db, func(tx *gorm.DB) error {
		for _, table := range strings.Fields("evidence_sources evidence_source_revisions evidence_exact_aliases evidence_occurrences evidence_fragments evidence_security_events evidence_security_signals evidence_quarantines evidence_lifecycle_operations evidence_lifecycle_events evidence_conflict_cases evidence_conflict_positions evidence_conflict_events") {
			var body string
			if err := tx.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY to_jsonb(row_value)::text)::text,'[]') FROM ` + table + ` row_value`).Row().Scan(&body); err != nil {
				return err
			}
			_, _ = digest.Write([]byte(table + "\n" + body + "\n"))
		}
		return nil
	}))
	return hex.EncodeToString(digest.Sum(nil))
}

func TestEvidenceOnlyPostgresProgressLiveSQL(t *testing.T) {
	db := openMigrationIntegrationDB(t)
	migrator, err := postgres.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.PrepareEvidenceOnlyMigration(context.Background()))
	seedProgressMigration(t, db, 100, 1000)
	before := progressEvidenceFingerprint(t, db)
	copyProgressMigration(t, func(s string) string {
		return strings.Replace(s, " EXECUTE 'TRUNCATE TABLE '", " PERFORM pg_advisory_xact_lock(749,1);\n EXECUTE 'TRUNCATE TABLE '", 1)
	})
	conn, err := db.DB()
	require.NoError(t, err)
	blocker, err := conn.Conn(context.Background())
	require.NoError(t, err)
	defer blocker.Close()
	_, err = blocker.ExecContext(context.Background(), `SELECT pg_advisory_lock(749,1)`)
	require.NoError(t, err)
	defer blocker.ExecContext(context.Background(), `SELECT pg_advisory_unlock(749,1)`)
	logs := newProgressLog(t)
	done := make(chan error, 1)
	go func() { done <- RunUp(context.Background(), db, time.Minute, logs.logger) }()
	require.Eventually(t, func() bool {
		waits := 0
		var last any
		for _, event := range logs.records(t) {
			if event["msg"] == "evidence_only_migration_wait" && event["wait_event_type"] == "Lock" && event["phase"] == "ontology_reset" {
				if waits > 0 {
					require.Equal(t, last, event["last_progress_at"])
				}
				last = event["last_progress_at"]
				waits++
			}
		}
		return waits >= 2
	}, 20*time.Second, 20*time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("batch returned before live observations: %v", err)
	default:
	}
	counted := 0
	for _, event := range logs.records(t) {
		if event["phase"] == "inventory_count" && event["state"] == "completed" && event["completed"].(float64) > 0 {
			counted++
		}
	}
	require.Greater(t, counted, 2)
	_, err = blocker.ExecContext(context.Background(), `SELECT pg_advisory_unlock(749,1)`)
	require.NoError(t, err)
	require.NoError(t, <-done)
	require.Equal(t, before, progressEvidenceFingerprint(t, db))
	resetCompleted := false
	for _, event := range logs.records(t) {
		if event["phase"] == "ontology_reset" {
			require.Equal(t, "ontology_catalog_heads", event["work_unit"])
			require.Equal(t, "exact", event["total_kind"])
			require.Greater(t, event["total"].(float64), float64(200))
			if event["state"] == "completed" {
				resetCompleted = true
				require.Equal(t, event["total"], event["completed"])
				require.Zero(t, event["remaining"])
			}
		}
		require.NotContains(t, []string{"dependency_closure", "fingerprint_load", "fingerprint_rebind"}, event["phase"])
	}
	require.True(t, resetCompleted)

	positions := map[string]int{}
	for index, event := range logs.records(t) {
		if event["msg"] != "evidence_only_migration_progress" {
			continue
		}
		phase := event["phase"].(string)
		if _, ok := positions[phase]; !ok {
			positions[phase] = index
		}
		require.NotEmpty(t, event["phase_started_at"])
		require.GreaterOrEqual(t, event["phase_elapsed_ms"].(float64), float64(0))
		require.Equal(t, "transaction_local", event["work_durability"])
	}
	previous := -1
	for _, phase := range []string{"legal_hold_statistics", "guards", "payload_cleanup", "session_cleanup", "ontology_reset", "ontology_cleanup", "table_retirement", "guard_restoration", "fingerprint_after", "validation", "receipt", "policy_restoration"} {
		position, ok := positions[phase]
		require.True(t, ok, phase)
		require.Greater(t, position, previous, phase)
		previous = position
	}
	require.True(t, progressHas(t, logs, "evidence_only_migration_completed", ""))
	require.NoError(t, RunUp(context.Background(), db, time.Minute, logs.logger))
	require.True(t, progressHas(t, logs, "evidence_only_migration_skipped", ""))
	var last map[string]any
	for _, event := range logs.records(t) {
		if event["msg"] == "evidence_only_migration_completed" {
			last = event
		}
	}
	require.False(t, last["attempted"].(bool))
	require.False(t, last["committed_work"].(bool))
	require.True(t, last["previous_commit_verified"].(bool))
}

func TestEvidenceOnlyPostgresProgressWaitCancellation(t *testing.T) {
	t.Run("malformed_then_wait_unavailable", testProgressCombinedCaptureFailure)
	for _, mode := range []string{"advisory", "table", "non_lock", "disconnect", "missing_capture", "malformed", "oversized", "gap"} {
		t.Run(mode, func(t *testing.T) {
			db, dsn := openMigrationProgressDB(t)
			migrator, err := postgres.NewMigrator(db)
			require.NoError(t, err)
			require.NoError(t, migrator.PrepareEvidenceOnlyMigration(context.Background()))
			seedProgressMigration(t, db, 10, 10)
			before := progressEvidenceFingerprint(t, db)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			blocker, err := sqlDB.Conn(context.Background())
			require.NoError(t, err)
			defer blocker.Close()
			logs := newProgressLog(t)
			if mode == "missing_capture" || mode == "malformed" || mode == "oversized" || mode == "gap" {
				copyProgressMigration(t, func(s string) string {
					injection := "SET LOCAL client_min_messages='warning';"
					if mode == "malformed" {
						injection = `DO $fault$ BEGIN RAISE NOTICE 'dense_mem_migration_progress:%:{"body":"Exact progress Evidence. café 🧭 progress-credential-canary"}',current_setting('app.migration_execution_id'); END $fault$;`
					}
					if mode == "oversized" {
						injection = `DO $fault$ BEGIN RAISE NOTICE 'dense_mem_migration_progress:%:%',current_setting('app.migration_execution_id'),repeat('Exact progress Evidence. café 🧭 progress-credential-canary',100); END $fault$;`
					}
					if mode == "gap" {
						injection = `UPDATE evidence_only_progress_state SET sequence=sequence+1;`
					}
					return strings.Replace(s, "DO $ontology_reset$", injection+"\nDO $ontology_reset$", 1)
				})
				require.NoError(t, RunUp(context.Background(), db, time.Minute, logs.logger))
				require.True(t, progressHas(t, logs, "evidence_only_migration_capture_incomplete", ""))
				require.Equal(t, before, progressEvidenceFingerprint(t, db))
				expected := map[string]string{"missing_capture": "sql_stream_interrupted_or_unavailable", "malformed": "invalid_notice", "oversized": "notice_size_limit", "gap": "notice_sequence_gap"}[mode]
				found := false
				for _, event := range logs.records(t) {
					if event["msg"] == "evidence_only_migration_completed" {
						require.Equal(t, "committed", event["transaction_outcome"])
						require.Equal(t, "incomplete", event["capture_state"])
						require.Equal(t, expected, event["capture_reason"])
						found = true
					}
				}
				require.True(t, found)
				count := len(logs.records(t))
				time.Sleep(1100 * time.Millisecond)
				require.Len(t, logs.records(t), count)
				return
			}
			if mode == "non_lock" {
				copyProgressMigration(t, func(s string) string {
					return strings.Replace(s, " EXECUTE 'TRUNCATE TABLE '", " PERFORM pg_sleep(30);\n EXECUTE 'TRUNCATE TABLE '", 1)
				})
			} else if mode == "advisory" {
				_, err = blocker.ExecContext(context.Background(), `SELECT pg_advisory_lock(4097083626)`)
				defer blocker.ExecContext(context.Background(), `SELECT pg_advisory_unlock(4097083626)`)
			} else {
				_, err = blocker.ExecContext(context.Background(), `BEGIN; LOCK TABLE v2_compatibility_markers IN ACCESS EXCLUSIVE MODE`)
				defer blocker.ExecContext(context.Background(), `ROLLBACK`)
			}
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- RunUp(ctx, db, time.Minute, logs.logger) }()
			require.Eventually(t, func() bool {
				for _, event := range logs.records(t) {
					if event["msg"] != "evidence_only_migration_wait" {
						continue
					}
					if mode == "non_lock" && event["wait_event_type"] == "Timeout" && event["wait_event"] == "PgSleep" {
						require.Zero(t, event["blocker_count"])
						require.Equal(t, "ontology_reset", event["phase"])
						return true
					}
					if mode != "non_lock" && event["wait_event_type"] == "Lock" {
						return true
					}
				}
				return false
			}, 20*time.Second, 20*time.Millisecond)
			if mode == "disconnect" {
				var pid int
				require.NoError(t, db.Raw(`SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND pid<>pg_backend_pid() LIMIT 1`).Row().Scan(&pid))
				go func() { _ = sqlDB.Close() }()
				require.Eventually(t, func() bool { return sqlDB.PingContext(context.Background()) != nil }, time.Second, 10*time.Millisecond)
				_, err = blocker.ExecContext(context.Background(), `SELECT pg_terminate_backend($1)`, pid)
				require.NoError(t, err)
			} else {
				cancel()
				if mode == "table" {
					_, err = blocker.ExecContext(context.Background(), `ROLLBACK`)
					require.NoError(t, err)
				}
			}
			require.Error(t, <-done)
			if mode == "disconnect" {
				db, err = postgres.Open(context.Background(), migrationProgressTestConfig{dsn})
				require.NoError(t, err)
				fresh, err := db.DB()
				require.NoError(t, err)
				defer fresh.Close()
				found := false
				for _, event := range logs.records(t) {
					if event["msg"] == "evidence_only_migration_failed" {
						require.Equal(t, "unconfirmed", event["transaction_outcome"])
						require.Equal(t, "database connection closed", event["outcome_verification_error"])
						found = true
					}
				}
				require.True(t, found)
			}
			require.NoError(t, db.Exec(`SELECT 1`).Error)
			require.Equal(t, before, progressEvidenceFingerprint(t, db))
			require.False(t, progressHas(t, logs, "evidence_only_migration_completed", ""))
			if mode != "advisory" {
				require.True(t, progressHas(t, logs, "evidence_only_migration_capture_incomplete", ""))
			}
			count := len(logs.records(t))
			time.Sleep(1100 * time.Millisecond)
			require.Len(t, logs.records(t), count)
		})
	}
}

func TestEvidenceOnlyPostgresProgressConcurrentStarters(t *testing.T) {
	db := openMigrationIntegrationDB(t)
	migrator, err := postgres.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.PrepareEvidenceOnlyMigration(context.Background()))
	seedProgressMigration(t, db, 100, 100)
	before := progressEvidenceFingerprint(t, db)
	logs := newProgressLog(t)
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- RunUp(context.Background(), db, time.Minute, logs.logger) }()
	}
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	executions := map[string]bool{}
	newlyCommitted := 0
	for _, event := range logs.records(t) {
		if event["msg"] == "evidence_only_migration_completed" {
			require.Equal(t, "committed", event["transaction_outcome"])
			executions[event["migration_execution_id"].(string)] = true
			if event["committed_work"].(bool) {
				newlyCommitted++
				require.True(t, event["attempted"].(bool))
			} else {
				require.False(t, event["attempted"].(bool))
				require.True(t, event["previous_commit_verified"].(bool))
			}
		}
	}
	require.Len(t, executions, 2)
	require.Equal(t, 1, newlyCommitted)
	require.Equal(t, before, progressEvidenceFingerprint(t, db))
}
