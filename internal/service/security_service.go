package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

var (
	ErrInvalidSecurityIP       = errors.New("invalid security IP")
	ErrInvalidSecuritySettings = errors.New("invalid security settings")
)

type SecurityService interface {
	CheckBan(ctx context.Context, ip string) (*domain.SecurityIPBan, error)
	RecordAuthFailure(ctx context.Context, ip, surface, reason string) (*domain.SecurityIPBan, error)
	GetSecuritySettings(ctx context.Context) (*domain.SecuritySettings, error)
	UpdateSecuritySettings(ctx context.Context, settings domain.SecuritySettings, actorRole, clientIP, correlationID string) (*domain.SecuritySettings, error)
	ListSecurityBans(ctx context.Context, includeExpired bool, limit, offset int) ([]domain.SecurityIPBan, int64, error)
	CreateManualSecurityBan(ctx context.Context, ip, reason string, expiresAt *time.Time, actorRole, clientIP, correlationID string) (*domain.SecurityIPBan, error)
	DeleteSecurityBan(ctx context.Context, ip, actorRole, clientIP, correlationID string) error
}

type SecurityServiceImpl struct {
	repo  repository.SecurityRepository
	audit AuditService
	now   func() time.Time
}

var _ SecurityService = (*SecurityServiceImpl)(nil)

func NewSecurityService(repo repository.SecurityRepository, audit AuditService) *SecurityServiceImpl {
	return &SecurityServiceImpl{repo: repo, audit: audit, now: time.Now}
}

func (s *SecurityServiceImpl) CheckBan(ctx context.Context, ip string) (*domain.SecurityIPBan, error) {
	normalized, ok := normalizeSecurityIP(ip)
	if !ok {
		return nil, nil
	}
	settings, err := s.repo.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	if settings != nil && !settings.Enabled {
		return nil, nil
	}
	return s.repo.GetActiveBan(ctx, normalized, s.now().UTC())
}

func (s *SecurityServiceImpl) RecordAuthFailure(ctx context.Context, ip, surface, reason string) (*domain.SecurityIPBan, error) {
	normalized, ok := normalizeSecurityIP(ip)
	if !ok {
		return nil, nil
	}
	if !isAutoBannableSecurityIP(normalized) {
		return nil, nil
	}

	settings, err := s.repo.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	if settings == nil || !settings.Enabled {
		return nil, nil
	}

	now := s.now().UTC()
	active, err := s.repo.GetActiveBan(ctx, normalized, now)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return active, nil
	}

	failure, err := s.repo.RecordFailure(ctx, normalized, normalizeSurface(surface), normalizeReason(reason), settings.FailureWindowSeconds, now)
	if err != nil {
		return nil, err
	}
	if failure.FailureCount < settings.FailureThreshold {
		return nil, nil
	}

	var expiresAt *time.Time
	if settings.BanDurationSeconds > 0 {
		expires := now.Add(time.Duration(settings.BanDurationSeconds) * time.Second)
		expiresAt = &expires
	}
	ban := &domain.SecurityIPBan{
		IP:           normalized,
		Reason:       fmt.Sprintf("auth failures: %s", failure.LastReason),
		Source:       domain.SecurityBanSourceAuto,
		FailureCount: failure.FailureCount,
		BannedAt:     now,
		ExpiresAt:    expiresAt,
		LastFailedAt: &failure.LastFailedAt,
		Metadata: map[string]any{
			"surface":                failure.LastSurface,
			"reason":                 failure.LastReason,
			"failure_threshold":      settings.FailureThreshold,
			"failure_window_seconds": settings.FailureWindowSeconds,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.UpsertBan(ctx, ban); err != nil {
		return nil, err
	}
	s.appendAudit("SECURITY_AUTO_BAN", "security_ip_ban", normalized, "system", normalized, "", nil, securityBanPayload(ban), ban.Metadata)
	return ban, nil
}

func (s *SecurityServiceImpl) GetSecuritySettings(ctx context.Context) (*domain.SecuritySettings, error) {
	return s.repo.GetSettings(ctx)
}

func (s *SecurityServiceImpl) UpdateSecuritySettings(ctx context.Context, settings domain.SecuritySettings, actorRole, clientIP, correlationID string) (*domain.SecuritySettings, error) {
	if err := validateSecuritySettings(settings); err != nil {
		return nil, err
	}
	before, err := s.repo.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	updated, err := s.repo.UpdateSettings(ctx, settings)
	if err != nil {
		return nil, err
	}
	s.appendAudit(
		"SECURITY_SETTINGS_UPDATE",
		"security_settings",
		"global",
		actorRole,
		clientIP,
		correlationID,
		securitySettingsPayload(before),
		securitySettingsPayload(updated),
		map[string]any{"actor": actorRole},
	)
	return updated, nil
}

func (s *SecurityServiceImpl) ListSecurityBans(ctx context.Context, includeExpired bool, limit, offset int) ([]domain.SecurityIPBan, int64, error) {
	return s.repo.ListBans(ctx, includeExpired, limit, offset)
}

func (s *SecurityServiceImpl) CreateManualSecurityBan(ctx context.Context, ip, reason string, expiresAt *time.Time, actorRole, clientIP, correlationID string) (*domain.SecurityIPBan, error) {
	normalized, ok := normalizeSecurityIP(ip)
	if !ok {
		return nil, ErrInvalidSecurityIP
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "manual ban"
	}
	now := s.now().UTC()
	if expiresAt != nil && !expiresAt.After(now) {
		return nil, fmt.Errorf("%w: expires_at must be in the future", ErrInvalidSecuritySettings)
	}
	ban := &domain.SecurityIPBan{
		IP:           normalized,
		Reason:       reason,
		Source:       domain.SecurityBanSourceManual,
		FailureCount: 0,
		BannedAt:     now,
		ExpiresAt:    expiresAt,
		Metadata:     map[string]any{"actor": actorRole},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.repo.UpsertBan(ctx, ban); err != nil {
		return nil, err
	}
	s.appendAudit("SECURITY_MANUAL_BAN", "security_ip_ban", normalized, actorRole, clientIP, correlationID, nil, securityBanPayload(ban), ban.Metadata)
	return ban, nil
}

func (s *SecurityServiceImpl) DeleteSecurityBan(ctx context.Context, ip, actorRole, clientIP, correlationID string) error {
	normalized, ok := normalizeSecurityIP(ip)
	if !ok {
		return ErrInvalidSecurityIP
	}
	if err := s.repo.DeleteBan(ctx, normalized, s.now().UTC()); err != nil {
		return err
	}
	s.appendAudit("SECURITY_UNBAN", "security_ip_ban", normalized, actorRole, clientIP, correlationID, nil, map[string]any{"ip": normalized}, map[string]any{"actor": actorRole})
	return nil
}

func validateSecuritySettings(settings domain.SecuritySettings) error {
	if settings.FailureThreshold <= 0 {
		return fmt.Errorf("%w: failure_threshold must be greater than 0", ErrInvalidSecuritySettings)
	}
	if settings.FailureWindowSeconds <= 0 {
		return fmt.Errorf("%w: failure_window_seconds must be greater than 0", ErrInvalidSecuritySettings)
	}
	if settings.BanDurationSeconds < 0 {
		return fmt.Errorf("%w: ban_duration_seconds must be greater than or equal to 0", ErrInvalidSecuritySettings)
	}
	return nil
}

func normalizeSecurityIP(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	parsed := net.ParseIP(raw)
	if parsed == nil {
		return "", false
	}
	if ipv4 := parsed.To4(); ipv4 != nil {
		return ipv4.String(), true
	}
	return parsed.String(), true
}

func isAutoBannableSecurityIP(raw string) bool {
	parsed := net.ParseIP(raw)
	if parsed == nil {
		return false
	}
	if !parsed.IsGlobalUnicast() {
		return false
	}
	// In proxy deployments without trusted client-IP extraction, the direct
	// address is often a private proxy/container IP. Auto-banning that address
	// can block every public client behind the proxy.
	return !parsed.IsPrivate() && !parsed.IsLoopback() && !parsed.IsLinkLocalUnicast()
}

func normalizeSurface(surface string) string {
	surface = strings.TrimSpace(strings.ToLower(surface))
	if surface == "" {
		return "api"
	}
	if len(surface) > 64 {
		return surface[:64]
	}
	return surface
}

func normalizeReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "AUTH_INVALID"
	}
	if len(reason) > 128 {
		return reason[:128]
	}
	return reason
}

func (s *SecurityServiceImpl) appendAudit(operation, entityType, entityID, actorRole, clientIP, correlationID string, before, after, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	if actorRole == "" {
		actorRole = "system"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.audit.Append(ctx, AuditLogEntry{
		Operation:     operation,
		EntityType:    entityType,
		EntityID:      entityID,
		BeforePayload: before,
		AfterPayload:  after,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
		Metadata:      metadata,
	})
}

func securitySettingsPayload(settings *domain.SecuritySettings) map[string]any {
	if settings == nil {
		return nil
	}
	return map[string]any{
		"enabled":                settings.Enabled,
		"failure_threshold":      settings.FailureThreshold,
		"failure_window_seconds": settings.FailureWindowSeconds,
		"ban_duration_seconds":   settings.BanDurationSeconds,
	}
}

func securityBanPayload(ban *domain.SecurityIPBan) map[string]any {
	if ban == nil {
		return nil
	}
	return map[string]any{
		"ip":            ban.IP,
		"reason":        ban.Reason,
		"source":        ban.Source,
		"failure_count": ban.FailureCount,
		"banned_at":     ban.BannedAt.Format(time.RFC3339),
		"expires_at":    timePayload(ban.ExpiresAt),
	}
}

func timePayload(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Format(time.RFC3339)
}
