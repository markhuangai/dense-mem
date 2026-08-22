package access

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	ControlSessionCookieName = "dense_mem_control_session"
	ControlCSRFCookieName    = "dense_mem_control_csrf"
	ControlCSRFHeaderName    = "X-Dense-Mem-Control-CSRF"
)

var (
	ErrControlAccessDenied   = errors.New("control access denied")
	ErrControlSessionInvalid = errors.New("control session invalid")
	ErrControlCSRFInvalid    = errors.New("control csrf invalid")
	ErrControlSSOUnavailable = errors.New("control sso is unavailable")
)

type ControlIdentityConfig struct {
	RuntimeConfig SSORuntimeConfigProvider
	HTTPClient    *http.Client
	GroupResolver SSOGroupResolver
	Now           func() time.Time
}

type ControlIdentityService struct {
	repo                ControlIdentityStore
	ssoRepo             SSOStore
	runtimeConfigSource SSORuntimeConfigProvider
	httpClient          *http.Client
	groupResolver       SSOGroupResolver
	now                 func() time.Time
}

type ControlLoginStart struct {
	AuthURL string
}

type ControlLoginResult struct {
	SessionToken string
	CSRFToken    string
	Identity     domain.SSOIdentity
	ExpiresAt    time.Time
}

func NewControlIdentityService(repo ControlIdentityStore, ssoRepo SSOStore, cfg ControlIdentityConfig) *ControlIdentityService {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ControlIdentityService{
		repo:                repo,
		ssoRepo:             ssoRepo,
		runtimeConfigSource: cfg.RuntimeConfig,
		httpClient:          cfg.HTTPClient,
		groupResolver:       cfg.GroupResolver,
		now:                 now,
	}
}

func (s *ControlIdentityService) ListEnabledProviders(ctx context.Context) ([]*domain.SSOProvider, error) {
	if s == nil || s.repo == nil || s.ssoRepo == nil {
		return []*domain.SSOProvider{}, nil
	}
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !controlRuntimeReady(runtime) {
		return []*domain.SSOProvider{}, nil
	}
	providers, err := s.ssoRepo.ListEnabledProviders(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*domain.SSOProvider, 0, len(providers))
	for _, provider := range providers {
		if provider == nil || !provider.Enabled || provider.RetiredAt != nil {
			continue
		}
		groups, err := s.activeAdminGroups(ctx, provider.ID)
		if err != nil {
			return nil, err
		}
		if len(groups) > 0 {
			result = append(result, provider)
		}
	}
	return result, nil
}

func (s *ControlIdentityService) ListAdminGroups(ctx context.Context, providerID uuid.UUID) ([]*domain.ControlAdminGroup, error) {
	if s == nil || s.repo == nil {
		return nil, ErrControlSSOUnavailable
	}
	if providerID == uuid.Nil {
		return nil, fmt.Errorf("sso provider ID is required")
	}
	return s.repo.ListControlAdminGroups(ctx, providerID)
}

func (s *ControlIdentityService) CreateAdminGroup(ctx context.Context, group domain.ControlAdminGroup) (*domain.ControlAdminGroup, error) {
	if s == nil || s.repo == nil || s.ssoRepo == nil {
		return nil, ErrControlSSOUnavailable
	}
	if err := normalizeControlAdminGroup(&group); err != nil {
		return nil, err
	}
	provider, err := s.ssoRepo.GetProvider(ctx, group.ProviderID)
	if err != nil {
		return nil, err
	}
	if provider == nil || provider.RetiredAt != nil {
		return nil, fmt.Errorf("sso provider not found")
	}
	if err := s.repo.CreateControlAdminGroup(ctx, &group); err != nil {
		return nil, err
	}
	return &group, nil
}

func (s *ControlIdentityService) UpdateAdminGroup(ctx context.Context, group domain.ControlAdminGroup) (*domain.ControlAdminGroup, error) {
	if s == nil || s.repo == nil {
		return nil, ErrControlSSOUnavailable
	}
	if group.ID == uuid.Nil {
		return nil, fmt.Errorf("control admin group ID is required")
	}
	if err := normalizeControlAdminGroup(&group); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateControlAdminGroup(ctx, &group); err != nil {
		return nil, err
	}
	return &group, nil
}

func (s *ControlIdentityService) RetireAdminGroup(ctx context.Context, providerID, groupID uuid.UUID) error {
	if s == nil || s.repo == nil {
		return ErrControlSSOUnavailable
	}
	if providerID == uuid.Nil || groupID == uuid.Nil {
		return fmt.Errorf("sso provider ID and control admin group ID are required")
	}
	return s.repo.RetireControlAdminGroup(ctx, providerID, groupID)
}

func (s *ControlIdentityService) BeginLogin(ctx context.Context, providerID uuid.UUID, callbackURL string) (*ControlLoginStart, error) {
	if s == nil || s.repo == nil || s.ssoRepo == nil {
		return nil, ErrControlSSOUnavailable
	}
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !controlRuntimeReady(runtime) || strings.TrimSpace(callbackURL) == "" {
		return nil, ErrControlSSOUnavailable
	}
	provider, err := s.ssoRepo.GetProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if provider == nil || !provider.Enabled || provider.RetiredAt != nil {
		return nil, ErrControlAccessDenied
	}
	groups, err := s.activeAdminGroups(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, ErrControlAccessDenied
	}
	if err := s.repo.DeleteExpiredControlOAuthStates(ctx, s.now()); err != nil {
		return nil, err
	}
	providerCtx, cancel := s.providerContext(ctx, runtime)
	defer cancel()
	_, oauthConfig, err := s.oauthConfig(providerCtx, *provider, callbackURL)
	if err != nil {
		return nil, err
	}
	stateToken, err := secureRandomToken(32)
	if err != nil {
		return nil, err
	}
	nonce, err := secureRandomToken(24)
	if err != nil {
		return nil, err
	}
	pkceVerifier, err := secureRandomToken(48)
	if err != nil {
		return nil, err
	}
	state := domain.ControlOAuthState{
		StateHash:    HashSSOToken(stateToken),
		ProviderID:   provider.ID,
		PKCEVerifier: pkceVerifier,
		Nonce:        nonce,
		ExpiresAt:    s.now().Add(runtime.StateTTL),
		CreatedAt:    s.now(),
	}
	if err := s.repo.CreateControlOAuthState(ctx, state); err != nil {
		return nil, err
	}
	return &ControlLoginStart{AuthURL: oauthConfig.AuthCodeURL(
		stateToken,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", pkceChallenge(pkceVerifier)),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)}, nil
}

func (s *ControlIdentityService) CompleteLogin(ctx context.Context, stateToken, code, callbackURL string) (*ControlLoginResult, error) {
	if s == nil || s.repo == nil || s.ssoRepo == nil {
		return nil, ErrControlSSOUnavailable
	}
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !controlRuntimeReady(runtime) || strings.TrimSpace(stateToken) == "" || strings.TrimSpace(code) == "" || strings.TrimSpace(callbackURL) == "" {
		return nil, ErrControlAccessDenied
	}
	state, err := s.repo.ConsumeControlOAuthState(ctx, HashSSOToken(stateToken))
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, ErrControlAccessDenied
	}
	provider, err := s.ssoRepo.GetProvider(ctx, state.ProviderID)
	if err != nil {
		return nil, err
	}
	if provider == nil || !provider.Enabled || provider.RetiredAt != nil {
		return nil, ErrControlAccessDenied
	}
	providerCtx, cancel := s.providerContext(ctx, runtime)
	defer cancel()
	oidcProvider, oauthConfig, err := s.oauthConfig(providerCtx, *provider, callbackURL)
	if err != nil {
		return nil, err
	}
	token, err := oauthConfig.Exchange(providerCtx, code, oauth2.SetAuthURLParam("code_verifier", state.PKCEVerifier))
	if err != nil {
		return nil, ErrControlAccessDenied
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || strings.TrimSpace(rawIDToken) == "" {
		return nil, ErrControlAccessDenied
	}
	idToken, err := oidcProvider.Verifier(&oidc.Config{ClientID: provider.ClientID}).Verify(providerCtx, rawIDToken)
	if err != nil {
		return nil, ErrControlAccessDenied
	}
	claims, err := readOIDCClaims(providerCtx, oidcProvider, token, idToken, *provider)
	if err != nil || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(state.Nonce)) != 1 {
		return nil, ErrControlAccessDenied
	}
	if claims.Subject == "" {
		claims.Subject = idToken.Subject
	}
	if len(claims.Groups) == 0 {
		claims.Groups, err = s.resolveGroups(providerCtx, runtime, *provider, claims.Subject, token.AccessToken)
		if err != nil {
			return nil, ErrControlAccessDenied
		}
	}
	claims.Groups = dedupeStrings(claims.Groups)
	groups, err := s.activeAdminGroups(ctx, provider.ID)
	if err != nil {
		return nil, err
	}
	if !controlGroupsMatch(groups, claims.Groups) {
		return nil, ErrControlAccessDenied
	}
	now := s.now()
	identity := &domain.SSOIdentity{
		ProviderID:             provider.ID,
		Subject:                claims.Subject,
		ExternalID:             claims.ExternalID,
		Email:                  claims.Email,
		DisplayName:            claims.DisplayName,
		Active:                 true,
		LastLoginAt:            &now,
		LastEntitlementCheckAt: &now,
	}
	if err := s.ssoRepo.UpsertIdentity(ctx, identity); err != nil {
		if errors.Is(err, repository.ErrDirectoryIdentityNotProvisioned) {
			return nil, ErrControlAccessDenied
		}
		return nil, err
	}
	if err := s.repo.DeleteExpiredControlSessions(ctx, now); err != nil {
		return nil, err
	}
	sessionToken, err := secureRandomToken(32)
	if err != nil {
		return nil, err
	}
	csrfToken, err := secureRandomToken(32)
	if err != nil {
		return nil, err
	}
	session := domain.ControlSession{
		SessionHash: HashSSOToken(sessionToken),
		IdentityID:  identity.ID,
		ProviderID:  provider.ID,
		GroupIDs:    claims.Groups,
		CSRFHash:    HashSSOToken(csrfToken),
		ExpiresAt:   now.Add(runtime.SessionTTL),
		CreatedAt:   now,
		LastSeenAt:  now,
	}
	if err := s.repo.CreateControlSession(ctx, session); err != nil {
		return nil, err
	}
	return &ControlLoginResult{SessionToken: sessionToken, CSRFToken: csrfToken, Identity: *identity, ExpiresAt: session.ExpiresAt}, nil
}

func (s *ControlIdentityService) AuthenticateSession(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.SSOIdentity, error) {
	session, err := s.sessionFromToken(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	if requireCSRF && !hashMatches(csrfToken, session.CSRFHash) {
		return nil, ErrControlCSRFInvalid
	}
	identity, err := s.ssoRepo.GetIdentity(ctx, session.IdentityID)
	if err != nil {
		return nil, err
	}
	if identity == nil || !identity.Active || identity.ProviderID != session.ProviderID {
		return nil, ErrControlSessionInvalid
	}
	currentGroups, err := s.currentSessionGroups(ctx, *identity, session.GroupIDs)
	if err != nil {
		return nil, ErrControlAccessDenied
	}
	groups, err := s.activeAdminGroups(ctx, session.ProviderID)
	if err != nil {
		return nil, err
	}
	if !controlGroupsMatch(groups, currentGroups) {
		return nil, ErrControlAccessDenied
	}
	return identity, nil
}

func (s *ControlIdentityService) CurrentSession(ctx context.Context, sessionToken string) (*domain.SSOIdentity, error) {
	return s.AuthenticateSession(ctx, sessionToken, "", false)
}

func (s *ControlIdentityService) Logout(ctx context.Context, sessionToken string) error {
	if s == nil || s.repo == nil || strings.TrimSpace(sessionToken) == "" {
		return nil
	}
	return s.repo.DeleteControlSession(ctx, HashSSOToken(sessionToken))
}

func (s *ControlIdentityService) ControlPublicBaseURL(ctx context.Context) (string, error) {
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		return "", err
	}
	return runtime.ControlPublicBaseURL, nil
}

func (s *ControlIdentityService) CookieSecure(ctx context.Context) bool {
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		return true
	}
	return controlRuntimeReady(runtime)
}

func (s *ControlIdentityService) runtimeConfig(ctx context.Context) (SSORuntimeConfig, error) {
	if s == nil || s.runtimeConfigSource == nil {
		return normalizeSSORuntimeConfig(SSORuntimeConfig{}), nil
	}
	runtime, err := s.runtimeConfigSource.SSORuntimeConfig(ctx)
	if err != nil {
		return SSORuntimeConfig{}, err
	}
	return normalizeSSORuntimeConfig(runtime), nil
}

func (s *ControlIdentityService) activeAdminGroups(ctx context.Context, providerID uuid.UUID) ([]*domain.ControlAdminGroup, error) {
	groups, err := s.repo.ListControlAdminGroups(ctx, providerID)
	if err != nil {
		return nil, err
	}
	active := make([]*domain.ControlAdminGroup, 0, len(groups))
	for _, group := range groups {
		if group != nil && group.Enabled && group.RetiredAt == nil && strings.TrimSpace(group.GroupID) != "" {
			active = append(active, group)
		}
	}
	return active, nil
}

func (s *ControlIdentityService) oauthConfig(ctx context.Context, provider domain.SSOProvider, callbackURL string) (*oidc.Provider, *oauth2.Config, error) {
	copy := provider
	if err := normalizeSSOProvider(&copy); err != nil {
		return nil, nil, err
	}
	oidcProvider, err := oidc.NewProvider(ctx, copy.IssuerURL)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to discover oidc provider: %w", err)
	}
	secret := ""
	if copy.ClientSecretEnv != "" {
		secret = os.Getenv(copy.ClientSecretEnv)
	}
	return oidcProvider, &oauth2.Config{
		ClientID:     copy.ClientID,
		ClientSecret: secret,
		Endpoint:     oidcProvider.Endpoint(),
		Scopes:       interactiveOAuthScopes(copy),
		RedirectURL:  callbackURL,
	}, nil
}

func (s *ControlIdentityService) providerContext(ctx context.Context, runtime SSORuntimeConfig) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, boundedSSOHTTPClient(s.httpClient, runtime.HTTPTimeout))
	if runtime.HTTPTimeout <= 0 || ctx.Err() != nil {
		return ctx, func() {}
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, runtime.HTTPTimeout)
}

func (s *ControlIdentityService) resolveGroups(ctx context.Context, runtime SSORuntimeConfig, provider domain.SSOProvider, subject, accessToken string) ([]string, error) {
	if s.groupResolver != nil {
		return s.groupResolver.ResolveGroups(ctx, provider, subject, accessToken)
	}
	return (&HTTPSSOGroupResolver{HTTPClient: boundedSSOHTTPClient(s.httpClient, runtime.HTTPTimeout)}).ResolveGroups(ctx, provider, subject, accessToken)
}

func (s *ControlIdentityService) currentSessionGroups(ctx context.Context, identity domain.SSOIdentity, fallback []string) ([]string, error) {
	provider, err := s.ssoRepo.GetProvider(ctx, identity.ProviderID)
	if err != nil {
		return nil, err
	}
	if provider == nil || !provider.Enabled || provider.RetiredAt != nil {
		return nil, ErrControlAccessDenied
	}
	if strings.TrimSpace(provider.GroupsEndpoint) == "" {
		return dedupeStrings(fallback), nil
	}
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		return nil, err
	}
	providerCtx, cancel := s.providerContext(ctx, runtime)
	defer cancel()
	groups, err := s.resolveGroups(providerCtx, runtime, *provider, identity.Subject, "")
	if err != nil {
		return nil, err
	}
	return dedupeStrings(groups), nil
}

func (s *ControlIdentityService) sessionFromToken(ctx context.Context, sessionToken string) (*domain.ControlSession, error) {
	if s == nil || s.repo == nil || strings.TrimSpace(sessionToken) == "" {
		return nil, ErrControlSessionInvalid
	}
	session, err := s.repo.GetControlSession(ctx, HashSSOToken(sessionToken))
	if err != nil {
		return nil, err
	}
	if session == nil || !session.ExpiresAt.After(s.now()) {
		return nil, ErrControlSessionInvalid
	}
	return session, nil
}

func normalizeControlAdminGroup(group *domain.ControlAdminGroup) error {
	if group == nil || group.ProviderID == uuid.Nil {
		return fmt.Errorf("sso provider ID is required")
	}
	group.GroupID = strings.TrimSpace(group.GroupID)
	group.GroupName = strings.TrimSpace(group.GroupName)
	if group.GroupID == "" || len(group.GroupID) > 512 || len(group.GroupName) > 512 {
		return fmt.Errorf("control admin group ID or name is invalid")
	}
	return nil
}

func controlGroupsMatch(configured []*domain.ControlAdminGroup, claims []string) bool {
	for _, group := range configured {
		if group != nil && containsString(claims, group.GroupID) {
			return true
		}
	}
	return false
}

func controlRuntimeReady(runtime SSORuntimeConfig) bool {
	parsed, err := url.Parse(strings.TrimSpace(runtime.ControlPublicBaseURL))
	return err == nil && parsed.Host != "" && parsed.Scheme == "https"
}
