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

var ErrDreamConfirmationBusy = errors.New("dream confirmation is already in progress")

// WithHypothesisConfirmationLock admits one confirmation workflow for a
// team-owned Hypothesis without retaining pooled connections for lock waiters.
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
	canonicalID := hypothesisID
	if record, err := r.GetHypothesis(ctx, GetHypothesisInput{TeamID: teamID, HypothesisID: hypothesisID}); err == nil {
		canonicalID = record.HypothesisID
	} else if !errors.Is(err, ErrDreamHypothesisNotFound) {
		return fmt.Errorf("dream confirmation lock resolve hypothesis: %w", err)
	}
	key := dreamConfirmationLockPrefix + teamID + ":" + canonicalID
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var acquired bool
		if err := tx.WithContext(ctx).Raw("SELECT pg_try_advisory_xact_lock(hashtext(?))", key).Row().Scan(&acquired); err != nil {
			return fmt.Errorf("dream confirmation lock acquire: %w", err)
		}
		if !acquired {
			return ErrDreamConfirmationBusy
		}
		return fn()
	})
}
