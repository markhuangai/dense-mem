package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

type fakeSecurityBanService struct {
	ban      *domain.SecurityIPBan
	err      error
	lastIP   string
	checked  bool
	failures int
}

func (s *fakeSecurityBanService) CheckBan(_ context.Context, ip string) (*domain.SecurityIPBan, error) {
	s.checked = true
	s.lastIP = ip
	return s.ban, s.err
}

func (s *fakeSecurityBanService) RecordAuthFailure(context.Context, string, string, string) (*domain.SecurityIPBan, error) {
	s.failures++
	return nil, nil
}

func TestSecurityBanMiddleware(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(SecurityBanMiddleware(nil))
	e.GET("/ok", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	svc := &fakeSecurityBanService{err: errors.New("store unavailable")}
	e = echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(SecurityBanMiddleware(svc))
	e.GET("/ok", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	req = httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, svc.checked)

	svc = &fakeSecurityBanService{ban: &domain.SecurityIPBan{
		IP:       "203.0.113.10",
		Reason:   "manual",
		BannedAt: time.Now().UTC(),
	}}
	e = echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(SecurityBanMiddleware(svc))
	e.GET("/blocked", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	req = httptest.NewRequest(http.MethodGet, "/blocked", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "client blocked")
}
