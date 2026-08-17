package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
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
	TeamID        uuid.UUID
	IdentityID    uuid.UUID
	MembershipID  uuid.UUID
	OwnerID       uuid.UUID
	OwnerName     string
	CredentialID  *uuid.UUID
	Role          string
	Grants        []string
	KeyPrefix     string
	RateLimit     int
	AuthMethod    string
	SSOProviderID *uuid.UUID
	SSOSubject    string
	AllowedSpaces []domain.MemorySpaceAccess
}

// PrincipalInterface is the companion interface for Principal.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type PrincipalInterface interface {
	GetTeamID() uuid.UUID
	GetIdentityID() uuid.UUID
	GetMembershipID() uuid.UUID
	GetOwnerID() uuid.UUID
	GetOwnerName() string
	GetCredentialID() *uuid.UUID
	GetRole() string
	GetGrants() []string
	GetKeyPrefix() string
	GetRateLimit() int
}

// Ensure Principal implements PrincipalInterface
var _ PrincipalInterface = (*Principal)(nil)

// Getters for PrincipalInterface
func (p *Principal) GetTeamID() uuid.UUID        { return p.TeamID }
func (p *Principal) GetIdentityID() uuid.UUID    { return p.IdentityID }
func (p *Principal) GetMembershipID() uuid.UUID  { return p.MembershipID }
func (p *Principal) GetOwnerID() uuid.UUID       { return p.OwnerID }
func (p *Principal) GetOwnerName() string        { return p.OwnerName }
func (p *Principal) GetCredentialID() *uuid.UUID { return p.CredentialID }
func (p *Principal) GetRole() string             { return p.Role }
func (p *Principal) GetGrants() []string         { return p.Grants }
func (p *Principal) GetKeyPrefix() string        { return p.KeyPrefix }
func (p *Principal) GetRateLimit() int           { return p.RateLimit }

// principalContextKey is the unexported context key type for storing principals.
// Using an unexported type prevents downstream code from constructing fake principals.
type principalContextKey struct{}

// AuthMiddleware creates an authentication middleware that validates API keys.
// It requires the Authorization header in the format "Bearer <rawKey>".
func AuthMiddleware(repo repository.CredentialRepository, auditSvc service.AuditService) echo.MiddlewareFunc {
	return AuthMiddlewareWithSecurity(repo, auditSvc, nil)
}

func AuthMiddlewareWithSecurity(repo repository.CredentialRepository, auditSvc service.AuditService, securitySvc SecurityBanService) echo.MiddlewareFunc {
	return AuthMiddlewareWithOptions(repo, auditSvc, securitySvc, AuthOptions{})
}

type SSOEntitlementValidator interface {
	ValidateCredential(ctx context.Context, credential *domain.Credential) (*domain.Credential, error)
}

type SSOSessionAuthenticator interface {
	AuthenticateSession(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.AuthenticatedActor, error)
}

type UserPortalSessionAuthenticator interface {
	AuthenticateSession(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.AuthenticatedActor, error)
}

type AuthOptions struct {
	CredentialVerifier             crypto.CredentialVerifier
	SSOEntitlementValidator        SSOEntitlementValidator
	SSOSessionAuthenticator        SSOSessionAuthenticator
	UserPortalSessionAuthenticator UserPortalSessionAuthenticator
	AllowMissingCredentials        bool
}

func AuthMiddlewareWithOptions(repo repository.CredentialRepository, auditSvc service.AuditService, securitySvc SecurityBanService, opts AuthOptions) echo.MiddlewareFunc {
	verifier := opts.CredentialVerifier
	if verifier == nil {
		verifier = crypto.NewArgon2Verifier(0)
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Extract Authorization header
			authHeader := c.Request().Header.Get("Authorization")

			// Missing header
			if authHeader == "" {
				if opts.UserPortalSessionAuthenticator != nil {
					if err := authenticateUserPortalSession(c, opts.UserPortalSessionAuthenticator, opts.SSOEntitlementValidator); err != nil {
						if err != errNoUserPortalSession {
							logAuthFailure(c, auditSvc, securitySvc, nil, "UI_SESSION_AUTH_INVALID", "user portal session authentication failed")
							return err
						}
					} else {
						return next(c)
					}
				}
				if opts.SSOSessionAuthenticator != nil {
					if err := authenticateSSOSession(c, opts.SSOSessionAuthenticator); err != nil {
						if err == errNoSSOSession {
							if opts.AllowMissingCredentials {
								return next(c)
							}
							logAuthFailure(c, auditSvc, securitySvc, nil, "AUTH_MISSING", "missing authorization header")
							return httperr.New(httperr.AUTH_MISSING, "missing authorization header")
						}
						logAuthFailure(c, auditSvc, securitySvc, nil, "SSO_AUTH_INVALID", "sso session authentication failed")
						return err
					}
					return next(c)
				}
				if opts.AllowMissingCredentials {
					return next(c)
				}
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
				key    *domain.Credential
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
			verified, verifyErr := verifier.Verify(ctx, rawKey, key.KeyHash)
			if verifyErr != nil {
				return httperr.New(httperr.SERVICE_UNAVAILABLE, "authentication service unavailable")
			}
			if !verified {
				teamID := key.GetTeamID().String()
				logAuthFailure(c, auditSvc, securitySvc, &teamID, "AUTH_INVALID", "invalid api key")
				return httperr.New(httperr.AUTH_INVALID, "invalid api key")
			}

			// All runtime keys must be team-bound. Legacy team-less keys are
			// rejected so the server only accepts the multi-tenant bearer model.
			teamID := key.GetTeamID()
			if teamID == uuid.Nil {
				logAuthFailure(c, auditSvc, securitySvc, nil, "AUTH_INVALID", "api credential is not team bound")
				return httperr.New(httperr.AUTH_INVALID, "invalid api key")
			}
			if opts.SSOEntitlementValidator != nil {
				validated, err := opts.SSOEntitlementValidator.ValidateCredential(ctx, key)
				if err != nil {
					teamIDStr := teamID.String()
					logAuthFailure(c, auditSvc, securitySvc, &teamIDStr, "SSO_ENTITLEMENT_DENIED", "sso entitlement denied")
					return ssoAuthError(err)
				}
				if validated == nil {
					teamIDStr := teamID.String()
					logAuthFailure(c, auditSvc, securitySvc, &teamIDStr, "SSO_ENTITLEMENT_DENIED", "sso entitlement denied")
					return httperr.New(httperr.FORBIDDEN, "sso access denied")
				}
				key = validated
			}
			teamID = key.GetTeamID()
			if teamID == uuid.Nil {
				logAuthFailure(c, auditSvc, securitySvc, nil, "AUTH_INVALID", "api credential is not team bound")
				return httperr.New(httperr.AUTH_INVALID, "invalid api key")
			}

			actor := authenticatedActorFromCredential(key)
			principal, actorContext, err := principalAndActorContext(actor, "api_key", prefix)
			if err != nil {
				return err
			}
			ctx = context.WithValue(ctx, principalContextKey{}, principal)
			ctx = requestctx.WithActor(ctx, actorContext)

			// Remove the Authorization header to prevent downstream access to raw key
			req := c.Request().Clone(ctx)
			req.Header.Del("Authorization")
			c.SetRequest(req)

			return next(c)
		}
	}
}

var errNoSSOSession = errors.New("no sso session")
var errNoUserPortalSession = errors.New("no user portal session")

func authenticateSSOSession(c echo.Context, authenticator SSOSessionAuthenticator) error {
	cookie, err := c.Request().Cookie(service.SSOSessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return errNoSSOSession
	}
	requireCSRF := requestRequiresCSRF(c.Request().Method)
	csrfToken := c.Request().Header.Get(service.SSOCSRFHeaderName)
	if csrfToken == "" && !requireCSRF {
		if csrfCookie, err := c.Request().Cookie(service.SSOCSRFCookieName); err == nil {
			csrfToken = csrfCookie.Value
		}
	}
	actor, err := authenticator.AuthenticateSession(c.Request().Context(), cookie.Value, csrfToken, requireCSRF)
	if err != nil {
		return ssoAuthError(err)
	}
	return setSessionPrincipal(c, actor, "sso_session")
}

func authenticateUserPortalSession(c echo.Context, authenticator UserPortalSessionAuthenticator, entitlementValidator SSOEntitlementValidator) error {
	cookie, err := c.Request().Cookie(service.UserPortalSessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return errNoUserPortalSession
	}
	requireCSRF := requestRequiresCSRF(c.Request().Method)
	csrfToken := c.Request().Header.Get(service.SSOCSRFHeaderName)
	actor, err := authenticator.AuthenticateSession(c.Request().Context(), cookie.Value, csrfToken, requireCSRF)
	if err != nil {
		return userPortalSessionAuthError(err)
	}
	if actor == nil || actor.Credential == nil {
		return httperr.New(httperr.AUTH_INVALID, "invalid user portal session")
	}
	if entitlementValidator != nil {
		validated, err := entitlementValidator.ValidateCredential(c.Request().Context(), actor.Credential)
		if err != nil {
			return ssoAuthError(err)
		}
		if validated == nil {
			return httperr.New(httperr.FORBIDDEN, "sso access denied")
		}
		actor = authenticatedActorFromCredential(validated)
	}
	return setSessionPrincipal(c, actor, "credential_session")
}

func setSessionPrincipal(c echo.Context, actor *domain.AuthenticatedActor, authMethod string) error {
	principal, actorContext, err := principalAndActorContext(actor, authMethod, "")
	if err != nil {
		return err
	}
	ctx := context.WithValue(c.Request().Context(), principalContextKey{}, principal)
	ctx = requestctx.WithActor(ctx, actorContext)
	c.SetRequest(c.Request().WithContext(ctx))
	return nil
}

func authenticatedActorFromCredential(credential *domain.Credential) *domain.AuthenticatedActor {
	if credential == nil {
		return nil
	}
	return &domain.AuthenticatedActor{
		Team: domain.Team{ID: credential.TeamID, Name: credential.TeamName},
		Identity: domain.ActorIdentity{
			ID:          credential.ActorIdentityID,
			Kind:        "api_client",
			DisplayName: credential.Name,
		},
		Membership: domain.Membership{
			ID:              credential.MembershipID,
			ActorIdentityID: credential.ActorIdentityID,
			TeamID:          credential.TeamID,
			OwnerID:         credential.OwnerID,
			Name:            credential.Name,
			Grants:          append([]string(nil), credential.Scopes...),
			Role:            credential.GetRole(),
			Status:          "active",
		},
		OwnerID:    credential.OwnerID,
		Credential: credential,
	}
}

func principalAndActorContext(actor *domain.AuthenticatedActor, authMethod, keyPrefix string) (*Principal, requestctx.Actor, error) {
	if actor == nil ||
		actor.Team.ID == uuid.Nil ||
		actor.Identity.ID == uuid.Nil ||
		actor.Membership.ID == uuid.Nil ||
		actor.OwnerID == uuid.Nil ||
		actor.Membership.TeamID != actor.Team.ID ||
		actor.Membership.ActorIdentityID != actor.Identity.ID ||
		actor.Membership.OwnerID != actor.OwnerID {
		return nil, requestctx.Actor{}, httperr.New(httperr.AUTH_INVALID, "invalid authenticated actor")
	}

	var credentialID *uuid.UUID
	rateLimit := 0
	var ssoProviderID *uuid.UUID
	ssoSubject := actor.Membership.SSOSubject
	if actor.Membership.SSOProviderID != nil {
		providerID := *actor.Membership.SSOProviderID
		ssoProviderID = &providerID
	}
	if actor.Credential != nil {
		if actor.Credential.ID == uuid.Nil ||
			actor.Credential.ActorIdentityID != actor.Identity.ID ||
			actor.Credential.MembershipID != actor.Membership.ID ||
			actor.Credential.OwnerID != actor.OwnerID ||
			actor.Credential.TeamID != actor.Team.ID {
			return nil, requestctx.Actor{}, httperr.New(httperr.AUTH_INVALID, "invalid authenticated credential")
		}
		id := actor.Credential.ID
		credentialID = &id
		rateLimit = actor.Credential.RateLimit
		if actor.Credential.SSOProviderID != nil {
			providerID := *actor.Credential.SSOProviderID
			ssoProviderID = &providerID
		}
		if actor.Credential.SSOSubject != "" {
			ssoSubject = actor.Credential.SSOSubject
		}
	}

	principal := &Principal{
		TeamID:        actor.Team.ID,
		IdentityID:    actor.Identity.ID,
		MembershipID:  actor.Membership.ID,
		OwnerID:       actor.OwnerID,
		OwnerName:     actor.Membership.Name,
		CredentialID:  credentialID,
		Role:          actor.Membership.Role,
		Grants:        append([]string(nil), actor.Membership.Grants...),
		KeyPrefix:     keyPrefix,
		RateLimit:     rateLimit,
		AuthMethod:    authMethod,
		SSOProviderID: ssoProviderID,
		SSOSubject:    ssoSubject,
		AllowedSpaces: append([]domain.MemorySpaceAccess(nil), actorAllowedSpaces(actor)...),
	}
	actorContext := requestctx.Actor{
		TeamID:        actor.Team.ID,
		TeamName:      actor.Team.Name,
		IdentityID:    actor.Identity.ID,
		MembershipID:  actor.Membership.ID,
		OwnerID:       actor.OwnerID,
		OwnerName:     actor.Membership.Name,
		CredentialID:  credentialID,
		AuthMethod:    authMethod,
		Role:          actor.Membership.Role,
		Grants:        append([]string(nil), actor.Membership.Grants...),
		AllowedSpaces: append([]domain.MemorySpaceAccess(nil), actorAllowedSpaces(actor)...),
	}
	return principal, actorContext, nil
}

func actorAllowedSpaces(actor *domain.AuthenticatedActor) []domain.MemorySpaceAccess {
	if actor == nil {
		return nil
	}
	sharedID := uuid.Nil
	if actor.Credential != nil {
		sharedID = actor.Credential.TeamSharedSpaceID
	}
	spaces := []domain.MemorySpaceAccess{{ID: sharedID, Kind: domain.MemorySpaceTeamShared}}
	if actor.Credential != nil {
		switch actor.Credential.MemoryBinding {
		case domain.CredentialBindingProfilePrivate:
			spaces = append(spaces, domain.MemorySpaceAccess{ID: actor.Credential.MemorySpaceID, Kind: domain.MemorySpaceProfilePrivate})
		case domain.CredentialBindingCredentialPrivate:
			spaceID := actor.Credential.MemorySpaceID
			if spaceID != uuid.Nil {
				spaces = append(spaces, domain.MemorySpaceAccess{ID: spaceID, Kind: domain.MemorySpaceCredentialPrivate})
			}
		}
		return spaces
	}
	// An authenticated SSO session is owned by the selected team membership.
	if actor.Membership.MemorySpaceID != uuid.Nil {
		spaces = append(spaces, domain.MemorySpaceAccess{ID: actor.Membership.MemorySpaceID, Kind: domain.MemorySpaceProfilePrivate})
	}
	return spaces
}

func requestRequiresCSRF(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func ssoAuthError(err error) error {
	switch {
	case errors.Is(err, service.ErrSSOSessionInvalid):
		return httperr.New(httperr.AUTH_INVALID, "invalid sso session")
	case errors.Is(err, service.ErrSSOCSRFInvalid):
		return httperr.New(httperr.FORBIDDEN, "invalid sso csrf token")
	case errors.Is(err, service.ErrSSOAccessDenied), errors.Is(err, service.ErrSSOProviderDisabled), errors.Is(err, service.ErrSSOEntitlementRefreshStale):
		return httperr.New(httperr.FORBIDDEN, "sso access denied")
	default:
		return httperr.New(httperr.INTERNAL_ERROR, "sso authentication failed")
	}
}

func userPortalSessionAuthError(err error) error {
	switch {
	case errors.Is(err, service.ErrUserPortalSessionInvalid):
		return httperr.New(httperr.AUTH_INVALID, "invalid user portal session")
	case errors.Is(err, service.ErrUserPortalCSRFInvalid):
		return httperr.New(httperr.FORBIDDEN, "invalid user portal csrf token")
	default:
		return httperr.New(httperr.INTERNAL_ERROR, "user portal session authentication failed")
	}
}

// LastUsedRecorder receives an admitted API-credential activity event. Implementations
// own batching and lifecycle; the request path only enqueues an identifier.
type LastUsedRecorder interface {
	RecordLastUsed(id uuid.UUID, at time.Time)
}

// LastUsedMiddleware updates credential last_used_at after earlier middleware has
// admitted the request. Place it after rate limiting to avoid activity writes
// for over-quota valid keys.
func LastUsedMiddleware(recorder LastUsedRecorder) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal := GetPrincipal(c.Request().Context())
			if principal != nil && principal.AuthMethod == "api_key" && principal.CredentialID != nil {
				if recorder != nil {
					recorder.RecordLastUsed(*principal.CredentialID, time.Now().UTC())
				}
			}
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
var _ = domain.Credential{}
var _ = http.StatusOK
