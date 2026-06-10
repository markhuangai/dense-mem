package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	DefaultSSOEntitlementCacheTTL = 5 * time.Minute
	DefaultSSOSessionTTL          = 8 * time.Hour
	DefaultSSOStateTTL            = 10 * time.Minute
	DefaultSSOHTTPTimeout         = 10 * time.Second

	SSOSessionCookieName = "dense_mem_sso_session"
	SSOCSRFCookieName    = "dense_mem_sso_csrf"
	SSOCSRFHeaderName    = "X-Dense-Mem-CSRF"
)

var (
	ErrSSOAccessDenied            = errors.New("sso access denied")
	ErrSSOSessionInvalid          = errors.New("sso session invalid")
	ErrSSOCSRFInvalid             = errors.New("sso csrf invalid")
	ErrSSOProviderDisabled        = errors.New("sso provider disabled")
	ErrSSOEntitlementRefreshStale = errors.New("sso entitlement refresh failed")
	ErrSSOGroupRefreshUnavailable = errors.New("sso group refresh unavailable")
)

type SSOGroupResolver interface {
	ResolveGroups(ctx context.Context, provider domain.SSOProvider, subject, accessToken string) ([]string, error)
}

type SSORuntimeConfigProvider interface {
	SSORuntimeConfig(ctx context.Context) (SSORuntimeConfig, error)
}

type SSORuntimeConfig struct {
	PublicBaseURL       string
	EntitlementCacheTTL time.Duration
	SessionTTL          time.Duration
	StateTTL            time.Duration
	CookieSecure        bool
	HTTPTimeout         time.Duration
}

type SSOConfig struct {
	PublicBaseURL       string
	EntitlementCacheTTL time.Duration
	SessionTTL          time.Duration
	StateTTL            time.Duration
	CookieSecure        bool
	HTTPClient          *http.Client
	HTTPTimeout         time.Duration
	GroupResolver       SSOGroupResolver
	RuntimeConfig       SSORuntimeConfigProvider
	Now                 func() time.Time
}

type SSOService struct {
	repo                repository.SSORepository
	defaultRuntime      SSORuntimeConfig
	runtimeConfigSource SSORuntimeConfigProvider
	httpClient          *http.Client
	groupResolver       SSOGroupResolver
	now                 func() time.Time
}

type SSOLoginStart struct {
	AuthURL string
}

type SSOLoginResult struct {
	SessionToken string
	CSRFToken    string
	RedirectPath string
	Session      SSOSessionInfo
}

type SSOSessionInfo struct {
	Identity  domain.SSOIdentity
	Selected  domain.SSOTeamProfile
	Teams     []domain.SSOTeamProfile
	ExpiresAt time.Time
	CSRFToken string
}

func NewSSOService(repo repository.SSORepository, cfg SSOConfig) *SSOService {
	defaultRuntime := normalizeSSORuntimeConfig(SSORuntimeConfig{
		PublicBaseURL:       cfg.PublicBaseURL,
		EntitlementCacheTTL: cfg.EntitlementCacheTTL,
		SessionTTL:          cfg.SessionTTL,
		StateTTL:            cfg.StateTTL,
		CookieSecure:        cfg.CookieSecure,
		HTTPTimeout:         cfg.HTTPTimeout,
	})
	httpClient := boundedSSOHTTPClient(cfg.HTTPClient, defaultRuntime.HTTPTimeout)
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &SSOService{
		repo:                repo,
		defaultRuntime:      defaultRuntime,
		runtimeConfigSource: cfg.RuntimeConfig,
		httpClient:          httpClient,
		groupResolver:       cfg.GroupResolver,
		now:                 now,
	}
}

func (s *SSOService) CookieSecure(ctx context.Context) bool {
	if s == nil {
		return false
	}
	cfg, err := s.runtimeConfig(ctx)
	if err != nil {
		return s.defaultRuntime.CookieSecure
	}
	return cfg.CookieSecure
}

func (s *SSOService) PublicBaseURL(ctx context.Context) (string, error) {
	cfg, err := s.runtimeConfig(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/"), nil
}

func (s *SSOService) ListEnabledProviders(ctx context.Context) ([]*domain.SSOProvider, error) {
	if s == nil || s.repo == nil {
		return []*domain.SSOProvider{}, nil
	}
	return s.repo.ListEnabledProviders(ctx)
}

func (s *SSOService) ListProviders(ctx context.Context) ([]*domain.SSOProvider, error) {
	if s == nil || s.repo == nil {
		return []*domain.SSOProvider{}, nil
	}
	return s.repo.ListProviders(ctx)
}

func (s *SSOService) CreateProvider(ctx context.Context, provider domain.SSOProvider) (*domain.SSOProvider, error) {
	if err := normalizeSSOProvider(&provider); err != nil {
		return nil, err
	}
	if err := s.repo.CreateProvider(ctx, &provider); err != nil {
		return nil, err
	}
	return &provider, nil
}

func (s *SSOService) UpdateProvider(ctx context.Context, provider domain.SSOProvider) (*domain.SSOProvider, error) {
	if provider.ID == uuid.Nil {
		return nil, fmt.Errorf("sso provider ID is required")
	}
	if err := normalizeSSOProvider(&provider); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateProvider(ctx, &provider); err != nil {
		return nil, err
	}
	return s.repo.GetProvider(ctx, provider.ID)
}

func (s *SSOService) DeleteProvider(ctx context.Context, providerID uuid.UUID) error {
	if providerID == uuid.Nil {
		return fmt.Errorf("sso provider ID is required")
	}
	return s.repo.DeleteProvider(ctx, providerID)
}

func (s *SSOService) ListMappings(ctx context.Context, providerID uuid.UUID) ([]*domain.SSOGroupMapping, error) {
	if providerID == uuid.Nil {
		return nil, fmt.Errorf("sso provider ID is required")
	}
	return s.repo.ListMappings(ctx, providerID)
}

func (s *SSOService) CreateMapping(ctx context.Context, mapping domain.SSOGroupMapping) (*domain.SSOGroupMapping, error) {
	if err := normalizeSSOGroupMapping(&mapping); err != nil {
		return nil, err
	}
	if err := s.repo.CreateMapping(ctx, &mapping); err != nil {
		return nil, err
	}
	mappings, err := s.repo.ListMappings(ctx, mapping.ProviderID)
	if err != nil {
		return nil, err
	}
	for _, item := range mappings {
		if item.ID == mapping.ID {
			return item, nil
		}
	}
	return &mapping, nil
}

func (s *SSOService) UpdateMapping(ctx context.Context, mapping domain.SSOGroupMapping) (*domain.SSOGroupMapping, error) {
	if mapping.ID == uuid.Nil {
		return nil, fmt.Errorf("sso group mapping ID is required")
	}
	if err := normalizeSSOGroupMapping(&mapping); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateMapping(ctx, &mapping); err != nil {
		return nil, err
	}
	mappings, err := s.repo.ListMappings(ctx, mapping.ProviderID)
	if err != nil {
		return nil, err
	}
	for _, item := range mappings {
		if item.ID == mapping.ID {
			return item, nil
		}
	}
	return &mapping, nil
}

func (s *SSOService) DeleteMapping(ctx context.Context, mappingID uuid.UUID) error {
	if mappingID == uuid.Nil {
		return fmt.Errorf("sso group mapping ID is required")
	}
	return s.repo.DeleteMapping(ctx, mappingID)
}

func (s *SSOService) BeginLogin(ctx context.Context, providerID uuid.UUID, callbackURL, redirectPath string) (*SSOLoginStart, error) {
	if s == nil || s.repo == nil {
		return nil, ErrSSOProviderDisabled
	}
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		return nil, err
	}
	provider, err := s.repo.GetProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if provider == nil || !provider.Enabled {
		return nil, ErrSSOProviderDisabled
	}
	if err := s.repo.DeleteExpiredOAuthStates(ctx, s.now()); err != nil {
		return nil, err
	}

	providerCtx, cancel := s.providerContext(ctx, runtime)
	defer cancel()
	oidcProvider, oauthConfig, err := s.oauthConfig(providerCtx, *provider, callbackURL)
	if err != nil {
		return nil, err
	}
	_ = oidcProvider

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
	state := domain.SSOOAuthState{
		StateHash:    HashSSOToken(stateToken),
		ProviderID:   provider.ID,
		PKCEVerifier: pkceVerifier,
		Nonce:        nonce,
		RedirectPath: safeRedirectPath(redirectPath),
		ExpiresAt:    s.now().Add(runtime.StateTTL),
		CreatedAt:    s.now(),
	}
	if err := s.repo.CreateOAuthState(ctx, state); err != nil {
		return nil, err
	}

	authURL := oauthConfig.AuthCodeURL(
		stateToken,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", pkceChallenge(pkceVerifier)),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	return &SSOLoginStart{AuthURL: authURL}, nil
}

func (s *SSOService) CompleteLogin(ctx context.Context, stateToken, code, callbackURL string) (*SSOLoginResult, error) {
	if s == nil || s.repo == nil {
		return nil, ErrSSOProviderDisabled
	}
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		return nil, err
	}
	state, err := s.repo.ConsumeOAuthState(ctx, HashSSOToken(stateToken))
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, ErrSSOSessionInvalid
	}

	provider, err := s.repo.GetProvider(ctx, state.ProviderID)
	if err != nil {
		return nil, err
	}
	if provider == nil || !provider.Enabled {
		return nil, ErrSSOProviderDisabled
	}

	providerCtx, cancel := s.providerContext(ctx, runtime)
	defer cancel()
	oidcProvider, oauthConfig, err := s.oauthConfig(providerCtx, *provider, callbackURL)
	if err != nil {
		return nil, err
	}
	token, err := oauthConfig.Exchange(providerCtx, code, oauth2.SetAuthURLParam("code_verifier", state.PKCEVerifier))
	if err != nil {
		return nil, fmt.Errorf("sso token exchange failed: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || strings.TrimSpace(rawIDToken) == "" {
		return nil, fmt.Errorf("sso token exchange did not return id_token")
	}
	idToken, err := oidcProvider.Verifier(&oidc.Config{ClientID: provider.ClientID}).Verify(providerCtx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("sso id_token verification failed: %w", err)
	}

	claims, err := readOIDCClaims(providerCtx, oidcProvider, token, idToken, *provider)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(state.Nonce)) != 1 {
		return nil, fmt.Errorf("sso nonce mismatch")
	}
	if claims.Subject == "" {
		claims.Subject = idToken.Subject
	}
	if len(claims.Groups) == 0 {
		claims.Groups, err = s.resolveGroups(providerCtx, runtime, *provider, claims.Subject, token.AccessToken)
		if err != nil {
			return nil, ErrSSOAccessDenied
		}
	}

	mappings, err := s.repo.ListMappingsForGroups(ctx, provider.ID, claims.Groups)
	if err != nil {
		return nil, err
	}
	entitlements, err := s.entitlementsFromMappings(provider.ID, claims.Subject, mappings)
	if err != nil {
		_ = s.storeEntitlementCacheWithTTL(ctx, runtime.EntitlementCacheTTL, provider.ID, claims.Subject, claims.Groups, "denied", "")
		return nil, err
	}

	now := s.now()
	identity := &domain.SSOIdentity{
		ProviderID:             provider.ID,
		Subject:                claims.Subject,
		Email:                  claims.Email,
		DisplayName:            claims.DisplayName,
		LastLoginAt:            &now,
		LastEntitlementCheckAt: &now,
	}
	if err := s.repo.UpsertIdentity(ctx, identity); err != nil {
		return nil, err
	}

	activeByProfileID := make(map[uuid.UUID]struct{}, len(entitlements))
	for _, entitlement := range entitlements {
		name := ssoProfileName(claims.Email, claims.DisplayName, identity.ID)
		profile, err := s.repo.UpsertTeamProfileForMapping(ctx, *identity, entitlement, name)
		if err != nil {
			return nil, err
		}
		activeByProfileID[profile.ID] = struct{}{}
	}
	if err := s.storeEntitlementCacheWithTTL(ctx, runtime.EntitlementCacheTTL, provider.ID, claims.Subject, claims.Groups, "active", ""); err != nil {
		return nil, err
	}

	teams, err := s.currentEntitledTeams(ctx, identity.ID, activeByProfileID)
	if err != nil {
		return nil, err
	}
	if len(teams) == 0 {
		return nil, ErrSSOAccessDenied
	}
	selected := teams[0]
	sessionToken, err := secureRandomToken(32)
	if err != nil {
		return nil, err
	}
	csrfToken, err := secureRandomToken(32)
	if err != nil {
		return nil, err
	}
	session := domain.SSOSession{
		SessionHash:   HashSSOToken(sessionToken),
		IdentityID:    identity.ID,
		ProviderID:    provider.ID,
		TeamProfileID: selected.Profile.ID,
		TeamID:        selected.Team.ID,
		CSRFHash:      HashSSOToken(csrfToken),
		ExpiresAt:     now.Add(runtime.SessionTTL),
		CreatedAt:     now,
		LastSeenAt:    now,
	}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		return nil, err
	}

	info := SSOSessionInfo{
		Identity:  *identity,
		Selected:  selected,
		Teams:     teams,
		ExpiresAt: session.ExpiresAt,
		CSRFToken: csrfToken,
	}
	return &SSOLoginResult{
		SessionToken: sessionToken,
		CSRFToken:    csrfToken,
		RedirectPath: safeRedirectPath(state.RedirectPath),
		Session:      info,
	}, nil
}

func (s *SSOService) AuthenticateSession(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.APIKey, error) {
	session, err := s.sessionFromToken(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	if requireCSRF && !hashMatches(csrfToken, session.CSRFHash) {
		return nil, ErrSSOCSRFInvalid
	}
	key, err := s.repo.GetSSOProfileByID(ctx, session.TeamProfileID)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, ErrSSOSessionInvalid
	}
	return s.ValidateAPIKeyPrincipal(ctx, key)
}

func (s *SSOService) CurrentSession(ctx context.Context, sessionToken string) (*SSOSessionInfo, error) {
	session, err := s.sessionFromToken(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	identity, err := s.repo.GetIdentity(ctx, session.IdentityID)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, ErrSSOSessionInvalid
	}
	allTeams, err := s.repo.ListTeamProfilesForIdentity(ctx, identity.ID)
	if err != nil {
		return nil, err
	}
	teams := make([]domain.SSOTeamProfile, 0, len(allTeams))
	var selected *domain.SSOTeamProfile
	for _, team := range allTeams {
		if _, err := s.ValidateAPIKeyPrincipal(ctx, &team.Profile); err != nil {
			if errors.Is(err, ErrSSOAccessDenied) || errors.Is(err, ErrSSOEntitlementRefreshStale) {
				continue
			}
			return nil, err
		}
		teams = append(teams, *team)
		if team.Profile.ID == session.TeamProfileID {
			copy := *team
			selected = &copy
		}
	}
	if selected == nil {
		return nil, ErrSSOAccessDenied
	}
	return &SSOSessionInfo{
		Identity:  *identity,
		Selected:  *selected,
		Teams:     teams,
		ExpiresAt: session.ExpiresAt,
	}, nil
}

func (s *SSOService) SwitchSessionTeam(ctx context.Context, sessionToken string, teamProfileID uuid.UUID) (*SSOSessionInfo, error) {
	session, err := s.sessionFromToken(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	teams, err := s.repo.ListTeamProfilesForIdentity(ctx, session.IdentityID)
	if err != nil {
		return nil, err
	}
	for _, team := range teams {
		if team.Profile.ID != teamProfileID {
			continue
		}
		if _, err := s.ValidateAPIKeyPrincipal(ctx, &team.Profile); err != nil {
			return nil, err
		}
		if err := s.repo.UpdateSessionTeam(ctx, session.SessionHash, team.Profile.ID, team.Team.ID); err != nil {
			return nil, err
		}
		return s.CurrentSession(ctx, sessionToken)
	}
	return nil, ErrSSOAccessDenied
}

func (s *SSOService) Logout(ctx context.Context, sessionToken string) error {
	if s == nil || strings.TrimSpace(sessionToken) == "" {
		return nil
	}
	return s.repo.DeleteSession(ctx, HashSSOToken(sessionToken))
}

func (s *SSOService) ValidateAPIKeyPrincipal(ctx context.Context, key *domain.APIKey) (*domain.APIKey, error) {
	if key == nil {
		return nil, ErrSSOAccessDenied
	}
	if key.SSOProviderID == nil || strings.TrimSpace(key.SSOSubject) == "" {
		return key, nil
	}
	provider, err := s.repo.GetProvider(ctx, *key.SSOProviderID)
	if err != nil {
		return nil, err
	}
	if provider == nil || !provider.Enabled {
		return nil, ErrSSOProviderDisabled
	}

	cache, err := s.repo.GetEntitlementCache(ctx, provider.ID, key.SSOSubject)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if cache == nil || cache.ExpiresAt.Before(now) || cache.ExpiresAt.Equal(now) {
		runtime, err := s.runtimeConfig(ctx)
		if err != nil {
			return nil, err
		}
		providerCtx, cancel := s.providerContext(ctx, runtime)
		groups, refreshErr := s.resolveGroups(providerCtx, runtime, *provider, key.SSOSubject, "")
		cancel()
		if refreshErr != nil {
			_ = s.storeEntitlementCacheWithTTL(ctx, runtime.EntitlementCacheTTL, provider.ID, key.SSOSubject, nil, "error", refreshErr.Error())
			return nil, ErrSSOEntitlementRefreshStale
		}
		cache = &domain.SSOEntitlementCache{
			ProviderID: provider.ID,
			Subject:    key.SSOSubject,
			Groups:     groups,
			Status:     "active",
			CheckedAt:  now,
			ExpiresAt:  now.Add(runtime.EntitlementCacheTTL),
		}
		mappings, err := s.repo.ListMappingsForGroups(ctx, provider.ID, groups)
		if err != nil {
			return nil, err
		}
		if !hasMappingForTeam(mappings, key.GetTeamID()) {
			cache.Status = "denied"
		}
		if err := s.repo.SetEntitlementCache(ctx, *cache); err != nil {
			return nil, err
		}
	}
	if cache.Status != "active" {
		return nil, ErrSSOAccessDenied
	}
	mappings, err := s.repo.ListMappingsForGroups(ctx, provider.ID, cache.Groups)
	if err != nil {
		return nil, err
	}
	entitlement, ok := mergedEntitlementForTeam(mappings, key.GetTeamID())
	if !ok {
		return nil, ErrSSOAccessDenied
	}
	key.Scopes = entitlement.Scopes
	key.Role = entitlement.Role
	key.SSOGroupID = entitlement.GroupID
	key.SSOEntitlementStatus = "active"
	return key, nil
}

func (s *SSOService) oauthConfig(ctx context.Context, provider domain.SSOProvider, callbackURL string) (*oidc.Provider, *oauth2.Config, error) {
	oidcProvider, err := oidc.NewProvider(ctx, provider.IssuerURL)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to discover oidc provider: %w", err)
	}
	scopes := provider.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}
	if !containsString(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	secret := ""
	if provider.ClientSecretEnv != "" {
		secret = os.Getenv(provider.ClientSecretEnv)
	}
	return oidcProvider, &oauth2.Config{
		ClientID:     provider.ClientID,
		ClientSecret: secret,
		Endpoint:     oidcProvider.Endpoint(),
		Scopes:       scopes,
		RedirectURL:  callbackURL,
	}, nil
}

func (s *SSOService) providerContext(ctx context.Context, runtime SSORuntimeConfig) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, boundedSSOHTTPClient(s.httpClient, runtime.HTTPTimeout))
	}
	if runtime.HTTPTimeout <= 0 {
		return ctx, func() {}
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, runtime.HTTPTimeout)
}

func boundedSSOHTTPClient(client *http.Client, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultSSOHTTPTimeout
	}
	if client == nil {
		return &http.Client{Timeout: timeout}
	}
	copy := *client
	if copy.Timeout <= 0 {
		copy.Timeout = timeout
	}
	return &copy
}

func (s *SSOService) sessionFromToken(ctx context.Context, sessionToken string) (*domain.SSOSession, error) {
	if s == nil || s.repo == nil || strings.TrimSpace(sessionToken) == "" {
		return nil, ErrSSOSessionInvalid
	}
	session, err := s.repo.GetSession(ctx, HashSSOToken(sessionToken))
	if err != nil {
		return nil, err
	}
	if session == nil || session.ExpiresAt.Before(s.now()) {
		return nil, ErrSSOSessionInvalid
	}
	return session, nil
}

func (s *SSOService) entitlementsFromMappings(providerID uuid.UUID, subject string, mappings []*domain.SSOGroupMapping) ([]domain.SSOGroupMapping, error) {
	if len(mappings) == 0 {
		return nil, ErrSSOAccessDenied
	}
	byTeam := make(map[uuid.UUID]*domain.SSOGroupMapping)
	for _, mapping := range mappings {
		if mapping == nil || !mapping.Enabled || mapping.ProviderID != providerID {
			continue
		}
		current := byTeam[mapping.TeamID]
		if current == nil {
			copy := *mapping
			copy.Scopes = normalizeSSOScopes(mapping.Scopes)
			copy.Role = normalizeSSORole(mapping.Role)
			byTeam[mapping.TeamID] = &copy
			continue
		}
		current.Scopes = mergeScopes(current.Scopes, mapping.Scopes)
		if normalizeSSORole(mapping.Role) == APIKeyRoleManager {
			current.Role = APIKeyRoleManager
		}
		if !strings.Contains(","+current.GroupID+",", ","+mapping.GroupID+",") {
			current.GroupID += "," + mapping.GroupID
		}
	}
	if len(byTeam) == 0 {
		return nil, ErrSSOAccessDenied
	}
	result := make([]domain.SSOGroupMapping, 0, len(byTeam))
	for _, mapping := range byTeam {
		result = append(result, *mapping)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TeamName == result[j].TeamName {
			return result[i].TeamID.String() < result[j].TeamID.String()
		}
		return result[i].TeamName < result[j].TeamName
	})
	_ = subject
	return result, nil
}

func (s *SSOService) currentEntitledTeams(ctx context.Context, identityID uuid.UUID, allowed map[uuid.UUID]struct{}) ([]domain.SSOTeamProfile, error) {
	allTeams, err := s.repo.ListTeamProfilesForIdentity(ctx, identityID)
	if err != nil {
		return nil, err
	}
	teams := make([]domain.SSOTeamProfile, 0, len(allTeams))
	for _, team := range allTeams {
		if _, ok := allowed[team.Profile.ID]; ok {
			teams = append(teams, *team)
		}
	}
	return teams, nil
}

func (s *SSOService) storeEntitlementCache(ctx context.Context, providerID uuid.UUID, subject string, groups []string, status, message string) error {
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		return err
	}
	return s.storeEntitlementCacheWithTTL(ctx, runtime.EntitlementCacheTTL, providerID, subject, groups, status, message)
}

func (s *SSOService) storeEntitlementCacheWithTTL(ctx context.Context, ttl time.Duration, providerID uuid.UUID, subject string, groups []string, status, message string) error {
	now := s.now()
	return s.repo.SetEntitlementCache(ctx, domain.SSOEntitlementCache{
		ProviderID: providerID,
		Subject:    subject,
		Groups:     dedupeStrings(groups),
		Status:     status,
		CheckedAt:  now,
		ExpiresAt:  now.Add(ttl),
		Error:      message,
	})
}

func (s *SSOService) runtimeConfig(ctx context.Context) (SSORuntimeConfig, error) {
	if s == nil {
		return normalizeSSORuntimeConfig(SSORuntimeConfig{}), nil
	}
	if s.runtimeConfigSource == nil {
		return s.defaultRuntime, nil
	}
	cfg, err := s.runtimeConfigSource.SSORuntimeConfig(ctx)
	if err != nil {
		return SSORuntimeConfig{}, err
	}
	return normalizeSSORuntimeConfig(cfg), nil
}

func normalizeSSORuntimeConfig(cfg SSORuntimeConfig) SSORuntimeConfig {
	cfg.PublicBaseURL = strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	if cfg.EntitlementCacheTTL <= 0 {
		cfg.EntitlementCacheTTL = DefaultSSOEntitlementCacheTTL
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = DefaultSSOSessionTTL
	}
	if cfg.StateTTL <= 0 {
		cfg.StateTTL = DefaultSSOStateTTL
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = DefaultSSOHTTPTimeout
	}
	return cfg
}

func (s *SSOService) resolveGroups(ctx context.Context, runtime SSORuntimeConfig, provider domain.SSOProvider, subject, accessToken string) ([]string, error) {
	if s.groupResolver != nil {
		return s.groupResolver.ResolveGroups(ctx, provider, subject, accessToken)
	}
	return (&HTTPSSOGroupResolver{HTTPClient: boundedSSOHTTPClient(s.httpClient, runtime.HTTPTimeout)}).ResolveGroups(ctx, provider, subject, accessToken)
}
