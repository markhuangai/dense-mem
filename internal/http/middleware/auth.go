package middleware

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service"
)

// Principal represents the authenticated principal stored in context.
type Principal struct {
	KeyID       uuid.UUID
	TeamID      uuid.UUID
	ProfileID   *uuid.UUID
	ProfileName string
	Role        string
	Scopes      []string
	KeyPrefix   string
	RateLimit   int
}

// PrincipalInterface is the companion interface for Principal.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type PrincipalInterface interface {
	GetKeyID() uuid.UUID
	GetTeamID() uuid.UUID
	GetProfileID() *uuid.UUID
	GetProfileName() string
	GetRole() string
	GetScopes() []string
	GetKeyPrefix() string
	GetRateLimit() int
}

// Ensure Principal implements PrincipalInterface
var _ PrincipalInterface = (*Principal)(nil)

// Getters for PrincipalInterface
func (p *Principal) GetKeyID() uuid.UUID { return p.KeyID }
func (p *Principal) GetTeamID() uuid.UUID {
	if p.TeamID != uuid.Nil {
		return p.TeamID
	}
	if p.ProfileID != nil {
		return *p.ProfileID
	}
	return uuid.Nil
}
func (p *Principal) GetProfileID() *uuid.UUID { return p.ProfileID }
func (p *Principal) GetProfileName() string   { return p.ProfileName }
func (p *Principal) GetRole() string          { return p.Role }
func (p *Principal) GetScopes() []string      { return p.Scopes }
func (p *Principal) GetKeyPrefix() string     { return p.KeyPrefix }
func (p *Principal) GetRateLimit() int        { return p.RateLimit }

// principalContextKey is the unexported context key type for storing principals.
// Using an unexported type prevents downstream code from constructing fake principals.
type principalContextKey struct{}

var authVerifyLimiter struct {
	mu    sync.RWMutex
	slots chan struct{}
}

func SetAuthVerificationConcurrency(limit int) {
	authVerifyLimiter.mu.Lock()
	defer authVerifyLimiter.mu.Unlock()
	if limit <= 0 {
		authVerifyLimiter.slots = nil
		return
	}
	authVerifyLimiter.slots = make(chan struct{}, limit)
}

// AuthMiddleware creates an authentication middleware that validates API keys.
// It requires the Authorization header in the format "Bearer <rawKey>".
func AuthMiddleware(repo repository.APIKeyRepository, auditSvc service.AuditService) echo.MiddlewareFunc {
	return AuthMiddlewareWithSecurity(repo, auditSvc, nil)
}

func AuthMiddlewareWithSecurity(repo repository.APIKeyRepository, auditSvc service.AuditService, securitySvc SecurityBanService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Extract Authorization header
			authHeader := c.Request().Header.Get("Authorization")

			// Missing header
			if authHeader == "" {
				logAuthFailure(c, auditSvc, securitySvc, nil, "AUTH_MISSING", "missing authorization header")
				return httperr.New(httperr.AUTH_MISSING, "missing authorization header")
			}

			// Parse Bearer token
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				logAuthFailure(c, auditSvc, securitySvc, nil, "AUTH_INVALID", "malformed authorization header")
				return httperr.New(httperr.AUTH_INVALID, "malformed authorization header")
			}

			rawKey := parts[1]
			if rawKey == "" {
				logAuthFailure(c, auditSvc, securitySvc, nil, "AUTH_INVALID", "empty bearer token")
				return httperr.New(httperr.AUTH_INVALID, "empty bearer token")
			}

			prefixes := crypto.GetLookupPrefixes(rawKey)
			if len(prefixes) == 0 {
				logAuthFailure(c, auditSvc, securitySvc, nil, "AUTH_INVALID", "invalid key format")
				return httperr.New(httperr.AUTH_INVALID, "invalid key format")
			}

			// Look up active key by prefix
			ctx := c.Request().Context()
			var (
				key    *domain.APIKey
				prefix string
			)
			for _, candidate := range prefixes {
				found, err := repo.GetActiveByPrefix(ctx, candidate)
				if err != nil {
					return httperr.New(httperr.INTERNAL_ERROR, "failed to lookup key")
				}
				if found != nil {
					key = found
					prefix = candidate
					break
				}
			}

			// No matching key found
			if key == nil {
				logAuthFailure(c, auditSvc, securitySvc, nil, "AUTH_INVALID", "invalid api key")
				return httperr.New(httperr.AUTH_INVALID, "invalid api key")
			}

			// Check if key is revoked (shouldn't happen with GetActiveByPrefix, but defensive)
			if key.RevokedAt != nil {
				teamID := key.GetTeamID().String()
				logAuthFailure(c, auditSvc, securitySvc, &teamID, "AUTH_REVOKED", "api key has been revoked")
				return httperr.New(httperr.AUTH_REVOKED, "api key has been revoked")
			}

			// Check if key is expired (shouldn't happen with GetActiveByPrefix, but defensive)
			if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now().UTC()) {
				teamID := key.GetTeamID().String()
				logAuthFailure(c, auditSvc, securitySvc, &teamID, "AUTH_EXPIRED", "api key has expired")
				return httperr.New(httperr.AUTH_EXPIRED, "api key has expired")
			}

			// Verify the raw key against the stored Argon2id hash
			if !verifyKeyWithLimit(ctx, rawKey, key.KeyHash) {
				teamID := key.GetTeamID().String()
				logAuthFailure(c, auditSvc, securitySvc, &teamID, "AUTH_INVALID", "invalid api key")
				return httperr.New(httperr.AUTH_INVALID, "invalid api key")
			}

			teamID := key.GetTeamID()
			profileID := key.ID
			profileName := key.GetProfileName()

			// All runtime keys must be team-bound. Legacy team-less keys are
			// rejected so the server only accepts the multi-tenant bearer model.
			if teamID == uuid.Nil {
				logAuthFailure(c, auditSvc, securitySvc, nil, "AUTH_INVALID", "api key is not profile bound")
				return httperr.New(httperr.AUTH_INVALID, "invalid api key")
			}

			principal := &Principal{
				KeyID:       key.ID,
				TeamID:      teamID,
				ProfileID:   &profileID,
				ProfileName: profileName,
				Role:        "standard",
				Scopes:      key.Scopes,
				KeyPrefix:   prefix,
				RateLimit:   key.RateLimit,
			}

			// Store principal in context
			ctx = context.WithValue(ctx, principalContextKey{}, principal)
			ctx = requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
				TeamID:      teamID,
				ProfileID:   profileID,
				ProfileName: profileName,
			})

			// Remove the Authorization header to prevent downstream access to raw key
			req := c.Request().Clone(ctx)
			req.Header.Del("Authorization")
			c.SetRequest(req)

			// Touch last used asynchronously (fire and forget)
			go func(keyID uuid.UUID) {
				// Use a background context with timeout for the async operation
				touchCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = repo.TouchLastUsed(touchCtx, keyID)
			}(key.ID)

			return next(c)
		}
	}
}

// logAuthFailure logs an authentication failure event to the audit service.
func logAuthFailure(c echo.Context, auditSvc service.AuditService, securitySvc SecurityBanService, profileID *string, reason, message string) {
	if securitySvc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := securitySvc.RecordAuthFailure(ctx, c.RealIP(), authFailureSurface(c), reason); err != nil {
			c.Logger().Errorf("security auth failure record failed: %v", err)
		}
		cancel()
	}

	if auditSvc == nil {
		return
	}

	clientIP := c.RealIP()
	correlationID := GetCorrelationID(c.Request().Context())

	metadata := map[string]interface{}{
		"reason":  reason,
		"message": message,
	}

	// Use a background context with timeout for logging
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = auditSvc.AuthFailure(ctx, profileID, "api_key", "", metadata, clientIP, correlationID)
}

func verifyKeyWithLimit(ctx context.Context, rawKey, keyHash string) bool {
	authVerifyLimiter.mu.RLock()
	slots := authVerifyLimiter.slots
	authVerifyLimiter.mu.RUnlock()
	if slots == nil {
		return crypto.VerifyKey(rawKey, keyHash)
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
		return crypto.VerifyKey(rawKey, keyHash)
	case <-ctx.Done():
		return false
	}
}

func authFailureSurface(c echo.Context) string {
	path := c.Path()
	if path == "" {
		path = c.Request().URL.Path
	}
	if strings.HasPrefix(path, "/mcp") {
		return "mcp"
	}
	return "api"
}

// GetPrincipal retrieves the authenticated principal from the context.
// Returns nil if no principal is found.
func GetPrincipal(ctx context.Context) *Principal {
	if p, ok := ctx.Value(principalContextKey{}).(*Principal); ok {
		return p
	}
	return nil
}

// GetPrincipalInterface retrieves the authenticated principal as the interface type.
//
// Test helpers for injecting Principals live under the build tag `testhelpers`
// in auth_testhelpers.go; production code has no way to construct a Principal
// context.
// This is useful for consumers that want to depend on the interface rather than concrete type.
func GetPrincipalInterface(ctx context.Context) PrincipalInterface {
	return GetPrincipal(ctx)
}

// RequirePrincipal is a helper that returns the principal or an error if not authenticated.
// This is useful for handlers that require authentication.
func RequirePrincipal(ctx context.Context) (*Principal, error) {
	p := GetPrincipal(ctx)
	if p == nil {
		return nil, httperr.New(httperr.AUTH_MISSING, "authentication required")
	}
	return p, nil
}

// RequireAuth is middleware that ensures a principal is present in the context.
// It should be used after AuthMiddleware to enforce authentication on specific routes.
func RequireAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if GetPrincipal(c.Request().Context()) == nil {
				return httperr.New(httperr.AUTH_MISSING, "authentication required")
			}
			return next(c)
		}
	}
}

// Ensure the imports are used
var _ = domain.APIKey{}
var _ = http.StatusOK
