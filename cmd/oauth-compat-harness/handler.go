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
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	densehttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/observability"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
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

type oauthObservationContextKey struct{}

type oauthObservation struct {
	profile          string
	scopeCount       int
	teamClaimPresent bool
}

func newHarnessHandler(publicBaseURL string, profiles []domain.OAuthProtectedResourceProfile, validator harnessTokenValidator, loggers ...observability.LogProvider) (http.Handler, error) {
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
		authorization := request.Header.Values("Authorization")
		raw, ok := "", false
		if len(authorization) == 1 {
			raw, ok = parseHarnessBearer(authorization[0])
		}
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
		if observation, ok := request.Context().Value(oauthObservationContextKey{}).(*oauthObservation); ok {
			observation.profile = validated.ProfileName
			observation.scopeCount = len(validated.Scopes)
			observation.teamClaimPresent = validated.Team != ""
		}
		writeHarnessJSON(response, http.StatusOK, harnessValidationResult{
			Valid:            true,
			Profile:          validated.ProfileName,
			ScopeCount:       len(validated.Scopes),
			TeamClaimPresent: validated.Team != "",
		})
	})
	if len(loggers) == 0 || loggers[0] == nil {
		return mux, nil
	}
	return oauthRequestLogger{next: mux, logger: loggers[0], resourcePath: resourcePath, metadataPath: metadataPath}, nil
}

type oauthRequestLogger struct {
	next         http.Handler
	logger       observability.LogProvider
	resourcePath string
	metadataPath string
}

func (h oauthRequestLogger) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	responseWriter, delivery := densehttp.NewDeliveryResponseWriter(writer)
	oauthMeta := &oauthObservation{}
	requestContext := correlation.WithID(request.Context(), uuid.NewString())
	if authorization := request.Header.Values("Authorization"); len(authorization) == 1 {
		if raw, ok := parseHarnessBearer(authorization[0]); ok {
			requestContext = observability.WithAuthenticationSecrets(requestContext, raw)
		}
	}
	requestContext = context.WithValue(requestContext, oauthObservationContextKey{}, oauthMeta)
	request = request.WithContext(requestContext)
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		h.next.ServeHTTP(responseWriter, request)
	}()
	panicked := recovered != nil
	status := delivery.Status()
	if panicked && status == 0 {
		http.Error(responseWriter, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		status = delivery.Status()
	}
	if status == 0 {
		status = http.StatusOK
	}
	logContext := context.WithoutCancel(request.Context())
	if panicked {
		attrs := []observability.LogAttr{
			observability.String("route", canonicalOAuthRoute(request.URL.Path, h.resourcePath, h.metadataPath)),
			observability.String("correlation_id", correlation.FromContext(request.Context())),
		}
		if contextual, ok := h.logger.(interface {
			ErrorContext(context.Context, string, error, ...observability.LogAttr)
		}); ok {
			contextual.ErrorContext(logContext, "oauth_handler_panic", boundedOAuthPanicError(logContext, recovered), attrs...)
		} else {
			h.logger.Error("oauth_handler_panic", boundedOAuthPanicError(logContext, recovered), attrs...)
		}
	}
	attrs := []observability.LogAttr{
		observability.String("method", request.Method),
		observability.String("route", canonicalOAuthRoute(request.URL.Path, h.resourcePath, h.metadataPath)),
		observability.Int("status", status),
		observability.String("transport_status", densehttp.TransportStatus(status)),
		observability.Int("duration_ms", int(time.Since(started).Milliseconds())),
		observability.String("delivery_stage", delivery.Stage(request.Context(), nil)),
		observability.String("caller_receipt", "unknown"),
		observability.Int("write_bytes", int(delivery.WriteBytes())),
		observability.String("correlation_id", correlation.FromContext(request.Context())),
	}
	if oauthMeta.profile != "" {
		attrs = append(attrs,
			observability.String("profile", oauthMeta.profile),
			observability.Int("scope_count", oauthMeta.scopeCount),
			observability.Bool("team_claim_present", oauthMeta.teamClaimPresent),
		)
	}
	if status >= http.StatusBadRequest {
		if contextual, ok := h.logger.(interface {
			WarnContext(context.Context, string, ...observability.LogAttr)
		}); ok {
			contextual.WarnContext(logContext, "oauth_http_request", attrs...)
		} else {
			h.logger.Warn("oauth_http_request", attrs...)
		}
		return
	}
	if contextual, ok := h.logger.(interface {
		InfoContext(context.Context, string, ...observability.LogAttr)
	}); ok {
		contextual.InfoContext(logContext, "oauth_http_request", attrs...)
	} else {
		h.logger.Info("oauth_http_request", attrs...)
	}
}

func boundedOAuthPanicError(ctx context.Context, recovered any) error {
	protected := observability.NewCredentialProtector().Snapshot(
		recovered,
		observability.MaxOperationMetadataBytes,
		observability.AuthenticationSecretsFromContext(ctx)...,
	)
	if protected.UnavailableReason != 0 {
		return errors.New("handler panic")
	}
	cause := strings.TrimSpace(fmt.Sprint(protected.Value))
	if cause == "" || cause == "<nil>" {
		return errors.New("handler panic")
	}
	return errors.New(cause)
}

func canonicalOAuthRoute(requestPath, resourcePath, metadataPath string) string {
	switch {
	case requestPath == "/health":
		return "/health"
	case requestPath == metadataPath:
		return "/.well-known/oauth-protected-resource/:resource"
	case requestPath == resourcePath:
		return "/oauth-resource"
	default:
		return "/unmatched"
	}
}

func validatePublicBaseURL(raw string) (string, error) {
	if raw == "" || len(raw) > 2048 || raw != strings.TrimSpace(raw) {
		return "", fmt.Errorf("public base URL must be non-empty and exact")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", fmt.Errorf("trusted HTTPS public base URL is required")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("trusted HTTPS public base URL is required")
	}
	if port := parsed.Port(); port != "" {
		parsedPort, err := strconv.ParseUint(port, 10, 16)
		if err != nil || parsedPort == 0 {
			return "", fmt.Errorf("trusted HTTPS public base URL is required")
		}
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
	if errors.Is(err, accessservice.ErrOAuthProviderUnavailable) {
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
