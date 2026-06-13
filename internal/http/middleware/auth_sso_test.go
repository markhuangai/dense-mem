package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

func TestAuthMiddleware_SSOSessionFailureIsAuditedAndRecorded(t *testing.T) {
	e := newTestEcho()
	mockAudit := &mockAuditService{}
	mockSecurity := &mockSecurityService{}
	authenticator := mockSSOSessionAuthenticator{
		authenticateFunc: func(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.APIKey, error) {
			return nil, service.ErrSSOAccessDenied
		},
	}
	e.Use(AuthMiddlewareWithOptions(&mockAPIKeyRepository{}, mockAudit, mockSecurity, AuthOptions{SSOSessionAuthenticator: authenticator}))

	req := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	req.RemoteAddr = "198.51.100.10:12345"
	req.AddCookie(&http.Cookie{Name: service.SSOSessionCookieName, Value: "session-token"})
	rec := httptest.NewRecorder()
	handlerCalled := false
	e.GET("/ui/api/session", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.True(t, mockAudit.authFailureCalled)
	assert.True(t, mockSecurity.recordAuthFailureCalled)
	assert.Equal(t, "SSO_AUTH_INVALID", mockSecurity.recordAuthFailureReason)
	assert.Equal(t, "api", mockSecurity.recordAuthFailureSurface)
	assert.Equal(t, "198.51.100.10", mockSecurity.recordAuthFailureIP)
}
