package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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

func newSynchronousRepositorySQLMock(t *testing.T) (*LedgerRepositoryImpl, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := &LedgerRepositoryImpl{db: db, rls: passthroughRLS{}}
	return repo, mock, func() { _ = sqlDB.Close() }
}

func expectActiveTeam(mock sqlmock.Sqlmock, teamID string) {
	mock.ExpectQuery("SELECT id::text").WithArgs(teamID).WillReturnRows(
		sqlmock.NewRows([]string{"id"}).AddRow(teamID),
	)
}

func TestClaimPlacementRunValidatesAndClaimsExactRun(t *testing.T) {
	repo, mock, cleanup := newSynchronousRepositorySQLMock(t)
	defer cleanup()
	teamID, ownerID, ingestID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	expectActiveTeam(mock, teamID)
	mock.ExpectQuery("UPDATE placement_runs AS run").WillReturnRows(sqlmock.NewRows([]string{
		"team_id", "placement_run_id", "ingest_id", "owner_profile_id", "space_id", "space_generation",
		"status", "attempts", "max_attempts", "assessor_turns_reserved", "lease_until",
	}).AddRow(teamID, uuid.NewString(), ingestID, ownerID, uuid.NewString(), int64(1), "processing", 1, 3, 0, time.Now().UTC()))
	mock.ExpectQuery(`SELECT COALESCE\(metadata`).WithArgs(teamID, ingestID).WillReturnRows(
		sqlmock.NewRows([]string{"correlation_id"}).AddRow("corr-1"),
	)
	run, err := repo.ClaimPlacementRun(context.Background(), ClaimPlacementRunInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID, WorkerID: "remember-sync", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Equal(t, ingestID, run.IngestID)
	require.Equal(t, "processing", run.Status)
	require.Equal(t, "corr-1", run.CorrelationID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimPlacementRunRejectsInvalidInput(t *testing.T) {
	repo := &LedgerRepositoryImpl{}
	tests := []ClaimPlacementRunInput{
		{TeamID: "bad", OwnerProfileID: uuid.NewString(), IngestID: uuid.NewString(), WorkerID: "w", Lease: time.Second},
		{TeamID: uuid.NewString(), OwnerProfileID: "bad", IngestID: uuid.NewString(), WorkerID: "w", Lease: time.Second},
		{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), IngestID: "bad", WorkerID: "w", Lease: time.Second},
		{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), IngestID: uuid.NewString(), Lease: time.Second},
		{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), IngestID: uuid.NewString(), WorkerID: "w", Lease: time.Millisecond},
		{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), IngestID: uuid.NewString(), WorkerID: "w", Lease: time.Second, StaleAfter: -time.Second},
	}
	for _, input := range tests {
		_, err := repo.ClaimPlacementRun(context.Background(), input)
		require.Error(t, err)
	}
}

func TestLoadRememberAttemptReturnsSafeReplayResult(t *testing.T) {
	repo, mock, cleanup := newSynchronousRepositorySQLMock(t)
	defer cleanup()
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	expectActiveTeam(mock, teamID)
	attemptID := uuid.NewString()
	mock.ExpectQuery("SELECT attempt_id::text").WithArgs(teamID, ownerID, "remember-key").WillReturnRows(
		sqlmock.NewRows([]string{"attempt_id", "request_hash", "outcome", "public_result"}).AddRow(
			attemptID, "hash-1", "completed", []byte(`{"submission_id":"`+attemptID+`","processing_state":"completed"}`),
		),
	)
	attempt, err := repo.LoadRememberAttempt(context.Background(), RememberAttemptLookupInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "remember-key",
	})
	require.NoError(t, err)
	require.Equal(t, attemptID, attempt.AttemptID)
	require.Equal(t, "hash-1", attempt.RequestHash)
	require.Equal(t, "completed", attempt.Outcome)
	require.Equal(t, "completed", attempt.PublicResult["processing_state"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLoadRememberAttemptValidatesInputAndMissingRows(t *testing.T) {
	repo := &LedgerRepositoryImpl{}
	_, err := repo.LoadRememberAttempt(context.Background(), RememberAttemptLookupInput{TeamID: "bad", OwnerProfileID: uuid.NewString(), IdempotencyKey: "key"})
	require.Error(t, err)
	_, err = repo.LoadRememberAttempt(context.Background(), RememberAttemptLookupInput{TeamID: uuid.NewString(), OwnerProfileID: "bad", IdempotencyKey: "key"})
	require.Error(t, err)
	_, err = repo.LoadRememberAttempt(context.Background(), RememberAttemptLookupInput{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString()})
	require.Error(t, err)

	validRepo, mock, cleanup := newSynchronousRepositorySQLMock(t)
	defer cleanup()
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	expectActiveTeam(mock, teamID)
	mock.ExpectQuery("SELECT attempt_id::text").WithArgs(teamID, ownerID, "missing").WillReturnError(sql.ErrNoRows)
	_, err = validRepo.LoadRememberAttempt(context.Background(), RememberAttemptLookupInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "missing"})
	require.ErrorIs(t, err, ErrRememberAttemptNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInlineEmbeddingWriteContext(t *testing.T) {
	ctx := context.Background()
	require.False(t, InlineEmbeddingWrite(ctx))
	require.True(t, InlineEmbeddingWrite(WithInlineEmbeddingWrites(ctx)))
}
