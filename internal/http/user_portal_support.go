package http

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

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
	c.SetCookie(&http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: httpOnly,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSSOCookie(c echo.Context, name string) {
	c.SetCookie(&http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: name == service.SSOSessionCookieName,
		SameSite: http.SameSiteLaxMode,
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
