package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

func TestHTTPServerLogURIAndAnonymousProbeBoundaries(t *testing.T) {
	if requestLogURI(nil) != "" {
		t.Fatal("nil context URI was not empty")
	}
	if requestLogURI(echo.New().NewContext(nil, httptest.NewRecorder())) != "" {
		t.Fatal("missing request URI was not empty")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := echo.New().NewContext(req, httptest.NewRecorder())
	req.URL.Path = ""
	if requestLogURI(ctx) != "/" {
		t.Fatalf("empty URI = %q", requestLogURI(ctx))
	}
	req.URL.Path = "/plain"
	if requestLogURI(ctx) != "/plain" {
		t.Fatalf("plain URI = %q", requestLogURI(ctx))
	}
	req.URL.Path = "/ui/api/session"
	values := middleware.RequestLoggerValues{Method: http.MethodGet, Status: http.StatusUnauthorized, Error: httperr.New(httperr.AUTH_MISSING, "missing")}
	if !isAnonymousUserSessionProbe(ctx, values) {
		t.Fatal("anonymous session probe was not recognized")
	}
	values.Error = httperr.New(httperr.AUTH_INVALID, "invalid")
	if isAnonymousUserSessionProbe(ctx, values) {
		t.Fatal("invalid auth was treated as anonymous")
	}
	logger := &captureLogProvider{}
	e := NewServer(nil, logger, HealthConfig{})
	e.GET("/request-id", func(c echo.Context) error {
		c.Response().Header().Set(echo.HeaderXRequestID, "request-id")
		return c.NoContent(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/request-id", nil))
	if rec.Code != http.StatusNoContent || logAttrValue(logger.attrs, "request_id") != "request-id" {
		t.Fatalf("request id logging = %d/%q", rec.Code, logAttrValue(logger.attrs, "request_id"))
	}
}

func TestRunServerHandlesStartFailureAndInterrupt(t *testing.T) {
	e := echo.New()
	logger := &captureLogProvider{}
	timer := time.AfterFunc(50*time.Millisecond, func() {
		process, err := os.FindProcess(os.Getpid())
		if err == nil {
			_ = process.Signal(syscall.SIGINT)
		}
	})
	defer timer.Stop()
	if err := RunServer(e, "invalid-address", logger); err != nil {
		t.Fatalf("RunServer: %v", err)
	}
	if logger.msg != "shutting down server" && logger.msg != "server start error" {
		t.Fatalf("RunServer logger = %q", logger.msg)
	}
}
