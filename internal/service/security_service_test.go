package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSecurityServiceRecordAuthFailureCreatesPermanentBan(t *testing.T) {
	repo := newFakeSecurityRepository()
	repo.settings.FailureThreshold = 2
	repo.settings.BanDurationSeconds = 0
	svc := NewSecurityService(repo, nil)

	ban, err := svc.RecordAuthFailure(context.Background(), "192.0.2.10", "mcp", "AUTH_INVALID")
	require.NoError(t, err)
	require.Nil(t, ban)

	ban, err = svc.RecordAuthFailure(context.Background(), "192.0.2.10", "mcp", "AUTH_INVALID")
	require.NoError(t, err)
	require.NotNil(t, ban)
	require.Equal(t, "192.0.2.10", ban.IP)
	require.Equal(t, domain.SecurityBanSourceAuto, ban.Source)
	require.Equal(t, 2, ban.FailureCount)
	require.Nil(t, ban.ExpiresAt)

	active, err := svc.CheckBan(context.Background(), "192.0.2.10")
	require.NoError(t, err)
	require.NotNil(t, active)
}

func TestSecurityServiceRecordAuthFailureSkipsPrivateProxyAddress(t *testing.T) {
	repo := newFakeSecurityRepository()
	repo.settings.FailureThreshold = 1
	svc := NewSecurityService(repo, nil)

	ban, err := svc.RecordAuthFailure(context.Background(), "172.30.0.4", "mcp", "AUTH_INVALID")
	require.NoError(t, err)
	require.Nil(t, ban)
	require.Empty(t, repo.failures)
	require.Empty(t, repo.bans)
}

func TestSecurityServiceDisabledSettingsSkipsBanEnforcement(t *testing.T) {
	repo := newFakeSecurityRepository()
	repo.settings.Enabled = false
	repo.bans["192.0.2.11"] = &domain.SecurityIPBan{
		IP:       "192.0.2.11",
		Reason:   "manual",
		Source:   domain.SecurityBanSourceManual,
		BannedAt: time.Now().UTC(),
	}
	svc := NewSecurityService(repo, nil)

	active, err := svc.CheckBan(context.Background(), "192.0.2.11")
	require.NoError(t, err)
	require.Nil(t, active)
}

func TestSecurityServiceCreateManualBanRejectsInvalidIP(t *testing.T) {
	repo := newFakeSecurityRepository()
	svc := NewSecurityService(repo, nil)

	_, err := svc.CreateManualSecurityBan(context.Background(), "not an ip", "test", nil, "control", "127.0.0.1", "corr")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidSecurityIP))
}

func TestSecurityServiceDeleteSecurityBanResetsFailures(t *testing.T) {
	repo := newFakeSecurityRepository()
	now := time.Now().UTC()
	repo.failures["192.0.2.12"] = 7
	repo.bans["192.0.2.12"] = &domain.SecurityIPBan{
		IP:           "192.0.2.12",
		Reason:       "auth failures: AUTH_INVALID",
		Source:       domain.SecurityBanSourceAuto,
		FailureCount: 7,
		BannedAt:     now,
	}
	svc := NewSecurityService(repo, nil)

	err := svc.DeleteSecurityBan(context.Background(), "192.0.2.12", "control", "127.0.0.1", "corr")

	require.NoError(t, err)
	require.NotContains(t, repo.failures, "192.0.2.12")
	require.NotNil(t, repo.bans["192.0.2.12"].RevokedAt)
}

func TestSecurityServiceSettingsManualBanAndHelpers(t *testing.T) {
	repo := newFakeSecurityRepository()
	audit := new(MockAuditService)
	audit.On("Append", mock.Anything, mock.AnythingOfType("access.AuditLogEntry")).Return(nil)
	svc := NewSecurityService(repo, audit)
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }

	settings, err := svc.GetSecuritySettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)

	settings.FailureThreshold = 3
	updated, err := svc.UpdateSecuritySettings(context.Background(), *settings, "control", "127.0.0.1", "corr")
	require.NoError(t, err)
	require.Equal(t, 3, updated.FailureThreshold)

	settings.FailureThreshold = 0
	_, err = svc.UpdateSecuritySettings(context.Background(), *settings, "control", "127.0.0.1", "corr")
	require.ErrorIs(t, err, ErrInvalidSecuritySettings)
	settings.FailureThreshold = 1
	settings.FailureWindowSeconds = 0
	require.ErrorIs(t, validateSecuritySettings(*settings), ErrInvalidSecuritySettings)
	settings.FailureWindowSeconds = 1
	settings.BanDurationSeconds = -1
	require.ErrorIs(t, validateSecuritySettings(*settings), ErrInvalidSecuritySettings)

	future := now.Add(time.Hour)
	ban, err := svc.CreateManualSecurityBan(context.Background(), "203.0.113.9:443", "", &future, "control", "127.0.0.1", "corr")
	require.NoError(t, err)
	require.Equal(t, "203.0.113.9", ban.IP)
	require.Equal(t, "manual ban", ban.Reason)
	require.Equal(t, domain.SecurityBanSourceManual, ban.Source)

	_, err = svc.CreateManualSecurityBan(context.Background(), "203.0.113.10", "expired", &now, "control", "127.0.0.1", "corr")
	require.ErrorIs(t, err, ErrInvalidSecuritySettings)

	bans, total, err := svc.ListSecurityBans(context.Background(), true, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(len(bans)), total)
	require.NotEmpty(t, bans)

	require.ErrorIs(t, svc.DeleteSecurityBan(context.Background(), "not an ip", "control", "127.0.0.1", "corr"), ErrInvalidSecurityIP)
	require.Equal(t, "admin", normalizeSurface(" ADMIN "))
	require.Equal(t, "api", normalizeSurface(" "))
	require.Len(t, normalizeSurface("x"+string(make([]byte, 80))), 64)
	require.Equal(t, "AUTH_INVALID", normalizeReason(" "))
	require.Len(t, normalizeReason(string(make([]byte, 140))), 128)
	require.NotNil(t, securitySettingsPayload(updated))
	require.NotNil(t, securityBanPayload(ban))
	require.Equal(t, future.Format(time.RFC3339), timePayload(&future))
	audit.AssertExpectations(t)
}

func TestSecurityServiceHelperEdgeCases(t *testing.T) {
	normalized, ok := normalizeSecurityIP(" 2001:db8::1 ")
	require.True(t, ok)
	require.Equal(t, "2001:db8::1", normalized)

	normalized, ok = normalizeSecurityIP("")
	require.False(t, ok)
	require.Empty(t, normalized)

	normalized, ok = normalizeSecurityIP("not an ip")
	require.False(t, ok)
	require.Empty(t, normalized)

	require.False(t, isAutoBannableSecurityIP("not an ip"))
	require.False(t, isAutoBannableSecurityIP("127.0.0.1"))
	require.False(t, isAutoBannableSecurityIP("169.254.1.1"))
	require.True(t, isAutoBannableSecurityIP("8.8.8.8"))

	require.Nil(t, securitySettingsPayload(nil))
	require.Nil(t, securityBanPayload(nil))
	require.Nil(t, timePayload(nil))
}

func TestSecurityServiceExistingBanAndDisabledRecordingBranches(t *testing.T) {
	repo := newFakeSecurityRepository()
	now := time.Now().UTC()
	repo.settings.FailureThreshold = 1
	repo.bans["8.8.8.8"] = &domain.SecurityIPBan{
		IP:       "8.8.8.8",
		Reason:   "manual",
		Source:   domain.SecurityBanSourceManual,
		BannedAt: now,
	}
	svc := NewSecurityService(repo, nil)
	svc.now = func() time.Time { return now }

	ban, err := svc.RecordAuthFailure(context.Background(), "8.8.8.8", "api", "AUTH_INVALID")
	require.NoError(t, err)
	require.NotNil(t, ban)
	require.Equal(t, "manual", ban.Reason)

	repo.settings.Enabled = false
	ban, err = svc.RecordAuthFailure(context.Background(), "8.8.4.4", "api", "AUTH_INVALID")
	require.NoError(t, err)
	require.Nil(t, ban)
}

type fakeSecurityRepository struct {
	settings domain.SecuritySettings
	failures map[string]int
	bans     map[string]*domain.SecurityIPBan
}

func newFakeSecurityRepository() *fakeSecurityRepository {
	return &fakeSecurityRepository{
		settings: domain.SecuritySettings{
			Enabled:              true,
			FailureThreshold:     10,
			FailureWindowSeconds: 600,
			BanDurationSeconds:   0,
			CreatedAt:            time.Now().UTC(),
			UpdatedAt:            time.Now().UTC(),
		},
		failures: map[string]int{},
		bans:     map[string]*domain.SecurityIPBan{},
	}
}

func (r *fakeSecurityRepository) GetSettings(ctx context.Context) (*domain.SecuritySettings, error) {
	settings := r.settings
	return &settings, nil
}

func (r *fakeSecurityRepository) UpdateSettings(ctx context.Context, settings domain.SecuritySettings) (*domain.SecuritySettings, error) {
	settings.UpdatedAt = time.Now().UTC()
	r.settings = settings
	return &settings, nil
}

func (r *fakeSecurityRepository) GetActiveBan(ctx context.Context, ip string, now time.Time) (*domain.SecurityIPBan, error) {
	ban := r.bans[ip]
	if ban == nil || !ban.ActiveAt(now) {
		return nil, nil
	}
	copy := *ban
	return &copy, nil
}

func (r *fakeSecurityRepository) RecordFailure(ctx context.Context, ip, surface, reason string, windowSeconds int, now time.Time) (*domain.SecurityIPFailure, error) {
	r.failures[ip]++
	return &domain.SecurityIPFailure{
		IP:            ip,
		FailureCount:  r.failures[ip],
		FirstFailedAt: now,
		LastFailedAt:  now,
		LastReason:    reason,
		LastSurface:   surface,
		UpdatedAt:     now,
	}, nil
}

func (r *fakeSecurityRepository) UpsertBan(ctx context.Context, ban *domain.SecurityIPBan) error {
	copy := *ban
	r.bans[ban.IP] = &copy
	return nil
}

func (r *fakeSecurityRepository) ListBans(ctx context.Context, includeExpired bool, limit, offset int) ([]domain.SecurityIPBan, int64, error) {
	items := make([]domain.SecurityIPBan, 0, len(r.bans))
	now := time.Now().UTC()
	for _, ban := range r.bans {
		if includeExpired || ban.ActiveAt(now) {
			items = append(items, *ban)
		}
	}
	return items, int64(len(items)), nil
}

func (r *fakeSecurityRepository) DeleteBan(ctx context.Context, ip string, now time.Time) error {
	if ban := r.bans[ip]; ban != nil {
		ban.RevokedAt = &now
	}
	delete(r.failures, ip)
	return nil
}
