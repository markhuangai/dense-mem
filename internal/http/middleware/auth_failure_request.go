package middleware

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

const authFailurePrefixKey = "dense-mem.auth-failure-prefix"

type authFailureBody struct {
	io.ReadCloser
	bytes    []byte
	observed int64
	complete bool
	readErr  error
}

func (b *authFailureBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.observed += int64(n)
	if remaining := domain.SecurityRequestBodyLimit + 1 - len(b.bytes); remaining > 0 {
		b.bytes = append(b.bytes, p[:min(n, remaining)]...)
	}
	b.complete = b.complete || err == io.EOF
	if err != nil && err != io.EOF {
		b.readErr = err
	}
	return n, err
}

// PreserveAuthFailureBody observes bytes a form parser consumes without changing its reader.
func PreserveAuthFailureBody(c echo.Context) {
	req := c.Request()
	if req.Body == nil || req.Body == http.NoBody {
		return
	}
	if _, ok := req.Body.(*authFailureBody); !ok {
		req.Body = &authFailureBody{ReadCloser: req.Body}
	}
}

func sensitiveAuthHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-control-portal-token", "x-api-key", "api-key":
		return true
	}
	return strings.Contains(strings.ToLower(name), "csrf")
}

// CaptureAuthFailureRequest never drains more than the capture budget after a rejection.
func CaptureAuthFailureRequest(c echo.Context, message string) *domain.SecurityAuthFailureRequest {
	req := c.Request()
	result := &domain.SecurityAuthFailureRequest{
		ID: uuid.NewString(), CapturedAt: time.Now().UTC(), Method: req.Method,
		Route: c.Path(), Message: message, AuthScheme: "missing",
		HeaderCapture: domain.SecurityRequestCapture{State: "captured", Limit: domain.SecurityRequestHeaderLimit},
		URLCapture:    domain.SecurityRequestCapture{State: "captured", Limit: 8192},
	}
	if req.URL != nil {
		result.URL = req.URL.RequestURI()
	}
	result.URLCapture.Bytes = int64(len(result.URL))
	if len(result.URL) > result.URLCapture.Limit {
		result.URL, result.URLCapture.State, result.URLCapture.Reason = "", "unavailable", "size_limit"
	}
	secrets := append([]string(nil), requestctx.AuthenticationSecretsFromContext(req.Context())...)
	credential := ""
	if authorization := req.Header.Get("Authorization"); authorization != "" {
		secrets = append(secrets, authorization)
		result.AuthScheme, credential = "malformed", authorization
		parts := strings.SplitN(authorization, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			result.AuthScheme, credential = "bearer", parts[1]
			secrets = append(secrets, credential)
		} else if username, password, ok := req.BasicAuth(); ok {
			result.AuthScheme, credential = "basic", password
			secrets = append(secrets, username, password)
		}
	}
	if token := req.Header.Get("X-Control-Portal-Token"); token != "" {
		result.AuthScheme, credential = "control_token", token
	}
	if result.AuthScheme != "basic" && req.Form.Get("grant_type") == "client_credentials" && req.Form.Get("client_secret") != "" {
		result.AuthScheme, credential = "client_secret", req.Form.Get("client_secret")
		secrets = append(secrets, credential)
	}
	for _, cookie := range req.Cookies() {
		secrets = append(secrets, cookie.Value)
		if result.AuthScheme == "missing" && (cookie.Name == accessservice.ControlSessionCookieName || cookie.Name == accessservice.SSOSessionCookieName || cookie.Name == accessservice.UserPortalSessionCookieName) {
			result.AuthScheme, credential = "cookie", cookie.Value
		}
	}
	if credential != "" {
		digest := sha256.Sum256([]byte(credential))
		result.CredentialFingerprint = hex.EncodeToString(digest[:])
	}
	if prefix, ok := c.Get(authFailurePrefixKey).(string); ok {
		result.CredentialPrefix = prefix
	}
	result.Headers = req.Header.Clone()
	result.Headers["Host"] = []string{req.Host}
	for name, values := range result.Headers {
		for _, value := range values {
			result.HeaderCapture.Bytes += int64(len(name) + len(value) + 4)
		}
		if sensitiveAuthHeader(name) {
			secrets = append(secrets, values...)
			for _, value := range values {
				if parts := strings.SplitN(value, " ", 2); len(parts) == 2 {
					secrets = append(secrets, parts[1])
					if (strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization")) && strings.EqualFold(parts[0], "Basic") {
						if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1])); err == nil {
							secrets = append(secrets, string(decoded))
							if username, password, ok := strings.Cut(string(decoded), ":"); ok {
								secrets = append(secrets, username, password)
							}
						}
					}
				}
			}
			redacted := make([]string, len(values))
			for i := range redacted {
				redacted[i] = "[REDACTED]"
			}
			result.Headers[name] = redacted
		}
	}
	if result.HeaderCapture.Bytes > int64(domain.SecurityRequestHeaderLimit) {
		result.Headers, result.HeaderCapture.State, result.HeaderCapture.Reason = nil, "unavailable", "size_limit"
	}
	unique := make([]string, 0, len(secrets))
	seen := make(map[string]bool, len(secrets))
	for _, secret := range secrets {
		if secret != "" && !seen[secret] {
			unique = append(unique, secret)
			seen[secret] = true
		}
	}
	c.SetRequest(req.WithContext(requestctx.WithAuthenticationSecrets(req.Context(), unique...)))
	result.Body, result.BodyCapture = captureAuthFailureBody(c)
	return result
}

func captureAuthFailureBody(c echo.Context) (string, domain.SecurityRequestCapture) {
	req := c.Request()
	state := domain.SecurityRequestCapture{State: "captured", Limit: domain.SecurityRequestBodyLimit}
	unavailable := func(reason string) (string, domain.SecurityRequestCapture) {
		state.State, state.Reason = "unavailable", reason
		return "", state
	}
	if req.Body == nil || req.Body == http.NoBody || req.ContentLength == 0 {
		return "", state
	}
	state.Bytes = max(0, req.ContentLength)
	if req.ContentLength > int64(state.Limit) {
		return unavailable("size_limit")
	}
	if encoding := req.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return unavailable("compressed_body")
	}
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if req.Header.Get("Content-Type") != "" && err != nil {
		return unavailable("unsupported_content_type")
	}
	if mediaType != "" && !strings.HasPrefix(mediaType, "text/") && mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") && mediaType != "application/x-www-form-urlencoded" && mediaType != "application/xml" && !strings.HasSuffix(mediaType, "+xml") {
		return unavailable("binary_body")
	}
	PreserveAuthFailureBody(c)
	body := req.Body.(*authFailureBody)
	err = nil
	complete := body.complete || (req.ContentLength > 0 && body.observed == req.ContentLength)
	if !complete && body.readErr == nil && len(body.bytes) <= state.Limit {
		if err := http.NewResponseController(c.Response()).SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
			return unavailable("read_deadline_unsupported")
		}
		_, err = io.Copy(io.Discard, io.LimitReader(body, int64(state.Limit+1-len(body.bytes))))
		complete = body.complete || (req.ContentLength > 0 && body.observed == req.ContentLength)
	}
	state.Bytes = body.observed
	if len(body.bytes) > state.Limit {
		return unavailable("size_limit")
	}
	if err != nil || body.readErr != nil || !complete {
		return unavailable("incomplete_body")
	}
	if !utf8.Valid(body.bytes) {
		return unavailable("binary_body")
	}
	return string(body.bytes), state
}
