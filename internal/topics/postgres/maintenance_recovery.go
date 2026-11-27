package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/lib/pq"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

type maintenanceRecoveryBatch struct {
	ID, RunID, WindowID, TeamID, SpaceID, LeaseToken string
	Generation                                       int64
	LeaseUntil                                       time.Time
	CreatedAt                                        time.Time
	Sources, Revisions, Body                         []byte
}

func maintenanceFinalizationError(ctx context.Context, claim topic.MaintenanceClaim, phase string, started time.Time, err error) error {
	if err == nil {
		return nil
	}
	return &topic.MaintenanceFinalizationError{BatchID: claim.ID, RunID: claim.RunID, Phase: phase,
		Duration: time.Since(started), Cause: errors.Join(err, ctx.Err())}
}

func (s *Store) recoverMaintenanceBatches(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	started, phase := time.Now(), "recovery_candidates"
	var claim topic.MaintenanceClaim
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		var batches []maintenanceRecoveryBatch
		if err := tx.Raw(`SELECT batch.batch_id::text AS id,batch.run_id::text,batch.window_id::text,batch.team_id::text,
		 batch.shared_space_id::text AS space_id,batch.space_generation AS generation,batch.lease_token::text,batch.lease_until,batch.created_at,batch.sources,batch.revisions,assessment.body
		 FROM topic_maintenance_batches AS batch JOIN historical_organization_assessments AS assessment
		 ON assessment.team_id=batch.team_id AND assessment.shared_space_id=batch.shared_space_id AND assessment.space_generation=batch.space_generation AND assessment.operation_key='maintenance:'||batch.batch_id::text
		 JOIN memory_spaces AS space ON space.team_id=batch.team_id AND space.id=batch.shared_space_id AND space.generation=batch.space_generation AND space.lifecycle_state='active'
		 JOIN teams AS team ON team.id=batch.team_id AND team.status='active' AND team.deleted_at IS NULL
		 WHERE batch.batch_kind='assignment' AND batch.status='running' AND batch.lease_until>? AND batch.lease_until>clock_timestamp()
		 AND ((COALESCE(assessment.body->'result'->>'failure_code','')='' AND assessment.body->'result'->>'current'='true')
		 OR (COALESCE(assessment.body->'result'->>'failure_code','')<>'' AND assessment.body->'result'->>'current'='false'))
		 AND NOT EXISTS(SELECT 1 FROM topic_maintenance_attempts AS attempt WHERE attempt.batch_id=batch.batch_id AND attempt.reconciled_at IS NULL)
		 ORDER BY batch.batch_id LIMIT ? FOR UPDATE OF batch SKIP LOCKED`, now, topic.MaintenancePageSize).Scan(&batches).Error; err != nil {
			return err
		}
		if len(batches) == 0 {
			return nil
		}
		runIDs := make([]string, 0, len(batches))
		for _, batch := range batches {
			runIDs = append(runIDs, batch.RunID)
		}
		phase = "recovery_run_locks"
		var lockedRuns []string
		if err := tx.Raw(`SELECT run_id::text FROM topic_maintenance_runs WHERE run_id=ANY(?::uuid[])
			ORDER BY run_id FOR NO KEY UPDATE SKIP LOCKED`, pq.Array(runIDs)).Scan(&lockedRuns).Error; err != nil {
			return err
		}
		locked := make(map[string]bool, len(lockedRuns))
		for _, runID := range lockedRuns {
			locked[runID] = true
		}
		sort.SliceStable(batches, func(left, right int) bool {
			return batches[left].CreatedAt.Before(batches[right].CreatedAt)
		})
		for _, batch := range batches {
			if !locked[batch.RunID] {
				continue
			}
			claim = topic.MaintenanceClaim{ID: batch.ID, Kind: "assignment", RunID: batch.RunID, WindowID: batch.WindowID, TeamID: batch.TeamID,
				SpaceID: batch.SpaceID, Generation: batch.Generation, LeaseToken: batch.LeaseToken, LeaseUntil: batch.LeaseUntil}
			phase = "recovery_receipt"
			if err := json.Unmarshal(batch.Sources, &claim.Sources); err != nil {
				return err
			}
			if err := json.Unmarshal(batch.Revisions, &claim.Revisions); err != nil {
				return err
			}
			receipt, err := decodeOrganization(batch.Body)
			if err != nil {
				return err
			}
			if err := validateMaintenanceReceipt(claim, receipt); err != nil {
				return err
			}
			phase = "recovery_input_locks"
			locked, err := lockMaintenanceRecoveryInputs(tx, claim)
			if err != nil {
				return err
			}
			if !locked {
				continue
			}
			phase = "recovery_savepoint"
			// GORM's PostgreSQL savepoint helpers discard execution errors.
			if err := tx.Exec("SAVEPOINT maintenance_recovery_candidate").Error; err != nil {
				return err
			}
			err = completeMaintenanceBatch(tx, claim, receipt.Result, receipt.Result.FailureCode, now, &phase)
			if err == topic.ErrLeaseLost {
				phase = "recovery_rollback"
				err = tx.Exec("ROLLBACK TO SAVEPOINT maintenance_recovery_candidate").Error
			}
			if err != nil {
				return err
			}
			phase = "recovery_release"
			if err := tx.Exec("RELEASE SAVEPOINT maintenance_recovery_candidate").Error; err != nil {
				return err
			}
		}
		phase = "recovery_commit"
		return nil
	})
	return maintenanceFinalizationError(ctx, claim, phase, started, err)
}

func validateMaintenanceReceipt(claim topic.MaintenanceClaim, receipt topic.OrganizationReceipt) error {
	input, err := topic.PrepareOrganizationInput(topic.OrganizationInput{OperationKey: "maintenance:" + claim.ID, Sources: claim.Sources})
	if err != nil {
		return err
	}
	if receipt.OperationKey != input.OperationKey || len(receipt.Sources) != len(input.Sources) || len(claim.Revisions) != len(input.Sources) {
		return topic.ErrInvalid
	}
	versions := map[string]topic.SourceHandle{}
	for _, source := range receipt.Sources {
		versions[topic.SourceKey(source.SourceHandle)] = source.SourceHandle
	}
	for _, source := range input.Sources {
		key := topic.SourceKey(source)
		if versions[key] != source || claim.Revisions[key] < 1 {
			return topic.ErrInvalid
		}
	}
	return nil
}

func lockMaintenanceRecoveryInputs(tx *gorm.DB, claim topic.MaintenanceClaim) (bool, error) {
	fence := scope{TeamID: claim.TeamID, SpaceID: claim.SpaceID, Generation: claim.Generation}
	// Receipt revalidation takes the scoped lock before any queue-row locks.
	locked, err := tryLockOrganizationCatalog(tx, fence)
	if err != nil || !locked {
		return locked, err
	}
	err = tx.Raw(`SELECT true FROM topic_maintenance_teams
		WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? FOR NO KEY UPDATE SKIP LOCKED`,
		fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	keys := make([]string, 0, len(claim.Sources))
	for _, source := range claim.Sources {
		keys = append(keys, topic.SourceKey(source))
	}
	err = tx.Raw(`WITH wanted AS MATERIALIZED (
	 SELECT source_kind,source_id FROM topic_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind||':'||source_id=ANY(?::text[])
	), acquired AS (
	 SELECT source.source_id FROM topic_maintenance_sources AS source JOIN wanted USING(source_kind,source_id)
	 WHERE source.team_id=?::uuid AND source.shared_space_id=?::uuid AND source.space_generation=?
	 ORDER BY source.source_kind,source.source_id FOR UPDATE OF source SKIP LOCKED
	) SELECT (SELECT count(*) FROM wanted)=(SELECT count(*) FROM acquired)`, claim.TeamID, claim.SpaceID, claim.Generation, pq.Array(keys), claim.TeamID, claim.SpaceID, claim.Generation).Row().Scan(&locked)
	return locked, err
}
