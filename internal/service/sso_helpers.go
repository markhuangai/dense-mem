package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type oidcLoginClaims struct {
	Subject             string
	Email               string
	DisplayName         string
	Nonce               string
	Groups              []string
	IDTokenClaimNames   []string
	UserInfoClaimNames  []string
	GroupsFromUserInfo  bool
	UserInfoError       string
	UserInfoClaimsError string
}

type SSOSetupErrorCode string

const (
	SSOSetupGroupsMissing     SSOSetupErrorCode = "groups_missing"
	SSOSetupGroupLookupFailed SSOSetupErrorCode = "group_lookup_failed"
	SSOSetupMappingMissing    SSOSetupErrorCode = "mapping_missing"
	SSOSetupEntitlementEmpty  SSOSetupErrorCode = "entitlement_empty"

	ssoEntitlementSourceClaims   = "claims"
	ssoEntitlementSourceEndpoint = "endpoint"
)

type SSOSetupError struct {
	Code SSOSetupErrorCode
	Err  error
}

func NewSSOSetupError(code SSOSetupErrorCode, err error) error {
	if err == nil {
		err = ErrSSOAccessDenied
	}
	return &SSOSetupError{Code: code, Err: err}
}

func (e *SSOSetupError) Error() string {
	if e == nil || e.Err == nil {
		return ErrSSOAccessDenied.Error()
	}
	return e.Err.Error()
}

func (e *SSOSetupError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func SSOSetupErrorMessage(err error) (string, bool) {
	var setupErr *SSOSetupError
	if !errors.As(err, &setupErr) {
		return "", false
	}
	switch setupErr.Code {
	case SSOSetupGroupsMissing:
		return "sso setup failed: no groups found in configured claims", true
	case SSOSetupGroupLookupFailed:
		return "sso setup failed: group lookup failed", true
	case SSOSetupMappingMissing:
		return "sso setup failed: no mapping matched the user groups", true
	case SSOSetupEntitlementEmpty:
		return "sso setup failed: no enabled team entitlement matched", true
	default:
		return "sso setup failed", true
	}
}

func readOIDCClaims(ctx context.Context, provider *oidc.Provider, token *oauth2.Token, idToken *oidc.IDToken, ssoProvider domain.SSOProvider) (oidcLoginClaims, error) {
	var raw map[string]json.RawMessage
	if err := idToken.Claims(&raw); err != nil {
		return oidcLoginClaims{}, fmt.Errorf("failed to parse oidc claims: %w", err)
	}
	claims := oidcLoginClaims{
		Subject:           idToken.Subject,
		Email:             firstClaimString(raw, "email", "preferred_username", "upn"),
		DisplayName:       firstClaimString(raw, "name", "display_name"),
		Nonce:             firstClaimString(raw, "nonce"),
		Groups:            groupsFromRawClaims(raw, ssoProvider.GroupClaims),
		IDTokenClaimNames: rawClaimNames(raw),
	}
	if claims.Email == "" || claims.DisplayName == "" || len(claims.Groups) == 0 {
		info, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
		if err == nil && info != nil {
			var userInfoRaw map[string]json.RawMessage
			if err := info.Claims(&userInfoRaw); err == nil {
				claims.UserInfoClaimNames = rawClaimNames(userInfoRaw)
				if claims.Email == "" {
					claims.Email = firstClaimString(userInfoRaw, "email", "preferred_username", "upn")
				}
				if claims.DisplayName == "" {
					claims.DisplayName = firstClaimString(userInfoRaw, "name", "display_name")
				}
				if len(claims.Groups) == 0 {
					claims.Groups = groupsFromRawClaims(userInfoRaw, ssoProvider.GroupClaims)
					claims.GroupsFromUserInfo = len(claims.Groups) > 0
				}
			} else {
				claims.UserInfoClaimsError = err.Error()
			}
		} else if err != nil {
			claims.UserInfoError = err.Error()
		} else {
			claims.UserInfoError = "userinfo response missing"
		}
	}
	if claims.DisplayName == "" {
		claims.DisplayName = claims.Email
	}
	if claims.DisplayName == "" {
		claims.DisplayName = claims.Subject
	}
	return claims, nil
}

func rawClaimNames(raw map[string]json.RawMessage) []string {
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type HTTPSSOGroupResolver struct {
	HTTPClient *http.Client
}

func (r *HTTPSSOGroupResolver) ResolveGroups(ctx context.Context, provider domain.SSOProvider, subject, accessToken string) ([]string, error) {
	endpoint := strings.TrimSpace(provider.GroupsEndpoint)
	if endpoint == "" {
		return nil, ErrSSOGroupRefreshUnavailable
	}
	token := strings.TrimSpace(accessToken)
	if token == "" {
		credentialToken, err := r.clientCredentialsToken(ctx, provider)
		if err != nil {
			return nil, err
		}
		token = credentialToken
	}

	endpoint = strings.ReplaceAll(endpoint, "{{subject}}", url.PathEscape(subject))
	endpoint = strings.ReplaceAll(endpoint, "{subject}", url.PathEscape(subject))
	method := http.MethodGet
	var body io.Reader
	if provider.Kind == domain.SSOProviderKindAzureAD && strings.Contains(endpoint, "getMemberGroups") {
		method = http.MethodPost
		body = bytes.NewBufferString(`{"securityEnabledOnly":false}`)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}

	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("sso group endpoint returned status %d", resp.StatusCode)
	}
	var payload any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	groups := extractGroupsFromPayload(payload)
	if len(groups) == 0 {
		return nil, ErrSSOAccessDenied
	}
	return groups, nil
}

func (r *HTTPSSOGroupResolver) clientCredentialsToken(ctx context.Context, provider domain.SSOProvider) (string, error) {
	if provider.ClientSecretEnv == "" || len(provider.GroupsScopes) == 0 {
		return "", ErrSSOGroupRefreshUnavailable
	}
	secret := os.Getenv(provider.ClientSecretEnv)
	if strings.TrimSpace(secret) == "" {
		return "", ErrSSOGroupRefreshUnavailable
	}
	oidcProvider, err := oidc.NewProvider(ctx, provider.IssuerURL)
	if err != nil {
		return "", err
	}
	conf := clientcredentials.Config{
		ClientID:     provider.ClientID,
		ClientSecret: secret,
		TokenURL:     oidcProvider.Endpoint().TokenURL,
		Scopes:       provider.GroupsScopes,
	}
	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	token, err := conf.Token(ctx)
	if err != nil {
		return "", err
	}
	if token == nil || token.AccessToken == "" {
		return "", ErrSSOGroupRefreshUnavailable
	}
	return token.AccessToken, nil
}

func HashSSOToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func secureRandomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func hashMatches(raw, expectedHash string) bool {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(expectedHash) == "" {
		return false
	}
	got := HashSSOToken(raw)
	return subtle.ConstantTimeCompare([]byte(got), []byte(expectedHash)) == 1
}

func safeRedirectPath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") {
		return "/ui"
	}
	return trimmed
}

func ssoProfileName(email, displayName string, identityID uuid.UUID) string {
	base := strings.TrimSpace(email)
	if base == "" {
		base = strings.TrimSpace(displayName)
	}
	if base == "" {
		base = identityID.String()
	}
	suffix := identityID.String()
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	name := "SSO " + base + " " + suffix
	if len([]rune(name)) <= 100 {
		return name
	}
	runes := []rune(base)
	limit := 100 - len([]rune("SSO  "+suffix))
	if limit < 8 {
		limit = 8
	}
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return "SSO " + string(runes) + " " + suffix
}

func firstClaimString(raw map[string]json.RawMessage, names ...string) string {
	for _, name := range names {
		data, ok := raw[name]
		if !ok {
			continue
		}
		var value string
		if err := json.Unmarshal(data, &value); err == nil {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func groupsFromRawClaims(raw map[string]json.RawMessage, names []string) []string {
	if len(names) == 0 {
		names = []string{"groups"}
	}
	var groups []string
	for _, name := range names {
		data, ok := raw[name]
		if !ok {
			continue
		}
		var values []string
		if err := json.Unmarshal(data, &values); err == nil {
			groups = append(groups, values...)
			continue
		}
		var single string
		if err := json.Unmarshal(data, &single); err == nil && single != "" {
			groups = append(groups, single)
			continue
		}
		var keyed map[string]json.RawMessage
		if err := json.Unmarshal(data, &keyed); err == nil {
			for key := range keyed {
				groups = append(groups, key)
			}
		}
	}
	return dedupeStrings(groups)
}

func extractGroupsFromPayload(payload any) []string {
	switch value := payload.(type) {
	case []any:
		return groupsFromArray(value)
	case map[string]any:
		for _, key := range []string{"groups", "value", "items", "members"} {
			if child, ok := value[key]; ok {
				if groups := extractGroupsFromPayload(child); len(groups) > 0 {
					return groups
				}
			}
		}
		if embedded, ok := value["_embedded"].(map[string]any); ok {
			for _, child := range embedded {
				if groups := extractGroupsFromPayload(child); len(groups) > 0 {
					return groups
				}
			}
		}
	}
	return nil
}

func groupsFromArray(values []any) []string {
	groups := make([]string, 0, len(values))
	for _, item := range values {
		switch v := item.(type) {
		case string:
			groups = append(groups, v)
		case map[string]any:
			for _, key := range []string{"id", "group_id", "groupId"} {
				if raw, ok := v[key].(string); ok && strings.TrimSpace(raw) != "" {
					groups = append(groups, raw)
					break
				}
			}
		}
	}
	return dedupeStrings(groups)
}

func hasMappingForTeam(mappings []*domain.SSOGroupMapping, teamID uuid.UUID) bool {
	for _, mapping := range mappings {
		if mapping != nil && mapping.Enabled && mapping.TeamID == teamID {
			return true
		}
	}
	return false
}

type teamEntitlement struct {
	Scopes  []string
	Role    string
	GroupID string
}

func mergedEntitlementForTeam(mappings []*domain.SSOGroupMapping, teamID uuid.UUID) (teamEntitlement, bool) {
	entitlement := teamEntitlement{Role: APIKeyRoleMember}
	found := false
	for _, mapping := range mappings {
		if mapping == nil || !mapping.Enabled || mapping.TeamID != teamID {
			continue
		}
		found = true
		entitlement.Scopes = mergeScopes(entitlement.Scopes, mapping.Scopes)
		if normalizeSSORole(mapping.Role) == APIKeyRoleManager {
			entitlement.Role = APIKeyRoleManager
		}
		if entitlement.GroupID == "" {
			entitlement.GroupID = mapping.GroupID
		} else if !strings.Contains(","+entitlement.GroupID+",", ","+mapping.GroupID+",") {
			entitlement.GroupID += "," + mapping.GroupID
		}
	}
	if !found {
		return teamEntitlement{}, false
	}
	if len(entitlement.Scopes) == 0 {
		entitlement.Scopes = []string{APIKeyScopeRead}
	}
	return entitlement, true
}

func normalizeSSOScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return []string{APIKeyScopeRead}
	}
	normalized, err := NormalizeAPIKeyScopes(scopes)
	if err != nil {
		return []string{APIKeyScopeRead}
	}
	return normalized
}

func normalizeSSORole(role string) string {
	normalized, err := NormalizeAPIKeyRole(role)
	if err != nil || normalized == "" {
		return APIKeyRoleMember
	}
	return normalized
}

func mergeScopes(left, right []string) []string {
	set := make(map[string]struct{}, len(left)+len(right))
	for _, scope := range normalizeSSOScopes(left) {
		set[scope] = struct{}{}
	}
	for _, scope := range normalizeSSOScopes(right) {
		set[scope] = struct{}{}
	}
	result := make([]string, 0, len(set))
	if _, ok := set[APIKeyScopeRead]; ok {
		result = append(result, APIKeyScopeRead)
	}
	if _, ok := set[APIKeyScopeWrite]; ok {
		result = append(result, APIKeyScopeWrite)
	}
	return result
}

func dedupeStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func interactiveOAuthScopes(provider domain.SSOProvider) []string {
	scopes := normalizeOAuthScopes(provider.Scopes)
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}
	if strings.TrimSpace(provider.GroupsEndpoint) != "" {
		scopes = appendOAuthScopes(scopes, provider.GroupsScopes...)
	}
	if !containsString(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	return scopes
}

func normalizeOAuthScopes(scopes []string) []string {
	return appendOAuthScopes(nil, scopes...)
}

func appendOAuthScopes(scopes []string, values ...string) []string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" && !containsString(scopes, trimmed) {
			scopes = append(scopes, trimmed)
		}
	}
	return scopes
}

func ssoActiveEntitlementCacheTTL(runtime SSORuntimeConfig, source string) time.Duration {
	if source == ssoEntitlementSourceClaims {
		return runtime.SessionTTL
	}
	return runtime.EntitlementCacheTTL
}

func ssoEntitlementCacheMessage(source string) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	return "source=" + source
}

func ssoKeyRequiresEntitlementValidation(key *domain.APIKey) bool {
	if key == nil {
		return false
	}
	switch strings.TrimSpace(key.AuthSource) {
	case "sso", "hybrid":
		return true
	}
	status := strings.TrimSpace(key.SSOEntitlementStatus)
	return key.SSOIdentityID != nil ||
		key.SSOProviderID != nil ||
		strings.TrimSpace(key.SSOSubject) != "" ||
		strings.TrimSpace(key.SSOGroupID) != "" ||
		(status != "" && status != "unlinked")
}

func ssoRuntimeReadyForPublicLogin(runtime SSORuntimeConfig) bool {
	parsed, err := url.Parse(strings.TrimSpace(runtime.PublicBaseURL))
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func ssoProviderReadyForPublicLogin(provider *domain.SSOProvider) bool {
	if provider == nil || !provider.Enabled {
		return false
	}
	copy := *provider
	if err := normalizeSSOProvider(&copy); err != nil {
		return false
	}
	if copy.ClientSecretEnv != "" && strings.TrimSpace(os.Getenv(copy.ClientSecretEnv)) == "" {
		return false
	}
	return true
}

func normalizeSSOProvider(provider *domain.SSOProvider) error {
	provider.Name = strings.TrimSpace(provider.Name)
	if provider.Name == "" {
		return fmt.Errorf("sso provider name is required")
	}
	if len([]rune(provider.Name)) > 100 {
		return fmt.Errorf("sso provider name must be at most 100 characters")
	}
	switch provider.Kind {
	case domain.SSOProviderKindAzureAD, domain.SSOProviderKindPingOne, domain.SSOProviderKindGenericOIDC:
	default:
		return fmt.Errorf("sso provider kind must be azure_ad, pingone, or generic_oidc")
	}
	provider.IssuerURL = strings.TrimRight(strings.TrimSpace(provider.IssuerURL), "/")
	if provider.IssuerURL == "" {
		return fmt.Errorf("sso issuer_url is required")
	}
	parsed, err := url.Parse(provider.IssuerURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("sso issuer_url must be an absolute URL")
	}
	if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
		return fmt.Errorf("sso issuer_url must use https")
	}
	provider.ClientID = strings.TrimSpace(provider.ClientID)
	if provider.ClientID == "" {
		return fmt.Errorf("sso client_id is required")
	}
	provider.ClientSecretEnv = strings.TrimSpace(provider.ClientSecretEnv)
	provider.GroupsEndpoint = strings.TrimSpace(provider.GroupsEndpoint)
	provider.Scopes = dedupeStrings(provider.Scopes)
	if len(provider.Scopes) == 0 {
		provider.Scopes = []string{"openid", "profile", "email"}
	}
	if !containsString(provider.Scopes, "openid") {
		provider.Scopes = append([]string{"openid"}, provider.Scopes...)
	}
	provider.GroupClaims = dedupeStrings(provider.GroupClaims)
	if len(provider.GroupClaims) == 0 {
		provider.GroupClaims = []string{"groups"}
	}
	provider.GroupsScopes = dedupeStrings(provider.GroupsScopes)
	return nil
}

func normalizeSSOGroupMapping(mapping *domain.SSOGroupMapping) error {
	if mapping.ProviderID == uuid.Nil {
		return fmt.Errorf("sso provider ID is required")
	}
	if mapping.TeamID == uuid.Nil {
		return fmt.Errorf("sso team ID is required")
	}
	mapping.GroupID = strings.TrimSpace(mapping.GroupID)
	if mapping.GroupID == "" {
		return fmt.Errorf("sso group_id is required")
	}
	mapping.GroupName = strings.TrimSpace(mapping.GroupName)
	scopes, err := NormalizeAPIKeyScopes(mapping.Scopes)
	if err != nil {
		return err
	}
	if len(mapping.Scopes) == 0 {
		scopes = []string{APIKeyScopeRead}
	}
	mapping.Scopes = scopes
	role, err := NormalizeAPIKeyRole(mapping.Role)
	if err != nil {
		return err
	}
	if role == "" {
		role = APIKeyRoleMember
	}
	mapping.Role = role
	return nil
}
