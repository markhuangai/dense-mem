package service

import (
	"context"
	"errors"
	"testing"
	"time"

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
