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
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	dreamConfirmationLockPrefix         = "dream-confirmation:"
	dreamConfirmationLockCleanupTimeout = 5 * time.Second
	// Keep a small dedicated session budget; the application pool cannot share its sessions across provider work.
	dreamConfirmationLockAdmissionLimit = 4
)

var ErrDreamConfirmationBusy = errors.New("dream confirmation is already in progress")

type dreamConfirmationLockAdmissionState struct {
	once  sync.Once
	slots chan struct{}
}

// WithHypothesisConfirmationLock admits one confirmation workflow for a
// team-owned Hypothesis without retaining the application pool while provider
// work runs. The advisory lock uses a dedicated session that is closed after
// the callback, so a failed unlock cannot strand a pooled application session.
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

	lockConn, lockDB, err := r.openDreamConfirmationLockConnection(ctx)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if closed {
			return
		}
		_ = lockConn.Close()
		_ = lockDB.Close()
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
	lockDBErr := lockDB.Close()
	closed = true
	if releaseErr != nil {
		releaseErr = fmt.Errorf("dream confirmation lock release: %w", releaseErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("dream confirmation lock connection close: %w", closeErr)
	}
	if lockDBErr != nil {
		lockDBErr = fmt.Errorf("dream confirmation lock database close: %w", lockDBErr)
	}
	cleanupErr := errors.Join(releaseErr, closeErr, lockDBErr)
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
		r.dreamConfirmationLockState.slots = make(chan struct{}, dreamConfirmationLockAdmissionLimit)
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

func (r *SemanticRepositoryImpl) openDreamConfirmationLockConnection(ctx context.Context) (*sql.Conn, *sql.DB, error) {
	if r == nil || r.db == nil {
		return nil, nil, errors.New("dream confirmation lock: database is required")
	}
	dialector, ok := r.db.Dialector.(*gormpostgres.Dialector)
	if !ok || dialector == nil || dialector.Config == nil || strings.TrimSpace(dialector.DSN) == "" || dialector.Conn != nil {
		return nil, nil, errors.New("dream confirmation lock: postgres DSN is required for a dedicated lock session")
	}
	lockConfig := &gorm.Config{}
	if r.db.Config != nil {
		lockConfig.Logger = r.db.Config.Logger
	}
	lockDB, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		DriverName:           dialector.DriverName,
		DSN:                  dialector.DSN,
		WithoutQuotingCheck:  dialector.WithoutQuotingCheck,
		PreferSimpleProtocol: dialector.PreferSimpleProtocol,
		WithoutReturning:     dialector.WithoutReturning,
	}), lockConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("dream confirmation lock: open dedicated database: %w", err)
	}
	lockSQL, err := lockDB.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("dream confirmation lock: dedicated database handle: %w", err)
	}
	lockSQL.SetMaxOpenConns(1)
	lockSQL.SetMaxIdleConns(1)
	lockConn, err := lockSQL.Conn(ctx)
	if err != nil {
		_ = lockSQL.Close()
		return nil, nil, fmt.Errorf("dream confirmation lock: acquire dedicated connection: %w", err)
	}
	return lockConn, lockSQL, nil
}
