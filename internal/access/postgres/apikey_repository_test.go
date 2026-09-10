package postgres_test

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
)

type accessPassthroughRLS struct{}

func (accessPassthroughRLS) WithTeamTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (accessPassthroughRLS) WithTeamProfileTx(_ context.Context, db *gorm.DB, _, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (accessPassthroughRLS) WithSystemTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (accessPassthroughRLS) WithTeamReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (accessPassthroughRLS) WithTeamProfileReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}

func (accessPassthroughRLS) WithSystemReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}

func newAccessSQLMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	return db, mock
}

func TestCredentialRepositoryTouchLastUsedBatchKeepsNewestTimestamp(t *testing.T) {
	db, mock := newAccessSQLMockDB(t)
	id := uuid.New()
	older := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE credentials AS credential")).
		WithArgs(id, newer).
		WillReturnResult(sqlmock.NewResult(0, 1))

	repo := accesspostgres.NewCredentialRepository(db, accessPassthroughRLS{}, nil)
	require.NoError(t, repo.TouchLastUsedBatch(context.Background(), []accesspostgres.LastUsedUpdate{
		{ID: id, At: older},
		{ID: id, At: newer},
		{ID: id, At: older},
		{ID: uuid.Nil, At: newer},
		{ID: id, At: time.Time{}},
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}
