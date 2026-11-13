package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
)

const (
	FeatureStoragePrepareVersion int64 = 20261113010000
	FeatureStorageDataVersion    int64 = 20261113010001
	FeatureStorageCutoverVersion int64 = 20261113010002
	FeatureStorageLayout               = "feature-storage-v1"
	featureStorageDataSQLName          = "20261113010001_feature_storage_data.sql"
	featureStorageCutoverSQLName       = "20261113010002_feature_storage_cutover.sql"
	featureStorageBatchRows            = 100
	featureStorageBatchBytes           = 8 << 20
	featureStorageBatchTimeout         = 30 * time.Second
)

type featureCheckpoint struct {
	Cursor        json.RawMessage
	Rows, Batches int64
	Fingerprint   string
	Complete      bool
}

type featureMigration struct {
	conn                      *sql.Conn
	progress                  *MigrationProgress
	inventory, schema, source string
	tables                    []featureSource
}

func featureMigrationRegistration(version int64, source []byte, cutover bool) (*goose.Migration, error) {
	_, up, ok := strings.Cut(string(source), "-- +goose Up")
	if !ok {
		return nil, errors.New("feature storage: missing Up directive")
	}
	up, _, ok = strings.Cut(up, "-- +goose Down")
	if !ok {
		return nil, errors.New("feature storage: missing Down directive")
	}
	return goose.NewGoMigration(version, &goose.GoFunc{Mode: goose.TransactionDisabled, RunDB: func(ctx context.Context, db *sql.DB) error {
		return runFeatureStorage(ctx, db, up, cutover)
	}}, &goose.GoFunc{Mode: goose.TransactionDisabled, RunDB: func(context.Context, *sql.DB) error {
		return errors.New("feature storage recovery rolls forward with a compatible application")
	}}), nil
}

func runFeatureStorage(ctx context.Context, db *sql.DB, cutoverSQL string, cutover bool) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, DiscardAdvisoryLockConnection(conn)) }()
	m := &featureMigration{conn: conn, progress: MigrationProgressFromContext(ctx)}
	if m.progress != nil {
		version := FeatureStorageDataVersion
		if cutover {
			version = FeatureStorageCutoverVersion
		}
		m.progress.SetMigrationVersion(version)
		m.report("serialization", "migration", "waiting", featureCheckpoint{})
	}
	defer func() {
		if err != nil {
			reason := featureStorageFailureReason(err)
			state := "failed"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				state = "cancelled"
			}
			if m.progress != nil {
				m.progress.FeatureStorageDatabaseFailure(err)
				m.progress.FeatureStorageFailure(state, reason)
			}
			err = &MigrationFailure{Cause: err, Reason: reason}
		}
	}()
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(746,1)`); err != nil {
		return err
	}
	var complete bool
	err = m.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM data_migration_runs WHERE migration_version=$1 AND phase='complete')`, FeatureStorageDataVersion).Scan(&complete)
	})
	if err != nil {
		return err
	}
	if complete {
		if err = m.verifyReceipt(ctx); err != nil {
			return err
		}
		m.report("cutover", "catalog", "completed", featureCheckpoint{Complete: true})
		return nil
	}
	if err = m.prepare(ctx); err != nil {
		return err
	}
	for _, table := range m.tables {
		if err = m.scan(ctx, "snapshot", table, false); err != nil {
			return err
		}
	}
	if err = m.bindSource(ctx); err != nil {
		return err
	}
	if !cutover {
		for _, table := range m.tables {
			if table.Name == "ontology_record_revisions" || table.Name == "ontology_draft_revisions" {
				if err = m.scan(ctx, "copy", table, true); err != nil {
					return err
				}
			}
		}
	}
	for _, table := range m.tables {
		if err = m.scan(ctx, "verify", table, false); err != nil {
			return err
		}
	}
	if err = m.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE data_migration_runs SET phase='ready',updated_at=clock_timestamp() WHERE migration_version=$1`, FeatureStorageDataVersion)
		return err
	}); err != nil {
		return err
	}
	if !cutover {
		return nil
	}
	m.report("cutover", "catalog", "started", featureCheckpoint{})
	if err = m.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := validateFeatureCatalog(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, cutoverSQL)
		return err
	}); err != nil {
		return err
	}
	if err = m.verifyReceipt(ctx); err != nil {
		return err
	}
	m.report("cutover", "catalog", "completed", featureCheckpoint{Complete: true})
	return nil
}

func (m *featureMigration) transaction(ctx context.Context, fn func(context.Context, *sql.Tx) error) (err error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, featureStorageBatchTimeout)
	defer cancel()
	tx, err := m.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var xid string
	defer func() {
		if err == nil {
			return
		}
		_ = tx.Rollback()
		if m.progress == nil {
			return
		}
		outcome := "unconfirmed"
		if xid != "" {
			checkCtx, stop := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
			defer stop()
			var status sql.NullString
			if checkErr := m.conn.QueryRowContext(checkCtx, `SELECT pg_xact_status($1::xid8)`, xid).Scan(&status); checkErr == nil {
				if status.String == "aborted" {
					outcome = "rolled_back"
				}
				if status.String == "committed" {
					outcome = "committed"
				}
			}
		}
		m.progress.SetFeatureTransactionOutcome(outcome)
	}()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tx_mode','migration',true),set_config('lock_timeout','5s',true),set_config('statement_timeout','30s',true)`); err != nil {
		return err
	}
	if m.progress != nil {
		var pid int
		var start time.Time
		if err := tx.QueryRowContext(ctx, `SELECT pid,backend_start,pg_current_xact_id()::text FROM pg_stat_activity WHERE pid=pg_backend_pid()`).Scan(&pid, &start, &xid); err != nil {
			return err
		}
		m.progress.BindFeatureTransaction(pid, start)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	err = tx.Commit()
	if err == nil && m.progress != nil {
		m.progress.SetFeatureTransactionOutcome("committed")
	}
	return err
}

func (m *featureMigration) prepare(ctx context.Context) error {
	return m.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		m.tables, err = featureSources(ctx, tx)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT dense_mem_feature_catalog_fingerprint()`).Scan(&m.schema); err != nil {
			return err
		}
		inventoryHash := sha256.Sum256([]byte(FeatureStorageLayout + "\n" + featureSourceInventory))
		m.inventory = "sha256:" + hex.EncodeToString(inventoryHash[:])
		if _, err := tx.ExecContext(ctx, `INSERT INTO data_migration_runs(migration_version,executor_version,inventory_fingerprint,schema_fingerprint,phase) VALUES($1,$2,$3,$4,'snapshot') ON CONFLICT DO NOTHING`, FeatureStorageDataVersion, FeatureStorageLayout, m.inventory, m.schema); err != nil {
			return err
		}
		var executor, inventory, schema string
		if err := tx.QueryRowContext(ctx, `SELECT executor_version,inventory_fingerprint,schema_fingerprint,COALESCE(source_fingerprint,'') FROM data_migration_runs WHERE migration_version=$1 FOR UPDATE`, FeatureStorageDataVersion).Scan(&executor, &inventory, &schema, &m.source); err != nil {
			return err
		}
		if executor != FeatureStorageLayout || inventory != m.inventory || schema != m.schema {
			if m.progress != nil {
				m.progress.FeatureFingerprintMismatch(schema, m.schema)
			}
			return errors.New("feature_storage_blocked: source schema or executor fingerprint changed")
		}
		return nil
	})
}

func (m *featureMigration) bindSource(ctx context.Context) error {
	return m.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		digest := ""
		for _, table := range m.tables {
			cp, err := readFeatureCheckpoint(ctx, tx, "snapshot", table.Name)
			if err != nil {
				return err
			}
			if !cp.Complete {
				return errors.New("feature_storage_blocked: incomplete source snapshot")
			}
			digest = advanceFeatureFingerprint(digest, []byte(fmt.Sprintf("%s\n%d\n%s", table.Name, cp.Rows, cp.Fingerprint)))
		}
		source := "sha256:" + digest
		if m.source != "" && m.source != source {
			if m.progress != nil {
				m.progress.FeatureFingerprintMismatch(m.source, source)
			}
			return errors.New("feature_storage_blocked: retained source fingerprint changed")
		}
		m.source = source
		_, err := tx.ExecContext(ctx, `UPDATE data_migration_runs SET source_fingerprint=$2,phase='copy',updated_at=clock_timestamp() WHERE migration_version=$1 AND phase<>'complete'`, FeatureStorageDataVersion, m.source)
		return err
	})
}

func (m *featureMigration) verifyReceipt(ctx context.Context) error {
	return m.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tx_mode','system',true)`); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT inventory_fingerprint,schema_fingerprint,source_fingerprint FROM data_migration_runs WHERE migration_version=$1 AND phase='complete'`, FeatureStorageDataVersion).Scan(&m.inventory, &m.schema, &m.source); err != nil {
			return err
		}
		var compatible bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM data_migration_runs run JOIN v2_compatibility_markers marker ON marker.marker_kind='feature_storage' AND marker.version=$2 AND marker.status='compatible' WHERE run.migration_version=$1 AND run.phase='complete' AND marker.metadata->>'source_fingerprint'=run.source_fingerprint AND marker.metadata->>'retained_fingerprints_verified'='true' AND marker.metadata->>'transaction_outcome'='committed')`, FeatureStorageDataVersion, FeatureStorageLayout).Scan(&compatible); err != nil {
			return err
		}
		if !compatible {
			return errors.New("feature_storage_blocked: incompatible cutover receipt")
		}
		return nil
	})
}

func (m *featureMigration) report(phase, work, state string, cp featureCheckpoint) {
	if m.progress != nil {
		m.progress.FeatureStorageCheckpoint(phase, work, state, cp.Rows, cp.Batches, cp.Fingerprint, m.inventory, m.source, m.schema)
	}
}

func featureStorageFailureReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "feature_storage_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "feature_storage_deadline"
	}
	for _, reason := range []string{"destination payload mismatch", "destination draft mismatch", "draft payload count mismatch", "missing draft envelope", "unsupported draft shape", "draft feature is unavailable", "unsupported payload kind", "source schema or executor fingerprint changed", "retained source fingerprint changed", "source fingerprint mismatch", "payload verification failed", "source row exceeds batch byte bound", "source table has no primary key", "invalid checkpoint cursor", "incompatible cutover receipt", "feature catalog verification failed"} {
		if strings.Contains(err.Error(), "feature_storage_blocked: "+reason) {
			return "feature_storage_" + strings.ReplaceAll(reason, " ", "_")
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "55P03":
			return "feature_storage_lock_timeout"
		case "57014":
			return "feature_storage_query_cancelled"
		case "23503":
			return "feature_storage_dependency_constraint"
		case "42501":
			return "feature_storage_permission_denied"
		case "42703", "42P01":
			return "feature_storage_incompatible_schema"
		case "40P01":
			return "feature_storage_deadlock"
		}
		if strings.HasPrefix(pgErr.Code, "08") {
			return "feature_storage_connection_failure"
		}
	}
	return "feature_storage_database_failure"
}
