package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

func TestLoadHarnessConfigIsBoundedAndStrict(t *testing.T) {
	directory := t.TempDir()
	valid := `{"profiles":[{"name":"entra","issuer":"https://issuer.example/entra","protected_resource":{"audiences":["api://dense-mem"],"jwks_source":"discovery","jwks_uri":"","algorithms":["RS256"],"scope_claim":"scp","scope_mappings":[{"external_scope":"memory.read","internal_scopes":["read"]}],"team_claim":"tenant"}}]}`
	path := filepath.Join(directory, "oauth.json")
	require.NoError(t, os.WriteFile(path, []byte(valid), 0o600))
	config, err := loadHarnessConfig(path)
	require.NoError(t, err)
	require.Len(t, config.Profiles, 1)

	for name, contents := range map[string]string{
		"unknown":   strings.TrimSuffix(valid, "}") + `,"unknown":true}`,
		"duplicate": `{"profiles":[],"profiles":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			invalidPath := filepath.Join(directory, name+".json")
			require.NoError(t, os.WriteFile(invalidPath, []byte(contents), 0o600))
			_, err := loadHarnessConfig(invalidPath)
			require.Error(t, err)
		})
	}

	oversizedPath := filepath.Join(directory, "oversized.json")
	oversized := `{"profiles":[{"name":"` + strings.Repeat("x", harnessConfigLimit) + `"}]}`
	require.NoError(t, os.WriteFile(oversizedPath, []byte(oversized), 0o600))
	_, err = loadHarnessConfig(oversizedPath)
	require.ErrorContains(t, err, "JSON input exceeds")
}

func TestParseHarnessOptionsReportsMissingFlagsDeterministically(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "default listen", want: "--public-base-url is required"},
		{name: "empty listen", args: []string{"--listen="}, want: "--listen is required"},
		{name: "base URL only", args: []string{"--public-base-url=https://harness.example"}, want: "--config is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseHarnessOptions(test.args, io.Discard)
			require.EqualError(t, err, test.want)
		})
	}
}

func TestHarnessMetadataAndChallengeUseConfiguredHTTPSURL(t *testing.T) {
	profiles := []domain.OAuthProtectedResourceProfile{{
		Name: "entra", Issuer: "https://issuer.example/entra",
		ProtectedResource: domain.OAuthProtectedResourceConfig{
			Audiences: []string{"api://dense-mem"}, JWKSSource: "discovery", Algorithms: []string{"RS256"}, ScopeClaim: "scp",
			ScopeMappings: []domain.OAuthScopeMapping{{ExternalScope: "memory.read", InternalScopes: []string{"read"}}},
		},
	}}
	validator := harnessValidatorStub{result: &domain.OAuthValidatedToken{ProfileName: "entra", Scopes: []string{"read"}}}
	handler, err := newHarnessHandler("https://harness.example/base", profiles, validator)
	require.NoError(t, err)

	metadataRequest := httptest.NewRequest(http.MethodGet, "https://attacker.example/.well-known/oauth-protected-resource/base/mcp", nil)
	metadataRequest.Host = "attacker.example"
	metadataResponse := httptest.NewRecorder()
	handler.ServeHTTP(metadataResponse, metadataRequest)
	require.Equal(t, http.StatusOK, metadataResponse.Code)
	require.JSONEq(t, `{"resource":"https://harness.example/base/mcp","authorization_servers":["https://issuer.example/entra"],"scopes_supported":["memory.read"],"bearer_methods_supported":["header"]}`, metadataResponse.Body.String())

	challengeRequest := httptest.NewRequest(http.MethodPost, "https://attacker.example/base/mcp", nil)
	challengeRequest.Host = "attacker.example"
	challengeResponse := httptest.NewRecorder()
	handler.ServeHTTP(challengeResponse, challengeRequest)
	require.Equal(t, http.StatusUnauthorized, challengeResponse.Code)
	require.Equal(t, `Bearer resource_metadata="https://harness.example/.well-known/oauth-protected-resource/base/mcp"`, challengeResponse.Header().Get("WWW-Authenticate"))
	require.NotContains(t, challengeResponse.Body.String(), "attacker.example")

	successRequest := httptest.NewRequest(http.MethodPost, "https://attacker.example/base/mcp", nil)
	successRequest.Header.Set("Authorization", "Bearer secret-token")
	successResponse := httptest.NewRecorder()
	handler.ServeHTTP(successResponse, successRequest)
	require.Equal(t, http.StatusOK, successResponse.Code)
	require.JSONEq(t, `{"valid":true,"profile":"entra","scope_count":1,"team_claim_present":false}`, successResponse.Body.String())
	require.NotContains(t, successResponse.Body.String(), "secret-token")

	ambiguousRequest := httptest.NewRequest(http.MethodPost, "https://harness.example/base/mcp", nil)
	ambiguousRequest.Header.Add("Authorization", "Bearer secret-token")
	ambiguousRequest.Header.Add("Authorization", "Bearer second-token")
	ambiguousResponse := httptest.NewRecorder()
	handler.ServeHTTP(ambiguousResponse, ambiguousRequest)
	require.Equal(t, http.StatusUnauthorized, ambiguousResponse.Code)
	require.JSONEq(t, `{"error":"invalid_token"}`, ambiguousResponse.Body.String())
	require.NotContains(t, ambiguousResponse.Body.String(), "secret-token")
	require.NotContains(t, ambiguousResponse.Body.String(), "second-token")
}

func TestNewHarnessHandlerRejectsInvalidBasePaths(t *testing.T) {
	for _, raw := range []string{
		"https://harness.example/%7Btenant%7D",
		"https://harness.example/base%20path",
		"https://harness.example/base%09path",
		"https://harness.example/base%3Ftenant",
		"https://harness.example/base%23tenant",
		"https://harness.example/base%2Ftenant",
		"https://harness.example/base//",
	} {
		var handler http.Handler
		var err error
		require.NotPanics(t, func() {
			handler, err = newHarnessHandler(raw, nil, harnessValidatorStub{})
		}, raw)
		require.Error(t, err, raw)
		require.Nil(t, handler)
	}
}

func TestHarnessMapsTypedValidationErrorsWithoutReflectingTokens(t *testing.T) {
	profiles := []domain.OAuthProtectedResourceProfile{{Name: "entra", Issuer: "https://issuer.example/entra", ProtectedResource: domain.OAuthProtectedResourceConfig{
		Audiences: []string{"audience"}, JWKSSource: "discovery", Algorithms: []string{"RS256"}, ScopeClaim: "scp",
	}}}

	for name, test := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"invalid":     {err: service.OAuthTokenInvalidError{}, status: http.StatusUnauthorized, code: "invalid_token"},
		"expired":     {err: service.OAuthTokenExpiredError{}, status: http.StatusUnauthorized, code: "invalid_token"},
		"unavailable": {err: service.OAuthProviderUnavailableError{}, status: http.StatusServiceUnavailable, code: "temporarily_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, err := newHarnessHandler("https://harness.example", profiles, harnessValidatorStub{err: test.err})
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "https://harness.example/mcp", nil)
			request.Header.Set("Authorization", "Bearer token-must-not-appear")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, test.status, response.Code)
			require.JSONEq(t, `{"error":"`+test.code+`"}`, response.Body.String())
			require.NotContains(t, response.Body.String(), "token-must-not-appear")
		})
	}
}

func TestValidatePublicBaseURLRequiresTrustedHTTPSIdentifier(t *testing.T) {
	for _, raw := range []string{
		"",
		"http://harness.example",
		"https://:443",
		"https://harness.example:0",
		"https://harness.example:65536",
		"https://user@harness.example",
		"https://harness.example?query=1",
		"https://harness.example/#fragment",
		" https://harness.example",
		"https://harness.example/{tenant}",
		"https://harness.example/%7Btenant%7D",
		"https://harness.example/base%20path",
		"https://harness.example/base%09path",
		"https://harness.example/base%3Ftenant",
		"https://harness.example/base%23tenant",
		"https://harness.example/base%2Ftenant",
		"https://harness.example/base//",
		"https://harness.example/base/../tenant",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := validatePublicBaseURL(raw)
			require.Error(t, err)
		})
	}
	for raw, want := range map[string]string{
		"https://harness.example/base/":     "https://harness.example/base",
		"https://harness.example:443":       "https://harness.example:443",
		"https://[2001:db8::1]/base":        "https://[2001:db8::1]/base",
		"https://[2001:db8::1]:65535/base/": "https://[2001:db8::1]:65535/base",
	} {
		t.Run(raw, func(t *testing.T) {
			parsed, err := validatePublicBaseURL(raw)
			require.NoError(t, err)
			require.Equal(t, want, parsed)
		})
	}
}

type harnessValidatorStub struct {
	result *domain.OAuthValidatedToken
	err    error
}

func (stub harnessValidatorStub) Validate(context.Context, string) (*domain.OAuthValidatedToken, error) {
	if stub.err != nil {
		return nil, stub.err
	}
	if stub.result == nil {
		return nil, errors.New("missing test result")
	}
	result := *stub.result
	result.ExpiresAt = time.Now().Add(time.Minute)
	return &result, nil
}
