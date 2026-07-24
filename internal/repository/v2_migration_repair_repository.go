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

const v2MigrationRetryExhaustedValidationError = "placement_attempts: retryable semantic review exhausted placement attempts"

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
		if summary.BlockedItems > 0 || summary.BlockingExclusions > 0 {
			return fmt.Errorf("%w: blocked_items=%d blocking_exclusions=%d",
				ErrV2MigrationCutoverBlocked,
				summary.BlockedItems,
				summary.BlockingExclusions,
			)
		}
		legacyPredicateItems, err := prepareV2MigrationLegacyPredicateReviewsTx(tx, runID, now)
		if err != nil {
			return err
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
		summary.OrphanReviews = max(0, orphanReviews-legacyPredicateItems)
		summary.AbandonedProcessing = abandonedProcessing
		summary.RetryableFailures = retryableFailures
		summary.RepairedItems = orphanReviews + abandonedProcessing + retryableFailures
		summary.ClaimEpochBefore = claimEpoch
		summary.ClaimEpochAfter = claimEpoch
		operatorAction := input.OperatorAction
		operatorAction.Action = domain.V2MigrationActionResumed
		if summary.Required {
			operatorAction.Action = domain.V2MigrationActionRepairResumed
		}
		if err := insertV2MigrationOperatorActionTx(tx, operatorAction, summary, now); err != nil {
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
		WITH corpus_runs AS (
		    SELECT corpus.outcome, corpus.team_id, corpus.ingest_id,
		           run.placement_run_id, run.status AS run_status,
		           run.attempts, run.max_attempts
		    FROM v2_migration_corpus_items AS corpus
		    JOIN placement_runs AS run
		      ON run.team_id = corpus.team_id
		     AND run.ingest_id = corpus.ingest_id
		    WHERE corpus.run_id = ?::uuid
		), corpus_items AS (
		    SELECT corpus_runs.*,
		           item.placement_item_id,
		           item.status AS item_status,
		           COALESCE(review.open_review_count, 0)::int AS open_review_count,
		           COALESCE(review.legacy_predicate_review_count, 0)::int AS legacy_predicate_review_count,
		           COALESCE((
		               SELECT terminal.payload->>'retryable_exhausted' = 'true'
		                   OR terminal.payload @> jsonb_build_object(
		                          'validation_errors',
		                          jsonb_build_array(?::text)
		                      )
		               FROM placement_outcomes AS terminal
		               WHERE terminal.team_id = item.team_id
		                 AND terminal.placement_run_id = item.placement_run_id
		                 AND terminal.placement_item_id = item.placement_item_id
		                 AND terminal.outcome_kind = 'semantic_review_terminal'
		                 AND terminal.status = 'terminal_failure'
		               ORDER BY terminal.created_at DESC, terminal.outcome_id DESC
		               LIMIT 1
		           ), false) AS exhausted_retryable
		    FROM corpus_runs
		    JOIN placement_items AS item
		      ON item.team_id = corpus_runs.team_id
		     AND item.placement_run_id = corpus_runs.placement_run_id
		     AND item.evidence_index = 0
		    LEFT JOIN LATERAL (
		        SELECT count(*)::int AS open_review_count,
		               count(*) FILTER (
		                   WHERE task.task_type = 'predicate_needs_review'
		                     AND COALESCE(task.payload->>'predicate_policy_version', '') = ''
		               )::int AS legacy_predicate_review_count
		        FROM review_tasks AS task
		        WHERE task.team_id = item.team_id
		          AND task.placement_item_id = item.placement_item_id
		          AND task.status IN ('open', 'acknowledged')
		    ) AS review ON true
		), run_counts AS (
		    SELECT count(*) FILTER (
		        WHERE run_status = 'processing'
		    )::int AS abandoned_processing
		    FROM corpus_runs
		), exclusion_counts AS (
		    SELECT count(*)::int AS blocking_exclusions
		    FROM v2_migration_exclusions
		    WHERE run_id = ?::uuid
		      AND blocks_cutover
		), item_counts AS (
		    SELECT
		        COALESCE(sum(legacy_predicate_review_count), 0)::int AS legacy_predicate_reviews,
		        count(*) FILTER (
		            WHERE outcome = 'needs_review'
		              AND item_status <> 'awaiting_review'
		              AND open_review_count = 0
		        )::int AS orphan_reviews,
		        count(*) FILTER (
		            WHERE (outcome = 'failed' OR run_status = 'failed' OR item_status = 'failed')
		              AND legacy_predicate_review_count = 0
		              AND (attempts < max_attempts OR exhausted_retryable)
		        )::int AS retryable_failures,
		        COALESCE(sum(open_review_count - legacy_predicate_review_count), 0)::int AS held_reviews,
		        count(*) FILTER (
		            WHERE (outcome = 'failed' OR run_status = 'failed' OR item_status = 'failed')
		              AND legacy_predicate_review_count = 0
		              AND attempts >= max_attempts
		              AND NOT exhausted_retryable
		        )::int AS blocked_items
		    FROM corpus_items
		)
		SELECT
		    migration.claim_epoch::int,
		    item_counts.legacy_predicate_reviews,
		    item_counts.orphan_reviews,
		    run_counts.abandoned_processing,
		    item_counts.retryable_failures,
		    item_counts.held_reviews,
		    item_counts.blocked_items,
		    exclusion_counts.blocking_exclusions
		FROM v2_migration_runs AS migration
		CROSS JOIN run_counts
		CROSS JOIN exclusion_counts
		CROSS JOIN item_counts
		WHERE migration.run_id = ?::uuid
	`, runID, v2MigrationRetryExhaustedValidationError, runID, runID).Row()
	if err := row.Scan(
		&summary.ClaimEpochBefore,
		&summary.LegacyPredicateReviews,
		&summary.OrphanReviews,
		&summary.AbandonedProcessing,
		&summary.RetryableFailures,
		&summary.HeldReviews,
		&summary.BlockedItems,
		&summary.BlockingExclusions,
	); err != nil {
		return nil, err
	}
	if summary.ClaimEpochBefore == 0 {
		return nil, sql.ErrNoRows
	}
	summary.ClaimEpochAfter = summary.ClaimEpochBefore
	summary.Required = summary.LegacyPredicateReviews > 0 || summary.OrphanReviews > 0 ||
		summary.AbandonedProcessing > 0 || summary.RetryableFailures > 0
	groups, err := v2MigrationRepairFailureGroupsTx(tx, runID)
	if err != nil {
		return nil, err
	}
	summary.FailureGroups = groups
	return summary, nil
}

func v2MigrationRepairFailureGroupsTx(tx *gorm.DB, runID string) ([]domain.V2MigrationFailureGroup, error) {
	rows, err := tx.Raw(`
		WITH failed_items AS (
		    SELECT item.team_id, item.placement_run_id, item.placement_item_id
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
		), latest_terminal AS (
		    SELECT failed_items.placement_item_id,
		           COALESCE(NULLIF(terminal.payload->>'failure_stage', ''), 'unknown') AS failure_stage,
		           COALESCE(NULLIF(terminal.payload->>'failure_class', ''), 'unknown') AS failure_class
		    FROM failed_items
		    LEFT JOIN LATERAL (
		        SELECT payload
		        FROM placement_outcomes AS terminal
		        WHERE terminal.team_id = failed_items.team_id
		          AND terminal.placement_run_id = failed_items.placement_run_id
		          AND terminal.placement_item_id = failed_items.placement_item_id
		          AND terminal.outcome_kind = 'semantic_review_terminal'
		          AND terminal.status = 'terminal_failure'
		        ORDER BY terminal.created_at DESC, terminal.outcome_id DESC
		        LIMIT 1
		    ) AS terminal ON true
		)
		SELECT failure_stage, failure_class, count(*)::int
		FROM latest_terminal
		GROUP BY failure_stage, failure_class
		ORDER BY count(*) DESC, failure_stage ASC, failure_class ASC
	`, runID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.V2MigrationFailureGroup{}
	for rows.Next() {
		var group domain.V2MigrationFailureGroup
		if err := rows.Scan(&group.Stage, &group.Class, &group.Count); err != nil {
			return nil, err
		}
		out = append(out, group)
	}
	return out, rows.Err()
}

func prepareV2MigrationLegacyPredicateReviewsTx(tx *gorm.DB, runID string, now time.Time) (int, error) {
	var prepared int
	err := tx.Raw(`
		WITH target AS MATERIALIZED (
		    SELECT corpus.item_id, corpus.team_id, item.placement_item_id
		    FROM v2_migration_corpus_items AS corpus
		    JOIN placement_items AS item
		      ON item.team_id = corpus.team_id
		     AND item.ingest_id = corpus.ingest_id
		     AND item.evidence_index = 0
		    WHERE corpus.run_id = ?::uuid
		      AND EXISTS (
		          SELECT 1
		          FROM review_tasks AS task
		          WHERE task.team_id = item.team_id
		            AND task.placement_item_id = item.placement_item_id
		            AND task.status IN ('open', 'acknowledged')
		            AND task.task_type = 'predicate_needs_review'
		            AND COALESCE(task.payload->>'predicate_policy_version', '') = ''
		      )
		    FOR UPDATE OF corpus, item
		), canceled_tasks AS (
		    UPDATE review_tasks AS task
		    SET status = 'canceled',
		        resolution = task.resolution || jsonb_build_object(
		            'action', 'superseded_by_predicate_policy_replay',
		            'predicate_policy_version', ?::text,
		            'migration_run_id', ?::text,
		            'repaired_at', ?::text
		        ),
		        resolved_at = ?,
		        updated_at = ?
		    FROM target
		    WHERE task.team_id = target.team_id
		      AND task.placement_item_id = target.placement_item_id
		      AND task.status IN ('open', 'acknowledged')
		      AND task.task_type = 'predicate_needs_review'
		      AND COALESCE(task.payload->>'predicate_policy_version', '') = ''
		    RETURNING task.review_task_id
		), updated_items AS (
		    UPDATE placement_items AS item
		    SET status = CASE
		            WHEN EXISTS (
		                 SELECT 1
		                 FROM review_tasks AS remaining
		                 WHERE remaining.team_id = item.team_id
		                   AND remaining.placement_item_id = item.placement_item_id
		                   AND remaining.status IN ('open', 'acknowledged')
		                   AND NOT (
		                       remaining.task_type = 'predicate_needs_review'
		                       AND COALESCE(remaining.payload->>'predicate_policy_version', '') = ''
		                   )
		             )
		            THEN 'awaiting_review'
		            WHEN item.status = 'awaiting_review' THEN 'completed'
		            ELSE item.status
		        END,
		        updated_at = ?
		    FROM target
		    WHERE item.team_id = target.team_id
		      AND item.placement_item_id = target.placement_item_id
		    RETURNING item.placement_item_id
		), updated_corpus AS (
		    UPDATE v2_migration_corpus_items AS corpus
		    SET outcome = 'needs_review',
		        metadata = metadata || jsonb_build_object(
		            'migration_repair', 'legacy_predicate_review_detected',
		            'predicate_policy_version', ?::text,
		            'migration_repair_at', ?::text
		        ),
		        updated_at = ?
		    FROM target
		    WHERE corpus.item_id = target.item_id
		    RETURNING corpus.item_id
		)
		SELECT count(*)::int
		FROM target
	`, runID, domain.V2PredicatePolicyVersion, runID, now.UTC().Format(time.RFC3339Nano), now, now,
		now, domain.V2PredicatePolicyVersion, now.UTC().Format(time.RFC3339Nano), now,
	).Scan(&prepared).Error
	return prepared, err
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
		           'migration_repair:' || ? || ':' || placement_item_id::text || ':orphan_review:' || (?::int)::text,
		           jsonb_build_object(
		               'contract_version', ?::text,
		               'reason', 'orphan_review_without_open_task',
		               'migration_run_id', ?::text,
		               'claim_epoch', ?::int,
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
		           'migration_repair:' || ? || ':' || placement_item_id::text || ':abandoned_processing:' || (?::int)::text,
		           jsonb_build_object(
		               'contract_version', ?::text,
		               'reason', 'abandoned_processing_after_pause_or_restart',
		               'migration_run_id', ?::text,
		               'claim_epoch', ?::int,
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
		      AND (
		          run.attempts < run.max_attempts
		          OR COALESCE((
		              SELECT terminal.payload->>'retryable_exhausted' = 'true'
		                  OR terminal.payload @> jsonb_build_object(
		                         'validation_errors',
		                         jsonb_build_array(?::text)
		                     )
		              FROM placement_outcomes AS terminal
		              WHERE terminal.team_id = item.team_id
		                AND terminal.placement_run_id = item.placement_run_id
		                AND terminal.placement_item_id = item.placement_item_id
		                AND terminal.outcome_kind = 'semantic_review_terminal'
		                AND terminal.status = 'terminal_failure'
		              ORDER BY terminal.created_at DESC, terminal.outcome_id DESC
		              LIMIT 1
		          ), false)
		      )
		    FOR UPDATE OF corpus, run, item
		), outcomes AS (
		    INSERT INTO placement_outcomes (
		        team_id, placement_run_id, placement_item_id, owner_profile_id,
		        outcome_kind, status, idempotency_key, payload, created_at
		    )
		    SELECT team_id, placement_run_id, placement_item_id, owner_profile_id,
		           'migration_repair_requeued', 'retryable',
		           'migration_repair:' || ? || ':' || placement_item_id::text || ':retryable_failure:' || (?::int)::text,
		           jsonb_build_object(
		               'contract_version', ?::text,
		               'reason', 'retryable_failure_requeued',
		               'migration_run_id', ?::text,
		               'claim_epoch', ?::int,
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
	`, runID, v2MigrationRetryExhaustedValidationError,
		runID, claimEpoch, domain.V2ContractVersion, runID, claimEpoch, now.UTC().Format(time.RFC3339Nano),
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
			"required":                 repair.Required,
			"legacy_predicate_reviews": repair.LegacyPredicateReviews,
			"orphan_reviews":           repair.OrphanReviews,
			"abandoned_processing":     repair.AbandonedProcessing,
			"retryable_failures":       repair.RetryableFailures,
			"held_reviews":             repair.HeldReviews,
			"blocked_items":            repair.BlockedItems,
			"blocking_exclusions":      repair.BlockingExclusions,
			"failure_groups":           repair.FailureGroups,
			"repaired_items":           repair.RepairedItems,
			"claim_epoch_before":       repair.ClaimEpochBefore,
			"claim_epoch_after":        repair.ClaimEpochAfter,
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
