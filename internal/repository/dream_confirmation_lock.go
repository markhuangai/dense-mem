package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const dreamConfirmationLockPrefix = "dream-confirmation:"

// WithHypothesisConfirmationLock serializes confirmation workflows for one
// team-owned Hypothesis without holding the semantic transaction over provider work.
func (r *SemanticRepositoryImpl) WithHypothesisConfirmationLock(
	ctx context.Context,
	teamID string,
	hypothesisID string,
	fn func() error,
) error {
	if r == nil || r.db == nil {
		return errors.New("dream confirmation lock: database is required")
	}
	teamID = strings.TrimSpace(teamID)
	hypothesisID = strings.TrimSpace(hypothesisID)
	if _, err := uuid.Parse(teamID); err != nil {
		return fmt.Errorf("dream confirmation lock: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(hypothesisID); err != nil {
		return fmt.Errorf("dream confirmation lock: hypothesis_id is required: %w", err)
	}
	if fn == nil {
		return errors.New("dream confirmation lock: callback is required")
	}
	key := dreamConfirmationLockPrefix + teamID + ":" + hypothesisID
	return r.db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		if err := conn.WithContext(ctx).Exec("SELECT pg_advisory_lock(hashtext(?))", key).Error; err != nil {
			return fmt.Errorf("dream confirmation lock acquire: %w", err)
		}
		callbackErr := fn()
		unlockErr := conn.WithContext(ctx).Exec("SELECT pg_advisory_unlock(hashtext(?))", key).Error
		if callbackErr != nil {
			return callbackErr
		}
		if unlockErr != nil {
			return fmt.Errorf("dream confirmation lock release: %w", unlockErr)
		}
		return nil
	})
}
