package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type V2MigrationControlRepository interface {
	GetLatestRun(ctx context.Context) (*domain.V2MigrationRun, error)
	CreateRun(ctx context.Context, input V2CreateMigrationRunInput) (*domain.V2MigrationRun, error)
	UpdateRunState(ctx context.Context, input V2UpdateMigrationRunStateInput) (*domain.V2MigrationRun, error)
	FinalizeMigrationGateReport(ctx context.Context, input V2FinalizeMigrationGateReportInput) (*domain.V2MigrationRun, []domain.V2MigrationGateResult, error)
	CommitMigrationCutover(ctx context.Context, input V2CommitMigrationCutoverInput) (*domain.V2CompatibilityMarker, error)
	GetLatestMarker(ctx context.Context) (*domain.V2CompatibilityMarker, error)
	RecordOperatorAction(ctx context.Context, action domain.V2MigrationOperatorAction) error
	ListOperatorActions(ctx context.Context, runID string, limit int) ([]domain.V2MigrationOperatorAction, error)
	ListMigrationGateResults(ctx context.Context, runID string, limit int) ([]domain.V2MigrationGateResult, error)
}

type V2CreateMigrationRunInput struct {
	MigrationContractVersion string
	CorpusVersion            string
	SourceKind               string
	State                    string
	Phase                    string
	Required                 bool
	PreflightApproved        bool
	BackupReference          string
	PreflightChecks          map[string]any
	Now                      time.Time
}

type V2UpdateMigrationRunStateInput struct {
	RunID             string
	FromState         string
	ToState           string
	Phase             string
	PreflightApproved bool
	BackupReference   string
	PreflightChecks   map[string]any
	LastError         string
	Retryable         bool
	Now               time.Time
}

type V2MigrationControlRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ V2MigrationControlRepository = (*V2MigrationControlRepositoryImpl)(nil)

func NewV2MigrationControlRepository(db *gorm.DB, rls postgres.RLSHelper) *V2MigrationControlRepositoryImpl {
	return &V2MigrationControlRepositoryImpl{db: db, rls: rls}
}

func (r *V2MigrationControlRepositoryImpl) GetLatestRun(ctx context.Context) (*domain.V2MigrationRun, error) {
	var out *domain.V2MigrationRun
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		row := tx.Raw(`
			SELECT run_id::text, migration_contract_version, corpus_version, source_kind,
			       state, phase, required, preflight_approved, backup_reference,
			       preflight_checks::text, corpus_watermark, corpus_hash, gate_report_hash,
			       total_items, completed_items, failed_items, excluded_items,
			       last_error, retryable, lease_owner, checkpoint_key,
			       checkpoint_value::text, started_at, completed_at, cutover_at,
			       created_at, updated_at
			FROM v2_migration_runs
			ORDER BY created_at DESC, run_id DESC
			LIMIT 1
		`).Row()
		record, err := scanV2MigrationRun(row)
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("v2 migration control: get latest run: %w", err)
	}
	return out, nil
}

func (r *V2MigrationControlRepositoryImpl) CreateRun(ctx context.Context, input V2CreateMigrationRunInput) (*domain.V2MigrationRun, error) {
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	checks, err := marshalV2MigrationJSON(input.PreflightChecks)
	if err != nil {
		return nil, err
	}
	var out *domain.V2MigrationRun
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		row := tx.Raw(`
			INSERT INTO v2_migration_runs (
			    run_id, migration_contract_version, corpus_version, source_kind,
			    state, phase, required, preflight_approved, backup_reference,
			    preflight_checks, retryable, created_at, updated_at
			) VALUES (
			    ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, true, ?, ?
			)
			RETURNING run_id::text, migration_contract_version, corpus_version, source_kind,
			          state, phase, required, preflight_approved, backup_reference,
			          preflight_checks::text, corpus_watermark, corpus_hash, gate_report_hash,
			          total_items, completed_items, failed_items, excluded_items,
			          last_error, retryable, lease_owner, checkpoint_key,
			          checkpoint_value::text, started_at, completed_at, cutover_at,
			          created_at, updated_at
		`, uuid.NewString(), input.MigrationContractVersion, input.CorpusVersion, input.SourceKind,
			input.State, input.Phase, input.Required, input.PreflightApproved, input.BackupReference,
			string(checks), now, now).Row()
		record, err := scanV2MigrationRun(row)
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 migration control: create run: %w", err)
	}
	return out, nil
}

func (r *V2MigrationControlRepositoryImpl) UpdateRunState(ctx context.Context, input V2UpdateMigrationRunStateInput) (*domain.V2MigrationRun, error) {
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	checks, err := marshalV2MigrationJSON(input.PreflightChecks)
	if err != nil {
		return nil, err
	}
	var out *domain.V2MigrationRun
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		row := tx.Raw(`
			UPDATE v2_migration_runs
			SET state = ?,
			    phase = ?,
			    preflight_approved = preflight_approved OR ?,
			    backup_reference = COALESCE(NULLIF(?, ''), backup_reference),
			    preflight_checks = CASE WHEN ?::jsonb = '{}'::jsonb THEN preflight_checks ELSE ?::jsonb END,
			    last_error = ?,
			    retryable = ?,
			    started_at = CASE WHEN ? = 'running' AND started_at IS NULL THEN ? ELSE started_at END,
			    completed_at = CASE WHEN ? IN ('failed', 'cut_over', 'incompatible') THEN ? ELSE completed_at END,
			    cutover_at = CASE WHEN ? = 'cut_over' THEN ? ELSE cutover_at END,
			    updated_at = ?
			WHERE run_id = ?::uuid
			  AND state = ?
			RETURNING run_id::text, migration_contract_version, corpus_version, source_kind,
			          state, phase, required, preflight_approved, backup_reference,
			          preflight_checks::text, corpus_watermark, corpus_hash, gate_report_hash,
			          total_items, completed_items, failed_items, excluded_items,
			          last_error, retryable, lease_owner, checkpoint_key,
			          checkpoint_value::text, started_at, completed_at, cutover_at,
			          created_at, updated_at
		`, input.ToState, input.Phase, input.PreflightApproved, input.BackupReference,
			string(checks), string(checks), input.LastError, input.Retryable,
			input.ToState, now, input.ToState, now, input.ToState, now, now,
			input.RunID, input.FromState).Row()
		record, err := scanV2MigrationRun(row)
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("v2 migration control: stale or illegal state transition")
		}
		return nil, fmt.Errorf("v2 migration control: update run state: %w", err)
	}
	return out, nil
}

func (r *V2MigrationControlRepositoryImpl) GetLatestMarker(ctx context.Context) (*domain.V2CompatibilityMarker, error) {
	var out *domain.V2CompatibilityMarker
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		row := tx.Raw(`
			SELECT marker_id::text, marker_kind, version, status,
			       COALESCE(run_id::text, ''), corpus_hash, gate_report_hash,
			       metadata::text, created_at
			FROM v2_compatibility_markers
			WHERE marker_kind = ?
			ORDER BY created_at DESC, marker_id DESC
			LIMIT 1
		`, domain.V2MigrationMarkerKindCutover).Row()
		record, err := scanV2CompatibilityMarker(row)
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("v2 migration control: get marker: %w", err)
	}
	return out, nil
}

func (r *V2MigrationControlRepositoryImpl) RecordOperatorAction(ctx context.Context, action domain.V2MigrationOperatorAction) error {
	metadata, err := marshalV2MigrationJSON(action.Metadata)
	if err != nil {
		return err
	}
	now := action.CreatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO v2_migration_operator_actions (
			    action_id, run_id, action, actor, remote_ip, reason, metadata, created_at
			) VALUES (
			    ?::uuid, NULLIF(?, '')::uuid, ?, ?, ?, ?, ?::jsonb, ?
			)
		`, uuid.NewString(), action.RunID, action.Action, action.Actor,
			action.RemoteIP, action.Reason, string(metadata), now).Error
	})
	if err != nil {
		return fmt.Errorf("v2 migration control: record operator action: %w", err)
	}
	return nil
}

func (r *V2MigrationControlRepositoryImpl) ListOperatorActions(ctx context.Context, runID string, limit int) ([]domain.V2MigrationOperatorAction, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	out := []domain.V2MigrationOperatorAction{}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT action_id::text, COALESCE(run_id::text, ''), action, actor,
			       remote_ip, reason, metadata::text, created_at
			FROM v2_migration_operator_actions
			WHERE NULLIF(?, '') IS NULL OR run_id = NULLIF(?, '')::uuid
			ORDER BY created_at DESC, action_id DESC
			LIMIT ?
		`, runID, runID, limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			action, err := scanV2MigrationOperatorAction(rows)
			if err != nil {
				return err
			}
			out = append(out, action)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 migration control: list operator actions: %w", err)
	}
	return out, nil
}

func (r *V2MigrationControlRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls == nil {
		return r.db.WithContext(ctx).Transaction(fn)
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

type v2MigrationRowScanner interface {
	Scan(dest ...any) error
}

func scanV2MigrationRun(row v2MigrationRowScanner) (*domain.V2MigrationRun, error) {
	var record domain.V2MigrationRun
	var checksJSON, checkpointJSON string
	if err := row.Scan(
		&record.RunID,
		&record.MigrationContractVersion,
		&record.CorpusVersion,
		&record.SourceKind,
		&record.State,
		&record.Phase,
		&record.Required,
		&record.PreflightApproved,
		&record.BackupReference,
		&checksJSON,
		&record.CorpusWatermark,
		&record.CorpusHash,
		&record.GateReportHash,
		&record.TotalItems,
		&record.CompletedItems,
		&record.FailedItems,
		&record.ExcludedItems,
		&record.LastError,
		&record.Retryable,
		&record.LeaseOwner,
		&record.CheckpointKey,
		&checkpointJSON,
		&record.StartedAt,
		&record.CompletedAt,
		&record.CutoverAt,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := unmarshalV2MigrationJSON(checksJSON, &record.PreflightChecks); err != nil {
		return nil, err
	}
	if err := unmarshalV2MigrationJSON(checkpointJSON, &record.CheckpointValue); err != nil {
		return nil, err
	}
	return &record, nil
}

func scanV2CompatibilityMarker(row v2MigrationRowScanner) (*domain.V2CompatibilityMarker, error) {
	var record domain.V2CompatibilityMarker
	var metadataJSON string
	if err := row.Scan(
		&record.MarkerID,
		&record.MarkerKind,
		&record.Version,
		&record.Status,
		&record.RunID,
		&record.CorpusHash,
		&record.GateReportHash,
		&metadataJSON,
		&record.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := unmarshalV2MigrationJSON(metadataJSON, &record.Metadata); err != nil {
		return nil, err
	}
	return &record, nil
}

func scanV2MigrationOperatorAction(row v2MigrationRowScanner) (domain.V2MigrationOperatorAction, error) {
	var record domain.V2MigrationOperatorAction
	var metadataJSON string
	if err := row.Scan(
		&record.ActionID,
		&record.RunID,
		&record.Action,
		&record.Actor,
		&record.RemoteIP,
		&record.Reason,
		&metadataJSON,
		&record.CreatedAt,
	); err != nil {
		return domain.V2MigrationOperatorAction{}, err
	}
	if err := unmarshalV2MigrationJSON(metadataJSON, &record.Metadata); err != nil {
		return domain.V2MigrationOperatorAction{}, err
	}
	return record, nil
}

func marshalV2MigrationJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("v2 migration control: encode json: %w", err)
	}
	return data, nil
}

func unmarshalV2MigrationJSON(data string, target *map[string]any) error {
	if data == "" {
		*target = map[string]any{}
		return nil
	}
	if err := json.Unmarshal([]byte(data), target); err != nil {
		return fmt.Errorf("v2 migration control: decode json: %w", err)
	}
	if *target == nil {
		*target = map[string]any{}
	}
	return nil
}
