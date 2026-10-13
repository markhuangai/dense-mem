package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) NewTopicProjectionAdmission() func(context.Context, *gorm.DB, ontology.MaintenanceTurn, time.Time) (ontology.TopicProjectionAdmission, error) {
	return func(ctx context.Context, tx *gorm.DB, turn ontology.MaintenanceTurn, now time.Time) (ontology.TopicProjectionAdmission, error) {
		if err := requireAutomatic(ctx); err != nil {
			return ontology.TopicProjectionUnavailable, err
		}
		paused, err := maintenanceState(tx)
		if err != nil || paused {
			return ontology.TopicProjectionUnavailable, err
		}
		var concurrency int
		err = tx.Raw(`SELECT (policy->>'max_concurrency')::int FROM ontology_maintenance_windows
			WHERE starts_at<=? AND ends_at>? ORDER BY starts_at DESC LIMIT 1`, now, now).Row().Scan(&concurrency)
		if errors.Is(err, sql.ErrNoRows) {
			return ontology.TopicProjectionUnavailable, nil
		}
		if err != nil {
			return ontology.TopicProjectionUnavailable, err
		}
		if err := seedMaintenanceTeams(tx); err != nil {
			return ontology.TopicProjectionUnavailable, err
		}
		var active int
		if err := tx.Raw(`SELECT count(*) FROM ontology_maintenance_teams WHERE lease_until>?`, now).Row().Scan(&active); err != nil {
			return ontology.TopicProjectionUnavailable, err
		}
		if active >= concurrency {
			return ontology.TopicProjectionUnavailable, nil
		}
		if err := tx.Exec(`UPDATE ontology_maintenance_state SET turn_sequence=turn_sequence+1 WHERE singleton`).Error; err != nil {
			return ontology.TopicProjectionUnavailable, err
		}
		result := tx.Exec(`UPDATE ontology_maintenance_teams SET lease_token=?::uuid,lease_until=?,
			last_turn=(SELECT turn_sequence FROM ontology_maintenance_state WHERE singleton)
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND (lease_until IS NULL OR lease_until<=?)`,
			turn.LeaseToken, turn.LeaseUntil, turn.TeamID, turn.SpaceID, turn.Generation, now)
		if result.Error != nil {
			return ontology.TopicProjectionUnavailable, result.Error
		}
		if result.RowsAffected == 1 {
			return ontology.TopicProjectionAdmitted, nil
		}
		return ontology.TopicProjectionTeamBusy, nil
	}
}

func (s *Store) NewTopicProjectionRelease() func(context.Context, *gorm.DB, ontology.MaintenanceTurn) error {
	return func(ctx context.Context, tx *gorm.DB, turn ontology.MaintenanceTurn) error {
		if err := requireAutomatic(ctx); err != nil {
			return err
		}
		return releaseMaintenanceTurnTx(tx, turn)
	}
}
