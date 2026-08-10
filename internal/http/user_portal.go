package http

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/graphview"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

// UserPortalDeps holds the dependencies for the API-key user portal.
type UserPortalDeps struct {
	APIKeyRepo      repository.APIKeyRepository
	ProfileSvc      handler.ProfileServiceInterface
	APIKeySvc       handler.APIKeyServiceInterface
	RateLimitSvc    service.RateLimitServiceInterface
	UsageMetrics    service.UsageMetricsRecorder
	Telemetry       service.TelemetryReader
	GraphView       graphview.Service
	RecallSvc       memoryservice.RecallService
	DreamSvc        dreamservice.Service
	AuditSvc        service.AuditService
	SecuritySvc     httpmw.SecurityBanService
	SSOService      *service.SSOService
	PortalSession   service.UserPortalSessionManager
	AppConfig       service.AppConfigService
	Config          config.ConfigProvider
	UserStaticDir   string
	ExtraMiddleware []echo.MiddlewareFunc
}

type userPortalHandler struct {
	profiles  handler.ProfileServiceInterface
	keys      handler.APIKeyServiceInterface
	telemetry service.TelemetryReader
	graph     graphview.Service
	recall    *handler.RecallHandler
	dreams    *handler.DreamHandler
	audit     *handler.AuditHandler
	sso       *service.SSOService
	portal    service.UserPortalSessionManager
	appConfig service.AppConfigService
}

type userPortalSessionResponse struct {
	Team                 userPortalTeamResponse         `json:"team"`
	Key                  userPortalKeyResponse          `json:"key"`
	Teams                []userPortalTeamOptionResponse `json:"teams,omitempty"`
	AuthMethod           string                         `json:"auth_method"`
	CanRotate            bool                           `json:"can_rotate"`
	CanManageTeam        bool                           `json:"can_manage_team"`
	PersonalKey          *userPortalKeyResponse         `json:"personal_key"`
	CanCreatePersonalKey bool                           `json:"can_create_personal_key"`
	CanRotatePersonalKey bool                           `json:"can_rotate_personal_key"`
	PersonalKeyMaxScopes []string                       `json:"personal_key_max_scopes,omitempty"`
}

type userPortalTeamOptionResponse struct {
	Team          userPortalTeamResponse `json:"team"`
	Key           userPortalKeyResponse  `json:"key"`
	CanRotate     bool                   `json:"can_rotate"`
	CanManageTeam bool                   `json:"can_manage_team"`
}

type userPortalTeamResponse struct {
	ID                uuid.UUID                     `json:"id"`
	Name              string                        `json:"name"`
	Description       string                        `json:"description"`
	Config            map[string]any                `json:"config"`
	DreamingEffective *dreamservice.EffectiveConfig `json:"dreaming_effective,omitempty"`
	CreatedAt         string                        `json:"created_at"`
	UpdatedAt         string                        `json:"updated_at"`
}

type userPortalKeyResponse struct {
	ID         uuid.UUID `json:"id"`
	TeamID     uuid.UUID `json:"team_id"`
	Name       string    `json:"name"`
	KeySuffix  string    `json:"key_suffix"`
	Scopes     []string  `json:"scopes"`
	Role       string    `json:"role"`
	RateLimit  int       `json:"rate_limit"`
	LastUsedAt *string   `json:"last_used_at"`
	ExpiresAt  *string   `json:"expires_at"`
	CreatedAt  string    `json:"created_at"`
}

type userPortalRotateResponse struct {
	APIKey string                `json:"api_key"`
	Key    userPortalKeyResponse `json:"key"`
}

type userPortalSSOProviderResponse struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Kind string    `json:"kind"`
}

type userPortalSwitchSSOTeamRequest struct {
	ProfileID string `json:"profile_id"`
}

type userPortalCreateSSOKeyRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	RateLimit int      `json:"rate_limit"`
	ExpiresAt *string  `json:"expires_at"`
}

func (h *userPortalHandler) session(c echo.Context) error {
	session, err := h.currentSession(c)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": session})
}

func (h *userPortalHandler) telemetrySnapshot(c echo.Context) error {
	if h.telemetry == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "telemetry unavailable")
	}
	ctx := c.Request().Context()
	principal := httpmw.GetPrincipal(ctx)
	if principal == nil {
		return httperr.New(httperr.FORBIDDEN, "authentication required")
	}

	filter, err := userPortalTelemetryFilter(principal, c.QueryParam("window"), c.QueryParam("scope"))
	if err != nil {
		return err
	}

	snapshot, err := h.telemetry.Snapshot(ctx, filter)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": snapshot})
}

func (h *userPortalHandler) graphSnapshot(c echo.Context) error {
	c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
	if h.graph == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "graph view unavailable")
	}
	principal := httpmw.GetPrincipal(c.Request().Context())
	if principal == nil {
		return httperr.New(httperr.FORBIDDEN, "authentication required")
	}
	teamID := principal.GetTeamID()
	if teamID == uuid.Nil {
		return httperr.New(httperr.FORBIDDEN, "authenticated key is not team bound")
	}

	limit, err := userPortalOptionalIntQuery(c, "limit")
	if err != nil {
		return err
	}
	depth, err := userPortalOptionalIntQuery(c, "depth")
	if err != nil {
		return err
	}
	snapshot, err := h.graph.Graph(c.Request().Context(), teamID.String(), graphview.Query{
		Scope:      c.QueryParam("scope"),
		Query:      c.QueryParam("q"),
		Types:      userPortalGraphTypes(c.QueryParam("types")),
		AnchorType: c.QueryParam("anchor_type"),
		AnchorID:   c.QueryParam("anchor_id"),
		Depth:      depth,
		Limit:      limit,
	})
	if err != nil {
		if errors.Is(err, graphview.ErrMissingAnchor) || errors.Is(err, graphview.ErrInvalidAnchorType) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": snapshot})
}

func (h *userPortalHandler) graphNodeDetail(c echo.Context) error {
	c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
	if h.graph == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "graph view unavailable")
	}
	principal := httpmw.GetPrincipal(c.Request().Context())
	if principal == nil {
		return httperr.New(httperr.FORBIDDEN, "authentication required")
	}
	teamID := principal.GetTeamID()
	if teamID == uuid.Nil {
		return httperr.New(httperr.FORBIDDEN, "authenticated key is not team bound")
	}

	node, err := h.graph.NodeDetail(c.Request().Context(), teamID.String(), c.QueryParam("type"), c.QueryParam("id"))
	if err != nil {
		if errors.Is(err, graphview.ErrMissingNode) || errors.Is(err, graphview.ErrInvalidNodeType) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		if errors.Is(err, graphview.ErrNodeNotFound) {
			return httperr.New(httperr.NOT_FOUND, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": node})
}

func userPortalOptionalIntQuery(c echo.Context, name string) (int, error) {
	raw := strings.TrimSpace(c.QueryParam(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, httperr.New(httperr.VALIDATION_ERROR, name+" must be a non-negative integer")
	}
	return value, nil
}

func userPortalGraphTypes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
}

func userPortalTelemetryFilter(principal *httpmw.Principal, window, requestedScope string) (service.TelemetryFilter, error) {
	teamID := principal.GetTeamID()
	if teamID == uuid.Nil {
		return service.TelemetryFilter{}, httperr.New(httperr.FORBIDDEN, "authenticated key is not team bound")
	}

	scope := "self"
	var profileID *uuid.UUID
	if principal.GetRole() == service.APIKeyRoleManager {
		scope = "team"
	} else {
		keyID := principal.GetKeyID()
		if keyID == uuid.Nil {
			return service.TelemetryFilter{}, httperr.New(httperr.FORBIDDEN, "authenticated key is not profile bound")
		}
		profileID = &keyID
	}

	if requested := strings.TrimSpace(requestedScope); requested != "" && requested != scope {
		return service.TelemetryFilter{}, httperr.New(httperr.FORBIDDEN, "user portal telemetry scope is determined by the authenticated key")
	}

	return service.TelemetryFilter{
		Window:    strings.TrimSpace(window),
		Scope:     scope,
		TeamID:    &teamID,
		ProfileID: profileID,
		Audience:  service.TelemetryAudienceUser,
	}, nil
}

func (h *userPortalHandler) rotateCurrentKey(c echo.Context) error {
	if err := rejectEditableRotateBody(c); err != nil {
		return err
	}

	ctx := c.Request().Context()
	principal := httpmw.GetPrincipal(ctx)
	if principal == nil {
		return httperr.New(httperr.FORBIDDEN, "authentication required")
	}
	if principal.AuthMethod == "sso_session" {
		return httperr.New(httperr.FORBIDDEN, "sso sessions cannot rotate api keys")
	}

	teamID := principal.GetTeamID()
	keyID := principal.GetKeyID()
	current, err := h.keys.GetByIDForProfile(ctx, teamID, keyID)
	if err != nil {
		return err
	}
	if current == nil {
		return httperr.New(httperr.NOT_FOUND, "key not found")
	}

	actorKeyID := keyID.String()
	rotated, rawKey, err := h.keys.RotateForProfile(ctx, teamID, keyID, service.CreateAPIKeyRequest{
		Name:      current.GetProfileName(),
		RateLimit: current.RateLimit,
		ExpiresAt: current.ExpiresAt,
	}, &actorKeyID, principal.Role, c.RealIP(), httpmw.GetCorrelationID(ctx))
	if err != nil {
		return err
	}

	return c.JSON(nethttp.StatusOK, map[string]any{"data": userPortalRotateResponse{
		APIKey: rawKey,
		Key:    toUserPortalKey(rotated),
	}})
}

func (h *userPortalHandler) currentSession(c echo.Context) (userPortalSessionResponse, error) {
	ctx := c.Request().Context()
	principal := httpmw.GetPrincipal(ctx)
	if principal == nil {
		return userPortalSessionResponse{}, httperr.New(httperr.AUTH_MISSING, "authentication required")
	}
	if principal.AuthMethod == "sso_session" {
		return h.currentSSOSession(c)
	}

	if h.profiles == nil {
		return userPortalSessionResponse{}, httperr.New(httperr.SERVICE_UNAVAILABLE, "profile service unavailable")
	}
	if h.keys == nil {
		return userPortalSessionResponse{}, httperr.New(httperr.SERVICE_UNAVAILABLE, "api key service unavailable")
	}

	teamID := principal.GetTeamID()
	keyID := principal.GetKeyID()
	team, err := h.profiles.Get(ctx, teamID)
	if err != nil {
		return userPortalSessionResponse{}, err
	}
	if team == nil {
		return userPortalSessionResponse{}, httperr.New(httperr.NOT_FOUND, "team not found")
	}
	key, err := h.keys.GetByIDForProfile(ctx, teamID, keyID)
	if err != nil {
		return userPortalSessionResponse{}, err
	}
	if key == nil {
		return userPortalSessionResponse{}, httperr.New(httperr.NOT_FOUND, "key not found")
	}

	teamResponse, err := h.toUserPortalTeam(ctx, team)
	if err != nil {
		return userPortalSessionResponse{}, err
	}
	authMethod := principal.AuthMethod
	if authMethod == "" {
		authMethod = "api_key"
	}
	return userPortalSessionResponse{
		Team:          teamResponse,
		Key:           toUserPortalKey(key),
		AuthMethod:    authMethod,
		CanRotate:     userPortalHasScope(principal.Scopes, service.APIKeyScopeWrite),
		CanManageTeam: principal.Role == service.APIKeyRoleManager,
	}, nil
}

func (h *userPortalHandler) currentSSOSession(c echo.Context) (userPortalSessionResponse, error) {
	if h.sso == nil {
		return userPortalSessionResponse{}, httperr.New(httperr.AUTH_MISSING, "authentication required")
	}
	ctx := c.Request().Context()
	token, err := ssoSessionTokenFromRequest(c)
	if err != nil {
		return userPortalSessionResponse{}, err
	}
	info, err := h.sso.CurrentSession(ctx, token)
	if err != nil {
		return userPortalSessionResponse{}, userPortalSSOError(err)
	}
	teams := make([]userPortalTeamOptionResponse, 0, len(info.Teams))
	for _, team := range info.Teams {
		option, err := h.toUserPortalTeamOption(ctx, team)
		if err != nil {
			return userPortalSessionResponse{}, err
		}
		teams = append(teams, option)
	}
	selected, err := h.toUserPortalTeamOption(ctx, info.Selected)
	if err != nil {
		return userPortalSessionResponse{}, err
	}
	response := userPortalSessionResponse{
		Team:          selected.Team,
		Key:           selected.Key,
		Teams:         teams,
		AuthMethod:    "sso",
		CanRotate:     false,
		CanManageTeam: selected.CanManageTeam,
	}
	if !selected.CanManageTeam && h.keys != nil {
		personalKey, err := h.ssoOwnedKey(ctx, info.Selected.Team.ID, info.Identity.ID)
		if err != nil {
			return userPortalSessionResponse{}, err
		}
		response.CanCreatePersonalKey = personalKey == nil
		response.PersonalKeyMaxScopes = append([]string{}, info.Selected.Profile.Scopes...)
		if personalKey != nil {
			keyResponse := toUserPortalKey(personalKey)
			response.PersonalKey = &keyResponse
			response.CanRotatePersonalKey = userPortalHasScope(personalKey.Scopes, service.APIKeyScopeWrite) &&
				userPortalHasScope(info.Selected.Profile.Scopes, service.APIKeyScopeWrite)
		}
	}
	return response, nil
}

func (h *userPortalHandler) ssoProviders(c echo.Context) error {
	if h.sso == nil {
		return c.JSON(nethttp.StatusOK, map[string]any{"data": []userPortalSSOProviderResponse{}})
	}
	providers, err := h.sso.ListEnabledProviders(c.Request().Context())
	if err != nil {
		return err
	}
	items := make([]userPortalSSOProviderResponse, 0, len(providers))
	for _, provider := range providers {
		items = append(items, userPortalSSOProviderResponse{
			ID:   provider.ID,
			Name: provider.Name,
			Kind: string(provider.Kind),
		})
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": items})
}

func (h *userPortalHandler) startSSO(c echo.Context) error {
	if h.sso == nil {
		return httperr.New(httperr.NOT_FOUND, "sso is not configured")
	}
	providerID, err := uuid.Parse(c.Param("providerId"))
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid sso provider ID format")
	}
	callbackURL, err := h.ssoCallbackURL(c.Request().Context())
	if err != nil {
		return err
	}
	start, err := h.sso.BeginLogin(c.Request().Context(), providerID, callbackURL, c.QueryParam("redirect"))
	if err != nil {
		return userPortalSSOError(err)
	}
	return c.Redirect(nethttp.StatusFound, start.AuthURL)
}

func (h *userPortalHandler) completeSSO(c echo.Context) error {
	if h.sso == nil {
		return httperr.New(httperr.NOT_FOUND, "sso is not configured")
	}
	if errText := strings.TrimSpace(c.QueryParam("error")); errText != "" {
		return httperr.New(httperr.AUTH_INVALID, "sso login failed")
	}
	callbackURL, err := h.ssoCallbackURL(c.Request().Context())
	if err != nil {
		return err
	}
	result, err := h.sso.CompleteLogin(c.Request().Context(), c.QueryParam("state"), c.QueryParam("code"), callbackURL)
	if err != nil {
		return userPortalSSOError(err)
	}
	cookieSecure := h.sso.CookieSecure(c.Request().Context())
	setSSOCookie(c, service.SSOSessionCookieName, result.SessionToken, true, result.Session.ExpiresAt, cookieSecure)
	setSSOCookie(c, service.SSOCSRFCookieName, result.CSRFToken, false, result.Session.ExpiresAt, cookieSecure)
	clearUserPortalCookie(c, service.UserPortalSessionCookieName, true, cookieSecure)
	clearUserPortalCookie(c, service.UserPortalCSRFCookieName, false, cookieSecure)
	return c.Redirect(nethttp.StatusFound, result.RedirectPath)
}

func (h *userPortalHandler) logoutSSO(c echo.Context) error {
	if cookie, err := c.Request().Cookie(service.SSOSessionCookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		if err := validateSSOLogoutCSRF(c); err != nil {
			return err
		}
		if h.sso != nil {
			_ = h.sso.Logout(c.Request().Context(), cookie.Value)
		}
	}
	clearSSOCookie(c, service.SSOSessionCookieName)
	clearSSOCookie(c, service.SSOCSRFCookieName)
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]string{"status": "signed_out"}})
}

func (h *userPortalHandler) switchSSOTeam(c echo.Context) error {
	if h.sso == nil {
		return httperr.New(httperr.NOT_FOUND, "sso is not configured")
	}
	principal := httpmw.GetPrincipal(c.Request().Context())
	if principal == nil {
		return httperr.New(httperr.FORBIDDEN, "authentication required")
	}
	if principal.AuthMethod != "sso_session" {
		return httperr.New(httperr.FORBIDDEN, "sso session required")
	}
	token, err := ssoSessionTokenFromRequest(c)
	if err != nil {
		return err
	}
	var body userPortalSwitchSSOTeamRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	profileID, err := uuid.Parse(strings.TrimSpace(body.ProfileID))
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid SSO profile ID format")
	}
	if _, err := h.sso.SwitchSessionTeam(c.Request().Context(), token, profileID); err != nil {
		return userPortalSSOError(err)
	}
	session, err := h.currentSSOSession(c)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": session})
}

func (h *userPortalHandler) createSSOKey(c echo.Context) error {
	info, principal, err := h.ssoRequestSession(c)
	if err != nil {
		return err
	}
	if info.Selected.Profile.GetRole() == service.APIKeyRoleManager {
		return httperr.New(httperr.FORBIDDEN, "sso managers should create api keys from the team section")
	}
	if h.keys == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "api key service unavailable")
	}
	existing, err := h.keys.GetSSOOwnedKey(c.Request().Context(), info.Selected.Team.ID, info.Identity.ID)
	if err != nil {
		return err
	}
	if existing != nil {
		return httperr.New(httperr.CONFLICT, "sso-owned api key already exists for this team")
	}

	var body userPortalCreateSSOKeyRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	req, err := userPortalSSOCreateKeyRequest(body, info.Identity, info.Selected.Profile.Scopes)
	if err != nil {
		return err
	}
	req.SSOOwnerIdentityID = &info.Identity.ID

	actorKeyID := principal.KeyID.String()
	key, rawKey, err := h.keys.CreateStandardKey(
		c.Request().Context(),
		info.Selected.Team.ID,
		req,
		&actorKeyID,
		principal.Role,
		c.RealIP(),
		httpmw.GetCorrelationID(c.Request().Context()),
	)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusCreated, map[string]any{"data": userPortalRotateResponse{
		APIKey: rawKey,
		Key:    toUserPortalKey(key),
	}})
}

func (h *userPortalHandler) rotateSSOKey(c echo.Context) error {
	if err := rejectEditableRotateBody(c); err != nil {
		return err
	}
	info, principal, err := h.ssoRequestSession(c)
	if err != nil {
		return err
	}
	if info.Selected.Profile.GetRole() == service.APIKeyRoleManager {
		return httperr.New(httperr.FORBIDDEN, "sso managers should rotate api keys from the team section")
	}
	if h.keys == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "api key service unavailable")
	}
	personalKey, err := h.keys.GetSSOOwnedKey(c.Request().Context(), info.Selected.Team.ID, info.Identity.ID)
	if err != nil {
		return err
	}
	if personalKey == nil {
		return httperr.New(httperr.NOT_FOUND, "sso-owned api key not found")
	}
	if !userPortalHasScope(personalKey.Scopes, service.APIKeyScopeWrite) ||
		!userPortalHasScope(info.Selected.Profile.Scopes, service.APIKeyScopeWrite) {
		return httperr.New(httperr.FORBIDDEN, "sso-owned api key cannot be rotated")
	}

	actorKeyID := principal.KeyID.String()
	rotated, rawKey, err := h.keys.RotateForProfile(c.Request().Context(), info.Selected.Team.ID, personalKey.ID, service.CreateAPIKeyRequest{
		Name:      personalKey.GetProfileName(),
		RateLimit: personalKey.RateLimit,
		ExpiresAt: personalKey.ExpiresAt,
	}, &actorKeyID, principal.Role, c.RealIP(), httpmw.GetCorrelationID(c.Request().Context()))
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": userPortalRotateResponse{
		APIKey: rawKey,
		Key:    toUserPortalKey(rotated),
	}})
}

func (h *userPortalHandler) ssoRequestSession(c echo.Context) (*service.SSOSessionInfo, *httpmw.Principal, error) {
	if h.sso == nil {
		return nil, nil, httperr.New(httperr.NOT_FOUND, "sso is not configured")
	}
	principal := httpmw.GetPrincipal(c.Request().Context())
	if principal == nil {
		return nil, nil, httperr.New(httperr.FORBIDDEN, "authentication required")
	}
	if principal.AuthMethod != "sso_session" {
		return nil, nil, httperr.New(httperr.FORBIDDEN, "sso session required")
	}
	token, err := ssoSessionTokenFromRequest(c)
	if err != nil {
		return nil, nil, err
	}
	info, err := h.sso.CurrentSession(c.Request().Context(), token)
	if err != nil {
		return nil, nil, userPortalSSOError(err)
	}
	return info, principal, nil
}

func (h *userPortalHandler) ssoOwnedKey(ctx context.Context, teamID, identityID uuid.UUID) (*domain.APIKey, error) {
	if h.keys == nil {
		return nil, nil
	}
	return h.keys.GetSSOOwnedKey(ctx, teamID, identityID)
}

func userPortalSSOCreateKeyRequest(body userPortalCreateSSOKeyRequest, identity domain.SSOIdentity, maxScopes []string) (service.CreateAPIKeyRequest, error) {
	if !userPortalHasScope(maxScopes, service.APIKeyScopeRead) {
		return service.CreateAPIKeyRequest{}, httperr.New(httperr.FORBIDDEN, "sso access denied")
	}
	scopes := append([]string{}, body.Scopes...)
	if len(scopes) == 0 {
		scopes = []string{service.APIKeyScopeRead}
		if userPortalHasScope(maxScopes, service.APIKeyScopeWrite) {
			scopes = service.StandardAPIKeyScopes()
		}
	}
	normalizedScopes, err := service.NormalizeAPIKeyScopes(scopes)
	if err != nil {
		return service.CreateAPIKeyRequest{}, err
	}
	for _, scope := range normalizedScopes {
		if !userPortalHasScope(maxScopes, scope) {
			return service.CreateAPIKeyRequest{}, httperr.New(httperr.FORBIDDEN, "cannot create api key above sso entitlement")
		}
	}
	if body.RateLimit <= 0 {
		return service.CreateAPIKeyRequest{}, httperr.New(httperr.VALIDATION_ERROR, "rate_limit must be greater than zero")
	}
	var expiresAt *time.Time
	if body.ExpiresAt != nil && strings.TrimSpace(*body.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*body.ExpiresAt))
		if err != nil {
			return service.CreateAPIKeyRequest{}, httperr.New(httperr.VALIDATION_ERROR, "expires_at must be an RFC3339 timestamp")
		}
		expiresAt = &parsed
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = ssoOwnedKeyDefaultName(identity)
	}
	return service.CreateAPIKeyRequest{
		Name:      name,
		RateLimit: body.RateLimit,
		ExpiresAt: expiresAt,
		Scopes:    normalizedScopes,
		Role:      service.APIKeyRoleMember,
	}, nil
}

func ssoOwnedKeyDefaultName(identity domain.SSOIdentity) string {
	if email := strings.TrimSpace(identity.Email); email != "" {
		return "SSO " + email
	}
	if displayName := strings.TrimSpace(identity.DisplayName); displayName != "" {
		return "SSO " + displayName
	}
	subject := strings.TrimSpace(identity.Subject)
	if len(subject) > 12 {
		subject = subject[:12]
	}
	if subject != "" {
		return "SSO " + subject
	}
	return "SSO API key"
}

func validateSSOLogoutCSRF(c echo.Context) error {
	headerToken := strings.TrimSpace(c.Request().Header.Get(service.SSOCSRFHeaderName))
	cookie, err := c.Request().Cookie(service.SSOCSRFCookieName)
	if err != nil || headerToken == "" || strings.TrimSpace(cookie.Value) == "" {
		return httperr.New(httperr.FORBIDDEN, "invalid sso csrf token")
	}
	if subtle.ConstantTimeCompare([]byte(headerToken), []byte(cookie.Value)) != 1 {
		return httperr.New(httperr.FORBIDDEN, "invalid sso csrf token")
	}
	return nil
}

func rejectEditableRotateBody(c echo.Context) error {
	if c.Request().Body == nil {
		return nil
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	if strings.TrimSpace(string(body)) == "" {
		return nil
	}

	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	if len(fields) > 0 {
		return httperr.New(httperr.VALIDATION_ERROR, "rotate request does not accept editable fields")
	}
	return nil
}

func (h *userPortalHandler) toUserPortalTeam(ctx context.Context, team *domain.Profile) (userPortalTeamResponse, error) {
	effective, err := effectiveDreamingConfig(ctx, h.appConfig, team.Config)
	if err != nil {
		return userPortalTeamResponse{}, err
	}
	return userPortalTeamResponse{
		ID:                team.ID,
		Name:              team.Name,
		Description:       team.Description,
		Config:            team.Config,
		DreamingEffective: effective,
		CreatedAt:         team.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         team.UpdatedAt.Format(time.RFC3339),
	}, nil
}

func toUserPortalKey(key *domain.APIKey) userPortalKeyResponse {
	return userPortalKeyResponse{
		ID:         key.ID,
		TeamID:     key.GetTeamID(),
		Name:       key.GetProfileName(),
		KeySuffix:  key.KeySuffix,
		Scopes:     append([]string{}, key.Scopes...),
		Role:       key.GetRole(),
		RateLimit:  key.RateLimit,
		LastUsedAt: controlTimePtr(key.LastUsedAt),
		ExpiresAt:  controlTimePtr(key.ExpiresAt),
		CreatedAt:  key.CreatedAt.Format(time.RFC3339),
	}
}

func (h *userPortalHandler) toUserPortalTeamOption(ctx context.Context, item domain.SSOTeamProfile) (userPortalTeamOptionResponse, error) {
	team, err := h.toUserPortalTeam(ctx, &item.Team)
	if err != nil {
		return userPortalTeamOptionResponse{}, err
	}
	return userPortalTeamOptionResponse{
		Team:          team,
		Key:           toUserPortalKey(&item.Profile),
		CanRotate:     false,
		CanManageTeam: item.Profile.GetRole() == service.APIKeyRoleManager,
	}, nil
}

func userPortalHasScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

func (h *userPortalHandler) ssoCallbackURL(ctx context.Context) (string, error) {
	if h.sso == nil {
		return "", httperr.New(httperr.SERVICE_UNAVAILABLE, "sso is not configured")
	}
	baseURL, err := h.sso.PublicBaseURL(ctx)
	if err != nil {
		return "", err
	}
	if baseURL != "" {
		return baseURL + "/ui/api/sso/callback", nil
	}
	return "", httperr.New(httperr.SERVICE_UNAVAILABLE, "sso public base url is not configured")
}

func publicSSORateLimitMiddleware(svc service.RateLimitServiceInterface, cfg config.ConfigProvider) echo.MiddlewareFunc {
	return publicIPRateLimitMiddleware("public-sso", svc, cfg)
}

func publicIPRateLimitMiddleware(subjectNamespace string, svc service.RateLimitServiceInterface, cfg config.ConfigProvider) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if svc == nil || cfg == nil {
				return next(c)
			}
			limit := cfg.GetRateLimitPerMinute()
			if limit <= 0 {
				return next(c)
			}
			routePath := c.Path()
			if routePath == "" {
				routePath = c.Request().URL.Path
			}
			subject := subjectNamespace + ":ip:" + c.RealIP()
			allowed, remaining, resetAt, err := svc.Check(c.Request().Context(), subject, routePath, limit)
			if err != nil {
				c.Logger().Error("public ip rate limit check failed")
				return next(c)
			}
			c.Response().Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
			c.Response().Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
			c.Response().Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))
			if !allowed {
				retryAfter := int(time.Until(resetAt).Seconds())
				if retryAfter < 0 {
					retryAfter = 0
				}
				c.Response().Header().Set("Retry-After", strconv.Itoa(retryAfter))
				return httperr.New(httperr.RATE_LIMITED, "rate limit exceeded")
			}
			return next(c)
		}
	}
}

func ssoSessionTokenFromRequest(c echo.Context) (string, error) {
	cookie, err := c.Request().Cookie(service.SSOSessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return "", httperr.New(httperr.AUTH_MISSING, "authentication required")
	}
	return cookie.Value, nil
}

func setSSOCookie(c echo.Context, name, value string, httpOnly bool, expires time.Time, secure bool) {
	c.SetCookie(&nethttp.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: httpOnly,
		Secure:   secure,
		SameSite: nethttp.SameSiteLaxMode,
	})
}

func clearSSOCookie(c echo.Context, name string) {
	c.SetCookie(&nethttp.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: name == service.SSOSessionCookieName,
		SameSite: nethttp.SameSiteLaxMode,
	})
}

func userPortalSSOError(err error) error {
	if message, ok := service.SSOSetupErrorMessage(err); ok {
		return httperr.New(httperr.FORBIDDEN, message)
	}
	switch {
	case err == nil:
		return nil
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

func registerUserPortalStatic(e *echo.Echo, staticDir string) {
	if strings.TrimSpace(staticDir) == "" {
		return
	}
	indexPath := userPortalIndexPath(staticDir)
	if indexPath == "" {
		return
	}

	serveIndex := func(c echo.Context) error {
		return c.File(indexPath)
	}
	e.GET("/ui", serveIndex)
	e.GET("/ui/", serveIndex)

	if assetsDir := filepath.Join(staticDir, "assets"); dirExists(assetsDir) {
		e.Static("/ui/assets", assetsDir)
	}

	e.GET("/ui/*", func(c echo.Context) error {
		if strings.HasPrefix(c.Request().URL.Path, "/ui/api/") {
			return httperr.New(httperr.NOT_FOUND, "not found")
		}
		return c.File(indexPath)
	})
}

func defaultUserPortalStaticDir() string {
	candidates := []string{
		filepath.Join("web", "user-dist"),
		filepath.Join("/app", "dense-mem", "web", "user-dist"),
	}
	for _, candidate := range candidates {
		if dirExists(candidate) {
			return candidate
		}
	}
	return ""
}

func userPortalIndexPath(staticDir string) string {
	for _, name := range []string{"index.html", "user.html"} {
		candidate := filepath.Join(staticDir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
