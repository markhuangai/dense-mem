package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type V2MigrationExecutorRepository interface {
	GetLatestRun(ctx context.Context) (*domain.V2MigrationRun, error)
	ValidateMigrationOwnerProfile(ctx context.Context, input V2ValidateMigrationOwnerProfileInput) (bool, error)
	UpsertMigrationCorpusItem(ctx context.Context, input V2UpsertMigrationCorpusItemInput) (*domain.V2MigrationCorpusItem, error)
	UpdateMigrationCorpusOutcome(ctx context.Context, input V2UpdateMigrationCorpusOutcomeInput) (*domain.V2MigrationCorpusItem, error)
	UpsertMigrationSourceMap(ctx context.Context, input V2UpsertMigrationSourceMapInput) error
	UpsertMigrationCheckpoint(ctx context.Context, input V2UpsertMigrationCheckpointInput) error
	GetMigrationCheckpoint(ctx context.Context, runID string, checkpointKey string) (map[string]any, error)
	RecordMigrationError(ctx context.Context, input V2RecordMigrationErrorInput) error
	RecordMigrationExclusion(ctx context.Context, input V2RecordMigrationExclusionInput) error
	RefreshMigrationRunStats(ctx context.Context, runID string, now time.Time) (*domain.V2MigrationRun, error)
}

type V2ValidateMigrationOwnerProfileInput struct {
	TeamID         string
	OwnerProfileID string
}

type V2UpsertMigrationCorpusItemInput struct {
	RunID          string
	TeamID         string
	OwnerProfileID string
	SourceKind     string
	SourceID       string
	SourceHash     string
	ItemKind       string
	Outcome        string
	Metadata       map[string]any
	Now            time.Time
}

type V2UpdateMigrationCorpusOutcomeInput struct {
	RunID           string
	SourceKind      string
	SourceID        string
	Outcome         string
	IngestID        string
	PlacementItemID string
	ExclusionReason string
	Metadata        map[string]any
	Now             time.Time
}

type V2UpsertMigrationSourceMapInput struct {
	RunID      string
	SourceKind string
	SourceID   string
	TargetType string
	TargetID   string
	Metadata   map[string]any
	Now        time.Time
}

type V2UpsertMigrationCheckpointInput struct {
	RunID           string
	CheckpointKey   string
	CheckpointValue map[string]any
	LeaseOwner      string
	Now             time.Time
}

type V2RecordMigrationErrorInput struct {
	RunID      string
	SourceKind string
	SourceID   string
	Phase      string
	ErrorCode  string
	Message    string
	Retryable  bool
	Metadata   map[string]any
	Now        time.Time
}

type V2RecordMigrationExclusionInput struct {
	RunID         string
	SourceKind    string
	SourceID      string
	Reason        string
	BlocksCutover bool
	Metadata      map[string]any
	Now           time.Time
}

type v2MigrationRLSHelper interface {
	WithMigrationTx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error
}

func (r *V2MigrationControlRepositoryImpl) ValidateMigrationOwnerProfile(
	ctx context.Context,
	input V2ValidateMigrationOwnerProfileInput,
) (bool, error) {
	teamID := strings.TrimSpace(input.TeamID)
	ownerProfileID := strings.TrimSpace(input.OwnerProfileID)
	if _, err := uuid.Parse(teamID); err != nil {
		return false, fmt.Errorf("v2 migration executor: validate owner profile: invalid team_id: %w", err)
	}
	if _, err := uuid.Parse(ownerProfileID); err != nil {
		return false, fmt.Errorf("v2 migration executor: validate owner profile: invalid owner_profile_id: %w", err)
	}

	var exists bool
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM team_profiles profile
				JOIN teams team ON team.id = profile.team_id
				WHERE profile.team_id = ?::uuid
				  AND profile.id = ?::uuid
				  AND team.deleted_at IS NULL
				  AND team.status <> 'deleted'
			)
		`, teamID, ownerProfileID).Scan(&exists).Error
	})
	if err != nil {
		return false, fmt.Errorf("v2 migration executor: validate owner profile: %w", err)
	}
	return exists, nil
}

func (r *V2MigrationControlRepositoryImpl) UpsertMigrationCorpusItem(
	ctx context.Context,
	input V2UpsertMigrationCorpusItemInput,
) (*domain.V2MigrationCorpusItem, error) {
	now := v2MigrationTime(input.Now)
	metadata, err := marshalV2MigrationJSON(input.Metadata)
	if err != nil {
		return nil, err
	}
	var out *domain.V2MigrationCorpusItem
	err = r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		row := tx.Raw(`
			INSERT INTO v2_migration_corpus_items (
			    item_id, run_id, team_id, owner_profile_id, source_kind, source_id,
			    source_hash, item_kind, outcome, metadata, created_at, updated_at
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid, ?, ?,
			    ?, ?, ?, ?::jsonb, ?, ?
			)
			ON CONFLICT (run_id, source_kind, source_id) DO UPDATE
			SET source_hash = EXCLUDED.source_hash,
			    item_kind = EXCLUDED.item_kind,
			    metadata = v2_migration_corpus_items.metadata || EXCLUDED.metadata,
			    updated_at = EXCLUDED.updated_at
			RETURNING item_id::text, run_id::text, team_id::text,
			          COALESCE(owner_profile_id::text, ''), source_kind, source_id,
			          source_hash, item_kind, outcome, COALESCE(ingest_id::text, ''),
			          COALESCE(placement_item_id::text, ''), exclusion_reason,
			          metadata::text, created_at, updated_at
		`, uuid.NewString(), input.RunID, input.TeamID, input.OwnerProfileID,
			v2MigrationSourceKind(input.SourceKind), strings.TrimSpace(input.SourceID),
			strings.TrimSpace(input.SourceHash), v2MigrationItemKind(input.ItemKind),
			v2MigrationOutcome(input.Outcome), string(metadata), now, now).Row()
		record, err := scanV2MigrationCorpusItem(row)
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 migration executor: upsert corpus item: %w", err)
	}
	return out, nil
}

func (r *V2MigrationControlRepositoryImpl) UpdateMigrationCorpusOutcome(
	ctx context.Context,
	input V2UpdateMigrationCorpusOutcomeInput,
) (*domain.V2MigrationCorpusItem, error) {
	now := v2MigrationTime(input.Now)
	metadata, err := marshalV2MigrationJSON(input.Metadata)
	if err != nil {
		return nil, err
	}
	var out *domain.V2MigrationCorpusItem
	err = r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		row := tx.Raw(`
			UPDATE v2_migration_corpus_items
			SET outcome = ?,
			    ingest_id = NULLIF(?, '')::uuid,
			    placement_item_id = NULLIF(?, '')::uuid,
			    exclusion_reason = ?,
			    metadata = metadata || ?::jsonb,
			    updated_at = ?
			WHERE run_id = ?::uuid
			  AND source_kind = ?
			  AND source_id = ?
			RETURNING item_id::text, run_id::text, team_id::text,
			          COALESCE(owner_profile_id::text, ''), source_kind, source_id,
			          source_hash, item_kind, outcome, COALESCE(ingest_id::text, ''),
			          COALESCE(placement_item_id::text, ''), exclusion_reason,
			          metadata::text, created_at, updated_at
		`, v2MigrationOutcome(input.Outcome), input.IngestID, input.PlacementItemID,
			strings.TrimSpace(input.ExclusionReason), string(metadata), now, input.RunID,
			v2MigrationSourceKind(input.SourceKind), strings.TrimSpace(input.SourceID)).Row()
		record, err := scanV2MigrationCorpusItem(row)
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		if isV2MigrationRowNotFound(err) {
			return nil, fmt.Errorf("v2 migration executor: corpus item not found")
		}
		return nil, fmt.Errorf("v2 migration executor: update corpus outcome: %w", err)
	}
	return out, nil
}

func (r *V2MigrationControlRepositoryImpl) UpsertMigrationSourceMap(ctx context.Context, input V2UpsertMigrationSourceMapInput) error {
	metadata, err := marshalV2MigrationJSON(input.Metadata)
	if err != nil {
		return err
	}
	err = r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO v2_migration_source_maps (
			    map_id, run_id, source_kind, source_id, target_type, target_id, metadata, created_at
			) VALUES (
			    ?::uuid, ?::uuid, ?, ?, ?, ?, ?::jsonb, ?
			)
			ON CONFLICT (run_id, source_kind, source_id, target_type, target_id) DO UPDATE
			SET metadata = v2_migration_source_maps.metadata || EXCLUDED.metadata
		`, uuid.NewString(), input.RunID, v2MigrationSourceKind(input.SourceKind),
			strings.TrimSpace(input.SourceID), strings.TrimSpace(input.TargetType),
			strings.TrimSpace(input.TargetID), string(metadata), v2MigrationTime(input.Now)).Error
	})
	if err != nil {
		return fmt.Errorf("v2 migration executor: upsert source map: %w", err)
	}
	return nil
}

func (r *V2MigrationControlRepositoryImpl) UpsertMigrationCheckpoint(
	ctx context.Context,
	input V2UpsertMigrationCheckpointInput,
) error {
	value, err := marshalV2MigrationJSON(input.CheckpointValue)
	if err != nil {
		return err
	}
	checkpointKey := strings.TrimSpace(input.CheckpointKey)
	now := v2MigrationTime(input.Now)
	err = r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO v2_migration_checkpoints (
			    run_id, checkpoint_key, checkpoint_value, lease_owner, updated_at
			) VALUES (
			    ?::uuid, ?, ?::jsonb, ?, ?
			)
			ON CONFLICT (run_id, checkpoint_key) DO UPDATE
			SET checkpoint_value = EXCLUDED.checkpoint_value,
			    lease_owner = EXCLUDED.lease_owner,
			    updated_at = EXCLUDED.updated_at
		`, input.RunID, checkpointKey, string(value), strings.TrimSpace(input.LeaseOwner), now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE v2_migration_runs
			SET checkpoint_key = ?,
			    checkpoint_value = ?::jsonb,
			    updated_at = ?
			WHERE run_id = ?::uuid
		`, checkpointKey, string(value), now, input.RunID).Error
	})
	if err != nil {
		return fmt.Errorf("v2 migration executor: upsert checkpoint: %w", err)
	}
	return nil
}

func (r *V2MigrationControlRepositoryImpl) GetMigrationCheckpoint(
	ctx context.Context,
	runID string,
	checkpointKey string,
) (map[string]any, error) {
	var out map[string]any
	err := r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		var data string
		row := tx.Raw(`
			SELECT checkpoint_value::text
			FROM v2_migration_checkpoints
			WHERE run_id = ?::uuid
			  AND checkpoint_key = ?
		`, runID, strings.TrimSpace(checkpointKey)).Row()
		if err := row.Scan(&data); err != nil {
			return err
		}
		return unmarshalV2MigrationJSON(data, &out)
	})
	if err != nil {
		if isV2MigrationRowNotFound(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("v2 migration executor: get checkpoint: %w", err)
	}
	return out, nil
}

func (r *V2MigrationControlRepositoryImpl) RecordMigrationError(ctx context.Context, input V2RecordMigrationErrorInput) error {
	metadata, err := marshalV2MigrationJSON(input.Metadata)
	if err != nil {
		return err
	}
	err = r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO v2_migration_errors (
			    error_id, run_id, source_kind, source_id, phase, error_code,
			    message, retryable, metadata, created_at
			) VALUES (
			    ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?, ?::jsonb, ?
			)
		`, uuid.NewString(), input.RunID, v2MigrationSourceKind(input.SourceKind),
			strings.TrimSpace(input.SourceID), strings.TrimSpace(input.Phase),
			strings.TrimSpace(input.ErrorCode), strings.TrimSpace(input.Message),
			input.Retryable, string(metadata), v2MigrationTime(input.Now)).Error
	})
	if err != nil {
		return fmt.Errorf("v2 migration executor: record error: %w", err)
	}
	return nil
}

func (r *V2MigrationControlRepositoryImpl) RecordMigrationExclusion(ctx context.Context, input V2RecordMigrationExclusionInput) error {
	metadata, err := marshalV2MigrationJSON(input.Metadata)
	if err != nil {
		return err
	}
	err = r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO v2_migration_exclusions (
			    exclusion_id, run_id, source_kind, source_id, reason,
			    blocks_cutover, metadata, created_at
			) VALUES (
			    ?::uuid, ?::uuid, ?, ?, ?, ?, ?::jsonb, ?
			)
			ON CONFLICT (run_id, source_kind, source_id) DO UPDATE
			SET reason = EXCLUDED.reason,
			    blocks_cutover = EXCLUDED.blocks_cutover,
			    metadata = v2_migration_exclusions.metadata || EXCLUDED.metadata
		`, uuid.NewString(), input.RunID, v2MigrationSourceKind(input.SourceKind),
			strings.TrimSpace(input.SourceID), strings.TrimSpace(input.Reason),
			input.BlocksCutover, string(metadata), v2MigrationTime(input.Now)).Error
	})
	if err != nil {
		return fmt.Errorf("v2 migration executor: record exclusion: %w", err)
	}
	return nil
}

func (r *V2MigrationControlRepositoryImpl) RefreshMigrationRunStats(
	ctx context.Context,
	runID string,
	now time.Time,
) (*domain.V2MigrationRun, error) {
	var out *domain.V2MigrationRun
	err := r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		row := tx.Raw(`
			WITH counts AS (
			    SELECT
			        count(*)::int AS total_items,
			        count(*) FILTER (
			            WHERE outcome IN ('accepted', 'needs_review', 'rejected', 'quarantined')
			        )::int AS completed_items,
			        count(*) FILTER (WHERE outcome = 'failed')::int AS failed_items,
			        count(*) FILTER (WHERE outcome = 'excluded')::int AS excluded_items
			    FROM v2_migration_corpus_items
			    WHERE run_id = ?::uuid
			)
			UPDATE v2_migration_runs
			SET total_items = counts.total_items,
			    completed_items = counts.completed_items,
			    failed_items = counts.failed_items,
			    excluded_items = counts.excluded_items,
			    updated_at = ?
			FROM counts
			WHERE run_id = ?::uuid
			RETURNING run_id::text, migration_contract_version, corpus_version, source_kind,
			          state, phase, required, preflight_approved, backup_reference,
			          preflight_checks::text, corpus_watermark, corpus_hash,
			          total_items, completed_items, failed_items, excluded_items,
			          last_error, retryable, lease_owner, checkpoint_key,
			          checkpoint_value::text, started_at, completed_at, cutover_at,
			          created_at, updated_at
		`, runID, v2MigrationTime(now), runID).Row()
		record, err := scanV2MigrationRun(row)
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 migration executor: refresh stats: %w", err)
	}
	return out, nil
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

func scanV2MigrationCorpusItem(row v2MigrationRowScanner) (*domain.V2MigrationCorpusItem, error) {
	var record domain.V2MigrationCorpusItem
	var metadataJSON string
	if err := row.Scan(
		&record.ItemID,
		&record.RunID,
		&record.TeamID,
		&record.OwnerProfileID,
		&record.SourceKind,
		&record.SourceID,
		&record.SourceHash,
		&record.ItemKind,
		&record.Outcome,
		&record.IngestID,
		&record.PlacementItemID,
		&record.ExclusionReason,
		&metadataJSON,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := unmarshalV2MigrationJSON(metadataJSON, &record.Metadata); err != nil {
		return nil, err
	}
	return &record, nil
}

func v2MigrationTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func v2MigrationSourceKind(value string) string {
	if strings.TrimSpace(value) == "" {
		return "neo4j"
	}
	return strings.TrimSpace(value)
}

func v2MigrationItemKind(value string) string {
	if strings.TrimSpace(value) == "" {
		return domain.V2MigrationItemKindEvidence
	}
	return strings.TrimSpace(value)
}

func v2MigrationOutcome(value string) string {
	if strings.TrimSpace(value) == "" {
		return domain.V2MigrationOutcomePending
	}
	return strings.TrimSpace(value)
}
