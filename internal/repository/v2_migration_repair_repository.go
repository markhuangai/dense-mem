package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *V2MigrationControlRepositoryImpl) AssessMigrationRepair(
	ctx context.Context,
	runID string,
) (*domain.V2MigrationRepairSummary, error) {
	runID = strings.TrimSpace(runID)
	if _, err := uuid.Parse(runID); err != nil {
		return nil, fmt.Errorf("v2 migration repair: run_id is required: %w", err)
	}
	var out *domain.V2MigrationRepairSummary
	err := r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		summary, err := assessV2MigrationRepairTx(tx, runID)
		if err != nil {
			return err
		}
		out = summary
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 migration repair: assess: %w", err)
	}
	return out, nil
}

func (r *V2MigrationControlRepositoryImpl) RepairAndResumeMigration(
	ctx context.Context,
	input V2RepairAndResumeMigrationInput,
) (*domain.V2MigrationRun, error) {
	runID := strings.TrimSpace(input.RunID)
	if _, err := uuid.Parse(runID); err != nil {
		return nil, fmt.Errorf("v2 migration repair: run_id is required: %w", err)
	}
	now := v2MigrationTime(input.Now)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var out *domain.V2MigrationRun
	err := r.withMigrationTx(ctx, func(tx *gorm.DB) error {
		var currentState string
		var claimEpoch int
		row := tx.Raw(`
			SELECT state, claim_epoch
			FROM v2_migration_runs
			WHERE run_id = ?::uuid
			FOR UPDATE
		`, runID).Row()
		if err := row.Scan(&currentState, &claimEpoch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("v2 migration repair: run not found")
			}
			return err
		}
		if currentState != strings.TrimSpace(input.FromState) {
			return fmt.Errorf("v2 migration repair: stale state %s", currentState)
		}
		summary, err := assessV2MigrationRepairTx(tx, runID)
		if err != nil {
			return err
		}
		if summary.BlockedItems > 0 {
			return fmt.Errorf("%w: blocked_items=%d", ErrV2MigrationCutoverBlocked, summary.BlockedItems)
		}
		orphanReviews, err := repairV2MigrationOrphanReviewsTx(tx, runID, claimEpoch, now)
		if err != nil {
			return err
		}
		abandonedProcessing, err := repairV2MigrationAbandonedProcessingTx(tx, runID, claimEpoch, now)
		if err != nil {
			return err
		}
		retryableFailures, err := repairV2MigrationRetryableFailuresTx(tx, runID, claimEpoch, now)
		if err != nil {
			return err
		}
		summary.OrphanReviews = orphanReviews
		summary.AbandonedProcessing = abandonedProcessing
		summary.RetryableFailures = retryableFailures
		summary.RepairedItems = orphanReviews + abandonedProcessing + retryableFailures
		summary.ClaimEpochBefore = claimEpoch
		summary.ClaimEpochAfter = claimEpoch
		if err := insertV2MigrationOperatorActionTx(tx, input.OperatorAction, summary, now); err != nil {
			return err
		}
		rows, err := tx.Raw(`
			UPDATE v2_migration_runs
			SET state = ?,
			    phase = 'migration',
			    last_error = '',
			    retryable = true,
			    started_at = COALESCE(started_at, ?),
			    completed_at = NULL,
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
		`, domain.V2MigrationStateRunning, now, now, runID, input.FromState).Rows()
		if err != nil {
			return err
		}
		run, err := scanV2MigrationRunRows(rows)
		if err != nil {
			return err
		}
		out = run
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 migration repair: resume: %w", err)
	}
	return out, nil
}

func assessV2MigrationRepairTx(tx *gorm.DB, runID string) (*domain.V2MigrationRepairSummary, error) {
	summary := &domain.V2MigrationRepairSummary{}
	row := tx.Raw(`
		WITH target_run AS (
		    SELECT run_id, claim_epoch
		    FROM v2_migration_runs
		    WHERE run_id = ?::uuid
		)
		SELECT
		    COALESCE((SELECT claim_epoch FROM target_run), 0)::int AS claim_epoch,
		    (
		        SELECT count(*)::int
		        FROM v2_migration_corpus_items AS corpus
		        JOIN placement_items AS item
		          ON item.team_id = corpus.team_id
		         AND item.ingest_id = corpus.ingest_id
		         AND item.evidence_index = 0
		        LEFT JOIN review_tasks AS task
		          ON task.team_id = item.team_id
		         AND task.placement_item_id = item.placement_item_id
		         AND task.status IN ('open', 'acknowledged')
		        WHERE corpus.run_id = ?::uuid
		          AND corpus.outcome = 'needs_review'
		          AND item.status <> 'awaiting_review'
		          AND task.review_task_id IS NULL
		    ) AS orphan_reviews,
		    (
		        SELECT count(*)::int
		        FROM v2_migration_corpus_items AS corpus
		        JOIN placement_runs AS run
		          ON run.team_id = corpus.team_id
		         AND run.ingest_id = corpus.ingest_id
		        WHERE corpus.run_id = ?::uuid
		          AND run.status = 'processing'
		    ) AS abandoned_processing,
		    (
		        SELECT count(*)::int
		        FROM v2_migration_corpus_items AS corpus
		        JOIN placement_runs AS run
		          ON run.team_id = corpus.team_id
		         AND run.ingest_id = corpus.ingest_id
		        JOIN placement_items AS item
		          ON item.team_id = run.team_id
		         AND item.placement_run_id = run.placement_run_id
		         AND item.evidence_index = 0
		        WHERE corpus.run_id = ?::uuid
		          AND (corpus.outcome = 'failed' OR run.status = 'failed' OR item.status = 'failed')
		          AND run.attempts < run.max_attempts
		    ) AS retryable_failures,
		    (
		        SELECT count(*)::int
		        FROM v2_migration_corpus_items AS corpus
		        JOIN placement_items AS item
		          ON item.team_id = corpus.team_id
		         AND item.ingest_id = corpus.ingest_id
		         AND item.evidence_index = 0
		        JOIN review_tasks AS task
		          ON task.team_id = item.team_id
		         AND task.placement_item_id = item.placement_item_id
		         AND task.status IN ('open', 'acknowledged')
		        WHERE corpus.run_id = ?::uuid
		    ) AS held_reviews,
		    (
		        SELECT count(*)::int
		        FROM v2_migration_corpus_items AS corpus
		        JOIN placement_runs AS run
		          ON run.team_id = corpus.team_id
		         AND run.ingest_id = corpus.ingest_id
		        JOIN placement_items AS item
		          ON item.team_id = run.team_id
		         AND item.placement_run_id = run.placement_run_id
		         AND item.evidence_index = 0
		        WHERE corpus.run_id = ?::uuid
		          AND (corpus.outcome = 'failed' OR run.status = 'failed' OR item.status = 'failed')
		          AND run.attempts >= run.max_attempts
		    ) AS blocked_items
	`, runID, runID, runID, runID, runID, runID).Row()
	if err := row.Scan(
		&summary.ClaimEpochBefore,
		&summary.OrphanReviews,
		&summary.AbandonedProcessing,
		&summary.RetryableFailures,
		&summary.HeldReviews,
		&summary.BlockedItems,
	); err != nil {
		return nil, err
	}
	if summary.ClaimEpochBefore == 0 {
		return nil, sql.ErrNoRows
	}
	summary.ClaimEpochAfter = summary.ClaimEpochBefore
	summary.Required = summary.OrphanReviews > 0 || summary.AbandonedProcessing > 0 || summary.RetryableFailures > 0
	return summary, nil
}

func repairV2MigrationOrphanReviewsTx(tx *gorm.DB, runID string, claimEpoch int, now time.Time) (int, error) {
	var repaired int
	err := tx.Raw(`
		WITH target AS (
		    SELECT corpus.item_id, corpus.team_id, corpus.ingest_id,
		           item.placement_item_id, item.placement_run_id, item.owner_profile_id
		    FROM v2_migration_corpus_items AS corpus
		    JOIN placement_items AS item
		      ON item.team_id = corpus.team_id
		     AND item.ingest_id = corpus.ingest_id
		     AND item.evidence_index = 0
		    LEFT JOIN review_tasks AS task
		      ON task.team_id = item.team_id
		     AND task.placement_item_id = item.placement_item_id
		     AND task.status IN ('open', 'acknowledged')
		    WHERE corpus.run_id = ?::uuid
		      AND corpus.outcome = 'needs_review'
		      AND item.status <> 'awaiting_review'
		      AND task.review_task_id IS NULL
		    FOR UPDATE OF corpus, item
		), outcomes AS (
		    INSERT INTO placement_outcomes (
		        team_id, placement_run_id, placement_item_id, owner_profile_id,
		        outcome_kind, status, idempotency_key, payload, created_at
		    )
		    SELECT team_id, placement_run_id, placement_item_id, owner_profile_id,
		           'migration_repair_requeued', 'retryable',
		           'migration_repair:' || ? || ':' || placement_item_id::text || ':orphan_review:' || ?::text,
		           jsonb_build_object(
		               'contract_version', ?,
		               'reason', 'orphan_review_without_open_task',
		               'migration_run_id', ?,
		               'claim_epoch', ?,
		               'repaired_at', ?::text
		           ),
		           ?
		    FROM target
		    ON CONFLICT (team_id, owner_profile_id, idempotency_key)
		    WHERE idempotency_key <> ''
		    DO NOTHING
		), updated_items AS (
		    UPDATE placement_items AS item
		    SET status = 'queued',
		        category = 'pending',
		        result = '{}'::jsonb,
		        error = '',
		        version = version + 1,
		        updated_at = ?
		    FROM target
		    WHERE item.team_id = target.team_id
		      AND item.placement_item_id = target.placement_item_id
		    RETURNING target.team_id, target.placement_run_id, target.owner_profile_id, target.item_id
		), updated_runs AS (
		    UPDATE placement_runs
		    SET status = `+v2PlacementRunGuardedStatusCase+`,
		        attempts = 0,
		        migration_claim_epoch = ?,
		        worker_id = '',
		        lease_until = NULL,
		        available_at = ?,
		        completed_at = NULL,
		        updated_at = ?
		    FROM (SELECT DISTINCT team_id, placement_run_id, owner_profile_id FROM updated_items) AS target
		    WHERE placement_runs.team_id = target.team_id
		      AND placement_runs.placement_run_id = target.placement_run_id
		      AND placement_runs.owner_profile_id = target.owner_profile_id
		), updated_corpus AS (
		    UPDATE v2_migration_corpus_items AS corpus
		    SET outcome = 'pending',
		        placement_item_id = NULL,
		        metadata = metadata || jsonb_build_object(
		            'migration_repair', 'orphan_review_requeued',
		            'migration_repair_at', ?::text
		        ),
		        updated_at = ?
		    FROM updated_items
		    WHERE corpus.item_id = updated_items.item_id
		    RETURNING 1
		)
		SELECT count(*)::int FROM updated_corpus
	`, runID, runID, claimEpoch, domain.V2ContractVersion, runID, claimEpoch, now.UTC().Format(time.RFC3339Nano),
		now, now, claimEpoch, now, now, now.UTC().Format(time.RFC3339Nano), now).Scan(&repaired).Error
	return repaired, err
}

func repairV2MigrationAbandonedProcessingTx(tx *gorm.DB, runID string, claimEpoch int, now time.Time) (int, error) {
	var repaired int
	err := tx.Raw(`
		WITH target AS (
		    SELECT corpus.item_id, corpus.team_id, corpus.ingest_id,
		           item.placement_item_id, item.placement_run_id, item.owner_profile_id
		    FROM v2_migration_corpus_items AS corpus
		    JOIN placement_runs AS run
		      ON run.team_id = corpus.team_id
		     AND run.ingest_id = corpus.ingest_id
		    JOIN placement_items AS item
		      ON item.team_id = run.team_id
		     AND item.placement_run_id = run.placement_run_id
		     AND item.evidence_index = 0
		    WHERE corpus.run_id = ?::uuid
		      AND run.status = 'processing'
		    FOR UPDATE OF corpus, run, item
		), outcomes AS (
		    INSERT INTO placement_outcomes (
		        team_id, placement_run_id, placement_item_id, owner_profile_id,
		        outcome_kind, status, idempotency_key, payload, created_at
		    )
		    SELECT team_id, placement_run_id, placement_item_id, owner_profile_id,
		           'migration_repair_requeued', 'retryable',
		           'migration_repair:' || ? || ':' || placement_item_id::text || ':abandoned_processing:' || ?::text,
		           jsonb_build_object(
		               'contract_version', ?,
		               'reason', 'abandoned_processing_after_pause_or_restart',
		               'migration_run_id', ?,
		               'claim_epoch', ?,
		               'repaired_at', ?::text
		           ),
		           ?
		    FROM target
		    ON CONFLICT (team_id, owner_profile_id, idempotency_key)
		    WHERE idempotency_key <> ''
		    DO NOTHING
		), updated_items AS (
		    UPDATE placement_items AS item
		    SET status = CASE WHEN item.status = 'processing' THEN 'queued' ELSE item.status END,
		        updated_at = ?
		    FROM target
		    WHERE item.team_id = target.team_id
		      AND item.placement_item_id = target.placement_item_id
		    RETURNING target.team_id, target.placement_run_id, target.owner_profile_id, target.item_id
		), updated_runs AS (
		    UPDATE placement_runs
		    SET status = `+v2PlacementRunGuardedStatusCase+`,
		        migration_claim_epoch = ?,
		        worker_id = '',
		        lease_until = NULL,
		        available_at = ?,
		        completed_at = NULL,
		        updated_at = ?
		    FROM (SELECT DISTINCT team_id, placement_run_id, owner_profile_id FROM updated_items) AS target
		    WHERE placement_runs.team_id = target.team_id
		      AND placement_runs.placement_run_id = target.placement_run_id
		      AND placement_runs.owner_profile_id = target.owner_profile_id
		), updated_corpus AS (
		    UPDATE v2_migration_corpus_items AS corpus
		    SET outcome = 'pending',
		        placement_item_id = NULL,
		        metadata = metadata || jsonb_build_object(
		            'migration_repair', 'abandoned_processing_requeued',
		            'migration_repair_at', ?::text
		        ),
		        updated_at = ?
		    FROM updated_items
		    WHERE corpus.item_id = updated_items.item_id
		    RETURNING 1
		)
		SELECT count(*)::int FROM updated_corpus
	`, runID, runID, claimEpoch, domain.V2ContractVersion, runID, claimEpoch, now.UTC().Format(time.RFC3339Nano),
		now, now, claimEpoch, now, now, now.UTC().Format(time.RFC3339Nano), now).Scan(&repaired).Error
	return repaired, err
}

func repairV2MigrationRetryableFailuresTx(tx *gorm.DB, runID string, claimEpoch int, now time.Time) (int, error) {
	var repaired int
	err := tx.Raw(`
		WITH target AS (
		    SELECT corpus.item_id, corpus.team_id, corpus.ingest_id,
		           item.placement_item_id, item.placement_run_id, item.owner_profile_id
		    FROM v2_migration_corpus_items AS corpus
		    JOIN placement_runs AS run
		      ON run.team_id = corpus.team_id
		     AND run.ingest_id = corpus.ingest_id
		    JOIN placement_items AS item
		      ON item.team_id = run.team_id
		     AND item.placement_run_id = run.placement_run_id
		     AND item.evidence_index = 0
		    WHERE corpus.run_id = ?::uuid
		      AND (corpus.outcome = 'failed' OR run.status = 'failed' OR item.status = 'failed')
		      AND run.attempts < run.max_attempts
		    FOR UPDATE OF corpus, run, item
		), outcomes AS (
		    INSERT INTO placement_outcomes (
		        team_id, placement_run_id, placement_item_id, owner_profile_id,
		        outcome_kind, status, idempotency_key, payload, created_at
		    )
		    SELECT team_id, placement_run_id, placement_item_id, owner_profile_id,
		           'migration_repair_requeued', 'retryable',
		           'migration_repair:' || ? || ':' || placement_item_id::text || ':retryable_failure:' || ?::text,
		           jsonb_build_object(
		               'contract_version', ?,
		               'reason', 'retryable_failure_requeued',
		               'migration_run_id', ?,
		               'claim_epoch', ?,
		               'repaired_at', ?::text
		           ),
		           ?
		    FROM target
		    ON CONFLICT (team_id, owner_profile_id, idempotency_key)
		    WHERE idempotency_key <> ''
		    DO NOTHING
		), updated_items AS (
		    UPDATE placement_items AS item
		    SET status = 'queued',
		        category = 'pending',
		        result = '{}'::jsonb,
		        error = '',
		        version = version + 1,
		        updated_at = ?
		    FROM target
		    WHERE item.team_id = target.team_id
		      AND item.placement_item_id = target.placement_item_id
		    RETURNING target.team_id, target.placement_run_id, target.owner_profile_id, target.item_id
		), updated_runs AS (
		    UPDATE placement_runs
		    SET status = `+v2PlacementRunGuardedStatusCase+`,
		        attempts = 0,
		        migration_claim_epoch = ?,
		        error = '',
		        worker_id = '',
		        lease_until = NULL,
		        available_at = ?,
		        completed_at = NULL,
		        updated_at = ?
		    FROM (SELECT DISTINCT team_id, placement_run_id, owner_profile_id FROM updated_items) AS target
		    WHERE placement_runs.team_id = target.team_id
		      AND placement_runs.placement_run_id = target.placement_run_id
		      AND placement_runs.owner_profile_id = target.owner_profile_id
		), updated_corpus AS (
		    UPDATE v2_migration_corpus_items AS corpus
		    SET outcome = 'pending',
		        placement_item_id = NULL,
		        exclusion_reason = '',
		        metadata = metadata || jsonb_build_object(
		            'migration_repair', 'retryable_failure_requeued',
		            'migration_repair_at', ?::text
		        ),
		        updated_at = ?
		    FROM updated_items
		    WHERE corpus.item_id = updated_items.item_id
		    RETURNING 1
		)
		SELECT count(*)::int FROM updated_corpus
	`, runID, runID, claimEpoch, domain.V2ContractVersion, runID, claimEpoch, now.UTC().Format(time.RFC3339Nano),
		now, now, claimEpoch, now, now, now.UTC().Format(time.RFC3339Nano), now).Scan(&repaired).Error
	return repaired, err
}

func insertV2MigrationOperatorActionTx(
	tx *gorm.DB,
	action domain.V2MigrationOperatorAction,
	repair *domain.V2MigrationRepairSummary,
	now time.Time,
) error {
	if action.Action == "" {
		return nil
	}
	metadata := map[string]any{}
	for key, value := range action.Metadata {
		metadata[key] = value
	}
	if repair != nil {
		metadata["repair"] = map[string]any{
			"required":             repair.Required,
			"orphan_reviews":       repair.OrphanReviews,
			"abandoned_processing": repair.AbandonedProcessing,
			"retryable_failures":   repair.RetryableFailures,
			"held_reviews":         repair.HeldReviews,
			"blocked_items":        repair.BlockedItems,
			"repaired_items":       repair.RepairedItems,
			"claim_epoch_before":   repair.ClaimEpochBefore,
			"claim_epoch_after":    repair.ClaimEpochAfter,
		}
	}
	data, err := marshalV2MigrationJSON(metadata)
	if err != nil {
		return err
	}
	return tx.Exec(`
		INSERT INTO v2_migration_operator_actions (
		    action_id, run_id, action, actor, remote_ip, reason, metadata, created_at
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?, ?, ?, ?::jsonb, ?
		)
	`, uuid.NewString(), action.RunID, action.Action, action.Actor, action.RemoteIP,
		action.Reason, string(data), now).Error
}
