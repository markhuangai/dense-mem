package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestSecurityRepositorySettingsAndFailureSQL(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)
	now := time.Date(2026, 9, 9, 2, 3, 4, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT enabled, failure_threshold").WillReturnRows(sqlmock.NewRows([]string{
		"enabled", "failure_threshold", "failure_window_seconds", "ban_duration_seconds", "created_at", "updated_at",
	}).AddRow(true, 4, 600, 120, now, now))
	mock.ExpectCommit()
	settings, err := repo.GetSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 4, settings.FailureThreshold)

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO security_settings").WithArgs(true, 4, 600, 120, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{
		"enabled", "failure_threshold", "failure_window_seconds", "ban_duration_seconds", "created_at", "updated_at",
	}).AddRow(true, 4, 600, 120, now, now))
	mock.ExpectCommit()
	_, err = repo.UpdateSettings(context.Background(), domain.SecuritySettings{Enabled: true, FailureThreshold: 4, FailureWindowSeconds: 600, BanDurationSeconds: 120})
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO security_ip_failures").WithArgs("192.0.2.44", sqlmock.AnyArg(), "AUTH_INVALID", "control", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{
		"ip", "failure_count", "first_failed_at", "last_failed_at", "last_reason", "last_surface", "updated_at",
	}).AddRow("192.0.2.44", 2, now, now, "AUTH_INVALID", "control", now))
	mock.ExpectCommit()
	failure, err := repo.RecordFailure(context.Background(), "192.0.2.44", "control", "AUTH_INVALID", 600, now)
	require.NoError(t, err)
	require.Equal(t, 2, failure.FailureCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryBanLifecycleSQL(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)
	now := time.Date(2026, 9, 9, 2, 3, 4, 0, time.UTC)
	ban := &domain.SecurityIPBan{IP: "192.0.2.44", Reason: "manual", Source: domain.SecurityBanSourceManual, BannedAt: now, CreatedAt: now, UpdatedAt: now, Metadata: map[string]any{"actor": "control"}}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO security_ip_bans").WithArgs(
		ban.IP, ban.Reason, ban.Source, ban.FailureCount, ban.BannedAt, ban.ExpiresAt, ban.LastFailedAt, `{"actor":"control"}`, ban.CreatedAt,
	).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.UpsertBan(context.Background(), ban))

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT ip, reason, source, failure_count").WithArgs("192.0.2.44", now).WillReturnRows(sqlmock.NewRows([]string{
		"ip", "reason", "source", "failure_count", "banned_at", "expires_at", "last_failed_at", "metadata", "created_at", "updated_at", "revoked_at",
	}).AddRow("192.0.2.44", "manual", "manual", 0, now, nil, nil, []byte(`{"actor":"control"}`), now, now, nil))
	mock.ExpectCommit()
	active, err := repo.GetActiveBan(context.Background(), "192.0.2.44", now)
	require.NoError(t, err)
	require.NotNil(t, active)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE security_ip_bans").WithArgs("192.0.2.44", now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM security_ip_failures").WithArgs("192.0.2.44").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.DeleteBan(context.Background(), "192.0.2.44", now))

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM security_ip_bans WHERE revoked_at IS NULL")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT ip, reason, source, failure_count").WithArgs(20, 0).WillReturnRows(sqlmock.NewRows([]string{
		"ip", "reason", "source", "failure_count", "banned_at", "expires_at", "last_failed_at", "metadata", "created_at", "updated_at", "revoked_at",
	}).AddRow("192.0.2.44", "manual", "manual", 0, now, nil, nil, []byte(`{}`), now, now, nil))
	mock.ExpectCommit()
	bans, total, err := repo.ListBans(context.Background(), true, 20, 0)
	require.NoError(t, err)
	require.Len(t, bans, 1)
	require.Equal(t, int64(1), total)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryUsesDefaultsWhenSettingsAreAbsent(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT enabled, failure_threshold").WillReturnRows(sqlmock.NewRows([]string{
		"enabled", "failure_threshold", "failure_window_seconds", "ban_duration_seconds", "created_at", "updated_at",
	}))
	mock.ExpectCommit()

	settings, err := repo.GetSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 10, settings.FailureThreshold)
	require.Equal(t, 600, settings.FailureWindowSeconds)
	require.Zero(t, settings.BanDurationSeconds)
	require.False(t, settings.CreatedAt.IsZero())
	require.False(t, settings.UpdatedAt.IsZero())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryWrapsDatabaseErrors(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT enabled, failure_threshold").WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	_, err := repo.GetSettings(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to get security settings")
	require.ErrorContains(t, err, "database unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryRejectsMissingUpdatedSettingsRow(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO security_settings").WillReturnRows(sqlmock.NewRows([]string{
		"enabled", "failure_threshold", "failure_window_seconds", "ban_duration_seconds", "created_at", "updated_at",
	}))
	mock.ExpectRollback()

	_, err := repo.UpdateSettings(context.Background(), domain.SecuritySettings{Enabled: true, FailureThreshold: 1, FailureWindowSeconds: 1})
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to update security settings")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryReturnsNoActiveBanWhenQueryIsEmpty(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)
	now := time.Date(2026, 9, 9, 2, 3, 4, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT ip, reason, source, failure_count").WithArgs("192.0.2.44", now).
		WillReturnRows(sqlmock.NewRows([]string{
			"ip", "reason", "source", "failure_count", "banned_at", "expires_at", "last_failed_at", "metadata", "created_at", "updated_at", "revoked_at",
		}))
	mock.ExpectCommit()

	ban, err := repo.GetActiveBan(context.Background(), "192.0.2.44", now)
	require.NoError(t, err)
	require.Nil(t, ban)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryReturnsActiveBanQueryErrors(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)
	now := time.Date(2026, 9, 9, 2, 3, 4, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT ip, reason, source, failure_count").WithArgs("192.0.2.44", now).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	_, err := repo.GetActiveBan(context.Background(), "192.0.2.44", now)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to get active security ban")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryRejectsMalformedBanMetadata(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)
	now := time.Date(2026, 9, 9, 2, 3, 4, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT ip, reason, source, failure_count").WithArgs("192.0.2.44", now).
		WillReturnRows(sqlmock.NewRows([]string{
			"ip", "reason", "source", "failure_count", "banned_at", "expires_at", "last_failed_at", "metadata", "created_at", "updated_at", "revoked_at",
		}).AddRow("192.0.2.44", "manual", "manual", 0, now, nil, nil, []byte("{"), now, now, nil))
	mock.ExpectRollback()

	_, err := repo.GetActiveBan(context.Background(), "192.0.2.44", now)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to get active security ban")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryReturnsFailureErrorWhenInsertReturnsNoRows(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)
	now := time.Date(2026, 9, 9, 2, 3, 4, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO security_ip_failures").WillReturnRows(sqlmock.NewRows([]string{
		"ip", "failure_count", "first_failed_at", "last_failed_at", "last_reason", "last_surface", "updated_at",
	}))
	mock.ExpectRollback()

	_, err := repo.RecordFailure(context.Background(), "192.0.2.44", "control", "AUTH_INVALID", 600, now)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to record security failure")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryReturnsFailureQueryErrors(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)
	now := time.Date(2026, 9, 9, 2, 3, 4, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO security_ip_failures").
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	_, err := repo.RecordFailure(context.Background(), "192.0.2.44", "control", "AUTH_INVALID", 600, now)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to record security failure")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryRejectsNilBanAndInitializesNilMetadata(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)

	err := repo.UpsertBan(context.Background(), nil)
	require.ErrorContains(t, err, "security ban is required")

	now := time.Date(2026, 9, 9, 2, 3, 4, 0, time.UTC)
	ban := &domain.SecurityIPBan{IP: "192.0.2.44", Reason: "manual", Source: domain.SecurityBanSourceManual, BannedAt: now, CreatedAt: now}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO security_ip_bans").WithArgs(
		ban.IP, ban.Reason, ban.Source, ban.FailureCount, ban.BannedAt, ban.ExpiresAt, ban.LastFailedAt, "{}", ban.CreatedAt,
	).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.UpsertBan(context.Background(), ban))
	require.Equal(t, map[string]any{}, ban.Metadata)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryRejectsUnmarshalableBanMetadata(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)

	err := repo.UpsertBan(context.Background(), &domain.SecurityIPBan{Metadata: map[string]any{"channel": make(chan int)}})
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to marshal security ban metadata")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryListBansClampsBoundsAndFiltersExpired(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)
	now := time.Date(2026, 9, 9, 2, 3, 4, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM security_ip_bans WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > NOW())")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	expiresAt := now.Add(time.Hour)
	lastFailedAt := now.Add(-time.Minute)
	mock.ExpectQuery("SELECT ip, reason, source, failure_count").WithArgs(100, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"ip", "reason", "source", "failure_count", "banned_at", "expires_at", "last_failed_at", "metadata", "created_at", "updated_at", "revoked_at",
		}).AddRow("192.0.2.44", "manual", "manual", 0, now, expiresAt, lastFailedAt, nil, now, now, nil))
	mock.ExpectCommit()

	bans, total, err := repo.ListBans(context.Background(), false, 1000, -10)
	require.NoError(t, err)
	require.Len(t, bans, 1)
	require.Equal(t, int64(0), total)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryListBansUsesDefaultBounds(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM security_ip_bans WHERE revoked_at IS NULL")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT ip, reason, source, failure_count").WithArgs(20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"ip", "reason", "source", "failure_count", "banned_at", "expires_at", "last_failed_at", "metadata", "created_at", "updated_at", "revoked_at",
		}))
	mock.ExpectCommit()

	bans, total, err := repo.ListBans(context.Background(), true, 0, -1)
	require.NoError(t, err)
	require.Empty(t, bans)
	require.Zero(t, total)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryListBansReturnsCountErrors(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM security_ip_bans WHERE revoked_at IS NULL")).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	_, _, err := repo.ListBans(context.Background(), true, 20, 0)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to list security bans")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryListBansReturnsRowsErrors(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM security_ip_bans WHERE revoked_at IS NULL")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT ip, reason, source, failure_count").WithArgs(20, 0).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	_, _, err := repo.ListBans(context.Background(), true, 20, 0)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to list security bans")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryDeleteBanReportsFailure(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)
	now := time.Date(2026, 9, 9, 2, 3, 4, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE security_ip_bans").WithArgs("192.0.2.44", now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM security_ip_failures").WithArgs("192.0.2.44").
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	err := repo.DeleteBan(context.Background(), "192.0.2.44", now)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to revoke security ban")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityRepositoryDeleteBanReportsUpdateFailure(t *testing.T) {
	db, mock, cleanup := newSettingsMockDB(t)
	defer cleanup()
	repo := NewSecurityRepository(db, nil)
	now := time.Date(2026, 9, 9, 2, 3, 4, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE security_ip_bans").WithArgs("192.0.2.44", now).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	err := repo.DeleteBan(context.Background(), "192.0.2.44", now)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to revoke security ban")
	require.NoError(t, mock.ExpectationsWereMet())
}
