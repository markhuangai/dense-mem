package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	CommitCutover(ctx context.Context, input V2CommitCutoverInput) (*domain.V2CompatibilityMarker, error)
	CommitFreshV2Authority(ctx context.Context, input V2CommitFreshV2AuthorityInput) (*domain.V2CompatibilityMarker, error)
	GetLatestMarker(ctx context.Context) (*domain.V2CompatibilityMarker, error)
	RecordOperatorAction(ctx context.Context, action domain.V2MigrationOperatorAction) error
	ListOperatorActions(ctx context.Context, runID string, limit int) ([]domain.V2MigrationOperatorAction, error)
}

var ErrV2MigrationCutoverBlocked = errors.New("v2 migration control: cutover blocked")
var ErrV2MigrationFreshInitBlocked = errors.New("v2 migration control: fresh V2 authority blocked")

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
	RunID                    string
	FromState                string
	ToState                  string
	Phase                    string
	MigrationContractVersion string
	CorpusVersion            string
	PreflightApproved        bool
	BackupReference          string
	ClearBackupReference     bool
	PreflightChecks          map[string]any
	LastError                string
	Retryable                bool
	Now                      time.Time
}

type V2CommitCutoverInput struct {
	RunID          string
	FromState      string
	MarkerVersion  string
	CorpusHash     string
	GateReportHash string
	GateResults    []domain.V2MigrationGateResult
	Metadata       map[string]any
	OperatorAction domain.V2MigrationOperatorAction
	Now            time.Time
}

type V2CommitFreshV2AuthorityInput struct {
	MarkerVersion string
	Metadata      map[string]any
	Now           time.Time
}

type V2MigrationControlRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ V2MigrationControlRepository = (*V2MigrationControlRepositoryImpl)(nil)

type v2MigrationRLSHelper interface {
	WithMigrationTx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error
}

func NewV2MigrationControlRepository(db *gorm.DB, rls postgres.RLSHelper) *V2MigrationControlRepositoryImpl {
	return &V2MigrationControlRepositoryImpl{db: db, rls: rls}
}

var v2FreshAuthorityApplicationTables = []string{
	"profiles",
	"api_keys",
	"teams",
	"team_profiles",
	"audit_log",
	"usage_metric_buckets",
	"operation_logs",
	"recall_feedback_events",
	"security_ip_failures",
	"security_ip_bans",
	"memory_placement_runs",
	"memory_placement_items",
	"memory_dispute_sessions",
	"skill_pack_imports",
	"skill_pack_import_changes",
	"sso_providers",
	"sso_identities",
	"sso_group_mappings",
	"sso_entitlement_cache",
	"sso_oauth_states",
	"sso_sessions",
	"community_detection_runs",
	"semantic_team_refs",
	"team_predicate_definitions",
	"semantic_profile_refs",
	"knowledge_ingests",
	"evidence_sources",
	"evidence_source_revisions",
	"evidence_fragments",
	"evidence_security_events",
	"evidence_security_signals",
	"evidence_quarantines",
	"placement_runs",
	"placement_items",
	"placement_outcomes",
	"entity_records",
	"entity_names",
	"value_records",
	"relationship_records",
	"relationship_observations",
	"verification_events",
	"relationship_evidence_supports",
	"relationship_support_decision_events",
	"relationship_transition_events",
	"entity_resolution_events",
	"entity_correction_events",
	"relationship_cross_references",
	"entity_correction_plans",
	"review_tasks",
	"hypotheses",
	"embedding_contracts",
	"search_index_generations",
	"search_documents",
	"embedding_jobs",
	"community_snapshot_runs",
	"community_records",
	"community_memberships",
	"community_sources",
	"dream_cycle_runs",
	"v2_migration_runs",
	"v2_migration_corpus_items",
	"v2_migration_source_maps",
	"v2_migration_checkpoints",
	"v2_migration_errors",
	"v2_migration_exclusions",
	"v2_migration_gate_results",
	"v2_migration_operator_actions",
}

func (r *V2MigrationControlRepositoryImpl) GetLatestRun(ctx context.Context) (*domain.V2MigrationRun, error) {
	var out *domain.V2MigrationRun
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		row := tx.Raw(`
			SELECT run_id::text, migration_contract_version, corpus_version, source_kind,
			       state, phase, required, preflight_approved, backup_reference,
			       preflight_checks::text, corpus_watermark, corpus_hash,
			       total_items, completed_items, failed_items, excluded_items,
			       last_error, retryable, lease_owner, checkpoint_key,
			       checkpoint_value::text, claim_epoch, started_at, completed_at, cutover_at,
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
		if isV2MigrationRowNotFound(err) {
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
			          preflight_checks::text, corpus_watermark, corpus_hash,
			          total_items, completed_items, failed_items, excluded_items,
			          last_error, retryable, lease_owner, checkpoint_key,
			          checkpoint_value::text, claim_epoch, started_at, completed_at, cutover_at,
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
			    migration_contract_version = COALESCE(NULLIF(?, ''), migration_contract_version),
			    corpus_version = COALESCE(NULLIF(?, ''), corpus_version),
			    preflight_approved = preflight_approved OR ?,
			    backup_reference = CASE WHEN ? THEN '' ELSE COALESCE(NULLIF(?, ''), backup_reference) END,
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
			          preflight_checks::text, corpus_watermark, corpus_hash,
			          total_items, completed_items, failed_items, excluded_items,
			          last_error, retryable, lease_owner, checkpoint_key,
			          checkpoint_value::text, claim_epoch, started_at, completed_at, cutover_at,
			          created_at, updated_at
		`, input.ToState, input.Phase, input.MigrationContractVersion, input.CorpusVersion,
			input.PreflightApproved, input.ClearBackupReference, input.BackupReference,
			string(checks), string(checks), input.LastError, input.Retryable,
			input.ToState, now, input.ToState, input.ToState, now, input.ToState, now, now,
			input.RunID, input.FromState).Row()
		record, err := scanV2MigrationRun(row)
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		if isV2MigrationRowNotFound(err) {
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
		if isV2MigrationRowNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("v2 migration control: get marker: %w", err)
	}
	return out, nil
}

func isV2MigrationRowNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func (r *V2MigrationControlRepositoryImpl) CommitCutover(
	ctx context.Context,
	input V2CommitCutoverInput,
) (*domain.V2CompatibilityMarker, error) {
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	metadata, err := marshalV2MigrationJSON(input.Metadata)
	if err != nil {
		return nil, err
	}
	actionMetadata, err := marshalV2MigrationJSON(input.OperatorAction.Metadata)
	if err != nil {
		return nil, err
	}
	var out *domain.V2CompatibilityMarker
	err = r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		if err := validateV2CutoverRepositoryState(tx, input); err != nil {
			return err
		}
		for _, gate := range input.GateResults {
			gateMetadata, err := marshalV2MigrationJSON(gate.Metadata)
			if err != nil {
				return err
			}
			if err := tx.Exec(`
				INSERT INTO v2_migration_gate_results (
				    gate_id, run_id, gate_name, outcome, evidence_ref, evidence_hash,
				    message, metadata, created_at
				) VALUES (
				    ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?::jsonb, ?
				)
			`, uuid.NewString(), input.RunID, gate.GateName, gate.Outcome,
				gate.EvidenceRef, gate.EvidenceHash, gate.Message, string(gateMetadata), now).Error; err != nil {
				return err
			}
		}
		update := tx.Exec(`
			UPDATE v2_migration_runs
			SET state = ?,
			    phase = 'cutover',
			    corpus_hash = COALESCE(NULLIF(?, ''), corpus_hash),
			    completed_at = ?,
			    cutover_at = ?,
			    updated_at = ?
			WHERE run_id = ?::uuid
			  AND state = ?
		`, domain.V2MigrationStateCutOver, input.CorpusHash, now, now, now, input.RunID, input.FromState)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("%w: cutover state compare-and-swap failed", ErrV2MigrationCutoverBlocked)
		}
		row := tx.Raw(`
			INSERT INTO v2_compatibility_markers (
			    marker_id, marker_kind, version, status, run_id,
			    corpus_hash, gate_report_hash, metadata, created_at
			) VALUES (
			    ?::uuid, ?, ?, ?, ?::uuid, ?, ?, ?::jsonb, ?
			)
			RETURNING marker_id::text, marker_kind, version, status,
			          COALESCE(run_id::text, ''), corpus_hash, gate_report_hash,
			          metadata::text, created_at
		`, uuid.NewString(), domain.V2MigrationMarkerKindCutover, input.MarkerVersion,
			domain.V2MigrationMarkerCompatible, input.RunID, input.CorpusHash,
			input.GateReportHash, string(metadata), now).Row()
		marker, err := scanV2CompatibilityMarker(row)
		if err != nil {
			return err
		}
		out = marker
		if input.OperatorAction.Action != "" {
			if err := tx.Exec(`
				INSERT INTO v2_migration_operator_actions (
				    action_id, run_id, action, actor, remote_ip, reason, metadata, created_at
				) VALUES (
				    ?::uuid, ?::uuid, ?, ?, ?, ?, ?::jsonb, ?
				)
			`, uuid.NewString(), input.RunID, input.OperatorAction.Action,
				input.OperatorAction.Actor, input.OperatorAction.RemoteIP, input.OperatorAction.Reason,
				string(actionMetadata), now).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrV2MigrationCutoverBlocked) {
			return nil, err
		}
		return nil, fmt.Errorf("v2 migration control: commit cutover: %w", err)
	}
	return out, nil
}

func (r *V2MigrationControlRepositoryImpl) CommitFreshV2Authority(
	ctx context.Context,
	input V2CommitFreshV2AuthorityInput,
) (*domain.V2CompatibilityMarker, error) {
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	metadata := map[string]any{"fresh_install": true}
	for key, value := range input.Metadata {
		metadata[key] = value
	}
	metadataJSON, err := marshalV2MigrationJSON(metadata)
	if err != nil {
		return nil, err
	}
	var out *domain.V2CompatibilityMarker
	err = r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		var existingMarkerCount int
		if err := tx.Raw(`
			SELECT count(*)::int
			FROM v2_compatibility_markers
			WHERE marker_kind = ?
		`, domain.V2MigrationMarkerKindCutover).Scan(&existingMarkerCount).Error; err != nil {
			return err
		}
		if existingMarkerCount > 0 {
			return fmt.Errorf("%w: cutover marker already exists", ErrV2MigrationFreshInitBlocked)
		}
		nonempty, err := v2FreshAuthorityNonemptyTables(tx)
		if err != nil {
			return err
		}
		if len(nonempty) > 0 {
			return fmt.Errorf("%w: nonempty application tables: %s", ErrV2MigrationFreshInitBlocked, strings.Join(nonempty, ", "))
		}
		row := tx.Raw(`
			INSERT INTO v2_compatibility_markers (
			    marker_id, marker_kind, version, status, run_id,
			    corpus_hash, gate_report_hash, metadata, created_at
			) VALUES (
			    ?::uuid, ?, ?, ?, NULL, '', '', ?::jsonb, ?
			)
			RETURNING marker_id::text, marker_kind, version, status,
			          COALESCE(run_id::text, ''), corpus_hash, gate_report_hash,
			          metadata::text, created_at
		`, uuid.NewString(), domain.V2MigrationMarkerKindCutover, input.MarkerVersion,
			domain.V2MigrationMarkerCompatible, string(metadataJSON), now).Row()
		marker, err := scanV2CompatibilityMarker(row)
		if err != nil {
			return err
		}
		out = marker
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrV2MigrationFreshInitBlocked) {
			return nil, err
		}
		return nil, fmt.Errorf("v2 migration control: commit fresh authority: %w", err)
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

func validateV2CutoverRepositoryState(tx *gorm.DB, input V2CommitCutoverInput) error {
	var existingMarkerCount int
	if err := tx.Raw(`
		SELECT count(*)::int
		FROM v2_compatibility_markers
		WHERE marker_kind = ?
	`, domain.V2MigrationMarkerKindCutover).Scan(&existingMarkerCount).Error; err != nil {
		return err
	}
	if existingMarkerCount > 0 {
		return fmt.Errorf("%w: cutover marker already exists", ErrV2MigrationCutoverBlocked)
	}

	var state string
	var preflightApproved bool
	var pendingItems, failedItems, blockingExclusions int
	row := tx.Raw(`
		SELECT r.state,
		       r.preflight_approved,
		       (
		           SELECT count(*)::int
		           FROM v2_migration_corpus_items
		           WHERE run_id = r.run_id
		             AND (
		                 outcome = 'pending'
		                 OR (
		                     outcome = 'needs_review'
		                     AND (
		                         placement_item_id IS NULL
		                         OR NOT EXISTS (
		                             SELECT 1
		                             FROM review_tasks AS task
		                             WHERE task.team_id = v2_migration_corpus_items.team_id
		                               AND task.placement_item_id = v2_migration_corpus_items.placement_item_id
		                               AND task.status IN ('open', 'acknowledged')
		                         )
		                     )
		                 )
		             )
		       ) AS pending_items,
		       (
		           SELECT count(*)::int
		           FROM v2_migration_corpus_items
		           WHERE run_id = r.run_id
		             AND outcome = 'failed'
		       ) AS failed_items,
		       (
		           SELECT count(*)::int
		           FROM v2_migration_exclusions
		           WHERE run_id = r.run_id
		             AND blocks_cutover
		       ) AS blocking_exclusions
		FROM v2_migration_runs r
		WHERE r.run_id = ?::uuid
		FOR UPDATE
	`, input.RunID).Row()
	if err := row.Scan(&state, &preflightApproved, &pendingItems, &failedItems, &blockingExclusions); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: run not found", ErrV2MigrationCutoverBlocked)
		}
		return err
	}
	if state != input.FromState || state != domain.V2MigrationStateReadyCutover {
		return fmt.Errorf("%w: run state %s", ErrV2MigrationCutoverBlocked, state)
	}
	if !preflightApproved {
		return fmt.Errorf("%w: preflight not approved", ErrV2MigrationCutoverBlocked)
	}
	if pendingItems > 0 || failedItems > 0 || blockingExclusions > 0 {
		return fmt.Errorf(
			"%w: pending_items=%d failed_items=%d blocking_exclusions=%d",
			ErrV2MigrationCutoverBlocked,
			pendingItems,
			failedItems,
			blockingExclusions,
		)
	}
	return nil
}

func v2FreshAuthorityNonemptyTables(tx *gorm.DB) ([]string, error) {
	nonempty := []string{}
	for _, table := range v2FreshAuthorityApplicationTables {
		var exists bool
		if err := tx.Raw(`SELECT to_regclass(?) IS NOT NULL`, table).Scan(&exists).Error; err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		quoted := pqQuoteIdentifier(table)
		if err := tx.Exec("LOCK TABLE " + quoted + " IN SHARE MODE").Error; err != nil {
			return nil, err
		}
		var hasRows bool
		if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM " + quoted + " LIMIT 1)").Scan(&hasRows).Error; err != nil {
			return nil, err
		}
		if hasRows {
			nonempty = append(nonempty, table)
		}
	}
	return nonempty, nil
}

func pqQuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func (r *V2MigrationControlRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls == nil {
		return r.db.WithContext(ctx).Transaction(fn)
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

func (r *V2MigrationControlRepositoryImpl) withMigrationTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls == nil {
		return r.db.WithContext(ctx).Transaction(fn)
	}
	if helper, ok := r.rls.(v2MigrationRLSHelper); ok {
		return helper.WithMigrationTx(ctx, r.db, fn)
	}
	return r.withSystemTx(ctx, fn)
}

func v2MigrationTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
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
		&record.TotalItems,
		&record.CompletedItems,
		&record.FailedItems,
		&record.ExcludedItems,
		&record.LastError,
		&record.Retryable,
		&record.LeaseOwner,
		&record.CheckpointKey,
		&checkpointJSON,
		&record.ClaimEpoch,
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
