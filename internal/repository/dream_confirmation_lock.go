package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
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
	releaseAdmission, err := acquireSharedAdvisoryLockAdmission(ctx, r.db, dreamConfirmationLockAdmissionLimit)
	if err != nil {
		if errors.Is(err, errAdvisoryLockAdmissionBusy) {
			return ErrDreamConfirmationBusy
		}
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
	var closeErr error
	if releaseErr != nil {
		closeErr = discardDreamConfirmationLockConnection(lockConn)
		if closeErr == nil {
			closed = true
		} else {
			closeErr = errors.Join(closeErr, lockConn.Close())
			closed = true
		}
	} else {
		closeErr = lockConn.Close()
		closed = true
	}
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

func discardDreamConfirmationLockConnection(lockConn *sql.Conn) error {
	if lockConn == nil {
		return nil
	}
	err := lockConn.Raw(func(any) error {
		return driver.ErrBadConn
	})
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("dream confirmation lock discard: %w", err)
	}
	return errors.New("dream confirmation lock discard: connection was not discarded")
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
