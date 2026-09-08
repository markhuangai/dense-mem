package postgres_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
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

func newPrivacySQLMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	return db, mock
}

func privateMemoryOperationRows(operationID, teamID, spaceID, credentialID uuid.UUID, requestedAt time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "team_id", "space_id", "space_kind", "target_credential_id",
		"action", "actor_class", "reason_code", "target_generation", "retire_space",
		"status", "manifest_position", "deleted_counts", "attempt_count", "fence",
		"worker_id", "lease_until", "next_attempt_at", "last_error_code", "requested_at", "started_at",
		"completed_at", "updated_at",
	}).AddRow(
		operationID, teamID, spaceID.String(), string(domain.MemorySpaceCredentialPrivate), credentialID.String(),
		string(domain.PrivateMemoryEraseCredentialPrivate), string(domain.PrivateMemoryActorOwnerCredential), "credential_deleted", int64(4), true,
		string(domain.PrivateMemoryErasureQueued), 2, []byte(`{"evidence_fragments":3}`), 1, int64(7),
		"worker-a", requestedAt.Add(time.Minute), nil, "", requestedAt, requestedAt.Add(10*time.Second),
		nil, requestedAt.Add(20*time.Second),
	)
}

func TestStoreGetOperationBindsIDAndScansResult(t *testing.T) {
	db, mock := newPrivacySQLMockDB(t)
	operationID, teamID, spaceID, credentialID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	requestedAt := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("FROM private_memory_erasure_operations AS operation WHERE id = $1")).
		WithArgs(operationID).
		WillReturnRows(privateMemoryOperationRows(operationID, teamID, spaceID, credentialID, requestedAt))

	store := privacypostgres.NewStore(db, passthroughRLS{})
	operation, err := store.GetOperation(context.Background(), operationID)

	require.NoError(t, err)
	require.Equal(t, operationID, operation.ID)
	require.Equal(t, teamID, operation.TeamID)
	require.Equal(t, spaceID, *operation.SpaceID)
	require.Equal(t, domain.MemorySpaceCredentialPrivate, *operation.SpaceKind)
	require.Equal(t, credentialID, *operation.TargetCredentialID)
	require.Equal(t, int64(4), *operation.TargetGeneration)
	require.Equal(t, map[string]int64{"evidence_fragments": 3}, operation.DeletedCounts)
	require.Equal(t, requestedAt.Add(time.Minute), *operation.LeaseUntil)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStoreListOperationsBindsPaginationAndScansRows(t *testing.T) {
	db, mock := newPrivacySQLMockDB(t)
	operationID, teamID, spaceID, credentialID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	requestedAt := time.Date(2026, 9, 8, 11, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("FROM private_memory_erasure_operations AS operation ORDER BY requested_at DESC, id DESC LIMIT $1 OFFSET $2")).
		WithArgs(25, 4).
		WillReturnRows(privateMemoryOperationRows(operationID, teamID, spaceID, credentialID, requestedAt))

	store := privacypostgres.NewStore(db, passthroughRLS{})
	operations, err := store.ListOperations(context.Background(), 25, 4)

	require.NoError(t, err)
	require.Len(t, operations, 1)
	require.Equal(t, operationID, operations[0].ID)
	require.Equal(t, domain.PrivateMemoryErasureQueued, operations[0].Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStoreListSpacesScansNullableOwnersAndActiveHold(t *testing.T) {
	db, mock := newPrivacySQLMockDB(t)
	spaceID, teamID, ownerID, holdID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	createdAt := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	privateContentAt := createdAt.Add(-time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT space.id, space.team_id, space.kind, space.owner_profile_id, space.owner_credential_id")).
		WithArgs(10, 2).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "team_id", "kind", "owner_profile_id", "owner_credential_id",
			"generation", "lifecycle_state", "private_content_at", "sealed_at", "retired_at", "created_at", "updated_at",
			"hold_id", "hold_reason", "hold_actor", "hold_placed",
		}).AddRow(
			spaceID, teamID, string(domain.MemorySpaceProfilePrivate), ownerID, nil,
			int64(3), string(domain.MemorySpaceActive), privateContentAt, nil, nil, createdAt, createdAt.Add(time.Minute),
			holdID.String(), "retention_review", "control", createdAt.Add(2*time.Minute),
		))

	store := privacypostgres.NewStore(db, passthroughRLS{})
	spaces, err := store.ListSpaces(context.Background(), 10, 2)

	require.NoError(t, err)
	require.Len(t, spaces, 1)
	require.Equal(t, spaceID, spaces[0].Space.ID)
	require.Equal(t, teamID, spaces[0].Space.TeamID)
	require.Equal(t, ownerID, *spaces[0].Space.OwnerProfileID)
	require.Nil(t, spaces[0].Space.OwnerCredentialID)
	require.NotNil(t, spaces[0].ActiveHold)
	require.Equal(t, holdID, spaces[0].ActiveHold.ID)
	require.Equal(t, "retention_review", spaces[0].ActiveHold.ReasonCode)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStoreGetOperationBoundsDatabaseError(t *testing.T) {
	db, mock := newPrivacySQLMockDB(t)
	operationID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta("FROM private_memory_erasure_operations AS operation WHERE id = $1")).
		WithArgs(operationID).
		WillReturnError(errors.New("pq: password=secret connection details"))

	store := privacypostgres.NewStore(db, passthroughRLS{})
	_, err := store.GetOperation(context.Background(), operationID)

	require.ErrorIs(t, err, privacypostgres.ErrPrivateMemoryInternal)
	require.NotContains(t, err.Error(), "password=secret")
	require.NoError(t, mock.ExpectationsWereMet())
}
