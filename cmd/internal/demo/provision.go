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
	teams       service.TeamService
	credentials service.CredentialService
	store       CounterStore
	quotas      Quotas
	now         func() time.Time
}

type ProvisionOptions struct {
	ClientIP string
	BaseURL  string
}

type ProvisionResponse struct {
	APIKey         string      `json:"api_key"`
	TeamID         string      `json:"team_id"`
	CredentialID   string      `json:"credential_id"`
	TeamName       string      `json:"team_name"`
	CredentialName string      `json:"credential_name"`
	ExpiresAt      time.Time   `json:"expires_at"`
	MCPURL         string      `json:"mcp_url"`
	UIURL          string      `json:"ui_url"`
	Quotas         QuotaLimits `json:"quotas"`
	Notice         string      `json:"notice"`
}

func NewProvisioner(teams service.TeamService, credentials service.CredentialService, store CounterStore, quotas Quotas) *Provisioner {
	return &Provisioner{
		teams:       teams,
		credentials: credentials,
		store:       store,
		quotas:      quotas.normalized(),
		now:         func() time.Time { return time.Now().UTC() },
	}
}

func (p *Provisioner) Provision(ctx context.Context, opts ProvisionOptions) (*ProvisionResponse, error) {
	if p == nil || p.teams == nil || p.credentials == nil || p.store == nil {
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
	credentialName := "demo-credential-" + suffix

	team, err := p.teams.Create(ctx, service.CreateTeamRequest{
		Name:        teamName,
		Description: "Temporary dense-mem public demo team. Data expires automatically.",
		Metadata: map[string]any{
			"demo":            true,
			"demo_expires_at": expiresAt.Format(time.RFC3339),
			"demo_created_at": now.Format(time.RFC3339),
			"demo_quotas":     quotas.Limits(),
		},
		Config: map[string]any{"demo": true},
	}, nil, "demo", opts.ClientIP, "demo-provision")
	if err != nil {
		return nil, err
	}

	credential, rawKey, err := p.credentials.CreateCredential(ctx, team.ID, service.CreateCredentialRequest{
		Name:      credentialName,
		RateLimit: quotas.PerMinuteRequests,
		ExpiresAt: &expiresAt,
		Scopes:    service.StandardCredentialScopes(),
		Role:      service.CredentialRoleMember,
	}, nil, "demo", opts.ClientIP, "demo-provision")
	if err != nil {
		_ = p.teams.Delete(ctx, team.ID, nil, "demo", opts.ClientIP, "demo-provision-rollback")
		return nil, err
	}

	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	return &ProvisionResponse{
		APIKey:         rawKey,
		TeamID:         team.ID.String(),
		CredentialID:   credential.ID.String(),
		TeamName:       team.Name,
		CredentialName: credential.GetName(),
		ExpiresAt:      expiresAt,
		MCPURL:         baseURL + "/mcp",
		UIURL:          baseURL + "/ui",
		Quotas:         quotas.Limits(),
		Notice:         "Use this demo only for disposable test data. Do not store secrets, personal data, or critical information.",
	}, nil
}

func (p *Provisioner) consumeIssueQuota(ctx context.Context, clientIP string, now time.Time, quotas Quotas) error {
	ipHash := hashClientIP(clientIP)
	key := fmt.Sprintf("demo:issue:ip:%s:day:%s", ipHash, now.Format("20060102"))
	next, err := p.store.IncrWithExpire(ctx, key, int64((48 * time.Hour).Seconds()))
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
