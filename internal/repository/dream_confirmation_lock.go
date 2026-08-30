package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	dreamConfirmationLockPrefix         = "dream-confirmation:"
	dreamConfirmationLockCleanupTimeout = 5 * time.Second
	// Keep a small session budget so provider work cannot exhaust the application pool.
	dreamConfirmationLockAdmissionLimit = 4
)

var ErrDreamConfirmationBusy = errors.New("dream confirmation is already in progress")

type dreamConfirmationLockAdmissionState struct {
	once  sync.Once
	slots chan struct{}
}

// WithHypothesisConfirmationLock admits one confirmation workflow for a
// team-owned Hypothesis. The advisory lock uses one application-pool session
// through the callback, so lock sessions count against the configured pool.
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
	releaseAdmission, err := r.acquireDreamConfirmationLockAdmission(ctx)
	if err != nil {
		return err
	}
	defer releaseAdmission()
	canonicalID := hypothesisID
	if record, err := r.GetHypothesis(ctx, GetHypothesisInput{TeamID: teamID, HypothesisID: hypothesisID}); err == nil {
		if strings.TrimSpace(record.HypothesisID) != "" {
			canonicalID = record.HypothesisID
		}
	} else if !errors.Is(err, ErrDreamHypothesisNotFound) {
		return fmt.Errorf("dream confirmation lock resolve hypothesis: %w", err)
	}

	lockConn, err := r.openDreamConfirmationLockConnection(ctx)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if closed {
			return
		}
		_ = lockConn.Close()
	}()

	key := dreamConfirmationLockPrefix + teamID + ":" + canonicalID
	var acquired bool
	if err := lockConn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", key).Scan(&acquired); err != nil {
		return fmt.Errorf("dream confirmation lock acquire: %w", err)
	}
	if !acquired {
		return ErrDreamConfirmationBusy
	}

	callbackErr := fn(r)
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dreamConfirmationLockCleanupTimeout)
	var released bool
	releaseErr := lockConn.QueryRowContext(releaseCtx, "SELECT pg_advisory_unlock(hashtext($1))", key).Scan(&released)
	cancel()
	if releaseErr == nil && !released {
		releaseErr = errors.New("database did not release the advisory lock")
	}
	closeErr := lockConn.Close()
	closed = true
	if releaseErr != nil {
		releaseErr = fmt.Errorf("dream confirmation lock release: %w", releaseErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("dream confirmation lock connection close: %w", closeErr)
	}
	cleanupErr := errors.Join(releaseErr, closeErr)
	if callbackErr != nil {
		return errors.Join(callbackErr, cleanupErr)
	}
	if cleanupErr != nil && r.db.Logger != nil {
		r.db.Logger.Warn(ctx, "dream confirmation lock cleanup failed", "error_class", "database_cleanup")
	}
	return nil
}

func (r *SemanticRepositoryImpl) acquireDreamConfirmationLockAdmission(ctx context.Context) (func(), error) {
	r.dreamConfirmationLockState.once.Do(func() {
		limit := dreamConfirmationLockAdmissionLimit
		if sqlDB, err := r.db.DB(); err == nil {
			if configured := sqlDB.Stats().MaxOpenConnections; configured > 0 && configured-1 < limit {
				limit = configured - 1
			}
		}
		r.dreamConfirmationLockState.slots = make(chan struct{}, limit)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	select {
	case r.dreamConfirmationLockState.slots <- struct{}{}:
		return func() { <-r.dreamConfirmationLockState.slots }, nil
	default:
		return nil, ErrDreamConfirmationBusy
	}
}

func (r *SemanticRepositoryImpl) openDreamConfirmationLockConnection(ctx context.Context) (*sql.Conn, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("dream confirmation lock: database is required")
	}
	appDB, err := r.db.DB()
	if err != nil {
		return nil, fmt.Errorf("dream confirmation lock: application database handle: %w", err)
	}
	lockConn, err := appDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("dream confirmation lock: acquire application connection: %w", err)
	}
	return lockConn, nil
}
