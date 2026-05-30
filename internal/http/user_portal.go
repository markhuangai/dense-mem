package http

import (
	"encoding/json"
	"io"
	nethttp "net/http"
	"os"
	"path/filepath"
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
)

// UserPortalDeps holds the dependencies for the API-key user portal.
type UserPortalDeps struct {
	APIKeyRepo    repository.APIKeyRepository
	ProfileSvc    handler.ProfileServiceInterface
	APIKeySvc     handler.APIKeyServiceInterface
	RateLimitSvc  service.RateLimitServiceInterface
	UsageMetrics  service.UsageMetricsRecorder
	AuditSvc      service.AuditService
	SecuritySvc   httpmw.SecurityBanService
	Config        config.ConfigProvider
	UserStaticDir string
}

// RegisterUserPortal registers the API-key user portal under /ui on the main API server.
func RegisterUserPortal(e *echo.Echo, deps UserPortalDeps) {
	portal := &userPortalHandler{
		profiles: deps.ProfileSvc,
		keys:     deps.APIKeySvc,
	}

	api := e.Group("/ui/api")
	api.Use(httpmw.AuthMiddlewareWithSecurity(deps.APIKeyRepo, deps.AuditSvc, deps.SecuritySvc))
	api.Use(httpmw.UsageMetricsMiddleware(deps.UsageMetrics))
	api.Use(httpmw.RateLimitMiddleware(deps.RateLimitSvc, deps.Config, deps.AuditSvc))
	api.GET("/session", portal.session)
	api.POST("/key/rotate", portal.rotateCurrentKey, httpmw.RequireScopes("write"))

	staticDir := strings.TrimSpace(deps.UserStaticDir)
	if staticDir == "" {
		staticDir = defaultUserPortalStaticDir()
	}
	registerUserPortalStatic(e, staticDir)
}

type userPortalHandler struct {
	profiles handler.ProfileServiceInterface
	keys     handler.APIKeyServiceInterface
}

type userPortalSessionResponse struct {
	Team      userPortalTeamResponse `json:"team"`
	Key       userPortalKeyResponse  `json:"key"`
	CanRotate bool                   `json:"can_rotate"`
}

type userPortalTeamResponse struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   string    `json:"created_at"`
	UpdatedAt   string    `json:"updated_at"`
}

type userPortalKeyResponse struct {
	ID         uuid.UUID `json:"id"`
	TeamID     uuid.UUID `json:"team_id"`
	Name       string    `json:"name"`
	KeySuffix  string    `json:"key_suffix"`
	Scopes     []string  `json:"scopes"`
	RateLimit  int       `json:"rate_limit"`
	LastUsedAt *string   `json:"last_used_at"`
	ExpiresAt  *string   `json:"expires_at"`
	CreatedAt  string    `json:"created_at"`
}

type userPortalRotateResponse struct {
	APIKey string                `json:"api_key"`
	Key    userPortalKeyResponse `json:"key"`
}

func (h *userPortalHandler) session(c echo.Context) error {
	session, err := h.currentSession(c)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": session})
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
		return userPortalSessionResponse{}, httperr.New(httperr.FORBIDDEN, "authentication required")
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

	return userPortalSessionResponse{
		Team:      toUserPortalTeam(team),
		Key:       toUserPortalKey(key),
		CanRotate: userPortalHasScope(principal.Scopes, service.APIKeyScopeWrite),
	}, nil
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

func toUserPortalTeam(team *domain.Profile) userPortalTeamResponse {
	return userPortalTeamResponse{
		ID:          team.ID,
		Name:        team.Name,
		Description: team.Description,
		CreatedAt:   team.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   team.UpdatedAt.Format(time.RFC3339),
	}
}

func toUserPortalKey(key *domain.APIKey) userPortalKeyResponse {
	return userPortalKeyResponse{
		ID:         key.ID,
		TeamID:     key.GetTeamID(),
		Name:       key.GetProfileName(),
		KeySuffix:  key.KeySuffix,
		Scopes:     append([]string{}, key.Scopes...),
		RateLimit:  key.RateLimit,
		LastUsedAt: controlTimePtr(key.LastUsedAt),
		ExpiresAt:  controlTimePtr(key.ExpiresAt),
		CreatedAt:  key.CreatedAt.Format(time.RFC3339),
	}
}

func userPortalHasScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
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
