package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

const v2MigrationLeaderAdvisoryKey int64 = 0x444d56324d4947

type MigrationLeaderLock struct {
	db *gorm.DB
}

type MigrationLeaderLease struct {
	conn *sql.Conn
}

func NewMigrationLeaderLock(db *gorm.DB) *MigrationLeaderLock {
	return &MigrationLeaderLock{db: db}
}

func (l *MigrationLeaderLock) TryLock(ctx context.Context) (interface {
	Release(context.Context) error
}, error) {
	if l == nil || l.db == nil {
		return nil, errors.New("migration leader lock: postgres db is required")
	}
	sqlDB, err := l.db.DB()
	if err != nil {
		return nil, fmt.Errorf("migration leader lock: sql db: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration leader lock: open session: %w", err)
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", v2MigrationLeaderAdvisoryKey).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migration leader lock: acquire: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, nil
	}
	return &MigrationLeaderLease{conn: conn}, nil
}

func (l *MigrationLeaderLease) Release(ctx context.Context) error {
	if l == nil || l.conn == nil {
		return nil
	}
	defer l.conn.Close()
	var released bool
	if err := l.conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", v2MigrationLeaderAdvisoryKey).Scan(&released); err != nil {
		return fmt.Errorf("migration leader lock: release: %w", err)
	}
	if !released {
		return errors.New("migration leader lock: lock was not held")
	}
	return nil
}
