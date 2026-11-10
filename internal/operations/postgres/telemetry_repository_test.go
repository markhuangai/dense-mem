package postgres

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

func newOperationsMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *gorm.DB) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	return sqlDB, mock, db
}

type passthroughRLS struct{}

func (passthroughRLS) WithTeamTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithTeamProfileTx(_ context.Context, db *gorm.DB, _, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithSystemTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithTeamReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithTeamProfileReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (passthroughRLS) WithSystemReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}
