package migrationapp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

const HeartbeatInterval = 10 * time.Second

type infoLogger interface {
	Info(msg string, args ...any)
}

type migrationFunc func(context.Context, *gorm.DB) error

func RunUp(parent context.Context, db *gorm.DB, timeout time.Duration, logger infoLogger) error {
	return run(parent, db, timeout, "up", logger, postgres.RunUp)
}

func RunDown(parent context.Context, db *gorm.DB, timeout time.Duration, logger infoLogger) error {
	return run(parent, db, timeout, "down", logger, postgres.RunDown)
}

func run(parent context.Context, db *gorm.DB, timeout time.Duration, direction string, logger infoLogger, migrate migrationFunc) (err error) {
	return runWithInterval(parent, db, timeout, direction, logger, HeartbeatInterval, migrate)
}

func runWithInterval(parent context.Context, db *gorm.DB, timeout time.Duration, direction string, logger infoLogger, interval time.Duration, migrate migrationFunc) (err error) {
	if timeout <= 0 {
		return fmt.Errorf("migration timeout must be greater than zero")
	}
	if interval <= 0 {
		return fmt.Errorf("migration heartbeat interval must be greater than zero")
	}
	if logger == nil {
		return fmt.Errorf("migration logger is required")
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	startedAt := time.Now()
	logger.Info(
		"running postgres migrations",
		"direction", direction,
		"timeout_seconds", int64(timeout/time.Second),
	)

	stopHeartbeat := make(chan struct{})
	heartbeatStopped := make(chan struct{})
	go reportHeartbeat(stopHeartbeat, heartbeatStopped, startedAt, timeout, direction, logger, interval)

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			close(stopHeartbeat)
			<-heartbeatStopped
		})
	}
	defer stop()

	err = migrate(ctx, db)
	stop()
	if err != nil {
		return err
	}

	logger.Info(
		"postgres migrations completed",
		"direction", direction,
		"elapsed_seconds", int64(time.Since(startedAt)/time.Second),
	)
	return nil
}

func reportHeartbeat(stop <-chan struct{}, stopped chan<- struct{}, startedAt time.Time, timeout time.Duration, direction string, logger infoLogger, interval time.Duration) {
	defer close(stopped)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			select {
			case <-stop:
				return
			default:
			}
			logger.Info(
				"postgres migrations still running",
				"direction", direction,
				"elapsed_seconds", int64(time.Since(startedAt)/time.Second),
				"timeout_seconds", int64(timeout/time.Second),
			)
		}
	}
}
