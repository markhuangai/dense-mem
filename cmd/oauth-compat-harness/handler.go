package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

type harnessTokenValidator interface {
	Validate(context.Context, string) (*domain.OAuthValidatedToken, error)
}

type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

type harnessValidationResult struct {
	Valid            bool   `json:"valid"`
	Profile          string `json:"profile"`
	ScopeCount       int    `json:"scope_count"`
	TeamClaimPresent bool   `json:"team_claim_present"`
}

type harnessError struct {
	Error string `json:"error"`
}

func newHarnessHandler(publicBaseURL string, profiles []domain.OAuthProtectedResourceProfile, validator harnessTokenValidator) (http.Handler, error) {
	trustedBaseURL, err := validatePublicBaseURL(publicBaseURL)
	if err != nil {
		return nil, err
	}
	if validator == nil {
		return nil, fmt.Errorf("OAuth token validator is required")
	}
	parsed, err := url.Parse(trustedBaseURL)
	if err != nil {
		return nil, err
	}
	resourcePath := strings.TrimSuffix(parsed.Path, "/") + "/mcp"
	resourceURL := trustedBaseURL + "/mcp"
	metadataPath := "/.well-known/oauth-protected-resource" + resourcePath
	metadataURL := parsed.Scheme + "://" + parsed.Host + metadataPath
	metadata := protectedResourceMetadata{
		Resource:               resourceURL,
		AuthorizationServers:   configuredAuthorizationServers(profiles),
		ScopesSupported:        configuredExternalScopes(profiles),
		BearerMethodsSupported: []string{"header"},
	}
	challenge := `Bearer resource_metadata="` + metadataURL + `"`

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeHarnessJSON(response, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc(metadataPath, func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeHarnessJSON(response, http.StatusOK, metadata)
	})
	mux.HandleFunc(resourcePath, func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		raw, ok := parseHarnessBearer(request.Header.Get("Authorization"))
		if !ok {
			response.Header().Set("WWW-Authenticate", challenge)
			writeHarnessJSON(response, http.StatusUnauthorized, harnessError{Error: "invalid_token"})
			return
		}
		validated, err := validator.Validate(request.Context(), raw)
		if err != nil {
			response.Header().Set("WWW-Authenticate", challenge)
			status, code := harnessValidationError(err)
			writeHarnessJSON(response, status, harnessError{Error: code})
			return
		}
		writeHarnessJSON(response, http.StatusOK, harnessValidationResult{
			Valid:            true,
			Profile:          validated.ProfileName,
			ScopeCount:       len(validated.Scopes),
			TeamClaimPresent: validated.Team != "",
		})
	})
	return mux, nil
}

func validatePublicBaseURL(raw string) (string, error) {
	if raw == "" || len(raw) > 2048 || raw != strings.TrimSpace(raw) {
		return "", fmt.Errorf("public base URL must be non-empty and exact")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", fmt.Errorf("trusted HTTPS public base URL is required")
	}
	basePath := strings.TrimSuffix(parsed.Path, "/")
	if parsed.RawPath != "" ||
		strings.ContainsAny(parsed.Path, "{} \t?#") ||
		strings.Contains(parsed.Path, "//") ||
		(parsed.Path != "" && parsed.Path != "/" && path.Clean(parsed.Path) != basePath) {
		return "", fmt.Errorf("public base URL path must be canonical and literal")
	}
	return strings.TrimSuffix(raw, "/"), nil
}

func parseHarnessBearer(header string) (string, bool) {
	scheme, raw, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, " \t\r\n") {
		return "", false
	}
	return raw, true
}

func harnessValidationError(err error) (int, string) {
	if errors.Is(err, service.ErrOAuthProviderUnavailable) {
		return http.StatusServiceUnavailable, "temporarily_unavailable"
	}
	return http.StatusUnauthorized, "invalid_token"
}

func configuredAuthorizationServers(profiles []domain.OAuthProtectedResourceProfile) []string {
	values := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if profile.Issuer != "" {
			values[profile.Issuer] = struct{}{}
		}
	}
	return sortedHarnessValues(values)
}

func configuredExternalScopes(profiles []domain.OAuthProtectedResourceProfile) []string {
	values := make(map[string]struct{})
	for _, profile := range profiles {
		for _, mapping := range profile.ProtectedResource.ScopeMappings {
			if mapping.ExternalScope != "" {
				values[mapping.ExternalScope] = struct{}{}
			}
		}
	}
	return sortedHarnessValues(values)
}

func sortedHarnessValues(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func writeHarnessJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
