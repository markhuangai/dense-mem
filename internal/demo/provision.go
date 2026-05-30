package demo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

type Provisioner struct {
	profiles service.ProfileService
	keys     service.APIKeyService
	store    CounterStore
	quotas   Quotas
	now      func() time.Time
}

type ProvisionOptions struct {
	ClientIP string
	BaseURL  string
}

type ProvisionResponse struct {
	APIKey      string      `json:"api_key"`
	TeamID      string      `json:"team_id"`
	ProfileID   string      `json:"profile_id"`
	TeamName    string      `json:"team_name"`
	ProfileName string      `json:"profile_name"`
	ExpiresAt   time.Time   `json:"expires_at"`
	MCPURL      string      `json:"mcp_url"`
	UIURL       string      `json:"ui_url"`
	Quotas      QuotaLimits `json:"quotas"`
	Notice      string      `json:"notice"`
}

func NewProvisioner(profiles service.ProfileService, keys service.APIKeyService, store CounterStore, quotas Quotas) *Provisioner {
	return &Provisioner{
		profiles: profiles,
		keys:     keys,
		store:    store,
		quotas:   quotas.normalized(),
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func (p *Provisioner) Provision(ctx context.Context, opts ProvisionOptions) (*ProvisionResponse, error) {
	if p == nil || p.profiles == nil || p.keys == nil || p.store == nil {
		return nil, httperr.New(httperr.SERVICE_UNAVAILABLE, "demo provisioner unavailable")
	}
	now := p.now().UTC()
	quotas := p.quotas.normalized()

	if err := p.consumeIssueQuota(ctx, opts.ClientIP, now, quotas); err != nil {
		return nil, err
	}

	suffix, err := randomHex(4)
	if err != nil {
		return nil, httperr.New(httperr.INTERNAL_ERROR, "failed to generate demo id")
	}

	expiresAt := now.Add(quotas.SessionTTL).UTC()
	teamName := fmt.Sprintf("demo-%s-%s", now.Format("20060102-150405"), suffix)
	profileName := "demo-profile-" + suffix

	profile, err := p.profiles.Create(ctx, service.CreateProfileRequest{
		Name:        teamName,
		Description: "Temporary dense-mem public demo team. Data expires automatically.",
		Metadata: map[string]any{
			"demo":            true,
			"demo_expires_at": expiresAt.Format(time.RFC3339),
			"demo_created_at": now.Format(time.RFC3339),
			"demo_quotas":     quotas.Limits(),
		},
		Config: map[string]any{
			"demo": true,
		},
	}, nil, "demo", opts.ClientIP, "demo-provision")
	if err != nil {
		return nil, err
	}

	key, rawKey, err := p.keys.CreateStandardKey(ctx, profile.ID, service.CreateAPIKeyRequest{
		Name:      profileName,
		RateLimit: quotas.PerMinuteRequests,
		ExpiresAt: &expiresAt,
		Scopes:    service.StandardAPIKeyScopes(),
	}, nil, "demo", opts.ClientIP, "demo-provision")
	if err != nil {
		_ = p.profiles.Delete(ctx, profile.ID, nil, "demo", opts.ClientIP, "demo-provision-rollback")
		return nil, err
	}

	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	return &ProvisionResponse{
		APIKey:      rawKey,
		TeamID:      profile.ID.String(),
		ProfileID:   key.ID.String(),
		TeamName:    profile.Name,
		ProfileName: key.GetProfileName(),
		ExpiresAt:   expiresAt,
		MCPURL:      baseURL + "/mcp",
		UIURL:       baseURL + "/ui",
		Quotas:      quotas.Limits(),
		Notice:      "Use this demo only for disposable test data. Do not store secrets, personal data, or critical information.",
	}, nil
}

func (p *Provisioner) consumeIssueQuota(ctx context.Context, clientIP string, now time.Time, quotas Quotas) error {
	ipHash := hashClientIP(clientIP)
	ttlDay := int64((48 * time.Hour).Seconds())

	key := fmt.Sprintf("demo:issue:ip:%s:day:%s", ipHash, now.Format("20060102"))
	next, err := p.store.IncrWithExpire(ctx, key, ttlDay)
	if err != nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "demo session quota check failed")
	}
	if next > quotas.IssuePerIPDay {
		return httperr.New(httperr.RATE_LIMITED, fmt.Sprintf("demo quota exceeded: demo sessions per IP per day limit is %d", quotas.IssuePerIPDay))
	}
	return nil
}

func hashClientIP(ip string) string {
	trimmed := strings.TrimSpace(ip)
	if trimmed == "" {
		trimmed = "unknown"
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:16])
}

func randomHex(bytesLen int) (string, error) {
	if bytesLen <= 0 {
		bytesLen = 4
	}
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
