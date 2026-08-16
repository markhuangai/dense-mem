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
	actorID := uuid.New()
	membershipID := uuid.New()
	ownerID := uuid.New()
	teamID := uuid.New()
	rows := sqlmock.NewRows([]string{
		"id", "actor_identity_id", "membership_id", "owner_id", "team_id", "team_name",
		"key_hash", "key_prefix", "key_suffix", "name", "scopes", "rate_limit", "role",
		"last_used_at", "expires_at", "created_at", "revoked_at", "owner_identity_id",
		"sso_provider_id", "sso_subject", "sso_email", "sso_group_id", "sso_entitlement_status",
		"sso_last_entitlement_checked_at", "sso_last_login_at",
	}).AddRow(
		id.String(), actorID.String(), membershipID.String(), ownerID.String(), teamID.String(), "Project",
		"hash", "dm_test_key", "suffix", "key", "{read,write}", 23, "manager",
		nil, nil, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC), nil, "",
		"", "", "", "", "", nil, nil,
	)
	mock.ExpectQuery("SELECT").WithArgs("dm_test_key").WillReturnRows(rows)

	key, err := lookupCanonicalCredential(db, "dm_test_key")
	require.NoError(t, err)
	require.Equal(t, id, key.ID)
	require.Equal(t, actorID, key.ActorIdentityID)
	require.Equal(t, membershipID, key.MembershipID)
	require.Equal(t, ownerID, key.OwnerID)
	require.Equal(t, teamID, key.TeamID)
	require.Equal(t, "manager", key.Role)
	require.Equal(t, 23, key.RateLimit)
	require.Equal(t, []string{"read", "write"}, key.Scopes)
	require.NoError(t, mock.ExpectationsWereMet())
}
