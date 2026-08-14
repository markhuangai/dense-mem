package repository

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestLookupCanonicalCredentialScansRateLimitBeforeRole(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sqlDB.Close()
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	id := uuid.New()
	teamID := uuid.New()
	rows := sqlmock.NewRows([]string{
		"id", "team_id", "team_name", "key_hash", "key_suffix", "name", "scopes", "rate_limit", "role",
		"last_used_at", "expires_at", "created_at", "revoked_at", "owner_identity_id",
	}).AddRow(
		id.String(), teamID.String(), "Project", "hash", "suffix", "key", "{read,write}", 23, "manager",
		nil, nil, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC), nil, "",
	)
	mock.ExpectQuery("SELECT").WithArgs("dm_test_key").WillReturnRows(rows)

	key, err := lookupCanonicalCredential(db, "dm_test_key")
	require.NoError(t, err)
	require.Equal(t, id, key.ID)
	require.Equal(t, "manager", key.Role)
	require.Equal(t, 23, key.RateLimit)
	require.Equal(t, []string{"read", "write"}, key.Scopes)
	require.NoError(t, mock.ExpectationsWereMet())
}
