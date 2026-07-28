package repository

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultControlDreamRefreshLimit = 200
	maxControlDreamRefreshLimit     = 500
	maxControlDreamRunLimit         = 100
)

type DreamControlRepository interface {
	ListHypotheses(ctx context.Context, input ListHypothesesInput) ([]HypothesisRecord, string, error)
	GetHypothesis(ctx context.Context, input GetHypothesisInput) (*HypothesisRecord, error)
	CountHypotheses(ctx context.Context, teamID, status string) (int, error)
	ListDreamCyclesForTeam(ctx context.Context, teamID string, limit int) ([]DreamCycleRun, error)
	RefreshTeamHypothesisStaleness(ctx context.Context, input RefreshTeamHypothesisStalenessInput) (int, error)
}

type RefreshTeamHypothesisStalenessInput struct {
	TeamID        string
	Limit         int
	ActorSource   string
	ActorRole     string
	ClientIP      string
	CorrelationID string
}

var _ DreamControlRepository = (*SemanticRepositoryImpl)(nil)

func (r *SemanticRepositoryImpl) CountHypotheses(ctx context.Context, teamID, status string) (int, error) {
	teamID = strings.TrimSpace(teamID)
	status = strings.TrimSpace(status)
	if _, err := uuid.Parse(teamID); err != nil {
		return 0, fmt.Errorf("team_id is required: %w", err)
	}
	count := 0
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT count(*)
			FROM hypotheses
			WHERE team_id = ?::uuid
			  AND (? = '' OR status = ?)
		`, teamID, status, status).Scan(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("dream: count hypotheses: %w", err)
	}
	return count, nil
}

func (r *SemanticRepositoryImpl) ListDreamCyclesForTeam(ctx context.Context, teamID string, limit int) ([]DreamCycleRun, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > maxControlDreamRunLimit {
		limit = maxControlDreamRunLimit
	}
	runs := []DreamCycleRun{}
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, run_id::text, owner_profile_id::text,
			       run_date, window_key, status, input_count,
			       created_hypotheses, rejected_hypotheses, error,
			       started_at, completed_at
			FROM dream_cycle_runs
			WHERE team_id = ?::uuid
			ORDER BY started_at DESC, run_id
			LIMIT ?
		`, teamID, limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanDreamCycleRun(rows)
			if err != nil {
				return err
			}
			runs = append(runs, *run)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("dream: list team cycles: %w", err)
	}
	return runs, nil
}

func (r *SemanticRepositoryImpl) RefreshTeamHypothesisStaleness(
	ctx context.Context,
	input RefreshTeamHypothesisStalenessInput,
) (int, error) {
	input = normalizeRefreshTeamHypothesisStalenessInput(input)
	if err := validateRefreshTeamHypothesisStalenessInput(input); err != nil {
		return 0, err
	}
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("dream: refresh team hypothesis staleness: database is required")
	}
	if r.rls == nil {
		return 0, fmt.Errorf("dream: refresh team hypothesis staleness: rls helper is required")
	}

	active := false
	updated := 0
	audited := 0
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		// Team scope authorizes the row lock while system mode permits cross-owner updates.
		if err := tx.Exec("SELECT set_config('app.current_team_id', ?, true)", input.TeamID).Error; err != nil {
			return fmt.Errorf("set team context: %w", err)
		}
		if err := ensureActiveTeamForMutation(ctx, tx, input.TeamID); err != nil {
			return err
		}
		return tx.WithContext(ctx).Raw(`
			WITH active_team AS MATERIALIZED (
			    SELECT id
			    FROM teams
			    WHERE id = ?::uuid
			      AND status = 'active'
			      AND deleted_at IS NULL
			),
			stale AS MATERIALIZED (
			    SELECT h.team_id, h.hypothesis_id
			    FROM hypotheses h
			    JOIN active_team t ON t.id = h.team_id
			    WHERE h.status IN ('proposed', 'reinforced')
			      AND `+hypothesisStaleSourcePredicateSQL+`
			    ORDER BY h.updated_at, h.hypothesis_id
			    LIMIT ?
			),
			updated AS (
			    UPDATE hypotheses hypothesis
			    SET status = 'stale',
			        invalidated_reason = 'source relationship changed or became ineligible',
			        updated_at = now()
			    FROM stale
			    WHERE hypothesis.team_id = stale.team_id
			      AND hypothesis.hypothesis_id = stale.hypothesis_id
			    RETURNING hypothesis.hypothesis_id
			),
			audit AS (
			    INSERT INTO audit_log (
			        team_id, operation, entity_type, entity_id,
			        after_payload, actor_role, client_ip, correlation_id, metadata
			    )
			    SELECT active_team.id,
			           'dream_staleness_refreshed',
			           'dream_hypotheses',
			           active_team.id::text,
			           jsonb_build_object('updated_count', (SELECT count(*) FROM updated)),
			           ?,
			           NULLIF(?, '')::inet,
			           NULLIF(?, ''),
			           jsonb_build_object('actor_source', ?::text, 'limit', ?::integer)
			    FROM active_team
			    RETURNING 1
			)
			SELECT EXISTS(SELECT 1 FROM active_team),
			       (SELECT count(*) FROM updated),
			       (SELECT count(*) FROM audit)
		`, input.TeamID, input.Limit, input.ActorRole, input.ClientIP,
			input.CorrelationID, input.ActorSource, input.Limit).
			Row().
			Scan(&active, &updated, &audited)
	})
	if err != nil {
		return 0, fmt.Errorf("dream: refresh team hypothesis staleness: %w", err)
	}
	if !active {
		return 0, ErrTeamInactive
	}
	if audited != 1 {
		return 0, fmt.Errorf("dream: refresh team hypothesis staleness: audit record was not written")
	}
	return updated, nil
}

func normalizeRefreshTeamHypothesisStalenessInput(input RefreshTeamHypothesisStalenessInput) RefreshTeamHypothesisStalenessInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ActorSource = strings.TrimSpace(input.ActorSource)
	input.ActorRole = strings.TrimSpace(input.ActorRole)
	input.ClientIP = strings.TrimSpace(input.ClientIP)
	input.CorrelationID = strings.TrimSpace(input.CorrelationID)
	if input.Limit <= 0 {
		input.Limit = defaultControlDreamRefreshLimit
	}
	return input
}

func validateRefreshTeamHypothesisStalenessInput(input RefreshTeamHypothesisStalenessInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.Limit > maxControlDreamRefreshLimit {
		return fmt.Errorf("limit must be at most %d", maxControlDreamRefreshLimit)
	}
	if input.ActorSource == "" || len(input.ActorSource) > 128 {
		return fmt.Errorf("actor_source must be between 1 and 128 characters")
	}
	if input.ActorRole == "" || len(input.ActorRole) > 20 {
		return fmt.Errorf("actor_role must be between 1 and 20 characters")
	}
	if input.ClientIP != "" {
		if _, err := netip.ParseAddr(input.ClientIP); err != nil {
			return fmt.Errorf("client_ip must be an IP address: %w", err)
		}
	}
	if len(input.CorrelationID) > 255 {
		return fmt.Errorf("correlation_id must be at most 255 characters")
	}
	return nil
}
