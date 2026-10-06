package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) ClaimMaintenanceBatch(ctx context.Context, turn ontology.MaintenanceTurn, windowID string, now time.Time) (*ontology.MaintenanceClaim, error) {
	claim := &ontology.MaintenanceClaim{ID: uuid.NewString(), WindowID: windowID, TeamID: turn.TeamID, SpaceID: turn.SpaceID, Generation: turn.Generation, LeaseToken: turn.LeaseToken, LeaseUntil: turn.LeaseUntil, Revisions: map[string]int64{}}
	err := s.withScope(ctx, turn.TeamID, false, func(tx *gorm.DB, fence scope) error {
		if fence.SpaceID != turn.SpaceID || fence.Generation != turn.Generation {
			return ontology.ErrLeaseLost
		}
		if err := checkMaintenanceTurn(tx, turn); err != nil {
			return err
		}
		var seed ontology.SourceHandle
		err := tx.Raw(`SELECT source_kind,source_id,source_version FROM ontology_maintenance_sources AS source WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND eligible AND
		 ((?<>'' AND source.last_run_id=NULLIF(?,'')::uuid AND status='failed') OR
		 (?='' AND (status='pending' OR (status='budget_deferred' AND NOT EXISTS(SELECT 1 FROM ontology_maintenance_batches AS batch WHERE batch.run_id=source.last_run_id AND batch.window_id=?::uuid)))))
		 ORDER BY pending_at,source_kind,source_id LIMIT 1`, fence.TeamID, fence.SpaceID, fence.Generation, turn.RetryRunID, turn.RetryRunID, turn.RetryRunID, windowID).Row().Scan(&seed.Kind, &seed.ID, &seed.Version)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		contextData, err := s.organizationContext(tx, fence, []ontology.SourceHandle{seed})
		if err != nil && !errors.Is(err, ontology.ErrContextBound) {
			return err
		}
		if errors.Is(err, ontology.ErrContextBound) {
			claim.Sources = []ontology.SourceHandle{seed}
			return collectMaintenanceRevisions(tx, fence, claim)
		}
		if len(contextData.Sources) != 1 || !contextData.Sources[0].Eligible {
			return refreshMaintenanceSource(tx, fence, seed, false)
		}
		handles, overflow, err := s.maintenanceGroupClosure(tx, fence, []ontology.SourceHandle{seed})
		if err != nil {
			return err
		}
		if overflow {
			return tx.Exec(`UPDATE ontology_maintenance_sources SET status='ambiguous',reason='group_exceeds_batch_limit',updated_at=? WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind=? AND source_id=?`, now, fence.TeamID, fence.SpaceID, fence.Generation, seed.Kind, seed.ID).Error
		}
		peers, err := maintenanceRelatedSources(tx, fence, contextData.Sources[0], turn.RetryRunID)
		if err != nil {
			return err
		}
		for _, peer := range peers {
			if len(handles) == ontology.MaxOrganizationSources {
				break
			}
			if hasMaintenanceSource(handles, peer) {
				continue
			}
			candidate, tooLarge, err := s.maintenanceGroupClosure(tx, fence, append(append([]ontology.SourceHandle{}, handles...), peer))
			if err != nil {
				return err
			}
			if !tooLarge {
				handles = candidate
			}
		}
		claim.Sources = handles
		return collectMaintenanceRevisions(tx, fence, claim)
	})
	if err != nil || len(claim.Sources) == 0 {
		return nil, err
	}
	err = s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		paused, err := maintenanceState(tx)
		if err != nil {
			return err
		}
		if paused {
			return ontology.ErrMaintenancePaused
		}
		enabled, err := maintenanceEnabled(tx)
		if err != nil {
			return err
		}
		if !enabled {
			return ontology.ErrMaintenanceDisabled
		}
		if err := checkMaintenanceTurn(tx, turn); err != nil {
			return err
		}
		var runID string
		err = tx.Raw(`SELECT run.run_id::text FROM ontology_maintenance_runs AS run WHERE run.run_id=?::uuid AND run.window_id=?::uuid AND run.kind IN ('scheduled','run','retry') AND run.status IN ('pending','running')
		 AND (run.max_batches=0 OR run.completed_batches+(SELECT count(*) FROM ontology_maintenance_batches AS batch WHERE batch.run_id=run.run_id AND batch.status='running')<run.max_batches)
		 ORDER BY run.created_at,run.run_id LIMIT 1 FOR UPDATE`, turn.RunID, windowID).Row().Scan(&runID)
		if errors.Is(err, sql.ErrNoRows) {
			claim = nil
			return nil
		}
		if err != nil {
			return err
		}
		claim.RunID = runID
		sources, err := json.Marshal(claim.Sources)
		if err != nil {
			return err
		}
		revisions, err := json.Marshal(claim.Revisions)
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ontology_maintenance_batches(batch_id,run_id,window_id,team_id,shared_space_id,space_generation,lease_token,lease_until,sources,revisions,created_at) VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?::uuid,?,?::uuid,?,?::jsonb,?::jsonb,?)`, claim.ID, runID, windowID, claim.TeamID, claim.SpaceID, claim.Generation, claim.LeaseToken, claim.LeaseUntil, string(sources), string(revisions), now).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE ontology_maintenance_runs SET status='running',updated_at=? WHERE run_id=?::uuid`, now, runID).Error
	})
	return claim, err
}

func hasMaintenanceSource(sources []ontology.SourceHandle, source ontology.SourceHandle) bool {
	for _, existing := range sources {
		if ontology.SourceKey(existing) == ontology.SourceKey(source) {
			return true
		}
	}
	return false
}

func (s *Store) maintenanceGroupClosure(tx *gorm.DB, fence scope, initial []ontology.SourceHandle) ([]ontology.SourceHandle, bool, error) {
	handles := append([]ontology.SourceHandle{}, initial...)
	for {
		if len(handles) > ontology.MaxOrganizationSources {
			return nil, true, nil
		}
		data, err := s.organizationContext(tx, fence, handles)
		if err != nil && !errors.Is(err, ontology.ErrContextBound) {
			return nil, false, err
		}
		if errors.Is(err, ontology.ErrContextBound) {
			return handles, false, nil
		}
		before := len(handles)
		for _, record := range data.Records {
			var members []ontology.SourceHandle
			if record.Current && record.Group != nil {
				members = record.Group.Members
			}
			if record.Override != nil && record.Override.Action == ontology.GroupTogether {
				members = record.Override.Members
			}
			for _, member := range members {
				if hasMaintenanceSource(handles, member) {
					continue
				}
				latest, err := latestMaintenanceHandle(tx, fence, member)
				if err != nil {
					return nil, false, err
				}
				handles = append(handles, latest)
			}
		}
		if len(handles) == before {
			sort.Slice(handles, func(i, j int) bool { return ontology.SourceKey(handles[i]) < ontology.SourceKey(handles[j]) })
			return handles, false, nil
		}
	}
}

func maintenanceRelatedSources(tx *gorm.DB, fence scope, snapshot ontology.SourceSnapshot, retryRunID string) ([]ontology.SourceHandle, error) {
	query := maintenanceLexicalQuery(snapshot)
	rows, err := tx.Raw(`SELECT source_kind,source_id,source_version FROM ontology_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?
	 AND eligible AND source_kind=? AND source_id<>? AND ((meaning_key<>'' AND meaning_key=?) OR (?!='' AND search_tsv@@websearch_to_tsquery('simple',?)))
	 AND status<>'ambiguous' AND (status<>'failed' OR last_run_id=NULLIF(?,'')::uuid)
	 ORDER BY (meaning_key<>'' AND meaning_key=?) DESC,ts_rank_cd(search_tsv,websearch_to_tsquery('simple',?)) DESC,source_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, snapshot.Kind, snapshot.ID, snapshot.MeaningKey, query, query, retryRunID, snapshot.MeaningKey, query, ontology.MaxOrganizationSources).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ontology.SourceHandle{}
	for rows.Next() {
		var handle ontology.SourceHandle
		if err := rows.Scan(&handle.Kind, &handle.ID, &handle.Version); err != nil {
			return nil, err
		}
		result = append(result, handle)
	}
	return result, rows.Err()
}

func collectMaintenanceRevisions(tx *gorm.DB, fence scope, claim *ontology.MaintenanceClaim) error {
	for index, handle := range claim.Sources {
		if err := refreshMaintenanceSource(tx, fence, handle, false); err != nil {
			return err
		}
		var revision int64
		if err := tx.Raw(`SELECT revision,source_version FROM ontology_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind=? AND source_id=?`, fence.TeamID, fence.SpaceID, fence.Generation, handle.Kind, handle.ID).Row().Scan(&revision, &claim.Sources[index].Version); err != nil {
			return err
		}
		claim.Revisions[ontology.SourceKey(handle)] = revision
	}
	return nil
}

func (s *Store) CompleteMaintenanceBatch(ctx context.Context, claim ontology.MaintenanceClaim, result ontology.OrganizationResult, failure string, now time.Time) error {
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		var status string
		err := tx.Raw(`SELECT status FROM ontology_maintenance_batches WHERE batch_id=?::uuid AND lease_token=?::uuid FOR UPDATE`, claim.ID, claim.LeaseToken).Row().Scan(&status)
		if err != nil {
			return err
		}
		if status == "completed" {
			return nil
		}
		if status != "running" || !now.Before(claim.LeaseUntil) {
			return ontology.ErrLeaseLost
		}
		progress := false
		outcomes := map[string]ontology.OrganizationOutcome{}
		for _, outcome := range result.Outcomes {
			outcomes[ontology.SourceKey(outcome.Source)] = outcome
		}
		for _, source := range claim.Sources {
			outcome, exists := outcomes[ontology.SourceKey(source)]
			if !exists {
				outcome = ontology.OrganizationOutcome{Source: source, Status: "failed", Reason: failure}
			}
			state := ontology.MaintenanceOutcomeState(outcome, result, failure)
			reason := outcome.Reason
			if failure != "" {
				reason = failure
			}
			var assessmentID any
			if result.AssessmentID != "" {
				assessmentID = result.AssessmentID
			}
			updated := tx.Exec(`UPDATE ontology_maintenance_sources SET status=?,reason=?,eligible=CASE WHEN ?='unavailable' THEN false ELSE eligible END,assessment_id=?::uuid,last_run_id=?::uuid,updated_at=?
			 WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind=? AND source_id=? AND revision=?`, state, reason, state, assessmentID, claim.RunID, now, claim.TeamID, claim.SpaceID, claim.Generation, source.Kind, source.ID, claim.Revisions[ontology.SourceKey(source)])
			if updated.Error != nil {
				return updated.Error
			}
			progress = progress || (state == "organized" && updated.RowsAffected > 0)
		}
		if err := tx.Exec(`UPDATE ontology_maintenance_batches SET status='completed',failure_code=?,completed_at=? WHERE batch_id=?::uuid`, failure, now, claim.ID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE ontology_maintenance_runs SET completed_batches=completed_batches+1,failure_code=CASE WHEN ?<>'' THEN ? ELSE failure_code END,
		 status=CASE WHEN max_batches>0 AND completed_batches+1>=max_batches THEN CASE WHEN ?<>'' THEN 'incomplete' ELSE 'completed' END ELSE status END,updated_at=? WHERE run_id=?::uuid`, failure, failure, failure, now, claim.RunID).Error; err != nil {
			return err
		}
		if progress && failure == "" && result.FailureCode == "" && result.Current {
			return tx.Exec(`UPDATE ontology_maintenance_state SET last_successful_progress=? WHERE singleton`, now).Error
		}
		return nil
	})
}
