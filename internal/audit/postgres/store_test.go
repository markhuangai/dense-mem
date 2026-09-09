package postgres

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/audit/contract"
)

func newMockStore(t *testing.T) (*Store, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true})
	require.NoError(t, err)
	return NewStore(db, nil), mock, func() { _ = sqlDB.Close() }
}

func TestStoreAppendPersistsSerializedEntry(t *testing.T) {
	store, mock, cleanup := newMockStore(t)
	defer cleanup()

	args := make([]driver.Value, 0, 14)
	for i := 0; i < 14; i++ {
		args = append(args, sqlmock.AnyArg())
	}
	mock.ExpectExec("INSERT INTO audit_log").WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.Append(context.Background(), contract.Entry{
		ID:            "audit-1",
		ProfileID:     stringPointer("team-1"),
		Timestamp:     time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		Operation:     "CREATE",
		EntityType:    "profile",
		EntityID:      "team-1",
		BeforePayload: []byte(`null`),
		AfterPayload:  []byte(`{"name":"safe"}`),
		Metadata:      []byte(`{}`),
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStoreAppendInfersCredentialSpaceOnlyWithinTeam(t *testing.T) {
	store, mock, cleanup := newMockStore(t)
	defer cleanup()
	teamID := uuid.New()
	credentialID := uuid.New()
	spaceID := uuid.New()
	mock.ExpectQuery("SELECT memory_space_id::text\\s+FROM credentials\\s+WHERE id = \\$1 AND team_id = \\$2").
		WithArgs(credentialID, teamID).
		WillReturnRows(sqlmock.NewRows([]string{"memory_space_id"}).AddRow(spaceID.String()))

	args := make([]driver.Value, 0, 14)
	for i := 0; i < 13; i++ {
		args = append(args, sqlmock.AnyArg())
	}
	args = append(args, spaceID.String())
	mock.ExpectExec("INSERT INTO audit_log").WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
	team := teamID.String()
	err := store.Append(context.Background(), contract.Entry{
		ProfileID: &team, EntityType: "api_key", EntityID: credentialID.String(), Operation: "DELETE", Metadata: []byte(`{}`),
		CredentialMemorySpaceLookup: &contract.CredentialMemorySpaceLookup{TeamID: teamID, CredentialID: credentialID},
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStoreListAndCountUseTheActiveSpacePredicate(t *testing.T) {
	store, mock, cleanup := newMockStore(t)
	defer cleanup()
	when := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT id, team_id, timestamp, operation, entity_type, entity_id[\\s\\S]+ORDER BY audit\\.timestamp DESC, audit\\.id DESC[\\s\\S]+LIMIT").
		WithArgs("team-1", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "team_id", "timestamp", "operation", "entity_type", "entity_id",
			"before_payload", "after_payload", "actor_profile_id", "actor_role",
			"client_ip", "correlation_id", "metadata", "memory_space_id",
		}).AddRow("audit-1", "team-1", when, "UPDATE", "profile", "team-1",
			[]byte(`{"before":true}`), []byte(`{"after":true}`), "profile-1", "admin",
			"203.0.113.10", "corr-1", []byte(`{"source":"test"}`), "space-1"))
	entries, err := store.List(context.Background(), "team-1", 20, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "audit-1", entries[0].ID)
	require.Equal(t, "space-1", *entries[0].MemorySpaceID)

	mock.ExpectQuery("SELECT COUNT\\(\\*\\)\\s+FROM audit_log AS audit\\s+WHERE audit.team_id = \\$1").
		WithArgs("team-1").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	count, err := store.Count(context.Background(), "team-1")
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStoreRequiresDatabase(t *testing.T) {
	require.ErrorContains(t, NewStore(nil, nil).Append(context.Background(), contract.Entry{}), "database is required")
	_, err := NewStore(nil, nil).List(context.Background(), "team", 1, 0)
	require.ErrorContains(t, err, "database is required")
	_, err = NewStore(nil, nil).Count(context.Background(), "team")
	require.ErrorContains(t, err, "database is required")
}

func stringPointer(value string) *string {
	return &value
}
