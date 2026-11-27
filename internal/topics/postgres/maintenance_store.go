package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/markhuangai/dense-mem/internal/domain"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) withMaintenanceSystem(ctx context.Context, fn func(*gorm.DB) error) error {
	if err := requireAutomatic(ctx); err != nil {
		return err
	}
	if s == nil || s.db == nil || s.rls == nil {
		return fmt.Errorf("%w: database and RLS required", topic.ErrAccounting)
	}
	return s.rls.WithSystemTx(ctx, s.db, fn)
}

func (s *Store) readMaintenanceSystem(ctx context.Context, fn func(*gorm.DB) error) error {
	if err := requireAutomatic(ctx); err != nil {
		return err
	}
	if s == nil || s.db == nil || s.rls == nil {
		return fmt.Errorf("%w: database and RLS required", topic.ErrAccounting)
	}
	return s.rls.WithSystemReadOnlyRepeatableTx(ctx, s.db, fn)
}

const maintenanceWindowColumns = `window_id::text,starts_at,ends_at,policy`
const maintenanceRunColumns = `run_id::text,COALESCE(window_id::text,''),kind,status,operation_key,max_batches,completed_batches,failure_code,created_at,updated_at,COALESCE((command->>'input_tokens')::bigint,0),COALESCE((command->>'output_tokens')::bigint,0)`

type maintenanceScanner interface{ Scan(...any) error }

func scanMaintenanceWindow(row maintenanceScanner) (*topic.MaintenanceWindow, error) {
	result := &topic.MaintenanceWindow{}
	var policy []byte
	err := row.Scan(&result.ID, &result.StartsAt, &result.EndsAt, &policy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(policy, &result.Policy); err != nil {
		return nil, err
	}
	result.Policy = maintenanceWindowPolicy(result.Policy)
	return result, nil
}

func maintenanceWindowPolicy(policy domain.TopicMaintenanceConfig) domain.TopicMaintenanceConfig {
	if policy.TeamMaxConcurrency == 0 {
		policy.TeamMaxConcurrency = domain.DefaultTopicTeamConcurrency
	}
	if policy.TeamConcurrencyOverrides == nil {
		policy.TeamConcurrencyOverrides = []domain.TopicTeamConcurrencyOverride{}
	}
	return policy
}

func scanMaintenanceRun(row maintenanceScanner) (topic.MaintenanceRun, error) {
	var value topic.MaintenanceRun
	err := row.Scan(&value.ID, &value.WindowID, &value.Kind, &value.Status, &value.OperationKey, &value.MaxBatches, &value.CompletedBatches, &value.FailureCode, &value.CreatedAt, &value.UpdatedAt, &value.InputTokens, &value.OutputTokens)
	return value, err
}

func maintenanceEnabled(tx *gorm.DB) (bool, error) {
	var value string
	err := tx.Raw(`SELECT value FROM app_config WHERE key=? FOR SHARE`, domain.AppConfigTopicMaintenanceEnabled).Row().Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return value == "true", err
}

func maintenanceState(tx *gorm.DB) (bool, error) {
	var paused bool
	err := tx.Raw(`SELECT paused FROM topic_maintenance_state WHERE singleton FOR UPDATE`).Row().Scan(&paused)
	return paused, err
}

func maintenanceStateShared(tx *gorm.DB) (bool, error) {
	var paused bool
	err := tx.Raw(`SELECT paused FROM topic_maintenance_state WHERE singleton FOR SHARE`).Row().Scan(&paused)
	return paused, err
}

func recordTeamMaintenanceProgress(tx *gorm.DB, teamID, spaceID string, generation int64) error {
	return tx.Exec(`UPDATE topic_maintenance_teams SET last_successful_progress=clock_timestamp()
		WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, teamID, spaceID, generation).Error
}

func currentMaintenanceWindow(tx *gorm.DB, now time.Time) (*topic.MaintenanceWindow, error) {
	return scanMaintenanceWindow(tx.Raw(`SELECT `+maintenanceWindowColumns+` FROM topic_maintenance_windows
	 WHERE starts_at<=? AND ends_at>? AND jsonb_exists(policy,'context_window_tokens') ORDER BY starts_at DESC LIMIT 1`, now, now).Row())
}

func (s *Store) CurrentMaintenanceWindow(ctx context.Context, now time.Time) (*topic.MaintenanceWindow, error) {
	var result *topic.MaintenanceWindow
	err := s.readMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		var err error
		result, err = currentMaintenanceWindow(tx, now)
		return err
	})
	return result, err
}

func (s *Store) EnsureMaintenanceWindow(ctx context.Context, policy domain.TopicMaintenanceConfig, now time.Time) (*topic.MaintenanceWindow, error) {
	policy = maintenanceWindowPolicy(policy)
	var result *topic.MaintenanceWindow
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		var version string
		if err := tx.Raw(`SELECT value FROM app_config WHERE key=? FOR SHARE`, domain.AppConfigUpdateTimeKey).Row().Scan(&version); err != nil {
			return err
		}
		if policy.SettingsVersion == "" || version != policy.SettingsVersion {
			return topic.ErrConflict
		}
		previous, err := scanMaintenanceWindow(tx.Raw(`SELECT ` + maintenanceWindowColumns + ` FROM topic_maintenance_windows ORDER BY starts_at DESC LIMIT 1`).Row())
		if err != nil {
			return err
		}
		if previous != nil && previous.Policy.ContextWindowTokens > 0 && now.Before(previous.EndsAt) {
			result = previous
			return nil
		}
		if !policy.Enabled {
			return nil
		}
		if policy.ContextWindowTokens < 1 || policy.ContextWindowTokens > 1000000000 {
			return topic.ErrConfiguration
		}
		start, end, err := topic.MaintenanceWindowBounds(policy, now, previous)
		if err != nil {
			return err
		}
		if start.IsZero() {
			return nil
		}
		encoded, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		result = &topic.MaintenanceWindow{ID: uuid.NewString(), StartsAt: start, EndsAt: end, Policy: policy}
		if err := tx.Exec(`INSERT INTO topic_maintenance_windows(window_id,starts_at,ends_at,policy) VALUES(?::uuid,?,?,?::jsonb)`, result.ID, start, end, string(encoded)).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE topic_maintenance_runs SET status='incomplete',failure_code='window_closed',updated_at=? WHERE window_id<>?::uuid AND status IN ('pending','running')`, now, result.ID).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO topic_maintenance_runs(run_id,window_id,operation_key,request_hash,command,kind,status,max_batches,created_at,updated_at)
		 VALUES(?::uuid,?::uuid,?,'scheduled','{}','scheduled','pending',0,?,?)`, uuid.NewString(), result.ID, "scheduled:"+result.ID, now, now).Error
	})
	return result, err
}

func seedMaintenanceTeams(tx *gorm.DB) error {
	return tx.Exec(`INSERT INTO topic_maintenance_teams(team_id,shared_space_id,space_generation)
	 SELECT team.id,space.id,space.generation FROM teams AS team JOIN memory_spaces AS space
	 ON space.team_id=team.id AND space.kind='team_shared' AND space.lifecycle_state='active'
	 WHERE team.status='active' AND team.deleted_at IS NULL AND NOT EXISTS(
	 SELECT 1 FROM topic_maintenance_teams AS state WHERE state.team_id=team.id AND state.shared_space_id=space.id AND state.space_generation=space.generation)
	 ORDER BY team.id LIMIT ? ON CONFLICT DO NOTHING`, topic.MaintenancePageSize).Error
}

type maintenanceTurnPreview struct {
	runID, retryRunID, reassessTeamID string
	reassessEvidenceIDs               []string
	runUpdatedAt                      time.Time
	reopenScheduled                   bool
	unresolved                        bool
	active                            int
	candidates                        []scope
}

const nextMaintenanceRun = ` FROM topic_maintenance_runs AS run
	WHERE window_id=?::uuid AND kind IN ('scheduled','run','retry','reassess') AND status IN ('pending','running')
	AND (max_batches=0 OR completed_batches+(SELECT count(*) FROM topic_maintenance_batches AS batch WHERE batch.run_id=run.run_id AND batch.status='running')<max_batches)
	ORDER BY (kind='scheduled'),created_at,run_id LIMIT 1`

func (s *Store) seedMissingMaintenanceTeams(ctx context.Context) error {
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		return seedMaintenanceTeams(tx)
	})
}

func (s *Store) expireOverdueMaintenanceBatches(ctx context.Context, now time.Time) error {
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		var expiredIDs []string
		if err := tx.Raw(`WITH expired AS (
			SELECT batch_id FROM topic_maintenance_batches
			WHERE status='running' AND lease_until<=?
			ORDER BY batch_id FOR UPDATE SKIP LOCKED
		)
		UPDATE topic_maintenance_batches AS batch SET status='lost',failure_code='lease_lost'
		FROM expired WHERE batch.batch_id=expired.batch_id RETURNING batch.batch_id::text`, now).Scan(&expiredIDs).Error; err != nil {
			return err
		}
		if len(expiredIDs) == 0 {
			return nil
		}
		if err := tx.Exec(`UPDATE topic_maintenance_community_work SET lease_batch_id=NULL,lease_until=NULL
			WHERE lease_batch_id=ANY(?::uuid[])`, pq.Array(expiredIDs)).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE topic_embeddings SET consolidation_batch_id=NULL,consolidation_lease_until=NULL
			WHERE consolidation_batch_id=ANY(?::uuid[])`, pq.Array(expiredIDs)).Error
	})
}

func (s *Store) previewMaintenanceTurn(ctx context.Context, windowID string, now time.Time) (*maintenanceTurnPreview, error) {
	var preview *maintenanceTurnPreview
	err := s.readMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		var paused bool
		if err := tx.Raw(`SELECT paused FROM topic_maintenance_state WHERE singleton`).Row().Scan(&paused); err != nil {
			return err
		}
		if paused {
			return topic.ErrMaintenancePaused
		}
		var enabled string
		err := tx.Raw(`SELECT value FROM app_config WHERE key=?`, domain.AppConfigTopicMaintenanceEnabled).Row().Scan(&enabled)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if enabled != "true" {
			return topic.ErrMaintenanceDisabled
		}
		window, err := scanMaintenanceWindow(tx.Raw(`SELECT `+maintenanceWindowColumns+` FROM topic_maintenance_windows WHERE window_id=?::uuid`, windowID).Row())
		if err != nil {
			return err
		}
		if window == nil || !now.Before(window.EndsAt) {
			return topic.ErrWindowClosed
		}
		active, err := activeMaintenanceWork(tx, now)
		if err != nil {
			return err
		}
		if active >= window.Policy.MaxConcurrency {
			return nil
		}
		next := &maintenanceTurnPreview{active: active}
		var selected []byte
		err = tx.Raw(`SELECT run_id::text,COALESCE(command->>'retry_run_id',''),COALESCE(command->>'team_id',''),
			COALESCE(command->'evidence_ids','[]'::jsonb),updated_at`+nextMaintenanceRun, windowID).Row().Scan(
			&next.runID, &next.retryRunID, &next.reassessTeamID, &selected, &next.runUpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.Raw(`SELECT run_id::text,'[]'::jsonb,updated_at FROM topic_maintenance_runs
				WHERE window_id=?::uuid AND kind='scheduled' AND status IN ('completed','incomplete') ORDER BY created_at,run_id LIMIT 1`,
				windowID).Row().Scan(&next.runID, &selected, &next.runUpdatedAt)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			next.reopenScheduled = true
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(selected, &next.reassessEvidenceIDs); err != nil {
			return err
		}
		limits, err := maintenanceTeamLimits(window.Policy)
		if err != nil {
			return err
		}
		rows, err := tx.Raw(`SELECT state.team_id::text,state.shared_space_id::text,state.space_generation FROM topic_maintenance_teams AS state
		 JOIN teams AS team ON team.id=state.team_id AND team.status='active' AND team.deleted_at IS NULL
		 JOIN memory_spaces AS space ON space.team_id=state.team_id AND space.id=state.shared_space_id AND space.generation=state.space_generation AND space.lifecycle_state='active'
		 WHERE (state.lease_until IS NULL OR state.lease_until<=?)
		 AND (SELECT count(*) FROM topic_maintenance_batches AS active_batch WHERE active_batch.team_id=state.team_id AND active_batch.shared_space_id=state.shared_space_id AND active_batch.space_generation=state.space_generation AND active_batch.status='running' AND active_batch.lease_until>?) < COALESCE((?::jsonb->>state.team_id::text)::int,?)
		 AND (?='' OR (state.team_id=NULLIF(?,'')::uuid AND EXISTS(SELECT 1 FROM topic_maintenance_sources AS chosen
		 WHERE chosen.team_id=state.team_id AND chosen.shared_space_id=state.shared_space_id AND chosen.space_generation=state.space_generation
		 AND chosen.source_kind='evidence' AND chosen.source_id=ANY(?::text[]) AND chosen.eligible AND chosen.status='pending')))
		 AND (?='' OR EXISTS(SELECT 1 FROM topic_maintenance_sources AS retry_source
		 WHERE retry_source.team_id=state.team_id AND retry_source.shared_space_id=state.shared_space_id AND retry_source.space_generation=state.space_generation AND retry_source.eligible AND
		 ((retry_source.last_run_id=NULLIF(?,'')::uuid AND retry_source.status='failed') OR
		  (? AND retry_source.equivalence_last_run_id=NULLIF(?,'')::uuid AND retry_source.equivalence_status='failed'))))
		 AND (?<>'' OR state.discovery_kind<1 OR state.next_discovery_at<=clock_timestamp() OR EXISTS(
		 SELECT 1 FROM topic_maintenance_markers AS marker WHERE marker.team_id=state.team_id AND marker.shared_space_id=state.shared_space_id AND marker.space_generation=state.space_generation)
		 OR EXISTS(SELECT 1 FROM topic_maintenance_sources AS source WHERE source.team_id=state.team_id AND source.shared_space_id=state.shared_space_id AND source.space_generation=state.space_generation AND source.eligible AND
		 (source.status='pending' OR (? AND source.equivalence_status='pending')))
		 OR (? AND EXISTS(SELECT 1 FROM topics AS pending_topic JOIN embedding_contracts AS active_contract
		 ON active_contract.lifecycle_state='active' AND active_contract.distance_metric='cosine'
		 JOIN search_index_generations AS active_generation ON active_generation.embedding_contract_id=active_contract.embedding_contract_id
		 AND active_generation.activation_state='active'
		 WHERE pending_topic.team_id=state.team_id AND pending_topic.shared_space_id=state.shared_space_id
		 AND pending_topic.space_generation=state.space_generation AND NOT pending_topic.retired AND NOT EXISTS(
		 SELECT 1 FROM topic_embeddings AS embedding WHERE
		 (embedding.team_id,embedding.shared_space_id,embedding.space_generation,embedding.topic_id)=
		 (pending_topic.team_id,pending_topic.shared_space_id,pending_topic.space_generation,pending_topic.topic_id)
		 AND embedding.embedding_contract_id=active_contract.embedding_contract_id AND embedding.topic_version=pending_topic.definition_version)))
		 OR (? AND EXISTS(SELECT 1 FROM topic_embeddings AS embedding JOIN topics AS current
		 USING(team_id,shared_space_id,space_generation,topic_id) JOIN embedding_contracts AS contract
		 ON contract.embedding_contract_id=embedding.embedding_contract_id AND contract.lifecycle_state='active'
		 WHERE embedding.team_id=state.team_id AND embedding.shared_space_id=state.shared_space_id AND embedding.space_generation=state.space_generation
		 AND embedding.consolidation_pending AND (embedding.consolidation_lease_until IS NULL OR embedding.consolidation_lease_until<=clock_timestamp())
		 AND embedding.topic_version=current.definition_version AND NOT current.retired))
		 OR EXISTS(SELECT 1 FROM topic_maintenance_community_work AS work JOIN topics AS current
		 USING(team_id,shared_space_id,space_generation,topic_id)
		 WHERE work.team_id=state.team_id AND work.shared_space_id=state.shared_space_id AND work.space_generation=state.space_generation
		 AND NOT current.retired AND `+communityEligible+` AND `+communityWorkReady+`))
		 ORDER BY state.last_turn,state.team_id LIMIT 16`, now, now, limits, window.Policy.TeamConcurrency(""),
			next.reassessTeamID, next.reassessTeamID, pq.Array(next.reassessEvidenceIDs),
			next.retryRunID, next.retryRunID, topic.EquivalenceStageEnabled(ctx), next.retryRunID, next.retryRunID,
			topic.EquivalenceStageEnabled(ctx),
			topic.TopicEmbeddingStageEnabled(ctx), topic.ConsolidationStageEnabled(ctx)).Rows()
		if err != nil {
			return err
		}
		for rows.Next() {
			var item scope
			if err := rows.Scan(&item.TeamID, &item.SpaceID, &item.Generation); err != nil {
				rows.Close()
				return err
			}
			next.candidates = append(next.candidates, item)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(next.candidates) == 0 && next.active == 0 && !next.reopenScheduled {
			next.unresolved, err = maintenanceRunHasUnresolvedSources(tx, next.runID, next.retryRunID, next.retryRunID == "" && next.reassessTeamID == "")
			if err != nil {
				return err
			}
		}
		preview = next
		return nil
	})
	return preview, err
}

func (s *Store) ClaimMaintenanceTurn(ctx context.Context, windowID string, now time.Time, lease time.Duration) (*topic.MaintenanceTurn, error) {
	if _, err := uuid.Parse(windowID); err != nil || lease <= 0 {
		return nil, topic.ErrInvalid
	}
	if err := s.recoverMaintenanceBatches(ctx, now); err != nil {
		return nil, err
	}
	if err := s.expireOverdueMaintenanceBatches(ctx, now); err != nil {
		return nil, err
	}
	if err := s.seedMissingMaintenanceTeams(ctx); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		preview, err := s.previewMaintenanceTurn(ctx, windowID, now)
		if err != nil || preview == nil {
			return nil, err
		}
		if len(preview.candidates) == 0 && (preview.active > 0 || preview.reopenScheduled) {
			return nil, nil
		}
		var result *topic.MaintenanceTurn
		retryPreview := false
		err = s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
			paused, err := maintenanceState(tx)
			if err != nil {
				return err
			}
			if paused {
				return topic.ErrMaintenancePaused
			}
			enabled, err := maintenanceEnabled(tx)
			if err != nil {
				return err
			}
			if !enabled {
				return topic.ErrMaintenanceDisabled
			}
			window, err := scanMaintenanceWindow(tx.Raw(`SELECT `+maintenanceWindowColumns+` FROM topic_maintenance_windows WHERE window_id=?::uuid`, windowID).Row())
			if err != nil {
				return err
			}
			if window == nil || !now.Before(window.EndsAt) {
				return topic.ErrWindowClosed
			}
			active, err := activeMaintenanceWork(tx, now)
			if err != nil {
				return err
			}
			if active >= window.Policy.MaxConcurrency {
				return nil
			}
			candidate := &topic.MaintenanceTurn{LeaseToken: uuid.NewString(), LeaseUntil: now.Add(lease),
				RetryRunID: preview.retryRunID, ReassessTeamID: preview.reassessTeamID, ReassessEvidenceIDs: preview.reassessEvidenceIDs}
			reopen := false
			err = tx.Raw(`SELECT run_id::text`+nextMaintenanceRun, windowID).Row().Scan(&candidate.RunID)
			if errors.Is(err, sql.ErrNoRows) && preview.reopenScheduled {
				err = tx.Raw(`SELECT run_id::text FROM topic_maintenance_runs WHERE run_id=?::uuid AND status IN ('completed','incomplete')
					FOR NO KEY UPDATE SKIP LOCKED`, preview.runID).Row().Scan(&candidate.RunID)
				reopen = err == nil
			}
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if candidate.RunID != preview.runID {
				retryPreview = true
				return nil
			}
			if len(preview.candidates) == 0 {
				if active > 0 {
					return nil
				}
				var live bool
				err := tx.Raw(`SELECT true FROM topic_maintenance_runs WHERE run_id=?::uuid AND updated_at=?
					AND status IN ('pending','running') FOR NO KEY UPDATE SKIP LOCKED`, candidate.RunID, preview.runUpdatedAt).Row().Scan(&live)
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				if err != nil {
					return err
				}
				return writeMaintenanceRunOutcome(tx, candidate.RunID, preview.unresolved, "")
			}
			limits, err := maintenanceTeamLimits(window.Policy)
			if err != nil {
				return err
			}
			teamIDs := make([]string, len(preview.candidates))
			spaceIDs := make([]string, len(preview.candidates))
			generations := make([]int64, len(preview.candidates))
			for i, item := range preview.candidates {
				teamIDs[i], spaceIDs[i], generations[i] = item.TeamID, item.SpaceID, item.Generation
			}
			err = tx.Raw(`WITH preselected AS (
				SELECT team_id,shared_space_id,space_generation
				FROM unnest(?::uuid[],?::uuid[],?::bigint[]) AS p(team_id,shared_space_id,space_generation)
			)
			SELECT state.team_id::text,state.shared_space_id::text,state.space_generation,state.next_stage
			FROM topic_maintenance_teams AS state
			JOIN preselected USING(team_id,shared_space_id,space_generation)
			JOIN teams AS team ON team.id=state.team_id AND team.status='active' AND team.deleted_at IS NULL
			JOIN memory_spaces AS space ON space.team_id=state.team_id AND space.id=state.shared_space_id AND space.generation=state.space_generation AND space.lifecycle_state='active'
			WHERE (state.lease_until IS NULL OR state.lease_until<=?)
			AND (SELECT count(*) FROM topic_maintenance_batches AS active_batch WHERE active_batch.team_id=state.team_id AND active_batch.shared_space_id=state.shared_space_id AND active_batch.space_generation=state.space_generation AND active_batch.status='running' AND active_batch.lease_until>?) < COALESCE((?::jsonb->>state.team_id::text)::int,?)
			ORDER BY state.last_turn,state.team_id LIMIT 1 FOR UPDATE OF state SKIP LOCKED`,
				pq.Array(teamIDs), pq.Array(spaceIDs), pq.Array(generations),
				now, now, limits, window.Policy.TeamConcurrency("")).Row().Scan(&candidate.TeamID, &candidate.SpaceID, &candidate.Generation, &candidate.Stage)
			if errors.Is(err, sql.ErrNoRows) {
				if len(preview.candidates) == 16 && attempt == 0 {
					retryPreview = true
					return nil
				}
				return nil
			}
			if err != nil {
				return err
			}
			if reopen {
				if err := tx.Exec(`UPDATE topic_maintenance_runs SET status='pending',updated_at=clock_timestamp() WHERE run_id=?::uuid`, candidate.RunID).Error; err != nil {
					return err
				}
			}
			if err = tx.Exec(`UPDATE topic_maintenance_state SET turn_sequence=turn_sequence+1 WHERE singleton`).Error; err != nil {
				return err
			}
			if err = tx.Exec(`UPDATE topic_maintenance_teams SET last_turn=(SELECT turn_sequence FROM topic_maintenance_state WHERE singleton),next_stage=(next_stage+1)%5,lease_token=?::uuid,lease_until=? WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, candidate.LeaseToken, candidate.LeaseUntil, candidate.TeamID, candidate.SpaceID, candidate.Generation).Error; err != nil {
				return err
			}
			result = candidate
			return nil
		})
		if err != nil {
			return nil, err
		}
		if retryPreview {
			continue
		}
		return result, nil
	}
	return nil, nil
}

func activeMaintenanceWork(tx *gorm.DB, now time.Time) (int, error) {
	var active int
	err := tx.Raw(`SELECT (SELECT count(*) FROM topic_maintenance_teams WHERE lease_until>?)
	 + (SELECT count(*) FROM topic_maintenance_batches WHERE status='running' AND lease_until>?)`, now, now).Row().Scan(&active)
	return active, err
}

func maintenanceTeamLimits(policy domain.TopicMaintenanceConfig) (string, error) {
	limits := map[string]int{}
	for _, override := range policy.TeamConcurrencyOverrides {
		limits[override.TeamID] = policy.TeamConcurrency(override.TeamID)
	}
	encoded, err := json.Marshal(limits)
	return string(encoded), err
}

func (s *Store) ReleaseMaintenanceTurn(ctx context.Context, turn topic.MaintenanceTurn) error {
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		return releaseMaintenanceTurnTx(tx, turn)
	})
}

func releaseMaintenanceTurnTx(tx *gorm.DB, turn topic.MaintenanceTurn) error {
	return tx.Exec(`UPDATE topic_maintenance_teams SET lease_token=NULL,lease_until=NULL WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid`, turn.TeamID, turn.SpaceID, turn.Generation, turn.LeaseToken).Error
}

func checkMaintenanceTurn(tx *gorm.DB, turn topic.MaintenanceTurn) error {
	var found bool
	if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM topic_maintenance_teams WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid AND lease_until>clock_timestamp())`, turn.TeamID, turn.SpaceID, turn.Generation, turn.LeaseToken).Row().Scan(&found); err != nil {
		return err
	}
	if !found {
		return topic.ErrLeaseLost
	}
	return nil
}

func maintenancePublicationFence(ctx context.Context, tx *gorm.DB, fence scope) error {
	claim, ok := topic.MaintenanceClaimFromContext(ctx)
	if !ok {
		return nil
	}
	if claim.TeamID != fence.TeamID || claim.SpaceID != fence.SpaceID || claim.Generation != fence.Generation {
		return topic.ErrUnauthorized
	}
	var status string
	err := tx.Raw(`SELECT status FROM topic_maintenance_batches WHERE batch_id=?::uuid AND team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid AND lease_until>clock_timestamp() FOR UPDATE`, claim.ID, fence.TeamID, fence.SpaceID, fence.Generation, claim.LeaseToken).Row().Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "running") {
		return topic.ErrLeaseLost
	}
	return err
}

func lockMaintenanceRun(tx *gorm.DB, runID string) error {
	return tx.Exec(`SELECT 1 FROM topic_maintenance_runs WHERE run_id=?::uuid FOR NO KEY UPDATE`, runID).Error
}

func lockMaintenanceProgressTeam(tx *gorm.DB, fence scope) error {
	return tx.Exec(`SELECT 1 FROM topic_maintenance_teams
		WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? FOR NO KEY UPDATE`,
		fence.TeamID, fence.SpaceID, fence.Generation).Error
}

var _ topic.MaintenanceRepository = (*Store)(nil)

func expireMaintenanceBatches(tx *gorm.DB, now time.Time) (int64, error) {
	count, err := expireMaintenanceBatchRecords(tx, now)
	if err != nil {
		return count, err
	}
	err = tx.Exec(`UPDATE topic_maintenance_community_work SET lease_batch_id=NULL,lease_until=NULL
		WHERE lease_until<=?`, now).Error
	if err != nil {
		return count, err
	}
	err = tx.Exec(`UPDATE topic_embeddings SET consolidation_batch_id=NULL,consolidation_lease_until=NULL
		WHERE consolidation_lease_until<=?`, now).Error
	return count, err
}

func expireMaintenanceBatchRecords(tx *gorm.DB, now time.Time) (int64, error) {
	result := tx.Exec(`UPDATE topic_maintenance_batches SET status='lost',failure_code='lease_lost' WHERE status='running' AND lease_until<=?`, now)
	return result.RowsAffected, result.Error
}
