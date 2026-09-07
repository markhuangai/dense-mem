package http

import (
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

// MCPBindings owns the transport dependencies required by protected MCP
// endpoints. Flat fields remain as a compatibility bridge for first-party
// callers until transport consolidation.
type MCPBindings struct {
	CredentialRepo   repository.CredentialRepository
	TeamSvc          handler.TeamServiceInterface
	RateLimitService service.RateLimitServiceInterface
	UsageMetrics     service.UsageMetricsRecorder
	AuditService     service.AuditService
	SecurityService  middleware.SecurityBanService
	SSOAuthenticator interface {
		middleware.SSOEntitlementValidator
		middleware.SSOSessionAuthenticator
	}
	OAuthAuthenticator middleware.OAuthBearerAuthenticator
	OAuthMetadata      OAuthProtectedResourceProvider
	Config             config.ConfigProvider
	Logger             observability.LogProvider
	CredentialVerifier crypto.CredentialVerifier
	LastUsedRecorder   middleware.LastUsedRecorder
	PostAuthMiddleware []echo.MiddlewareFunc
}

func (d ProtectedDeps) withMCPBindings() ProtectedDeps {
	if d.MCP.CredentialRepo != nil {
		d.CredentialRepo = d.MCP.CredentialRepo
	}
	if d.MCP.TeamSvc != nil {
		d.TeamSvc = d.MCP.TeamSvc
	}
	if d.MCP.RateLimitService != nil {
		d.RateLimitService = d.MCP.RateLimitService
	}
	if d.MCP.UsageMetrics != nil {
		d.UsageMetrics = d.MCP.UsageMetrics
	}
	if d.MCP.AuditService != nil {
		d.AuditService = d.MCP.AuditService
	}
	if d.MCP.SecurityService != nil {
		d.SecurityService = d.MCP.SecurityService
	}
	if d.MCP.SSOAuthenticator != nil {
		d.SSOAuthenticator = d.MCP.SSOAuthenticator
	}
	if d.MCP.OAuthAuthenticator != nil {
		d.OAuthAuthenticator = d.MCP.OAuthAuthenticator
	}
	if d.MCP.OAuthMetadata != nil {
		d.OAuthMetadata = d.MCP.OAuthMetadata
	}
	if d.MCP.Config != nil {
		d.Config = d.MCP.Config
	}
	if d.MCP.Logger != nil {
		d.Logger = d.MCP.Logger
	}
	if d.MCP.CredentialVerifier != nil {
		d.CredentialVerifier = d.MCP.CredentialVerifier
	}
	if d.MCP.LastUsedRecorder != nil {
		d.LastUsedRecorder = d.MCP.LastUsedRecorder
	}
	if len(d.MCP.PostAuthMiddleware) > 0 {
		d.PostAuthMiddleware = d.MCP.PostAuthMiddleware
	}
	return d
}
