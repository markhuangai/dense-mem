package main

import (
	"context"
	"errors"
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
		"oversized": strings.Repeat("x", harnessConfigLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			invalidPath := filepath.Join(directory, name+".json")
			require.NoError(t, os.WriteFile(invalidPath, []byte(contents), 0o600))
			_, err := loadHarnessConfig(invalidPath)
			require.Error(t, err)
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
	for _, raw := range []string{"", "http://harness.example", "https://user@harness.example", "https://harness.example?query=1", "https://harness.example/#fragment", " https://harness.example"} {
		_, err := validatePublicBaseURL(raw)
		require.Error(t, err, raw)
	}
	parsed, err := validatePublicBaseURL("https://harness.example/base/")
	require.NoError(t, err)
	require.Equal(t, "https://harness.example/base", parsed)
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
