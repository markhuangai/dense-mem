package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

type maintenanceRecoveryBatch struct {
	ID, RunID, WindowID, TeamID, SpaceID, LeaseToken string
	Generation                                       int64
	LeaseUntil                                       time.Time
	Sources, Revisions, Body                         []byte
}

func maintenanceFinalizationError(ctx context.Context, claim ontology.MaintenanceClaim, phase string, started time.Time, err error) error {
	if err == nil {
		return nil
	}
	return &ontology.MaintenanceFinalizationError{BatchID: claim.ID, RunID: claim.RunID, Phase: phase,
		Duration: time.Since(started), Cause: errors.Join(err, ctx.Err())}
}

func (s *Store) recoverMaintenanceBatches(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	started, phase := time.Now(), "recovery_accounting_lock"
	var claim ontology.MaintenanceClaim
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		phase = "recovery_candidates"
		var batches []maintenanceRecoveryBatch
		if err := tx.Raw(`SELECT batch.batch_id::text AS id,batch.run_id::text,batch.window_id::text,batch.team_id::text,
		 batch.shared_space_id::text AS space_id,batch.space_generation AS generation,batch.lease_token::text,batch.lease_until,batch.sources,batch.revisions,assessment.body
		 FROM ontology_maintenance_batches AS batch JOIN ontology_assessments AS assessment
		 ON assessment.team_id=batch.team_id AND assessment.shared_space_id=batch.shared_space_id AND assessment.space_generation=batch.space_generation AND assessment.operation_key='maintenance:'||batch.batch_id::text
		 JOIN memory_spaces AS space ON space.team_id=batch.team_id AND space.id=batch.shared_space_id AND space.generation=batch.space_generation AND space.lifecycle_state='active'
		 JOIN teams AS team ON team.id=batch.team_id AND team.status='active' AND team.deleted_at IS NULL
		 WHERE batch.status='running' AND batch.lease_until>? AND batch.lease_until>clock_timestamp()
		 AND ((COALESCE(assessment.body->'result'->>'failure_code','')='' AND assessment.body->'result'->>'current'='true')
		 OR (COALESCE(assessment.body->'result'->>'failure_code','')<>'' AND assessment.body->'result'->>'current'='false'))
		 AND NOT EXISTS(SELECT 1 FROM ontology_maintenance_attempts AS attempt WHERE attempt.batch_id=batch.batch_id AND attempt.reconciled_at IS NULL)
		 ORDER BY batch.created_at,batch.batch_id LIMIT ? FOR UPDATE OF batch SKIP LOCKED`, now, ontology.MaintenancePageSize).Scan(&batches).Error; err != nil {
			return err
		}
		for _, batch := range batches {
			claim = ontology.MaintenanceClaim{ID: batch.ID, RunID: batch.RunID, WindowID: batch.WindowID, TeamID: batch.TeamID,
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
			if err := validateMaintenanceRecovery(claim, receipt); err != nil {
				return err
			}
			phase = "recovery_source_locks"
			locked, err := lockMaintenanceRecoverySources(tx, claim)
			if err != nil {
				return err
			}
			if !locked {
				continue
			}
			if err := completeMaintenanceBatch(tx, claim, receipt.Result, receipt.Result.FailureCode, now, &phase); err != nil {
				return err
			}
		}
		phase = "recovery_commit"
		return nil
	})
	return maintenanceFinalizationError(ctx, claim, phase, started, err)
}

func validateMaintenanceRecovery(claim ontology.MaintenanceClaim, receipt ontology.OrganizationReceipt) error {
	input, err := ontology.PrepareOrganizationInput(ontology.OrganizationInput{OperationKey: "maintenance:" + claim.ID, Sources: claim.Sources})
	if err != nil {
		return err
	}
	if receipt.OperationKey != input.OperationKey || len(receipt.Sources) != len(input.Sources) || len(claim.Revisions) != len(input.Sources) {
		return ontology.ErrInvalid
	}
	versions := map[string]ontology.SourceHandle{}
	for _, source := range receipt.Sources {
		versions[ontology.SourceKey(source.SourceHandle)] = source.SourceHandle
	}
	for _, source := range input.Sources {
		key := ontology.SourceKey(source)
		if versions[key] != source || claim.Revisions[key] < 1 {
			return ontology.ErrInvalid
		}
	}
	return nil
}

func lockMaintenanceRecoverySources(tx *gorm.DB, claim ontology.MaintenanceClaim) (bool, error) {
	keys := make([]string, 0, len(claim.Sources))
	for _, source := range claim.Sources {
		keys = append(keys, ontology.SourceKey(source))
	}
	var locked bool
	err := tx.Raw(`WITH wanted AS MATERIALIZED (
	 SELECT source_kind,source_id FROM ontology_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind||':'||source_id=ANY(?::text[])
	), acquired AS (
	 SELECT source.source_id FROM ontology_maintenance_sources AS source JOIN wanted USING(source_kind,source_id)
	 WHERE source.team_id=?::uuid AND source.shared_space_id=?::uuid AND source.space_generation=?
	 ORDER BY source.source_kind,source.source_id FOR UPDATE OF source SKIP LOCKED
	) SELECT (SELECT count(*) FROM wanted)=(SELECT count(*) FROM acquired)`, claim.TeamID, claim.SpaceID, claim.Generation, pq.Array(keys), claim.TeamID, claim.SpaceID, claim.Generation).Row().Scan(&locked)
	return locked, err
}
