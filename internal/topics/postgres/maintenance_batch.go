package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
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
		busy, err := maintenanceBusySources(tx, fence)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(busy))
		for key := range busy {
			keys = append(keys, key)
		}
		var seeds []ontology.SourceHandle
		rows, err := tx.Raw(`SELECT source_kind,source_id,source_version FROM topic_maintenance_sources AS source WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND eligible AND
         NOT (source_kind || ':' || source_id=ANY(?::text[])) AND
         ((?<>'' AND source.last_run_id=NULLIF(?,'')::uuid AND status='failed') OR
         (?='' AND status='pending'))
         ORDER BY pending_at,source_kind,source_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(keys), turn.RetryRunID, turn.RetryRunID, turn.RetryRunID, ontology.MaintenancePageSize).Rows()
		if err != nil {
			return err
		}
		for rows.Next() {
			var seed ontology.SourceHandle
			if err := rows.Scan(&seed.Kind, &seed.ID, &seed.Version); err != nil {
				_ = rows.Close()
				return err
			}
			seeds = append(seeds, seed)
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		for _, seed := range seeds {
			_, err := readSource(tx, fence, seed)
			if err != nil && !errors.Is(err, ontology.ErrSourceStale) {
				return err
			}
			if errors.Is(err, ontology.ErrSourceStale) {
				if err := refreshMaintenanceSource(tx, fence, seed, false); err != nil {
					return err
				}
				continue
			}
			claim.Sources = append(claim.Sources, seed)
			if len(claim.Sources) == ontology.MaxOrganizationSources {
				break
			}
		}
		return collectMaintenanceRevisions(tx, fence, claim)
	})
	if err != nil || len(claim.Sources) == 0 {
		return nil, err
	}
	return s.admitMaintenanceBatch(ctx, turn, claim, nil, now)
}

func (s *Store) admitMaintenanceBatch(ctx context.Context, turn ontology.MaintenanceTurn, claim *ontology.MaintenanceClaim, community *ontology.CommunityInput, now time.Time) (*ontology.MaintenanceClaim, error) {
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
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
		err = tx.Raw(`SELECT run.run_id::text FROM topic_maintenance_runs AS run WHERE run.run_id=?::uuid AND run.window_id=?::uuid AND run.kind IN ('scheduled','run','retry') AND run.status IN ('pending','running')
		 AND (run.max_batches=0 OR run.completed_batches+(SELECT count(*) FROM topic_maintenance_batches AS batch WHERE batch.run_id=run.run_id AND batch.status='running')<run.max_batches)
		 ORDER BY run.created_at,run.run_id LIMIT 1 FOR UPDATE`, turn.RunID, claim.WindowID).Row().Scan(&runID)
		if errors.Is(err, sql.ErrNoRows) {
			claim = nil
			return nil
		}
		if err != nil {
			return err
		}
		claim.RunID = runID

		var communityTopicID string
		if community != nil {
			err := tx.Raw(`SELECT topic_id::text FROM topic_maintenance_community_work
				WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?
				AND topic_id=?::uuid AND topic_version=? AND work_id=?::uuid
				AND ready_at<=statement_timestamp() FOR UPDATE`, claim.TeamID, claim.SpaceID, claim.Generation,
				community.Topic.ID, community.Topic.Version, community.WorkID).Row().Scan(&communityTopicID)
			if errors.Is(err, sql.ErrNoRows) {
				return ontology.ErrSourceStale
			}
			if err != nil {
				return err
			}
		}

		sources, err := json.Marshal(claim.Sources)
		if err != nil {
			return err
		}
		revisions, err := json.Marshal(claim.Revisions)
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO topic_maintenance_batches(batch_id,run_id,window_id,team_id,shared_space_id,space_generation,lease_token,lease_until,sources,revisions,created_at,community_topic_id) VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?::uuid,?,?::uuid,?,?::jsonb,?::jsonb,?,NULLIF(?,'')::uuid)`, claim.ID, runID, claim.WindowID, claim.TeamID, claim.SpaceID, claim.Generation, claim.LeaseToken, claim.LeaseUntil, string(sources), string(revisions), now, communityTopicID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE topic_maintenance_runs SET status='running',updated_at=clock_timestamp() WHERE run_id=?::uuid`, runID).Error; err != nil {
			return err
		}
		return releaseMaintenanceTurnTx(tx, turn)
	})
	if err != nil {
		return nil, err
	}
	return claim, nil
}

func maintenanceBusySources(tx *gorm.DB, fence scope) (map[string]bool, error) {
	rows, err := tx.Raw(`SELECT sources FROM topic_maintenance_batches WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND status='running' AND lease_until>clock_timestamp()`, fence.TeamID, fence.SpaceID, fence.Generation).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	busy := map[string]bool{}
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var sources []ontology.SourceHandle
		if err := json.Unmarshal(body, &sources); err != nil {
			return nil, err
		}
		for _, source := range sources {
			busy[ontology.SourceKey(source)] = true
		}
	}
	return busy, rows.Err()
}

func maintenanceClosureBusy(sources []ontology.SourceHandle, busy map[string]bool) bool {
	for _, source := range sources {
		if busy[ontology.SourceKey(source)] {
			return true
		}
	}
	return false
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
		records, err := organizationSourceRecords(tx, fence, handles, true)
		if err != nil {
			return nil, false, err
		}
		views, err := s.organizationViews(tx, fence, records)
		if err != nil {
			return nil, false, err
		}
		before := len(handles)
		for _, record := range views {
			var members []ontology.SourceHandle
			if record.Current && record.Group != nil {
				members = record.Group.Members
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

func maintenanceRelatedSources(tx *gorm.DB, fence scope, snapshot ontology.SourceSnapshot) ([]ontology.SourceHandle, error) {
	query := maintenanceLexicalQuery(snapshot)
	rows, err := tx.Raw(`SELECT source_kind,source_id,source_version FROM topic_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?
	 AND eligible AND source_kind=? AND source_id<>? AND ((meaning_key<>'' AND meaning_key=?) OR (?!='' AND search_tsv@@websearch_to_tsquery('simple',?)))
	 ORDER BY (meaning_key<>'' AND meaning_key=?) DESC,ts_rank_cd(search_tsv,websearch_to_tsquery('simple',?)) DESC,source_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, snapshot.Kind, snapshot.ID, snapshot.MeaningKey, query, query, snapshot.MeaningKey, query, ontology.MaxOrganizationSources-1).Rows()
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
	if len(claim.Sources) == 0 {
		return nil
	}
	ids := make([]string, len(claim.Sources))
	positions := make(map[string]int, len(claim.Sources))
	for i, source := range claim.Sources {
		ids[i], positions[source.ID] = source.ID, i
	}
	rows, err := tx.Raw(`SELECT source_id,revision,source_version,
		COALESCE((SELECT outcome.input_hash FROM topic_assignment_outcomes outcome WHERE
			(outcome.team_id,outcome.shared_space_id,outcome.space_generation,outcome.evidence_id)=
			(source.team_id,source.shared_space_id,source.space_generation,source.source_id::uuid)
			AND outcome.status IN ('assigned','uncategorized','ambiguous')
			ORDER BY outcome.created_at DESC,outcome.outcome_id LIMIT 1),source.last_assignment_input_hash,'')
		FROM topic_maintenance_sources source WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind='evidence'
		AND source_id=ANY(?::text[])`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	claim.PriorAssignmentHashes = make(map[string]string, len(ids))
	for rows.Next() {
		var id, hash string
		var revision, version int64
		if err := rows.Scan(&id, &revision, &version, &hash); err != nil {
			return err
		}
		index := positions[id]
		key := ontology.SourceKey(claim.Sources[index])
		claim.Sources[index].Version = version
		claim.Revisions[key], claim.PriorAssignmentHashes[key] = revision, hash
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(claim.Revisions) != len(ids) {
		return ontology.ErrSourceStale
	}
	return nil
}

func (s *Store) CompleteMaintenanceBatch(ctx context.Context, claim ontology.MaintenanceClaim, result ontology.OrganizationResult, failure string, now time.Time) error {
	started, phase := time.Now(), "accounting_lock"
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		if err := completeMaintenanceBatch(tx, claim, result, failure, now, &phase); err != nil {
			return err
		}
		phase = "commit"
		return nil
	})
	return maintenanceFinalizationError(ctx, claim, phase, started, err)
}

func completeMaintenanceBatch(tx *gorm.DB, claim ontology.MaintenanceClaim, result ontology.OrganizationResult, failure string, now time.Time, phase *string) error {
	*phase = "batch_fence"
	var status string
	var live bool
	err := tx.Raw(`SELECT status,lease_until>? AND lease_until>clock_timestamp() FROM topic_maintenance_batches
	 WHERE batch_id=?::uuid AND run_id=?::uuid AND window_id=?::uuid AND team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid FOR UPDATE`, now, claim.ID, claim.RunID, claim.WindowID, claim.TeamID, claim.SpaceID, claim.Generation, claim.LeaseToken).Row().Scan(&status, &live)
	if errors.Is(err, sql.ErrNoRows) {
		return ontology.ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if status == "completed" {
		return nil
	}
	if status != "running" || !live || !now.Before(claim.LeaseUntil) {
		return ontology.ErrLeaseLost
	}
	*phase = "receipt_revalidation"
	receipt, revisions, err := currentMaintenanceCompletion(tx, claim, result, failure)
	if err != nil {
		return err
	}
	fingerprints := map[string]string{}
	if receipt != nil {
		result = receipt.Result
		for _, source := range receipt.Sources {
			fingerprints[ontology.SourceKey(source.SourceHandle)] = source.Fingerprint
		}
	}

	progress := false
	outcomes := map[string]ontology.OrganizationOutcome{}
	for _, outcome := range result.Outcomes {
		outcomes[ontology.SourceKey(outcome.Source)] = outcome
	}
	*phase = "source_updates"
	for _, source := range claim.Sources {
		key := ontology.SourceKey(source)
		revision, reuse := revisions[key]
		if !reuse {
			revision = claim.Revisions[key]
		}
		outcome, exists := outcomes[key]
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
		updated := tx.Exec(`UPDATE topic_maintenance_sources SET status=?,reason=?,eligible=CASE WHEN ?='unavailable' THEN false ELSE eligible END,assessment_id=?::uuid,last_run_id=?::uuid,updated_at=clock_timestamp()
		 WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind=? AND source_id=? AND revision=?
		 AND (NOT ? OR (eligible AND status='pending' AND source_version=? AND fingerprint=?))`, state, reason, state, assessmentID, claim.RunID, claim.TeamID, claim.SpaceID, claim.Generation, source.Kind, source.ID, revision, reuse, source.Version, fingerprints[key])
		if updated.Error != nil {
			return updated.Error
		}
		progress = progress || ((state == "organized" || state == "uncategorized") && updated.RowsAffected > 0)
	}
	*phase = "batch_update"
	updated := tx.Exec(`UPDATE topic_maintenance_batches SET status='completed',failure_code=?,completed_at=clock_timestamp() WHERE batch_id=?::uuid AND status='running' AND lease_until>clock_timestamp()`, failure, claim.ID)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ontology.ErrLeaseLost
	}
	*phase = "run_update"
	var bounded bool
	if err := tx.Raw(`UPDATE topic_maintenance_runs SET completed_batches=completed_batches+1,failure_code=CASE WHEN ?<>'' THEN ? ELSE failure_code END,
	 updated_at=clock_timestamp() WHERE run_id=?::uuid RETURNING max_batches>0 AND completed_batches>=max_batches`, failure, failure, claim.RunID).Row().Scan(&bounded); err != nil {
		return err
	}
	if bounded {
		requiredFailure := failure
		if requiredFailure == "" {
			requiredFailure = result.FailureCode
		}
		if err := completeMaintenanceRun(tx, claim.RunID, "", false, requiredFailure); err != nil {
			return err
		}
	}
	if progress && failure == "" && result.FailureCode == "" && result.Current {
		return tx.Exec(`UPDATE topic_maintenance_state SET last_successful_progress=clock_timestamp() WHERE singleton`).Error
	}
	return nil
}

func currentMaintenanceCompletion(tx *gorm.DB, claim ontology.MaintenanceClaim, result ontology.OrganizationResult, failure string) (*ontology.OrganizationReceipt, map[string]int64, error) {
	if failure != "" || result.FailureCode != "" || !result.Current || result.AssessmentID == "" {
		return nil, nil, nil
	}
	type queuedSource struct {
		handle              ontology.SourceHandle
		revision            int64
		fingerprint, status string
		eligible            bool
	}
	keys := make([]string, 0, len(claim.Sources))
	for _, source := range claim.Sources {
		keys = append(keys, ontology.SourceKey(source))
	}
	rows, err := tx.Raw(`SELECT source_kind,source_id,source_version,revision,fingerprint,status,eligible FROM topic_maintenance_sources
	 WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind || ':' || source_id=ANY(?::text[])`, claim.TeamID, claim.SpaceID, claim.Generation, pq.Array(keys)).Rows()
	if err != nil {
		return nil, nil, err
	}
	var pending []queuedSource
	for rows.Next() {
		var source queuedSource
		if err := rows.Scan(&source.handle.Kind, &source.handle.ID, &source.handle.Version, &source.revision, &source.fingerprint, &source.status, &source.eligible); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if source.revision > claim.Revisions[ontology.SourceKey(source.handle)] && source.eligible && source.status == "pending" {
			pending = append(pending, source)
		}
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	if len(pending) == 0 {
		return nil, nil, nil
	}
	fence := scope{TeamID: claim.TeamID, SpaceID: claim.SpaceID, Generation: claim.Generation}
	// A successful assessment already has a catalog; inserting it can reject historical generations before their fence check.
	var catalogVersion int64
	if err := tx.Raw(`SELECT revision FROM topic_catalog_heads WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? FOR UPDATE`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&catalogVersion); err != nil {
		return nil, nil, err
	}
	var active bool
	if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM memory_spaces AS space JOIN teams AS team ON team.id=space.team_id
	 WHERE team.id=?::uuid AND team.status='active' AND team.deleted_at IS NULL AND space.id=?::uuid
	 AND space.kind='team_shared' AND space.lifecycle_state='active' AND space.generation=?)`, claim.TeamID, claim.SpaceID, claim.Generation).Row().Scan(&active); err != nil {
		return nil, nil, err
	}
	if !active {
		return nil, nil, nil
	}
	receipt, found, err := organizationByKey(tx, fence, "maintenance:"+claim.ID, false)
	if err != nil {
		return nil, nil, err
	}
	if !found || receipt.ID != result.AssessmentID || receipt.Result.FailureCode != "" || !receipt.Result.Current {
		return nil, nil, nil
	}
	hash, err := ontology.OrganizationInputHash(ontology.OrganizationInput{Sources: claim.Sources}, receipt.ProviderIdentity)
	if err != nil {
		return nil, nil, err
	}
	if hash != receipt.InputHash {
		return nil, nil, nil
	}
	if err := validateMaintenanceReceipt(claim, receipt); err != nil {
		return nil, nil, err
	}
	current, err := (&Store{}).organizationCurrent(tx, fence, receipt)
	if err != nil || !current {
		return nil, nil, err
	}
	if err := maintenancePublicationFence(ontology.WithMaintenanceClaim(tx.Statement.Context, claim), tx, fence); err != nil {
		return nil, nil, err
	}
	revisions := map[string]int64{}
	for _, source := range pending {
		for _, dependency := range receipt.Sources {
			if source.handle == dependency.SourceHandle && source.fingerprint == dependency.Fingerprint {
				revisions[ontology.SourceKey(source.handle)] = source.revision
			}
		}
	}
	return &receipt, revisions, nil
}
