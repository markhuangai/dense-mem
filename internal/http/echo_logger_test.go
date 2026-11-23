package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
)

func TestRootRecoverLogsPanicAndSupportsNilLogger(t *testing.T) {
	require.NotNil(t, RootRecover(nil))

	logger := &captureLogProvider{}
	e := echo.New()
	e.Use(RootRecover(logger))
	e.GET("/panic", func(*echo.Context) error {
		panic("panic-secret")
	})

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic", nil))

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Equal(t, "http_panic_recovered", logger.msg)
	require.NotContains(t, logger.msg, "panic-secret")
	attrs := make(map[string]any, len(logger.attrs))
	for _, attr := range logger.attrs {
		attrs[attr.Key] = attr.Value
	}
	require.Equal(t, "/panic", attrs["route"])
	require.Contains(t, attrs, "stack_bytes")
	require.NotNil(t, logger.contextSeen)
}

func TestEchoServerErrorLoggerBridgesAndHandlesNilWriter(t *testing.T) {
	require.Nil(t, EchoServerErrorLogger(nil))

	logger := &captureLogProvider{}
	serverLogger := EchoServerErrorLogger(logger)
	serverLogger.Print("server error")
	require.Equal(t, "http_server_error", logger.msg)

	n, err := (rootLogWriter{}).Write([]byte("discarded"))
	require.NoError(t, err)
	require.Equal(t, len("discarded"), n)

}
