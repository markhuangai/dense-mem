package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	fn func(DreamRepository) error,
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
		lockedRepo := *r
		lockedRepo.db = conn
		canonicalID := hypothesisID
		if record, err := lockedRepo.GetHypothesis(ctx, GetHypothesisInput{TeamID: teamID, HypothesisID: hypothesisID}); err == nil {
			canonicalID = record.HypothesisID
		} else if !errors.Is(err, ErrDreamHypothesisNotFound) {
			return fmt.Errorf("dream confirmation lock resolve hypothesis: %w", err)
		}
		key = dreamConfirmationLockPrefix + teamID + ":" + canonicalID
		if err := conn.WithContext(ctx).Exec("SELECT pg_advisory_lock(hashtext(?))", key).Error; err != nil {
			return fmt.Errorf("dream confirmation lock acquire: %w", err)
		}
		callbackErr := fn(&lockedRepo)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		unlockErr := conn.WithContext(cleanupCtx).Exec("SELECT pg_advisory_unlock(hashtext(?))", key).Error
		if unlockErr != nil {
			unlockErr = fmt.Errorf("dream confirmation lock release: %w", unlockErr)
		}
		return errors.Join(callbackErr, unlockErr)
	})
}
