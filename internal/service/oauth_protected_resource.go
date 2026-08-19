package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

const (
	oauthJWTHeaderLimit       = 16 * 1024
	oauthJWTClaimsLimit       = 256 * 1024
	oauthEncodedTokenLimit    = 512 * 1024
	oauthMaximumProfiles      = 16
	oauthMaximumAudiences     = 16
	oauthMaximumScopeMappings = 32
	oauthMaximumMappedScopes  = 16
	oauthClockLeeway          = time.Minute
	oauthProviderTimeout      = 10 * time.Second
)

type OAuthTokenInvalidError struct{}

func (OAuthTokenInvalidError) Error() string { return "oauth access token invalid" }

type OAuthTokenExpiredError struct{}

func (OAuthTokenExpiredError) Error() string { return "oauth access token expired" }

type OAuthProviderUnavailableError struct{}

func (OAuthProviderUnavailableError) Error() string { return "oauth provider unavailable" }

var (
	ErrOAuthTokenInvalid        error = OAuthTokenInvalidError{}
	ErrOAuthTokenExpired        error = OAuthTokenExpiredError{}
	ErrOAuthProviderUnavailable error = OAuthProviderUnavailableError{}
)

type OAuthProtectedResourceValidatorOptions struct {
	HTTPClient *http.Client
	Now        func() time.Time
}

type OAuthProtectedResourceValidator struct {
	profilesMu sync.RWMutex
	profiles   map[string]domain.OAuthProtectedResourceProfile

	cachesMu sync.Mutex
	caches   map[string]*oauthJWKSCache

	httpClient *http.Client
	now        func() time.Time
}

func NewOAuthProtectedResourceValidator(profiles []domain.OAuthProtectedResourceProfile, options OAuthProtectedResourceValidatorOptions) (*OAuthProtectedResourceValidator, error) {
	client := boundedOAuthHTTPClient(options.HTTPClient)
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	validator := &OAuthProtectedResourceValidator{
		profiles:   make(map[string]domain.OAuthProtectedResourceProfile),
		caches:     make(map[string]*oauthJWKSCache),
		httpClient: client,
		now:        now,
	}
	if err := validator.Configure(profiles); err != nil {
		return nil, err
	}
	return validator, nil
}

func (validator *OAuthProtectedResourceValidator) Configure(profiles []domain.OAuthProtectedResourceProfile) error {
	configured, err := validateOAuthProfiles(profiles)
	if err != nil {
		return err
	}

	validator.profilesMu.Lock()
	validator.profiles = configured
	validator.profilesMu.Unlock()

	validator.cachesMu.Lock()
	for name := range validator.caches {
		if _, exists := configured[name]; !exists {
			delete(validator.caches, name)
		}
	}
	validator.cachesMu.Unlock()
	return nil
}

func (validator *OAuthProtectedResourceValidator) Validate(ctx context.Context, raw string) (*domain.OAuthValidatedToken, error) {
	headerJSON, claimsJSON, err := strictOAuthJWTParts(raw)
	if err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	if err := jsonstrict.RejectDuplicateFields(headerJSON); err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	if err := jsonstrict.RejectDuplicateFields(claimsJSON); err != nil {
		return nil, ErrOAuthTokenInvalid
	}

	var unverified map[string]json.RawMessage
	if err := json.Unmarshal(claimsJSON, &unverified); err != nil || unverified == nil {
		return nil, ErrOAuthTokenInvalid
	}
	issuer, err := requiredExactOAuthString(unverified, "iss")
	if err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	profile, ok := validator.profileForIssuer(issuer)
	if !ok {
		return nil, ErrOAuthTokenInvalid
	}

	algorithms := make([]jose.SignatureAlgorithm, 0, len(profile.ProtectedResource.Algorithms))
	for _, algorithm := range profile.ProtectedResource.Algorithms {
		algorithms = append(algorithms, jose.SignatureAlgorithm(algorithm))
	}
	parsed, err := jwt.ParseSigned(raw, algorithms)
	if err != nil || len(parsed.Headers) != 1 {
		return nil, ErrOAuthTokenInvalid
	}
	header := parsed.Headers[0]
	key, err := validator.verificationKey(ctx, profile, header.KeyID, header.Algorithm)
	if err != nil {
		return nil, err
	}

	var registered jwt.Claims
	var verified map[string]json.RawMessage
	if err := parsed.Claims(key, &registered, &verified); err != nil || verified == nil {
		return nil, ErrOAuthTokenInvalid
	}
	verifiedIssuer, err := requiredExactOAuthString(verified, "iss")
	if err != nil || verifiedIssuer != profile.Issuer {
		return nil, ErrOAuthTokenInvalid
	}
	subject, err := requiredExactOAuthString(verified, "sub")
	if err != nil || registered.Expiry == nil || len(registered.Audience) == 0 {
		return nil, ErrOAuthTokenInvalid
	}
	if err := registered.ValidateWithLeeway(jwt.Expected{
		Issuer:      profile.Issuer,
		AnyAudience: jwt.Audience(profile.ProtectedResource.Audiences),
		Time:        validator.now().UTC(),
	}, oauthClockLeeway); err != nil {
		if errors.Is(err, jwt.ErrExpired) {
			return nil, ErrOAuthTokenExpired
		}
		return nil, ErrOAuthTokenInvalid
	}

	externalScopes, err := oauthScopeValues(verified, profile.ProtectedResource.ScopeClaim)
	if err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	team, err := optionalExactOAuthString(verified, profile.ProtectedResource.TeamClaim)
	if err != nil {
		return nil, ErrOAuthTokenInvalid
	}
	return &domain.OAuthValidatedToken{
		ProfileName: profile.Name,
		Issuer:      profile.Issuer,
		Subject:     subject,
		Audiences:   append([]string(nil), registered.Audience...),
		Scopes:      mapOAuthProtectedResourceScopes(externalScopes, profile.ProtectedResource.ScopeMappings),
		Team:        team,
		ExpiresAt:   registered.Expiry.Time().UTC(),
	}, nil
}

func (validator *OAuthProtectedResourceValidator) profileForIssuer(issuer string) (domain.OAuthProtectedResourceProfile, bool) {
	validator.profilesMu.RLock()
	defer validator.profilesMu.RUnlock()
	for _, profile := range validator.profiles {
		if profile.Issuer == issuer {
			return cloneOAuthProfile(profile), true
		}
	}
	return domain.OAuthProtectedResourceProfile{}, false
}

func strictOAuthJWTParts(raw string) ([]byte, []byte, error) {
	if raw == "" || len(raw) > oauthEncodedTokenLimit || strings.TrimSpace(raw) != raw {
		return nil, nil, ErrOAuthTokenInvalid
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, nil, ErrOAuthTokenInvalid
	}
	encoding := base64.RawURLEncoding.Strict()
	header, err := encoding.DecodeString(parts[0])
	if err != nil || len(header) == 0 || len(header) > oauthJWTHeaderLimit {
		return nil, nil, ErrOAuthTokenInvalid
	}
	claims, err := encoding.DecodeString(parts[1])
	if err != nil || len(claims) == 0 || len(claims) > oauthJWTClaimsLimit {
		return nil, nil, ErrOAuthTokenInvalid
	}
	return header, claims, nil
}

func requiredExactOAuthString(claims map[string]json.RawMessage, name string) (string, error) {
	raw, exists := claims[name]
	if !exists {
		return "", ErrOAuthTokenInvalid
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" || value != strings.TrimSpace(value) {
		return "", ErrOAuthTokenInvalid
	}
	return value, nil
}

func optionalExactOAuthString(claims map[string]json.RawMessage, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	if _, exists := claims[name]; !exists {
		return "", nil
	}
	return requiredExactOAuthString(claims, name)
}

func oauthScopeValues(claims map[string]json.RawMessage, name string) ([]string, error) {
	raw, exists := claims[name]
	if !exists {
		return nil, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		values := strings.Fields(encoded)
		if len(values) == 0 {
			return nil, ErrOAuthTokenInvalid
		}
		for _, value := range values {
			if !validOAuthScope(value) {
				return nil, ErrOAuthTokenInvalid
			}
		}
		return uniqueOAuthStrings(values, validOAuthScope), nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, ErrOAuthTokenInvalid
	}
	for _, value := range values {
		if !validOAuthScope(value) {
			return nil, ErrOAuthTokenInvalid
		}
	}
	return uniqueOAuthStrings(values, validOAuthScope), nil
}

func mapOAuthProtectedResourceScopes(external []string, mappings []domain.OAuthScopeMapping) []string {
	externalSet := make(map[string]struct{}, len(external))
	for _, scope := range external {
		externalSet[scope] = struct{}{}
	}
	mapped := make(map[string]struct{})
	for _, mapping := range mappings {
		if _, exists := externalSet[mapping.ExternalScope]; !exists {
			continue
		}
		for _, scope := range mapping.InternalScopes {
			mapped[scope] = struct{}{}
		}
	}
	result := make([]string, 0, len(mapped))
	for scope := range mapped {
		result = append(result, scope)
	}
	sort.Strings(result)
	return result
}

func validateOAuthProfiles(profiles []domain.OAuthProtectedResourceProfile) (map[string]domain.OAuthProtectedResourceProfile, error) {
	if len(profiles) == 0 || len(profiles) > oauthMaximumProfiles {
		return nil, fmt.Errorf("OAuth protected-resource config requires between 1 and %d profiles", oauthMaximumProfiles)
	}
	configured := make(map[string]domain.OAuthProtectedResourceProfile, len(profiles))
	issuers := make(map[string]struct{}, len(profiles))
	for _, source := range profiles {
		profile := cloneOAuthProfile(source)
		if !validBoundedIdentifier(profile.Name, 64) {
			return nil, fmt.Errorf("OAuth profile name is invalid")
		}
		if _, exists := configured[profile.Name]; exists {
			return nil, fmt.Errorf("OAuth profile name %q is duplicated", profile.Name)
		}
		if err := validateOAuthHTTPSURL(profile.Issuer, false); err != nil {
			return nil, fmt.Errorf("OAuth profile %q issuer is invalid: %w", profile.Name, err)
		}
		if _, exists := issuers[profile.Issuer]; exists {
			return nil, fmt.Errorf("OAuth profile issuer %q is duplicated", profile.Issuer)
		}
		if err := validateOAuthProtectedResourceConfig(profile.Name, profile.ProtectedResource); err != nil {
			return nil, err
		}
		configured[profile.Name] = profile
		issuers[profile.Issuer] = struct{}{}
	}
	return configured, nil
}

func validateOAuthProtectedResourceConfig(name string, config domain.OAuthProtectedResourceConfig) error {
	if len(config.Audiences) == 0 || len(config.Audiences) > oauthMaximumAudiences {
		return fmt.Errorf("OAuth profile %q must configure between 1 and %d audiences", name, oauthMaximumAudiences)
	}
	if err := validateUniqueOAuthValues(config.Audiences, 512, validNonControlValue); err != nil {
		return fmt.Errorf("OAuth profile %q audiences are invalid: %w", name, err)
	}
	switch config.JWKSSource {
	case "discovery":
		if config.JWKSURI != "" {
			return fmt.Errorf("OAuth profile %q discovery source cannot configure jwks_uri", name)
		}
	case "static":
		if err := validateOAuthHTTPSURL(config.JWKSURI, true); err != nil {
			return fmt.Errorf("OAuth profile %q jwks_uri is invalid: %w", name, err)
		}
	default:
		return fmt.Errorf("OAuth profile %q jwks_source must be discovery or static", name)
	}
	if len(config.Algorithms) == 0 || len(config.Algorithms) > 3 {
		return fmt.Errorf("OAuth profile %q must configure between 1 and 3 algorithms", name)
	}
	algorithms := make(map[string]struct{}, len(config.Algorithms))
	for _, algorithm := range config.Algorithms {
		if algorithm != string(jose.RS256) && algorithm != string(jose.PS256) && algorithm != string(jose.ES256) {
			return fmt.Errorf("OAuth profile %q algorithm %q is not allowed", name, algorithm)
		}
		if _, exists := algorithms[algorithm]; exists {
			return fmt.Errorf("OAuth profile %q algorithm %q is duplicated", name, algorithm)
		}
		algorithms[algorithm] = struct{}{}
	}
	if !validOAuthClaimName(config.ScopeClaim) {
		return fmt.Errorf("OAuth profile %q scope_claim is invalid", name)
	}
	if config.TeamClaim != "" && !validOAuthClaimName(config.TeamClaim) {
		return fmt.Errorf("OAuth profile %q team_claim is invalid", name)
	}
	if len(config.ScopeMappings) > oauthMaximumScopeMappings {
		return fmt.Errorf("OAuth profile %q has more than %d scope mappings", name, oauthMaximumScopeMappings)
	}
	externalScopes := make(map[string]struct{}, len(config.ScopeMappings))
	for _, mapping := range config.ScopeMappings {
		if !validOAuthScope(mapping.ExternalScope) {
			return fmt.Errorf("OAuth profile %q external scope is invalid", name)
		}
		if _, exists := externalScopes[mapping.ExternalScope]; exists {
			return fmt.Errorf("OAuth profile %q external scope %q is duplicated", name, mapping.ExternalScope)
		}
		if len(mapping.InternalScopes) == 0 || len(mapping.InternalScopes) > oauthMaximumMappedScopes {
			return fmt.Errorf("OAuth profile %q mapping %q must have between 1 and %d internal scopes", name, mapping.ExternalScope, oauthMaximumMappedScopes)
		}
		if err := validateUniqueOAuthValues(mapping.InternalScopes, 128, validOAuthScope); err != nil {
			return fmt.Errorf("OAuth profile %q mapping %q is invalid: %w", name, mapping.ExternalScope, err)
		}
		externalScopes[mapping.ExternalScope] = struct{}{}
	}
	return nil
}

func validateUniqueOAuthValues(values []string, maximumLength int, validate func(string) bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if len(value) > maximumLength || !validate(value) {
			return fmt.Errorf("value is invalid")
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("value %q is duplicated", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateOAuthHTTPSURL(raw string, allowQuery bool) error {
	if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw {
		return fmt.Errorf("HTTPS URL must be non-empty and exact")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return fmt.Errorf("trusted HTTPS URL is required")
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("trusted HTTPS URL is required")
	}
	if port := parsed.Port(); port != "" {
		parsedPort, err := strconv.ParseUint(port, 10, 16)
		if err != nil || parsedPort == 0 {
			return fmt.Errorf("trusted HTTPS URL is required")
		}
	}
	if !allowQuery && parsed.RawQuery != "" {
		return fmt.Errorf("query is not allowed")
	}
	return nil
}

func validBoundedIdentifier(value string, maximumLength int) bool {
	return value != "" && len(value) <= maximumLength && value == strings.TrimSpace(value) && validNonControlValue(value)
}

func validNonControlValue(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validOAuthClaimName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func validOAuthScope(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range []byte(value) {
		if character == 0x21 || (character >= 0x23 && character <= 0x5b) || (character >= 0x5d && character <= 0x7e) {
			continue
		}
		return false
	}
	return true
}

func uniqueOAuthStrings(values []string, validate func(string) bool) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if validate(value) {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloneOAuthProfile(source domain.OAuthProtectedResourceProfile) domain.OAuthProtectedResourceProfile {
	clone := source
	clone.ProtectedResource.Audiences = append([]string(nil), source.ProtectedResource.Audiences...)
	clone.ProtectedResource.Algorithms = append([]string(nil), source.ProtectedResource.Algorithms...)
	clone.ProtectedResource.ScopeMappings = append([]domain.OAuthScopeMapping(nil), source.ProtectedResource.ScopeMappings...)
	for index := range clone.ProtectedResource.ScopeMappings {
		clone.ProtectedResource.ScopeMappings[index].InternalScopes = append([]string(nil), source.ProtectedResource.ScopeMappings[index].InternalScopes...)
	}
	return clone
}

func boundedOAuthHTTPClient(source *http.Client) *http.Client {
	var client http.Client
	if source != nil {
		client = *source
	}
	if client.Timeout <= 0 || client.Timeout > oauthProviderTimeout {
		client.Timeout = oauthProviderTimeout
	}
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return fmt.Errorf("too many OAuth provider redirects")
		}
		if err := validateOAuthHTTPSURL(request.URL.String(), true); err != nil {
			return err
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		return nil
	}
	return &client
}
