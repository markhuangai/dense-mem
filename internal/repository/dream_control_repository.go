package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	maxControlDreamRunLimit = 100
)

type DreamControlRepository interface {
	ListHypotheses(ctx context.Context, input ListHypothesesInput) ([]HypothesisRecord, string, error)
	GetHypothesis(ctx context.Context, input GetHypothesisInput) (*HypothesisRecord, error)
	CountHypotheses(ctx context.Context, teamID, status string) (int, error)
	ListDreamCyclesForTeam(ctx context.Context, teamID string, limit int) ([]DreamCycleRun, error)
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
			  AND canonical_hypothesis_id IS NULL
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
			SELECT team_id::text, run_id::text, COALESCE(initiated_by_profile_id::text, ''),
			       run_date, window_key, status, input_count,
			       created_hypotheses, rejected_hypotheses, error,
			       started_at, completed_at
			FROM dream_cycle_runs
			WHERE team_id = ?::uuid
			  AND canonical_run_id IS NULL
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
