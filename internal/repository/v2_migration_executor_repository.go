package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// ErrV2MigrationCorpusSourceMetadataMismatch reports legacy source owner or hash drift after staging.
var ErrV2MigrationCorpusSourceMetadataMismatch = errors.New("v2 migration executor: corpus source metadata mismatch")

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
	FinalizeMigrationRun(ctx context.Context, runID string, now time.Time) (*domain.V2MigrationRun, error)
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
		rows, err := tx.Raw(`
			INSERT INTO v2_migration_corpus_items (
			    item_id, run_id, team_id, owner_profile_id, source_kind, source_id,
			    source_hash, item_kind, outcome, metadata, created_at, updated_at
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid, ?, ?,
			    ?, ?, ?, ?::jsonb, ?, ?
			)
			ON CONFLICT (run_id, source_kind, source_id) DO UPDATE
			SET item_kind = EXCLUDED.item_kind,
			    metadata = v2_migration_corpus_items.metadata || EXCLUDED.metadata,
			    updated_at = EXCLUDED.updated_at
			WHERE v2_migration_corpus_items.team_id = EXCLUDED.team_id
			  AND v2_migration_corpus_items.owner_profile_id IS NOT DISTINCT FROM EXCLUDED.owner_profile_id
			  AND v2_migration_corpus_items.source_hash = EXCLUDED.source_hash
			RETURNING item_id::text, run_id::text, team_id::text,
			          COALESCE(owner_profile_id::text, ''), source_kind, source_id,
			          source_hash, item_kind, outcome, COALESCE(ingest_id::text, ''),
			          COALESCE(placement_item_id::text, ''), exclusion_reason,
			          metadata::text, created_at, updated_at
		`, uuid.NewString(), input.RunID, input.TeamID, input.OwnerProfileID,
			v2MigrationSourceKind(input.SourceKind), strings.TrimSpace(input.SourceID),
			strings.TrimSpace(input.SourceHash), v2MigrationItemKind(input.ItemKind),
			v2MigrationOutcome(input.Outcome), string(metadata), now, now).Rows()
		if err != nil {
			return err
		}
		record, err := scanV2MigrationCorpusItemRows(rows)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrV2MigrationCorpusSourceMetadataMismatch
			}
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
		rows, err := tx.Raw(`
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
			v2MigrationSourceKind(input.SourceKind), strings.TrimSpace(input.SourceID)).Rows()
		if err != nil {
			return err
		}
		record, err := scanV2MigrationCorpusItemRows(rows)
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
		rows, err := tx.Raw(`
			SELECT checkpoint_value::text
			FROM v2_migration_checkpoints
			WHERE run_id = ?::uuid
			  AND checkpoint_key = ?
		`, runID, strings.TrimSpace(checkpointKey)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return sql.ErrNoRows
		}
		if err := rows.Scan(&data); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
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
		record, err := refreshV2MigrationRunStatsTx(tx, runID, now)
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

func (r *V2MigrationControlRepositoryImpl) FinalizeMigrationRun(
	ctx context.Context,
	runID string,
	now time.Time,
) (*domain.V2MigrationRun, error) {
	var out *domain.V2MigrationRun
	err := r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		if err := syncV2MigrationCorpusPlacementOutcomes(tx, runID, now); err != nil {
			return err
		}
		record, err := refreshV2MigrationRunStatsTx(tx, runID, now)
		if err != nil {
			return err
		}
		out = record
		done, err := v2MigrationCursorDone(tx, runID)
		if err != nil {
			return err
		}
		if !done || record.State != domain.V2MigrationStateRunning {
			return nil
		}
		pendingItems, failedItems, blockingExclusions, err := v2MigrationCutoverBlockCounts(tx, runID)
		if err != nil {
			return err
		}
		if pendingItems > 0 || failedItems > 0 || blockingExclusions > 0 {
			return nil
		}
		corpusHash, err := v2MigrationCorpusHashTx(tx, runID)
		if err != nil {
			return err
		}
		completedAt := v2MigrationTime(now)
		rows, err := tx.Raw(`
			UPDATE v2_migration_runs
			SET state = ?,
			    phase = 'verifying',
			    corpus_hash = COALESCE(NULLIF(corpus_hash, ''), ?),
			    completed_at = COALESCE(completed_at, ?),
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
		`, domain.V2MigrationStateReadyCutover, corpusHash, completedAt, completedAt, runID,
			domain.V2MigrationStateRunning).Rows()
		if err != nil {
			return err
		}
		ready, err := scanV2MigrationRunRows(rows)
		if err != nil {
			return err
		}
		out = ready
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 migration executor: finalize run: %w", err)
	}
	return out, nil
}

func syncV2MigrationCorpusPlacementOutcomes(tx *gorm.DB, runID string, now time.Time) error {
	now = v2MigrationTime(now)
	if err := tx.Exec(`
		WITH mapped AS (
		    SELECT item.item_id,
		           item.run_id,
		           item.source_kind,
		           item.source_id,
		           placement.placement_item_id,
		           placement.status AS placement_status,
		           placement.category AS placement_category,
		           EXISTS (
		               SELECT 1
		               FROM review_tasks AS task
		               WHERE task.team_id = placement.team_id
		                 AND task.placement_item_id = placement.placement_item_id
		                 AND task.status IN ('open', 'acknowledged')
		           ) AS has_open_review,
		           CASE
		               WHEN placement.status = 'awaiting_review' AND EXISTS (
		                   SELECT 1
		                   FROM review_tasks AS task
		                   WHERE task.team_id = placement.team_id
		                     AND task.placement_item_id = placement.placement_item_id
		                     AND task.status IN ('open', 'acknowledged')
		               ) THEN 'needs_review'
		               WHEN placement.status = 'completed' AND COALESCE(placement.result->>'status', '') = 'rejected' THEN 'rejected'
		               WHEN placement.status = 'completed' THEN 'accepted'
		               WHEN placement.status = 'failed' THEN 'failed'
		               WHEN placement.status = 'quarantined' THEN 'quarantined'
		               ELSE 'pending'
		           END AS migration_outcome
		    FROM v2_migration_corpus_items item
		    JOIN placement_items placement
		      ON placement.team_id = item.team_id
		     AND placement.ingest_id = item.ingest_id
		     AND placement.evidence_index = 0
		    WHERE item.run_id = ?::uuid
		      AND item.outcome <> 'excluded'
		      AND item.ingest_id IS NOT NULL
		), updated AS (
		    UPDATE v2_migration_corpus_items item
		    SET outcome = mapped.migration_outcome,
		        placement_item_id = mapped.placement_item_id,
		        metadata = item.metadata || jsonb_build_object(
		            'placement_status', mapped.placement_status,
		            'placement_category', mapped.placement_category,
		            'placement_has_open_review', mapped.has_open_review,
		            'migration_finalized_at', ?::text
		        ),
		        updated_at = ?
		    FROM mapped
		    WHERE item.item_id = mapped.item_id
		    RETURNING mapped.run_id, mapped.source_kind, mapped.source_id,
		              mapped.placement_item_id, mapped.placement_status,
		              mapped.placement_category
		)
		INSERT INTO v2_migration_source_maps (
		    map_id, run_id, source_kind, source_id, target_type, target_id, metadata, created_at
		)
		SELECT gen_random_uuid(), run_id, source_kind, source_id, ?, placement_item_id::text,
		       jsonb_build_object(
		           'placement_status', placement_status,
		           'placement_category', placement_category,
		           'placement_has_open_review', has_open_review
		       ),
		       ?
		FROM updated
		ON CONFLICT (run_id, source_kind, source_id, target_type, target_id) DO UPDATE
		SET metadata = v2_migration_source_maps.metadata || EXCLUDED.metadata
	`, runID, now.UTC().Format(time.RFC3339Nano), now,
		domain.V2MigrationTargetPlacementItem, now).Error; err != nil {
		return err
	}
	return nil
}

func v2MigrationCorpusHashTx(tx *gorm.DB, runID string) (string, error) {
	rows, err := tx.Raw(`
		SELECT team_id::text,
		       COALESCE(owner_profile_id::text, ''),
		       source_kind,
		       source_id,
		       source_hash,
		       item_kind
		FROM v2_migration_corpus_items
		WHERE run_id = ?::uuid
		ORDER BY source_kind, source_id
	`, runID).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()

	hash := sha256.New()
	writeV2MigrationCorpusHashField(hash, "dense-mem.v2.migration-corpus-hash.v1")
	count := 0
	for rows.Next() {
		var teamID, ownerProfileID, sourceKind, sourceID, sourceHash, itemKind string
		if err := rows.Scan(&teamID, &ownerProfileID, &sourceKind, &sourceID, &sourceHash, &itemKind); err != nil {
			return "", err
		}
		writeV2MigrationCorpusHashField(hash, teamID)
		writeV2MigrationCorpusHashField(hash, ownerProfileID)
		writeV2MigrationCorpusHashField(hash, sourceKind)
		writeV2MigrationCorpusHashField(hash, sourceID)
		writeV2MigrationCorpusHashField(hash, sourceHash)
		writeV2MigrationCorpusHashField(hash, itemKind)
		count++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	writeV2MigrationCorpusHashField(hash, fmt.Sprintf("count:%d", count))
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func writeV2MigrationCorpusHashField(hash interface{ Write([]byte) (int, error) }, value string) {
	_, _ = fmt.Fprintf(hash, "%d:", len(value))
	_, _ = hash.Write([]byte(value))
	_, _ = hash.Write([]byte{'\n'})
}

func refreshV2MigrationRunStatsTx(tx *gorm.DB, runID string, now time.Time) (*domain.V2MigrationRun, error) {
	rows, err := tx.Raw(`
			WITH counts AS (
			    SELECT
			        count(*)::int AS total_items,
			        count(*) FILTER (
			            WHERE outcome IN ('accepted', 'rejected', 'quarantined')
			               OR (
			                   outcome = 'needs_review'
			                   AND placement_item_id IS NOT NULL
			                   AND EXISTS (
			                       SELECT 1
			                       FROM review_tasks AS task
			                       WHERE task.team_id = v2_migration_corpus_items.team_id
			                         AND task.placement_item_id = v2_migration_corpus_items.placement_item_id
			                         AND task.status IN ('open', 'acknowledged')
			                   )
			               )
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
			RETURNING v2_migration_runs.run_id::text,
			          v2_migration_runs.migration_contract_version,
			          v2_migration_runs.corpus_version,
			          v2_migration_runs.source_kind,
			          v2_migration_runs.state,
			          v2_migration_runs.phase,
			          v2_migration_runs.required,
			          v2_migration_runs.preflight_approved,
			          v2_migration_runs.backup_reference,
			          v2_migration_runs.preflight_checks::text,
			          v2_migration_runs.corpus_watermark,
			          v2_migration_runs.corpus_hash,
			          v2_migration_runs.total_items,
			          v2_migration_runs.completed_items,
			          v2_migration_runs.failed_items,
			          v2_migration_runs.excluded_items,
			          v2_migration_runs.last_error,
			          v2_migration_runs.retryable,
			          v2_migration_runs.lease_owner,
			          v2_migration_runs.checkpoint_key,
			          v2_migration_runs.checkpoint_value::text,
			          v2_migration_runs.claim_epoch,
			          v2_migration_runs.started_at,
			          v2_migration_runs.completed_at,
			          v2_migration_runs.cutover_at,
			          v2_migration_runs.created_at,
			          v2_migration_runs.updated_at
	`, runID, v2MigrationTime(now), runID).Rows()
	if err != nil {
		return nil, err
	}
	return scanV2MigrationRunRows(rows)
}

func v2MigrationCursorDone(tx *gorm.DB, runID string) (bool, error) {
	var done bool
	err := tx.Raw(`
		SELECT COALESCE(checkpoint_value->>'done', 'false') = 'true'
		FROM v2_migration_checkpoints
		WHERE run_id = ?::uuid
		  AND checkpoint_key = ?
	`, runID, domain.V2MigrationCheckpointLegacyNeo4jCursor).Scan(&done).Error
	return done, err
}

func v2MigrationCutoverBlockCounts(tx *gorm.DB, runID string) (pendingItems int, failedItems int, blockingExclusions int, err error) {
	row := tx.Raw(`
		SELECT
		    count(*) FILTER (
		        WHERE outcome = 'pending'
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
		    )::int,
		    count(*) FILTER (WHERE outcome = 'failed')::int,
		    (
		        SELECT count(*)::int
		        FROM v2_migration_exclusions
		        WHERE run_id = ?::uuid
		          AND blocks_cutover
		    )
		FROM v2_migration_corpus_items
		WHERE run_id = ?::uuid
	`, runID, runID).Row()
	err = row.Scan(&pendingItems, &failedItems, &blockingExclusions)
	return pendingItems, failedItems, blockingExclusions, err
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

func scanV2MigrationRunRows(rows *sql.Rows) (*domain.V2MigrationRun, error) {
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	record, err := scanV2MigrationRun(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return record, nil
}

func scanV2MigrationCorpusItemRows(rows *sql.Rows) (*domain.V2MigrationCorpusItem, error) {
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	record, err := scanV2MigrationCorpusItem(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return record, nil
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
