package middleware

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func TestAuthFailureRequestCapture(t *testing.T) {
	for _, test := range []struct{ name, body, contentType, encoding, reason string }{
		{name: "original JSON", body: "{\n  \"password\": \"ordinary content\"\n}", contentType: "application/json"},
		{name: "malformed JSON", body: "{ bad JSON", contentType: "application/json"},
		{name: "empty"},
		{name: "exact limit", body: strings.Repeat("a", domain.SecurityRequestBodyLimit), contentType: "text/plain"},
		{name: "oversized", body: strings.Repeat("a", domain.SecurityRequestBodyLimit+1), contentType: "text/plain", reason: "size_limit"},
		{name: "binary", body: "binary", contentType: "application/octet-stream", reason: "binary_body"},
		{name: "invalid UTF8", body: string([]byte{0xff}), contentType: "text/plain", reason: "binary_body"},
		{name: "compressed", body: "compressed", contentType: "application/json", encoding: "gzip", reason: "compressed_body"},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := echo.New()
			e.POST("/mcp", func(c echo.Context) error {
				snapshot := CaptureAuthFailureRequest(c, "invalid api key")
				require.Contains(t, requestctx.AuthenticationSecretsFromContext(c.Request().Context()), "presented-test-key")
				return c.JSON(http.StatusUnauthorized, snapshot)
			})
			server := httptest.NewServer(e)
			defer server.Close()
			req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp?mode=diagnose", strings.NewReader(test.body))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer presented-test-key")
			req.Header.Add("Authorization", "Bearer second-presented-key")
			req.Header.Set("Content-Type", test.contentType)
			req.Header.Set("Content-Encoding", test.encoding)
			req.Header.Add("X-Repeated", "first")
			req.Header.Add("X-Repeated", "second")
			response, err := server.Client().Do(req)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusUnauthorized, response.StatusCode)
			var snapshot domain.SecurityAuthFailureRequest
			require.NoError(t, json.NewDecoder(response.Body).Decode(&snapshot))
			require.Equal(t, "/mcp?mode=diagnose", snapshot.URL)
			require.Equal(t, []string{"first", "second"}, snapshot.Headers["X-Repeated"])
			require.Equal(t, []string{"[REDACTED]", "[REDACTED]"}, snapshot.Headers["Authorization"])
			require.NotEmpty(t, snapshot.Headers["Host"])
			require.Len(t, snapshot.CredentialFingerprint, 64)
			if test.reason == "" {
				require.Equal(t, test.body, snapshot.Body)
				require.Equal(t, "captured", snapshot.BodyCapture.State)
			} else {
				require.Empty(t, snapshot.Body)
				require.Equal(t, test.reason, snapshot.BodyCapture.Reason)
			}
		})
	}
}

func TestAuthFailureRequestPreservesConsumedFormAndSuccessfulBody(t *testing.T) {
	const body = "grant_type=client_credentials&client_secret=test%2Bsecret&client_id=example"
	e := echo.New()
	e.POST("/form", func(c echo.Context) error {
		PreserveAuthFailureBody(c)
		PreserveAuthFailureBody(c)
		require.NoError(t, c.Request().ParseForm())
		require.Equal(t, "test+secret", c.FormValue("client_secret"))
		snapshot := CaptureAuthFailureRequest(c, "invalid client")
		require.Equal(t, body, snapshot.Body)
		return c.NoContent(http.StatusUnauthorized)
	})
	e.POST("/success", func(c echo.Context) error {
		PreserveAuthFailureBody(c)
		received, err := io.ReadAll(c.Request().Body)
		require.NoError(t, err)
		return c.Blob(http.StatusOK, "text/plain", received)
	})
	for _, path := range []string{"/form", "/success"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if path == "/success" {
			require.Equal(t, body, rec.Body.String())
		}
	}
}

func TestAuthFailureRequestUnavailableBounds(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("unread"))
	req.Header.Set("X-Oversized", strings.Repeat("a", domain.SecurityRequestHeaderLimit))
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	c := e.NewContext(req, httptest.NewRecorder())
	snapshot := CaptureAuthFailureRequest(c, "invalid key")
	require.Equal(t, "size_limit", snapshot.HeaderCapture.Reason)
	require.Nil(t, snapshot.Headers)
	require.Equal(t, "read_deadline_unsupported", snapshot.BodyCapture.Reason)
	require.Empty(t, snapshot.Body)
	require.Equal(t, "basic", snapshot.AuthScheme)
	require.Contains(t, requestctx.AuthenticationSecretsFromContext(c.Request().Context()), "pass")
	remaining, err := io.ReadAll(c.Request().Body)
	require.NoError(t, err)
	require.Equal(t, "unread", string(remaining))
}

func TestAuthFailureRequestSlowOrDisconnectedBody(t *testing.T) {
	for _, disconnected := range []bool{false, true} {
		t.Run(fmt.Sprint(disconnected), func(t *testing.T) {
			e := echo.New()
			e.POST("/mcp", func(c echo.Context) error {
				return c.JSON(http.StatusUnauthorized, CaptureAuthFailureRequest(c, "invalid key"))
			})
			server := httptest.NewServer(e)
			defer server.Close()
			conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
			_, err = io.WriteString(conn, "POST /mcp HTTP/1.1\r\nHost: example.invalid\r\nContent-Type: application/json\r\nContent-Length: 100\r\nConnection: close\r\n\r\n{")
			require.NoError(t, err)
			if disconnected {
				require.NoError(t, conn.(*net.TCPConn).CloseWrite())
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusUnauthorized, response.StatusCode)
			var snapshot domain.SecurityAuthFailureRequest
			require.NoError(t, json.NewDecoder(response.Body).Decode(&snapshot))
			require.Empty(t, snapshot.Body)
			require.Equal(t, "incomplete_body", snapshot.BodyCapture.Reason)
		})
	}
}

func TestAuthFailureRequestThroughAuthentication(t *testing.T) {
	for _, authorization := range []string{"", "malformed-credential", "Basic dXNlcjpwYXNz", "Bearer "} {
		t.Run(authorization, func(t *testing.T) {
			e := newTestEcho()
			security := &mockSecurityService{}
			e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, nil, security, testAuthOptions(nil)))
			e.GET("/test", func(c echo.Context) error { return c.String(http.StatusOK, "unreachable") })
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if authorization != "" {
				req.Header.Set("Authorization", authorization)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
			require.NotNil(t, security.request)
			require.Equal(t, "GET", security.request.Method)
			require.Equal(t, "/test", security.request.Route)
			require.Equal(t, "captured", security.request.BodyCapture.State)
			if authorization == "" {
				require.Equal(t, "missing", security.request.AuthScheme)
			} else {
				require.Equal(t, []string{"[REDACTED]"}, security.request.Headers["Authorization"])
				require.Contains(t, security.secrets, authorization)
			}
		})
	}
}
