//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFeatureStoragePostgresFreshAndPopulated(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		db, cleanup := openMigrationSQLDB(t, context.Background())
		defer cleanup()
		require.NoError(t, NewMigratorWithDB(db).RunUp(context.Background()))
		require.NoError(t, ValidateStartupMigrationState(context.Background(), db, MigrationsDir()))
	})
	t.Run("populated", func(t *testing.T) {
		db, _ := featureStorageFixture(t, 210)
		evidence := featureStorageSnapshot(t, db, "evidence_fragments")
		revisions := featureStorageSnapshot(t, db, "ontology_record_revisions")
		drafts := featureStorageSnapshot(t, db, "ontology_draft_revisions")
		started := time.Now()
		require.NoError(t, NewMigratorWithDB(db).RunUp(context.Background()))
		t.Logf("feature_storage_fixture_migration rows=210 duration_ms=%d", time.Since(started).Milliseconds())
		require.Equal(t, evidence, featureStorageSnapshot(t, db, "evidence_fragments"))
		require.JSONEq(t, revisions, featureStorageSnapshot(t, db, "ontology_revision_records"))
		require.JSONEq(t, drafts, featureStorageSnapshot(t, db, "ontology_draft_revision_records"))
		var payloads, edits int
		require.NoError(t, execPostgresTxMode(context.Background(), db, "system", func(tx *sql.Tx) error {
			if err := tx.QueryRow(`SELECT count(*) FROM ontology_topic_revision_payloads`).Scan(&payloads); err != nil {
				return err
			}
			return tx.QueryRow(`SELECT count(*) FROM ontology_override_draft_payloads WHERE container='edit'`).Scan(&edits)
		}))
		require.Equal(t, 210, payloads)
		require.Equal(t, 1, edits)
		require.NoError(t, ValidateStartupMigrationState(context.Background(), db, MigrationsDir()))
	})
}

func TestFeatureStoragePostgresSingleConnectionPool(t *testing.T) {
	db, _ := featureStorageFixture(t, 3)
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, NewMigratorWithDB(db).RunUp(ctx))
	require.Equal(t, 1, db.Stats().MaxOpenConnections)
	require.NoError(t, ValidateStartupMigrationState(ctx, db, MigrationsDir()))
}

func TestFeatureStoragePostgresLargeRetainedRows(t *testing.T) {
	for _, variant := range []string{"resume", "changed_prefix", "changed_suffix"} {
		t.Run(variant, func(t *testing.T) {
			count := 3
			if variant == "changed_suffix" {
				count = 210
			}
			db, fixture := featureStorageFixture(t, count)
			invocation := uuid.NewString()
			require.NoError(t, execPostgresTxMode(context.Background(), db, "system", func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO remember_invocation_diagnostics(team_id,invocation_id,owner_profile_id,provider_exchanges,expires_at)
				 SELECT $1::uuid,$2::uuid,$3::uuid,jsonb_agg(jsonb_build_object('sequence_no',n,'kind','provider_exchange','component','provider',
				 'response_body',repeat('🧭',(63*1024*1024)/16),'outcome','captured','capture_state','captured') ORDER BY n),now()+interval '1 day'
				 FROM generate_series(1,4) n`, fixture.team, invocation, fixture.owner)
				return err
			}))
			historyHash := func() string {
				t.Helper()
				var fingerprint string
				require.NoError(t, execPostgresTxMode(context.Background(), db, "system", func(tx *sql.Tx) error {
					return tx.QueryRow(`SELECT encode(sha256(convert_to(to_jsonb(history)::text,'UTF8')),'hex')
					 FROM remember_invocation_diagnostics history WHERE team_id=$1::uuid AND invocation_id=$2::uuid`, fixture.team, invocation).Scan(&fingerprint)
				}))
				return fingerprint
			}
			before, evidence := historyHash(), featureStorageSnapshot(t, db, "evidence_fragments")
			if variant == "changed_suffix" {
				stopFeatureCopy(t, db, false)
			} else {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				interrupted := false
				ctx, progress := ObserveMigration(ctx, func(s MigrationSnapshot) {
					if s.Phase == "snapshot" && s.WorkUnit == "remember_invocation_diagnostics" && s.State == "batch_committed" && s.DurableBatches == 1 {
						require.Zero(t, s.DurableRows)
						interrupted = true
						cancel()
					}
				})
				defer progress.Close()
				require.Error(t, NewMigratorWithDB(db).RunUp(ctx))
				require.True(t, interrupted)
				var offset, rows int64
				require.NoError(t, execPostgresTxMode(context.Background(), db, "system", func(tx *sql.Tx) error {
					return tx.QueryRow(`SELECT (cursor->>2)::bigint,row_count FROM data_migration_checkpoints
					 WHERE migration_version=$1 AND phase='snapshot' AND work_unit='remember_invocation_diagnostics'`, FeatureStorageDataVersion).Scan(&offset, &rows)
				}))
				require.EqualValues(t, featureStorageBatchBytes, offset)
				require.Zero(t, rows)
			}
			if variant != "resume" {
				require.NoError(t, execPostgresTxMode(context.Background(), db, "migration", func(tx *sql.Tx) error {
					if _, err := tx.Exec(`ALTER TABLE remember_invocation_diagnostics DISABLE TRIGGER remember_invocation_diagnostics_append_only`); err != nil {
						return err
					}
					query := `UPDATE remember_invocation_diagnostics SET request_hash='changed prefix' WHERE team_id=$1::uuid AND invocation_id=$2::uuid`
					if variant == "changed_suffix" {
						query = `UPDATE remember_invocation_diagnostics SET provider_exchanges=jsonb_set(provider_exchanges,'{3,response_body}',to_jsonb('changed suffix'::text))
						 WHERE team_id=$1::uuid AND invocation_id=$2::uuid`
					}
					if _, err := tx.Exec(query, fixture.team, invocation); err != nil {
						return err
					}
					_, err := tx.Exec(`ALTER TABLE remember_invocation_diagnostics ENABLE TRIGGER remember_invocation_diagnostics_append_only`)
					return err
				}))
				require.ErrorContains(t, NewMigratorWithDB(db).RunUp(context.Background()), "feature_storage_source_fingerprint_mismatch")
			} else {
				require.NoError(t, NewMigratorWithDB(db).RunUp(context.Background()))
				require.Equal(t, before, historyHash())
				var complete int
				require.NoError(t, execPostgresTxMode(context.Background(), db, "system", func(tx *sql.Tx) error {
					return tx.QueryRow(`SELECT count(*) FROM data_migration_checkpoints WHERE migration_version=$1
					 AND phase IN ('snapshot','verify') AND work_unit='remember_invocation_diagnostics' AND row_count=1 AND complete`, FeatureStorageDataVersion).Scan(&complete)
				}))
				require.Equal(t, 2, complete)
			}
			require.Equal(t, evidence, featureStorageSnapshot(t, db, "evidence_fragments"))
		})
	}
}

func TestFeatureStoragePostgresOverlappingSingleConnectionStarters(t *testing.T) {
	for _, preparation := range []bool{false, true} {
		name := "run_up"
		if preparation {
			name = "preparation"
		}
		t.Run(name, func(t *testing.T) {
			db, _ := featureStorageFixture(t, 3)
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			acquired, allowExecutor := make(chan struct{}, 1), make(chan struct{})
			defer func() {
				select {
				case <-allowExecutor:
				default:
					close(allowExecutor)
				}
			}()
			firstCtx, firstProgress := ObserveMigration(ctx, func(s MigrationSnapshot) {
				if s.Phase == "migration_lock" && s.State == "completed" {
					acquired <- struct{}{}
					select {
					case <-allowExecutor:
					case <-ctx.Done():
					}
				}
			})
			defer firstProgress.Close()
			first := make(chan error, 1)
			go func() { first <- NewMigratorWithDB(db).RunUp(firstCtx) }()
			select {
			case <-acquired:
			case <-ctx.Done():
				t.Fatal("first starter did not acquire Goose ownership")
			}
			waiting := make(chan struct{}, 1)
			secondCtx, secondProgress := ObserveMigration(ctx, func(s MigrationSnapshot) {
				if s.Phase == "migration_lock" && s.State == "started" {
					waiting <- struct{}{}
				}
			})
			defer secondProgress.Close()
			second := make(chan error, 1)
			go func() {
				m := NewMigratorWithDB(db)
				if preparation {
					second <- m.PrepareEvidenceOnlyMigration(secondCtx)
				} else {
					second <- m.RunUp(secondCtx)
				}
			}()
			select {
			case <-waiting:
			case <-ctx.Done():
				t.Fatal("second starter did not reserve its Goose connection")
			}
			require.Equal(t, 2, db.Stats().InUse)
			close(allowExecutor)
			for _, result := range []<-chan error{first, second} {
				select {
				case err := <-result:
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal("overlapping starters exhausted executor capacity")
				}
			}
			require.Equal(t, 1, db.Stats().MaxOpenConnections)
			require.NoError(t, ValidateStartupMigrationState(ctx, db, MigrationsDir()))
		})
	}
}

func TestFeatureStoragePostgresRestartAndWriterFence(t *testing.T) {
	db, _ := featureStorageFixture(t, 210)
	before := featureStorageSnapshot(t, db, "ontology_record_revisions")
	stopFeatureCopy(t, db, false)
	require.Error(t, ValidateStartupMigrationState(context.Background(), db, MigrationsDir()))
	write := func(layout string) error {
		return execPostgresTxMode(context.Background(), db, "system", func(tx *sql.Tx) error {
			if _, err := tx.Exec(`SELECT set_config('app.storage_layout',$1,true)`, layout); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO operation_logs(severity,severity_rank,message,source,attrs) VALUES('INFO',20,'writer fixture','test','{}')`)
			return err
		})
	}
	require.ErrorContains(t, write(FeatureStorageLayout), "incompatible writer")
	require.NoError(t, NewMigratorWithDB(db).RunUp(context.Background()))
	require.JSONEq(t, before, featureStorageSnapshot(t, db, "ontology_revision_records"))
	require.ErrorContains(t, write(""), "incompatible writer")
	require.NoError(t, write(FeatureStorageLayout))
}

func TestFeatureStoragePostgresRejectsChangedSourcesAndPayloads(t *testing.T) {
	for _, mode := range []string{"source", "destination", "missing", "schema", "policy", "policy_scope"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := featureStorageFixture(t, 210)
			stopFeatureCopy(t, db, false)
			require.NoError(t, execPostgresTxMode(context.Background(), db, "migration", func(tx *sql.Tx) error {
				switch mode {
				case "source":
					_, err := tx.Exec(`ALTER TABLE ontology_record_revisions DISABLE TRIGGER ontology_record_revisions_append_only;
					 UPDATE ontology_record_revisions SET body=jsonb_set(body,'{definition,label}','"changed source"');
					 ALTER TABLE ontology_record_revisions ENABLE TRIGGER ontology_record_revisions_append_only`)
					return err
				case "destination":
					_, err := tx.Exec(`ALTER TABLE ontology_topic_revision_payloads DISABLE TRIGGER feature_append_only;
					 UPDATE ontology_topic_revision_payloads SET body=jsonb_set(body,'{definition,label}','"changed destination"');
					 ALTER TABLE ontology_topic_revision_payloads ENABLE TRIGGER feature_append_only`)
					return err
				case "missing":
					_, err := tx.Exec(`ALTER TABLE ontology_topic_revision_payloads DISABLE TRIGGER feature_append_only;
					 DELETE FROM ontology_topic_revision_payloads;
					 ALTER TABLE ontology_topic_revision_payloads ENABLE TRIGGER feature_append_only`)
					return err
				case "policy":
					_, err := tx.Exec(`DROP POLICY feature_read ON ontology_topic_revision_payloads`)
					return err
				case "policy_scope":
					_, err := tx.Exec(`ALTER POLICY feature_read ON ontology_topic_revision_payloads USING(true)`)
					return err
				default:
					_, err := tx.Exec(`ALTER TABLE ontology_record_revisions ADD COLUMN unexpected_source_column boolean`)
					return err
				}
			}))
			require.Error(t, NewMigratorWithDB(db).RunUp(context.Background()))
			var compatible bool
			require.NoError(t, execPostgresTxMode(context.Background(), db, "system", func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM v2_compatibility_markers WHERE marker_kind='feature_storage')`).Scan(&compatible)
			}))
			require.False(t, compatible)
			var retained bool
			require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='ontology_record_revisions' AND column_name='body')`).Scan(&retained))
			require.True(t, retained)
		})
	}
}

func TestFeatureStoragePostgresActiveBatchInterruption(t *testing.T) {
	for _, termination := range []bool{false, true} {
		name := "cancel"
		if termination {
			name = "connection_loss"
		}
		t.Run(name, func(t *testing.T) {
			db, _ := featureStorageFixture(t, 210)
			before := featureStorageSnapshot(t, db, "ontology_record_revisions")
			require.NoError(t, NewMigratorWithDB(db).PrepareFeatureStorageMigration(context.Background()))
			blocker, err := db.Conn(context.Background())
			require.NoError(t, err)
			defer DiscardAdvisoryLockConnection(blocker)
			_, err = blocker.ExecContext(context.Background(), `SELECT pg_advisory_lock(746,2)`)
			require.NoError(t, err)
			_, err = db.Exec(`CREATE FUNCTION feature_storage_fixture_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			 IF (SELECT count(*) FROM ontology_topic_revision_payloads)>=125 THEN PERFORM pg_advisory_xact_lock(746,2); END IF;
			 RETURN NEW; END $$;
			 CREATE TRIGGER feature_storage_fixture_pause AFTER INSERT ON ontology_topic_revision_payloads FOR EACH ROW EXECUTE FUNCTION feature_storage_fixture_pause()`)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			committed := make(chan int, 1)
			ctx, progress := ObserveMigration(ctx, func(s MigrationSnapshot) {
				if s.Phase == "copy" && s.WorkUnit == "ontology_record_revisions" && s.State == "batch_committed" {
					select {
					case committed <- s.BackendPID:
					default:
					}
				}
			})
			defer progress.Close()
			result := make(chan error, 1)
			go func() { result <- NewMigratorWithDB(db).RunUp(ctx) }()
			var pid int
			select {
			case pid = <-committed:
			case <-time.After(20 * time.Second):
				t.Fatal("copy did not commit its first batch")
			}
			require.Eventually(t, func() bool {
				var waiting bool
				err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND classid=746 AND objid=2 AND NOT granted)`, pid).Scan(&waiting)
				return err == nil && waiting
			}, 20*time.Second, 10*time.Millisecond, "second batch must block after its 25th uncommitted insert")
			if termination {
				_, err = db.Exec(`SELECT pg_terminate_backend($1)`, pid)
				require.NoError(t, err)
			} else {
				cancel()
			}
			select {
			case err := <-result:
				require.Error(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("interrupted active batch did not exit")
			}
			var payloads, checkpoint int64
			require.NoError(t, execPostgresTxMode(context.Background(), db, "system", func(tx *sql.Tx) error {
				if err := tx.QueryRow(`SELECT count(*) FROM ontology_topic_revision_payloads`).Scan(&payloads); err != nil {
					return err
				}
				return tx.QueryRow(`SELECT row_count FROM data_migration_checkpoints WHERE migration_version=$1 AND phase='copy' AND work_unit='ontology_record_revisions'`, FeatureStorageDataVersion).Scan(&checkpoint)
			}))
			require.EqualValues(t, 100, payloads)
			require.EqualValues(t, 100, checkpoint)
			_, err = db.Exec(`DROP TRIGGER feature_storage_fixture_pause ON ontology_topic_revision_payloads; DROP FUNCTION feature_storage_fixture_pause()`)
			require.NoError(t, err)
			require.NoError(t, NewMigratorWithDB(db).RunUp(context.Background()))
			require.JSONEq(t, before, featureStorageSnapshot(t, db, "ontology_revision_records"))
		})
	}
}

func TestFeatureStoragePostgresConnectionLossResumesCommittedBatch(t *testing.T) {
	db, _ := featureStorageFixture(t, 210)
	before := featureStorageSnapshot(t, db, "evidence_fragments")
	stopFeatureCopy(t, db, true)
	require.NoError(t, NewMigratorWithDB(db).RunUp(context.Background()))
	require.Equal(t, before, featureStorageSnapshot(t, db, "evidence_fragments"))
}

func TestFeatureStoragePostgresCutoverReceiptRecovery(t *testing.T) {
	db, _ := featureStorageFixture(t, 3)
	require.NoError(t, evidenceOnlyMigrationUpTo(context.Background(), db, FeatureStorageDataVersion))
	data, err := os.ReadFile(filepath.Join(MigrationsDir(), "v2_8", featureStorageCutoverSQLName))
	require.NoError(t, err)
	_, up, _ := strings.Cut(string(data), "-- +goose Up")
	up, _, _ = strings.Cut(up, "-- +goose Down")
	require.NoError(t, runFeatureStorage(context.Background(), db, up, true))
	var recorded bool
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=$1 AND is_applied)`, FeatureStorageCutoverVersion).Scan(&recorded))
	require.False(t, recorded)
	require.NoError(t, NewMigratorWithDB(db).RunUp(context.Background()))
	require.NoError(t, ValidateStartupMigrationState(context.Background(), db, MigrationsDir()))
}

func TestFeatureStoragePostgresConcurrentStartersAndCancellation(t *testing.T) {
	db, _ := featureStorageFixture(t, 3)
	lock, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer DiscardAdvisoryLockConnection(lock)
	_, err = lock.ExecContext(context.Background(), `SELECT pg_advisory_lock(746,1)`)
	require.NoError(t, err)
	waiting := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, progress := ObserveMigration(ctx, func(s MigrationSnapshot) {
		if s.MigrationVersion == FeatureStorageDataVersion && s.Phase == "serialization" && s.WorkUnit == "migration" && s.State == "waiting" {
			select {
			case waiting <- struct{}{}:
			default:
			}
		}
	})
	defer progress.Close()
	first := make(chan error, 1)
	go func() { first <- NewMigratorWithDB(db).RunUp(ctx) }()
	select {
	case <-waiting:
	case <-time.After(20 * time.Second):
		t.Fatal("first starter did not reach feature serialization")
	}
	second := make(chan error, 1)
	go func() { second <- NewMigratorWithDB(db).RunUp(context.Background()) }()
	cancel()
	select {
	case err := <-first:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled starter did not release migration ownership")
	}
	_, err = lock.ExecContext(context.Background(), `SELECT pg_advisory_unlock(746,1)`)
	require.NoError(t, err)
	select {
	case err := <-second:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("second starter did not resume after cancellation")
	}
	require.NoError(t, ValidateStartupMigrationState(context.Background(), db, MigrationsDir()))
}
