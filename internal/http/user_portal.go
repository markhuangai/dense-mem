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
	"github.com/markhuangai/dense-mem/internal/crypto"
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

// UserPortalDeps holds the dependencies for the user portal.
type UserPortalDeps struct {
	CredentialRepo     repository.CredentialRepository
	TeamSvc            handler.TeamServiceInterface
	CredentialSvc      handler.CredentialServiceInterface
	RateLimitSvc       service.RateLimitServiceInterface
	UsageMetrics       service.UsageMetricsRecorder
	Telemetry          service.TelemetryReader
	GraphView          graphview.Service
	RecallSvc          memoryservice.RecallService
	DreamSvc           dreamservice.Service
	AuditSvc           service.AuditService
	SecuritySvc        httpmw.SecurityBanService
	SSOService         *service.SSOService
	PortalSession      service.UserPortalSessionManager
	AppConfig          service.AppConfigService
	Config             config.ConfigProvider
	CredentialVerifier crypto.CredentialVerifier
	LastUsedRecorder   httpmw.LastUsedRecorder
	UserStaticDir      string
	ExtraMiddleware    []echo.MiddlewareFunc
}

type userPortalHandler struct {
	teams       handler.TeamServiceInterface
	credentials handler.CredentialServiceInterface
	telemetry   service.TelemetryReader
	graph       graphview.Service
	recall      *handler.RecallHandler
	dreams      *handler.DreamHandler
	audit       *handler.AuditHandler
	sso         *service.SSOService
	portal      service.UserPortalSessionManager
	appConfig   service.AppConfigService
}

type userPortalSessionResponse struct {
	Team               userPortalTeamResponse         `json:"team"`
	Membership         userPortalMembershipResponse   `json:"membership"`
	Credential         *userPortalCredentialResponse  `json:"credential"`
	Teams              []userPortalTeamOptionResponse `json:"teams"`
	PersonalCredential *userPortalCredentialResponse  `json:"personal_credential"`
}

type userPortalTeamOptionResponse struct {
	Team       userPortalTeamResponse       `json:"team"`
	Membership userPortalMembershipResponse `json:"membership"`
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

type userPortalMembershipResponse struct {
	TeamID uuid.UUID `json:"team_id"`
	Name   string    `json:"name"`
	Grants []string  `json:"grants"`
	Role   string    `json:"role"`
}

type userPortalCredentialResponse struct {
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
	APIKey     string                       `json:"api_key"`
	Credential userPortalCredentialResponse `json:"credential"`
}

type userPortalSSOProviderResponse struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Kind string    `json:"kind"`
}

type userPortalSwitchSSOTeamRequest struct {
	TeamID string `json:"team_id"`
}

type userPortalCreateSSOCredentialRequest struct {
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
	var ownerID *uuid.UUID
	if principal.GetRole() == service.CredentialRoleManager {
		scope = "team"
	} else {
		resolvedOwnerID := principal.GetOwnerID()
		if resolvedOwnerID == uuid.Nil {
			return service.TelemetryFilter{}, httperr.New(httperr.FORBIDDEN, "authenticated actor has no owner")
		}
		ownerID = &resolvedOwnerID
	}

	if requested := strings.TrimSpace(requestedScope); requested != "" && requested != scope {
		return service.TelemetryFilter{}, httperr.New(httperr.FORBIDDEN, "user portal telemetry scope is determined by the authenticated key")
	}

	return service.TelemetryFilter{
		Window:    strings.TrimSpace(window),
		Scope:     scope,
		TeamID:    &teamID,
		ProfileID: ownerID,
		Audience:  service.TelemetryAudienceUser,
	}, nil
}

func (h *userPortalHandler) rotateCurrentCredential(c echo.Context) error {
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
	credentialID := principal.GetCredentialID()
	if credentialID == nil || *credentialID == uuid.Nil {
		return httperr.New(httperr.FORBIDDEN, "api credential required")
	}
	current, err := h.credentials.GetByIDForTeam(ctx, teamID, *credentialID)
	if err != nil {
		return err
	}
	if current == nil {
		return httperr.New(httperr.NOT_FOUND, "credential not found")
	}

	actorCredentialID := credentialID.String()
	rotated, rawKey, err := h.credentials.RotateForTeam(ctx, teamID, *credentialID, service.CreateCredentialRequest{
		Name:      current.GetName(),
		RateLimit: current.RateLimit,
		ExpiresAt: current.ExpiresAt,
	}, &actorCredentialID, principal.Role, c.RealIP(), httpmw.GetCorrelationID(ctx))
	if err != nil {
		return err
	}

	return c.JSON(nethttp.StatusOK, map[string]any{"data": userPortalRotateResponse{
		APIKey:     rawKey,
		Credential: toUserPortalCredential(rotated),
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

	if h.teams == nil {
		return userPortalSessionResponse{}, httperr.New(httperr.SERVICE_UNAVAILABLE, "team service unavailable")
	}
	if h.credentials == nil {
		return userPortalSessionResponse{}, httperr.New(httperr.SERVICE_UNAVAILABLE, "credential service unavailable")
	}

	teamID := principal.GetTeamID()
	credentialID := principal.GetCredentialID()
	if credentialID == nil || *credentialID == uuid.Nil {
		return userPortalSessionResponse{}, httperr.New(httperr.AUTH_INVALID, "api credential required")
	}
	team, err := h.teams.Get(ctx, teamID)
	if err != nil {
		return userPortalSessionResponse{}, err
	}
	if team == nil {
		return userPortalSessionResponse{}, httperr.New(httperr.NOT_FOUND, "team not found")
	}
	credential, err := h.credentials.GetByIDForTeam(ctx, teamID, *credentialID)
	if err != nil {
		return userPortalSessionResponse{}, err
	}
	if credential == nil {
		return userPortalSessionResponse{}, httperr.New(httperr.NOT_FOUND, "credential not found")
	}

	teamResponse, err := h.toUserPortalTeam(ctx, team)
	if err != nil {
		return userPortalSessionResponse{}, err
	}
	credentialResponse := toUserPortalCredential(credential)
	return userPortalSessionResponse{
		Team:       teamResponse,
		Membership: userPortalMembershipFromPrincipal(principal),
		Credential: &credentialResponse,
		Teams:      []userPortalTeamOptionResponse{},
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
		Team:       selected.Team,
		Membership: selected.Membership,
		Credential: nil,
		Teams:      teams,
	}
	if selected.Membership.Role != service.CredentialRoleManager && h.credentials != nil {
		personalCredential, err := h.ssoOwnedCredential(ctx, info.Selected.Team.ID, info.Identity.ID)
		if err != nil {
			return userPortalSessionResponse{}, err
		}
		if personalCredential != nil {
			credentialResponse := toUserPortalCredential(personalCredential)
			response.PersonalCredential = &credentialResponse
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
	teamID, err := uuid.Parse(strings.TrimSpace(body.TeamID))
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}
	if _, err := h.sso.SwitchSessionTeam(c.Request().Context(), token, teamID); err != nil {
		return userPortalSSOError(err)
	}
	session, err := h.currentSSOSession(c)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": session})
}

func (h *userPortalHandler) createSSOCredential(c echo.Context) error {
	info, principal, err := h.ssoRequestSession(c)
	if err != nil {
		return err
	}
	if info.Selected.Membership.Role == service.CredentialRoleManager {
		return httperr.New(httperr.FORBIDDEN, "sso managers should create credentials from the team section")
	}
	if h.credentials == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "credential service unavailable")
	}
	existing, err := h.credentials.GetSSOOwnedCredential(c.Request().Context(), info.Selected.Team.ID, info.Identity.ID)
	if err != nil {
		return err
	}
	if existing != nil {
		return httperr.New(httperr.CONFLICT, "sso-owned credential already exists for this team")
	}

	var body userPortalCreateSSOCredentialRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	req, err := userPortalSSOCreateCredentialRequest(body, info.Identity, info.Selected.Membership.Grants)
	if err != nil {
		return err
	}
	req.OwnerIdentityID = &info.Identity.ID

	credential, rawKey, err := h.credentials.CreateCredential(
		c.Request().Context(),
		info.Selected.Team.ID,
		req,
		userPortalPrincipalCredentialID(principal),
		principal.Role,
		c.RealIP(),
		httpmw.GetCorrelationID(c.Request().Context()),
	)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusCreated, map[string]any{"data": userPortalRotateResponse{
		APIKey:     rawKey,
		Credential: toUserPortalCredential(credential),
	}})
}

func (h *userPortalHandler) rotateSSOCredential(c echo.Context) error {
	if err := rejectEditableRotateBody(c); err != nil {
		return err
	}
	info, principal, err := h.ssoRequestSession(c)
	if err != nil {
		return err
	}
	if info.Selected.Membership.Role == service.CredentialRoleManager {
		return httperr.New(httperr.FORBIDDEN, "sso managers should rotate credentials from the team section")
	}
	if h.credentials == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "credential service unavailable")
	}
	personalCredential, err := h.credentials.GetSSOOwnedCredential(c.Request().Context(), info.Selected.Team.ID, info.Identity.ID)
	if err != nil {
		return err
	}
	if personalCredential == nil {
		return httperr.New(httperr.NOT_FOUND, "sso-owned credential not found")
	}
	if !userPortalHasGrant(personalCredential.Scopes, service.CredentialScopeWrite) ||
		!userPortalHasGrant(info.Selected.Membership.Grants, service.CredentialScopeWrite) {
		return httperr.New(httperr.FORBIDDEN, "sso-owned credential cannot be rotated")
	}

	rotated, rawKey, err := h.credentials.RotateForTeam(c.Request().Context(), info.Selected.Team.ID, personalCredential.ID, service.CreateCredentialRequest{
		Name:      personalCredential.GetName(),
		RateLimit: personalCredential.RateLimit,
		ExpiresAt: personalCredential.ExpiresAt,
	}, userPortalPrincipalCredentialID(principal), principal.Role, c.RealIP(), httpmw.GetCorrelationID(c.Request().Context()))
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": userPortalRotateResponse{
		APIKey:     rawKey,
		Credential: toUserPortalCredential(rotated),
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

func (h *userPortalHandler) ssoOwnedCredential(ctx context.Context, teamID, identityID uuid.UUID) (*domain.Credential, error) {
	if h.credentials == nil {
		return nil, nil
	}
	return h.credentials.GetSSOOwnedCredential(ctx, teamID, identityID)
}

func userPortalSSOCreateCredentialRequest(body userPortalCreateSSOCredentialRequest, identity domain.SSOIdentity, maxGrants []string) (service.CreateCredentialRequest, error) {
	if !userPortalHasGrant(maxGrants, service.CredentialScopeRead) {
		return service.CreateCredentialRequest{}, httperr.New(httperr.FORBIDDEN, "sso access denied")
	}
	scopes := append([]string{}, body.Scopes...)
	if len(scopes) == 0 {
		scopes = []string{service.CredentialScopeRead}
		if userPortalHasGrant(maxGrants, service.CredentialScopeWrite) {
			scopes = service.StandardCredentialScopes()
		}
	}
	normalizedScopes, err := service.NormalizeCredentialScopes(scopes)
	if err != nil {
		return service.CreateCredentialRequest{}, err
	}
	for _, scope := range normalizedScopes {
		if !userPortalHasGrant(maxGrants, scope) {
			return service.CreateCredentialRequest{}, httperr.New(httperr.FORBIDDEN, "cannot create credential above sso entitlement")
		}
	}
	if body.RateLimit <= 0 {
		return service.CreateCredentialRequest{}, httperr.New(httperr.VALIDATION_ERROR, "rate_limit must be greater than zero")
	}
	var expiresAt *time.Time
	if body.ExpiresAt != nil && strings.TrimSpace(*body.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*body.ExpiresAt))
		if err != nil {
			return service.CreateCredentialRequest{}, httperr.New(httperr.VALIDATION_ERROR, "expires_at must be an RFC3339 timestamp")
		}
		expiresAt = &parsed
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = ssoOwnedKeyDefaultName(identity)
	}
	return service.CreateCredentialRequest{
		Name:      name,
		RateLimit: body.RateLimit,
		ExpiresAt: expiresAt,
		Scopes:    normalizedScopes,
		Role:      service.CredentialRoleMember,
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

func (h *userPortalHandler) toUserPortalTeam(ctx context.Context, team *domain.Team) (userPortalTeamResponse, error) {
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

func toUserPortalCredential(credential *domain.Credential) userPortalCredentialResponse {
	return userPortalCredentialResponse{
		ID:         credential.ID,
		TeamID:     credential.GetTeamID(),
		Name:       credential.GetName(),
		KeySuffix:  credential.KeySuffix,
		Scopes:     append([]string{}, credential.Scopes...),
		Role:       credential.GetRole(),
		RateLimit:  credential.RateLimit,
		LastUsedAt: controlTimePtr(credential.LastUsedAt),
		ExpiresAt:  controlTimePtr(credential.ExpiresAt),
		CreatedAt:  credential.CreatedAt.Format(time.RFC3339),
	}
}

func userPortalMembershipFromPrincipal(principal *httpmw.Principal) userPortalMembershipResponse {
	role := principal.GetRole()
	if role == "" {
		role = service.CredentialRoleMember
	}
	return userPortalMembershipResponse{
		TeamID: principal.GetTeamID(),
		Name:   principal.GetOwnerName(),
		Grants: append([]string{}, principal.GetGrants()...),
		Role:   role,
	}
}

func toUserPortalMembership(membership domain.Membership) userPortalMembershipResponse {
	role := membership.Role
	if role == "" {
		role = service.CredentialRoleMember
	}
	return userPortalMembershipResponse{
		TeamID: membership.TeamID,
		Name:   membership.Name,
		Grants: append([]string{}, membership.Grants...),
		Role:   role,
	}
}

func (h *userPortalHandler) toUserPortalTeamOption(ctx context.Context, item domain.SSOTeamMembership) (userPortalTeamOptionResponse, error) {
	team, err := h.toUserPortalTeam(ctx, &item.Team)
	if err != nil {
		return userPortalTeamOptionResponse{}, err
	}
	return userPortalTeamOptionResponse{
		Team:       team,
		Membership: toUserPortalMembership(item.Membership),
	}, nil
}

func userPortalHasGrant(grants []string, required string) bool {
	for _, grant := range grants {
		if grant == required {
			return true
		}
	}
	return false
}

func userPortalPrincipalCredentialID(principal *httpmw.Principal) *string {
	if principal == nil || principal.CredentialID == nil {
		return nil
	}
	credentialID := principal.CredentialID.String()
	return &credentialID
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
				return httperr.New(httperr.SERVICE_UNAVAILABLE, "rate limit service unavailable")
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
