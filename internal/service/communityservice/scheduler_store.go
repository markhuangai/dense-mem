package communityservice

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

type postgresSchedulerRunStore struct {
	db *gorm.DB
}

func NewPostgresSchedulerRunStore(db *gorm.DB) SchedulerRunStore {
	return &postgresSchedulerRunStore{db: db}
}

func (s *postgresSchedulerRunStore) TryMarkRun(ctx context.Context, profileID, runDate string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("community scheduler run store: db is required")
	}
	tx := s.db.WithContext(ctx).Exec(`
INSERT INTO community_detection_runs (profile_id, run_date)
VALUES (?, ?)
ON CONFLICT (profile_id, run_date) DO NOTHING`, profileID, runDate)
	if tx.Error != nil {
		return false, fmt.Errorf("community scheduler run reserve: %w", tx.Error)
	}
	return tx.RowsAffected > 0, nil
}

func (s *postgresSchedulerRunStore) Prune(ctx context.Context, beforeRunDate string) error {
	if s == nil || s.db == nil {
		return errors.New("community scheduler run store: db is required")
	}
	if err := s.db.WithContext(ctx).Exec(`
DELETE FROM community_detection_runs
WHERE run_date < ?`, beforeRunDate).Error; err != nil {
		return fmt.Errorf("community scheduler run prune: %w", err)
	}
	return nil
}
