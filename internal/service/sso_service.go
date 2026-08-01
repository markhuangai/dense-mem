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
	"github.com/markhuangai/dense-mem/internal/observability"
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
	PublicBaseURL        string
	SCIMPublicBaseURL    string
	ControlPublicBaseURL string
	EntitlementCacheTTL  time.Duration
	SessionTTL           time.Duration
	StateTTL             time.Duration
	CookieSecure         bool
	HTTPTimeout          time.Duration
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
	Logger              observability.LogProvider
	Now                 func() time.Time
}

type SSOService struct {
	repo                repository.SSORepository
	defaultRuntime      SSORuntimeConfig
	runtimeConfigSource SSORuntimeConfigProvider
	httpClient          *http.Client
	groupResolver       SSOGroupResolver
	logger              observability.LogProvider
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
		logger:              cfg.Logger,
		now:                 now,
	}
}

func (s *SSOService) CookieSecure(ctx context.Context) bool {
	if s == nil {
		return false
	}
	cfg, err := s.runtimeConfig(ctx)
	if err != nil {
		s.debugSSOFailure("sso cookie secure runtime config failed", err)
		return s.defaultRuntime.CookieSecure
	}
	return cfg.CookieSecure
}

func (s *SSOService) PublicBaseURL(ctx context.Context) (string, error) {
	cfg, err := s.runtimeConfig(ctx)
	if err != nil {
		s.debugSSOFailure("sso public base url runtime config failed", err)
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/"), nil
}

func (s *SSOService) ListEnabledProviders(ctx context.Context) ([]*domain.SSOProvider, error) {
	if s == nil || s.repo == nil {
		return []*domain.SSOProvider{}, nil
	}
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		s.debugSSOFailure("sso list enabled providers runtime config failed", err)
		return nil, err
	}
	if !ssoRuntimeReadyForPublicLogin(runtime) {
		return []*domain.SSOProvider{}, nil
	}
	providers, err := s.repo.ListEnabledProviders(ctx)
	if err != nil {
		s.debugSSOFailure("sso list enabled providers query failed", err)
		return nil, err
	}
	items := make([]*domain.SSOProvider, 0, len(providers))
	for _, provider := range providers {
		if ssoProviderReadyForPublicLogin(provider) {
			items = append(items, provider)
		}
	}
	return items, nil
}

func (s *SSOService) ListProviders(ctx context.Context) ([]*domain.SSOProvider, error) {
	if s == nil || s.repo == nil {
		return []*domain.SSOProvider{}, nil
	}
	providers, err := s.repo.ListProviders(ctx)
	if err != nil {
		s.debugSSOFailure("sso list providers query failed", err)
		return nil, err
	}
	return providers, nil
}

func (s *SSOService) CreateProvider(ctx context.Context, provider domain.SSOProvider) (*domain.SSOProvider, error) {
	if err := normalizeSSOProviderForWrite(&provider); err != nil {
		s.debugSSOProviderFailure("sso create provider validation failed", err, &provider)
		return nil, err
	}
	if err := s.repo.CreateProvider(ctx, &provider); err != nil {
		s.debugSSOProviderFailure("sso create provider query failed", err, &provider)
		return nil, err
	}
	return &provider, nil
}

func (s *SSOService) UpdateProvider(ctx context.Context, provider domain.SSOProvider) (*domain.SSOProvider, error) {
	if provider.ID == uuid.Nil {
		err := fmt.Errorf("sso provider ID is required")
		s.debugSSOProviderFailure("sso update provider validation failed", err, &provider)
		return nil, err
	}
	if err := normalizeSSOProviderForWrite(&provider); err != nil {
		s.debugSSOProviderFailure("sso update provider validation failed", err, &provider)
		return nil, err
	}
	if err := s.repo.UpdateProvider(ctx, &provider); err != nil {
		s.debugSSOProviderFailure("sso update provider query failed", err, &provider)
		return nil, err
	}
	updated, err := s.repo.GetProvider(ctx, provider.ID)
	if err != nil {
		s.debugSSOProviderFailure("sso update provider reload failed", err, &provider)
		return nil, err
	}
	return updated, nil
}

func (s *SSOService) DeleteProvider(ctx context.Context, providerID uuid.UUID) error {
	if providerID == uuid.Nil {
		err := fmt.Errorf("sso provider ID is required")
		s.debugSSOFailure("sso delete provider validation failed", err)
		return err
	}
	if err := s.repo.DeleteProvider(ctx, providerID); err != nil {
		s.debugSSOFailure("sso delete provider query failed", err, ssoUUIDLogAttr("provider_id", providerID))
		return err
	}
	return nil
}

func (s *SSOService) ListMappings(ctx context.Context, providerID uuid.UUID) ([]*domain.SSOGroupMapping, error) {
	if providerID == uuid.Nil {
		err := fmt.Errorf("sso provider ID is required")
		s.debugSSOFailure("sso list mappings validation failed", err)
		return nil, err
	}
	mappings, err := s.repo.ListMappings(ctx, providerID)
	if err != nil {
		s.debugSSOFailure("sso list mappings query failed", err, ssoUUIDLogAttr("provider_id", providerID))
		return nil, err
	}
	return mappings, nil
}

func (s *SSOService) CreateMapping(ctx context.Context, mapping domain.SSOGroupMapping) (*domain.SSOGroupMapping, error) {
	if err := normalizeSSOGroupMapping(&mapping); err != nil {
		s.debugSSOMappingFailure("sso create mapping validation failed", err, &mapping)
		return nil, err
	}
	if err := s.repo.CreateMapping(ctx, &mapping); err != nil {
		s.debugSSOMappingFailure("sso create mapping query failed", err, &mapping)
		return nil, err
	}
	mappings, err := s.repo.ListMappings(ctx, mapping.ProviderID)
	if err != nil {
		s.debugSSOMappingFailure("sso create mapping reload failed", err, &mapping)
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
		err := fmt.Errorf("sso group mapping ID is required")
		s.debugSSOMappingFailure("sso update mapping validation failed", err, &mapping)
		return nil, err
	}
	if err := normalizeSSOGroupMapping(&mapping); err != nil {
		s.debugSSOMappingFailure("sso update mapping validation failed", err, &mapping)
		return nil, err
	}
	if err := s.repo.UpdateMapping(ctx, &mapping); err != nil {
		s.debugSSOMappingFailure("sso update mapping query failed", err, &mapping)
		return nil, err
	}
	mappings, err := s.repo.ListMappings(ctx, mapping.ProviderID)
	if err != nil {
		s.debugSSOMappingFailure("sso update mapping reload failed", err, &mapping)
		return nil, err
	}
	for _, item := range mappings {
		if item.ID == mapping.ID {
			return item, nil
		}
	}
	return &mapping, nil
}

func (s *SSOService) DeleteMapping(ctx context.Context, providerID, mappingID uuid.UUID) error {
	if providerID == uuid.Nil {
		err := fmt.Errorf("sso provider ID is required")
		s.debugSSOFailure("sso delete mapping validation failed", err)
		return err
	}
	if mappingID == uuid.Nil {
		err := fmt.Errorf("sso group mapping ID is required")
		s.debugSSOFailure("sso delete mapping validation failed", err)
		return err
	}
	if err := s.repo.DeleteMapping(ctx, providerID, mappingID); err != nil {
		s.debugSSOFailure("sso delete mapping query failed", err, ssoUUIDLogAttr("provider_id", providerID), ssoUUIDLogAttr("mapping_id", mappingID))
		return err
	}
	return nil
}

func (s *SSOService) BeginLogin(ctx context.Context, providerID uuid.UUID, callbackURL, redirectPath string) (*SSOLoginStart, error) {
	if s == nil {
		return nil, ErrSSOProviderDisabled
	}
	if s.repo == nil {
		s.debugSSOFailure("sso begin login repository missing", ErrSSOProviderDisabled, ssoUUIDLogAttr("provider_id", providerID))
		return nil, ErrSSOProviderDisabled
	}
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		s.debugSSOFailure("sso begin login runtime config failed", err, ssoUUIDLogAttr("provider_id", providerID))
		return nil, err
	}
	provider, err := s.repo.GetProvider(ctx, providerID)
	if err != nil {
		s.debugSSOFailure("sso begin login provider lookup failed", err, ssoUUIDLogAttr("provider_id", providerID))
		return nil, err
	}
	if provider == nil || !provider.Enabled || provider.RetiredAt != nil {
		s.debugSSOProviderFailure("sso begin login provider disabled", ErrSSOProviderDisabled, provider, ssoUUIDLogAttr("requested_provider_id", providerID))
		return nil, ErrSSOProviderDisabled
	}
	if err := s.repo.DeleteExpiredOAuthStates(ctx, s.now()); err != nil {
		s.debugSSOProviderFailure("sso begin login expired state cleanup failed", err, provider)
		return nil, err
	}

	providerCtx, cancel := s.providerContext(ctx, runtime)
	defer cancel()
	oidcProvider, oauthConfig, err := s.oauthConfig(providerCtx, *provider, callbackURL)
	if err != nil {
		s.debugSSOProviderFailure("sso begin login oidc config failed", err, provider, ssoHashLogAttr("callback_url", callbackURL))
		return nil, err
	}
	_ = oidcProvider

	stateToken, err := secureRandomToken(32)
	if err != nil {
		s.debugSSOProviderFailure("sso begin login state generation failed", err, provider)
		return nil, err
	}
	nonce, err := secureRandomToken(24)
	if err != nil {
		s.debugSSOProviderFailure("sso begin login nonce generation failed", err, provider)
		return nil, err
	}
	pkceVerifier, err := secureRandomToken(48)
	if err != nil {
		s.debugSSOProviderFailure("sso begin login pkce generation failed", err, provider)
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
		s.debugSSOProviderFailure("sso begin login state persist failed", err, provider, ssoHashLogAttr("redirect_path", state.RedirectPath))
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
	if s == nil {
		return nil, ErrSSOProviderDisabled
	}
	if s.repo == nil {
		s.debugSSOFailure("sso complete login repository missing", ErrSSOProviderDisabled, observability.Bool("state_present", strings.TrimSpace(stateToken) != ""), observability.Bool("code_present", strings.TrimSpace(code) != ""))
		return nil, ErrSSOProviderDisabled
	}
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		s.debugSSOFailure("sso complete login runtime config failed", err)
		return nil, err
	}
	state, err := s.repo.ConsumeOAuthState(ctx, HashSSOToken(stateToken))
	if err != nil {
		s.debugSSOFailure("sso complete login state consume failed", err, observability.Bool("state_present", strings.TrimSpace(stateToken) != ""), observability.Bool("code_present", strings.TrimSpace(code) != ""))
		return nil, err
	}
	if state == nil {
		s.debugSSOFailure("sso complete login state missing", ErrSSOSessionInvalid, observability.Bool("state_present", strings.TrimSpace(stateToken) != ""), observability.Bool("code_present", strings.TrimSpace(code) != ""))
		return nil, ErrSSOSessionInvalid
	}

	provider, err := s.repo.GetProvider(ctx, state.ProviderID)
	if err != nil {
		s.debugSSOFailure("sso complete login provider lookup failed", err, ssoUUIDLogAttr("provider_id", state.ProviderID))
		return nil, err
	}
	if provider == nil || !provider.Enabled || provider.RetiredAt != nil {
		s.debugSSOProviderFailure("sso complete login provider disabled", ErrSSOProviderDisabled, provider, ssoUUIDLogAttr("requested_provider_id", state.ProviderID))
		return nil, ErrSSOProviderDisabled
	}

	providerCtx, cancel := s.providerContext(ctx, runtime)
	defer cancel()
	oidcProvider, oauthConfig, err := s.oauthConfig(providerCtx, *provider, callbackURL)
	if err != nil {
		s.debugSSOProviderFailure("sso complete login oidc config failed", err, provider, ssoHashLogAttr("callback_url", callbackURL))
		return nil, err
	}
	token, err := oauthConfig.Exchange(providerCtx, code, oauth2.SetAuthURLParam("code_verifier", state.PKCEVerifier))
	if err != nil {
		wrapped := fmt.Errorf("sso token exchange failed: %w", err)
		s.debugSSOProviderFailure("sso complete login token exchange failed", wrapped, provider, observability.Bool("code_present", strings.TrimSpace(code) != ""), ssoHashLogAttr("callback_url", callbackURL))
		return nil, wrapped
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || strings.TrimSpace(rawIDToken) == "" {
		err := fmt.Errorf("sso token exchange did not return id_token")
		s.debugSSOProviderFailure("sso complete login id token missing", err, provider)
		return nil, err
	}
	idToken, err := oidcProvider.Verifier(&oidc.Config{ClientID: provider.ClientID}).Verify(providerCtx, rawIDToken)
	if err != nil {
		wrapped := fmt.Errorf("sso id_token verification failed: %w", err)
		s.debugSSOProviderFailure("sso complete login id token verification failed", wrapped, provider)
		return nil, wrapped
	}

	claims, err := readOIDCClaims(providerCtx, oidcProvider, token, idToken, *provider)
	if err != nil {
		s.debugSSOProviderFailure("sso complete login claim read failed", err, provider)
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(state.Nonce)) != 1 {
		err := fmt.Errorf("sso nonce mismatch")
		s.debugSSOLoginFailure("sso complete login nonce mismatch", err, *provider, claims, observability.Bool("nonce_claim_present", strings.TrimSpace(claims.Nonce) != ""))
		return nil, err
	}
	if claims.Subject == "" {
		claims.Subject = idToken.Subject
	}
	s.debugSSOLoginClaims("sso login oidc claims read", *provider, claims)
	directoryAuthority, err := s.repo.DirectoryAuthorityActive(ctx, provider.ID)
	if err != nil {
		s.debugSSOLoginFailure("sso login directory authority lookup failed", err, *provider, claims)
		return nil, err
	}
	var (
		entitlements []domain.SSOGroupMapping
		groupSource  string
	)
	if !directoryAuthority {
		groupSource = ssoEntitlementSourceClaims
		if len(claims.Groups) == 0 {
			s.debugSSOLoginClaims("sso login groups missing from oidc claims", *provider, claims, observability.Bool("groups_endpoint_configured", strings.TrimSpace(provider.GroupsEndpoint) != ""))
			claims.Groups, err = s.resolveGroups(providerCtx, runtime, *provider, claims.Subject, token.AccessToken)
			if err != nil {
				s.debugSSOLoginFailure("sso login group resolution failed", err, *provider, claims)
				if errors.Is(err, ErrSSOGroupRefreshUnavailable) {
					return nil, NewSSOSetupError(SSOSetupGroupsMissing, ErrSSOAccessDenied)
				}
				return nil, NewSSOSetupError(SSOSetupGroupLookupFailed, ErrSSOAccessDenied)
			}
			claims.Groups = dedupeStrings(claims.Groups)
			groupSource = ssoEntitlementSourceEndpoint
			s.debugSSOLoginClaims("sso login groups resolved from endpoint", *provider, claims)
		}

		mappings, err := s.repo.ListMappingsForGroups(ctx, provider.ID, claims.Groups)
		if err != nil {
			s.debugSSOLoginFailure("sso login mapping lookup failed", err, *provider, claims)
			return nil, err
		}
		s.debugSSOLoginClaims("sso login mapping lookup complete", *provider, claims, observability.Int("mapping_count", len(mappings)))
		entitlements, err = s.entitlementsFromMappings(provider.ID, claims.Subject, mappings)
		if err != nil {
			s.debugSSOLoginFailure("sso login entitlement denied", err, *provider, claims, observability.Int("mapping_count", len(mappings)))
			if cacheErr := s.storeEntitlementCacheWithTTL(ctx, runtime.EntitlementCacheTTL, provider.ID, claims.Subject, claims.Groups, "denied", ""); cacheErr != nil {
				s.debugSSOLoginFailure("sso login denied entitlement cache store failed", cacheErr, *provider, claims, observability.Int("mapping_count", len(mappings)))
			}
			setupCode := SSOSetupMappingMissing
			if len(mappings) > 0 {
				setupCode = SSOSetupEntitlementEmpty
			}
			return nil, NewSSOSetupError(setupCode, ErrSSOAccessDenied)
		}
	}
	if !directoryAuthority {
		s.debugSSOLoginClaims("sso login entitlements resolved", *provider, claims, observability.Int("entitlement_count", len(entitlements)))
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
	if err := s.repo.UpsertIdentity(ctx, identity); err != nil {
		if errors.Is(err, repository.ErrDirectoryIdentityNotProvisioned) {
			return nil, NewSSOSetupError(SSOSetupEntitlementEmpty, ErrSSOAccessDenied)
		}
		s.debugSSOLoginFailure("sso login identity upsert failed", err, *provider, claims)
		return nil, err
	}
	var teams []domain.SSOTeamProfile
	if directoryAuthority {
		teams, err = s.directoryEntitledTeams(ctx, provider.ID, identity.ID)
		if err != nil {
			s.debugSSOLoginFailure("sso login directory team entitlement lookup failed", err, *provider, claims, ssoUUIDLogAttr("identity_id", identity.ID))
			return nil, err
		}
	} else {
		activeByProfileID := make(map[uuid.UUID]struct{}, len(entitlements))
		for _, entitlement := range entitlements {
			name := ssoProfileName(claims.Email, claims.DisplayName, identity.ID)
			profile, err := s.repo.UpsertTeamProfileForMapping(ctx, *identity, entitlement, name)
			if err != nil {
				s.debugSSOLoginFailure("sso login team profile upsert failed", err, *provider, claims, ssoUUIDLogAttr("team_id", entitlement.TeamID), ssoHashLogAttr("group_id", entitlement.GroupID), observability.String("role", entitlement.Role))
				if errors.Is(err, repository.ErrTeamInactive) {
					continue
				}
				return nil, err
			}
			activeByProfileID[profile.ID] = struct{}{}
		}
		if len(activeByProfileID) == 0 {
			if cacheErr := s.storeEntitlementCacheWithTTL(ctx, runtime.EntitlementCacheTTL, provider.ID, claims.Subject, claims.Groups, "denied", ""); cacheErr != nil {
				s.debugSSOLoginFailure("sso login denied entitlement cache store failed", cacheErr, *provider, claims, observability.Int("entitlement_count", len(entitlements)))
			}
			return nil, NewSSOSetupError(SSOSetupEntitlementEmpty, ErrSSOAccessDenied)
		}
		cacheTTL := ssoActiveEntitlementCacheTTL(runtime, groupSource)
		cacheMessage := ssoEntitlementCacheMessage(groupSource)
		if err := s.storeEntitlementCacheWithTTL(ctx, cacheTTL, provider.ID, claims.Subject, claims.Groups, "active", cacheMessage); err != nil {
			s.debugSSOLoginFailure("sso login entitlement cache store failed", err, *provider, claims)
			return nil, err
		}
		teams, err = s.currentEntitledTeams(ctx, identity.ID, activeByProfileID)
		if err != nil {
			s.debugSSOLoginFailure("sso login current entitled teams failed", err, *provider, claims, ssoUUIDLogAttr("identity_id", identity.ID))
			return nil, err
		}
	}
	if len(teams) == 0 {
		s.debugSSOLoginFailure("sso login entitled teams empty", ErrSSOAccessDenied, *provider, claims, observability.Int("active_profile_count", len(teams)))
		return nil, NewSSOSetupError(SSOSetupEntitlementEmpty, ErrSSOAccessDenied)
	}
	selected := teams[0]
	sessionToken, err := secureRandomToken(32)
	if err != nil {
		s.debugSSOLoginFailure("sso login session token generation failed", err, *provider, claims)
		return nil, err
	}
	csrfToken, err := secureRandomToken(32)
	if err != nil {
		s.debugSSOLoginFailure("sso login csrf token generation failed", err, *provider, claims)
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
		s.debugSSOLoginFailure("sso login session persist failed", err, *provider, claims, append(ssoSessionLogAttrs(&session), ssoUUIDLogAttr("selected_profile_id", selected.Profile.ID))...)
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
		s.debugSSOFailure("sso authenticate session csrf invalid", ErrSSOCSRFInvalid, append(ssoSessionLogAttrs(session), observability.Bool("csrf_present", strings.TrimSpace(csrfToken) != ""))...)
		return nil, ErrSSOCSRFInvalid
	}
	key, err := s.repo.GetSSOProfileByID(ctx, session.TeamProfileID)
	if err != nil {
		s.debugSSOFailure("sso authenticate session profile lookup failed", err, ssoSessionLogAttrs(session)...)
		return nil, err
	}
	if key == nil {
		s.debugSSOFailure("sso authenticate session profile missing", ErrSSOSessionInvalid, ssoSessionLogAttrs(session)...)
		return nil, ErrSSOSessionInvalid
	}
	validated, err := s.ValidateAPIKeyPrincipal(ctx, key)
	if err != nil {
		s.debugSSOFailure("sso authenticate session entitlement validation failed", err, append(ssoSessionLogAttrs(session), ssoAPIKeyLogAttrs(key)...)...)
		return nil, err
	}
	return validated, nil
}

func (s *SSOService) CurrentSession(ctx context.Context, sessionToken string) (*SSOSessionInfo, error) {
	session, err := s.sessionFromToken(ctx, sessionToken)
	if err != nil {
		return nil, err
	}
	identity, err := s.repo.GetIdentity(ctx, session.IdentityID)
	if err != nil {
		s.debugSSOFailure("sso current session identity lookup failed", err, ssoSessionLogAttrs(session)...)
		return nil, err
	}
	if identity == nil {
		s.debugSSOFailure("sso current session identity missing", ErrSSOSessionInvalid, ssoSessionLogAttrs(session)...)
		return nil, ErrSSOSessionInvalid
	}
	allTeams, err := s.repo.ListTeamProfilesForIdentity(ctx, identity.ID)
	if err != nil {
		s.debugSSOFailure("sso current session team list failed", err, append(ssoSessionLogAttrs(session), ssoUUIDLogAttr("identity_id", identity.ID))...)
		return nil, err
	}
	if changed, err := s.reconcileCurrentSessionTeamProfiles(ctx, *identity, allTeams); err != nil {
		s.debugSSOFailure("sso current session team reconciliation failed", err, append(ssoSessionLogAttrs(session), ssoUUIDLogAttr("identity_id", identity.ID))...)
		return nil, err
	} else if changed {
		allTeams, err = s.repo.ListTeamProfilesForIdentity(ctx, identity.ID)
		if err != nil {
			s.debugSSOFailure("sso current session reconciled team list failed", err, append(ssoSessionLogAttrs(session), ssoUUIDLogAttr("identity_id", identity.ID))...)
			return nil, err
		}
	}
	teams, selected, err := s.validCurrentSessionTeams(ctx, session, allTeams)
	if err != nil {
		return nil, err
	}
	if changed, err := s.reconcileCurrentSessionTeamProfiles(ctx, *identity, allTeams); err != nil {
		s.debugSSOFailure("sso current session post-validation team reconciliation failed", err, append(ssoSessionLogAttrs(session), ssoUUIDLogAttr("identity_id", identity.ID))...)
		return nil, err
	} else if changed {
		allTeams, err = s.repo.ListTeamProfilesForIdentity(ctx, identity.ID)
		if err != nil {
			s.debugSSOFailure("sso current session post-validation team list failed", err, append(ssoSessionLogAttrs(session), ssoUUIDLogAttr("identity_id", identity.ID))...)
			return nil, err
		}
		teams, selected, err = s.validCurrentSessionTeams(ctx, session, allTeams)
		if err != nil {
			return nil, err
		}
	}
	if selected == nil {
		s.debugSSOFailure("sso current session selected profile denied", ErrSSOAccessDenied, append(ssoSessionLogAttrs(session), observability.Int("team_count", len(teams)), observability.Int("profile_count", len(allTeams)))...)
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
		s.debugSSOFailure("sso switch session team list failed", err, append(ssoSessionLogAttrs(session), ssoUUIDLogAttr("requested_team_profile_id", teamProfileID))...)
		return nil, err
	}
	for _, team := range teams {
		if team.Profile.ID != teamProfileID {
			continue
		}
		if _, err := s.ValidateAPIKeyPrincipal(ctx, &team.Profile); err != nil {
			s.debugSSOFailure("sso switch session team validation failed", err, append(ssoSessionLogAttrs(session), ssoAPIKeyLogAttrs(&team.Profile)...)...)
			return nil, err
		}
		if err := s.repo.UpdateSessionTeam(ctx, session.SessionHash, team.Profile.ID, team.Team.ID); err != nil {
			s.debugSSOFailure("sso switch session team update failed", err, append(ssoSessionLogAttrs(session), ssoUUIDLogAttr("requested_team_profile_id", teamProfileID), ssoUUIDLogAttr("team_id", team.Team.ID))...)
			return nil, err
		}
		return s.CurrentSession(ctx, sessionToken)
	}
	s.debugSSOFailure("sso switch session team denied", ErrSSOAccessDenied, append(ssoSessionLogAttrs(session), ssoUUIDLogAttr("requested_team_profile_id", teamProfileID), observability.Int("profile_count", len(teams)))...)
	return nil, ErrSSOAccessDenied
}

func (s *SSOService) Logout(ctx context.Context, sessionToken string) error {
	if s == nil || strings.TrimSpace(sessionToken) == "" {
		return nil
	}
	if err := s.repo.DeleteSession(ctx, HashSSOToken(sessionToken)); err != nil {
		s.debugSSOFailure("sso logout delete session failed", err, observability.Bool("session_present", true))
		return err
	}
	return nil
}

func (s *SSOService) oauthConfig(ctx context.Context, provider domain.SSOProvider, callbackURL string) (*oidc.Provider, *oauth2.Config, error) {
	oidcProvider, err := oidc.NewProvider(ctx, provider.IssuerURL)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to discover oidc provider: %w", err)
	}
	scopes := interactiveOAuthScopes(provider)
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
	if s == nil {
		return nil, ErrSSOSessionInvalid
	}
	if s.repo == nil {
		s.debugSSOFailure("sso session lookup repository missing", ErrSSOSessionInvalid, observability.Bool("session_present", strings.TrimSpace(sessionToken) != ""))
		return nil, ErrSSOSessionInvalid
	}
	if strings.TrimSpace(sessionToken) == "" {
		s.debugSSOFailure("sso session token missing", ErrSSOSessionInvalid, observability.Bool("session_present", false))
		return nil, ErrSSOSessionInvalid
	}
	session, err := s.repo.GetSession(ctx, HashSSOToken(sessionToken))
	if err != nil {
		s.debugSSOFailure("sso session lookup failed", err, observability.Bool("session_present", true))
		return nil, err
	}
	if session == nil {
		s.debugSSOFailure("sso session missing", ErrSSOSessionInvalid, observability.Bool("session_present", true))
		return nil, ErrSSOSessionInvalid
	}
	if session.ExpiresAt.Before(s.now()) {
		s.debugSSOFailure("sso session expired", ErrSSOSessionInvalid, ssoSessionLogAttrs(session)...)
		return nil, ErrSSOSessionInvalid
	}
	return session, nil
}

func (s *SSOService) entitlementsFromMappings(providerID uuid.UUID, subject string, mappings []*domain.SSOGroupMapping) ([]domain.SSOGroupMapping, error) {
	if len(mappings) == 0 {
		s.debugSSOFailure("sso entitlement mappings empty", ErrSSOAccessDenied, ssoUUIDLogAttr("provider_id", providerID), ssoHashLogAttr("subject", subject), observability.Int("mapping_count", 0))
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
		s.debugSSOFailure("sso entitlement mappings inactive or mismatched", ErrSSOAccessDenied, ssoUUIDLogAttr("provider_id", providerID), ssoHashLogAttr("subject", subject), observability.Int("mapping_count", len(mappings)))
		return nil, ErrSSOAccessDenied
	}
	result := make([]domain.SSOGroupMapping, 0, len(byTeam))
	for _, mapping := range byTeam {
		if mapping.Role == APIKeyRoleManager {
			mapping.Scopes = managerAPIKeyScopes(mapping.Scopes)
		}
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
		s.debugSSOFailure("sso current entitled teams list failed", err, ssoUUIDLogAttr("identity_id", identityID), observability.Int("allowed_profile_count", len(allowed)))
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

func (s *SSOService) directoryEntitledTeams(ctx context.Context, providerID, identityID uuid.UUID) ([]domain.SSOTeamProfile, error) {
	profiles, err := s.repo.ListTeamProfilesForIdentity(ctx, identityID)
	if err != nil {
		return nil, err
	}
	teams := make([]domain.SSOTeamProfile, 0, len(profiles))
	for _, profile := range profiles {
		if profile == nil {
			continue
		}
		entitled, err := s.repo.DirectoryTeamProfileEntitled(ctx, profile.Profile.ID, providerID, identityID, profile.Profile.GetTeamID(), profile.Profile.SSOGroupID)
		if err != nil {
			return nil, err
		}
		if entitled {
			teams = append(teams, *profile)
		}
	}
	return teams, nil
}

func (s *SSOService) storeEntitlementCache(ctx context.Context, providerID uuid.UUID, subject string, groups []string, status, message string) error {
	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		s.debugSSOFailure("sso entitlement cache runtime config failed", err, ssoUUIDLogAttr("provider_id", providerID), ssoHashLogAttr("subject", subject), observability.String("cache_status", status))
		return err
	}
	return s.storeEntitlementCacheWithTTL(ctx, runtime.EntitlementCacheTTL, providerID, subject, groups, status, message)
}

func (s *SSOService) storeEntitlementCacheWithTTL(ctx context.Context, ttl time.Duration, providerID uuid.UUID, subject string, groups []string, status, message string) error {
	now := s.now()
	cache := domain.SSOEntitlementCache{
		ProviderID: providerID,
		Subject:    subject,
		Groups:     dedupeStrings(groups),
		Status:     status,
		CheckedAt:  now,
		ExpiresAt:  now.Add(ttl),
		Error:      message,
	}
	if err := s.repo.SetEntitlementCache(ctx, cache); err != nil {
		s.debugSSOFailure("sso entitlement cache store failed", err, ssoUUIDLogAttr("provider_id", providerID), ssoHashLogAttr("subject", subject), observability.String("cache_status", status), observability.Int("group_count", len(cache.Groups)), ssoHashesLogAttr("groups", cache.Groups))
		return err
	}
	return nil
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
	cfg.SCIMPublicBaseURL = strings.TrimRight(strings.TrimSpace(cfg.SCIMPublicBaseURL), "/")
	cfg.ControlPublicBaseURL = strings.TrimRight(strings.TrimSpace(cfg.ControlPublicBaseURL), "/")
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
