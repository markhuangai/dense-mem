package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestOAuthProtectedResourceValidatorAcceptsProviderProfiles(t *testing.T) {
	fixture := newOAuthCompatibilityFixture(t)
	validator := fixture.validator(t)

	tests := []struct {
		profile string
		scopes  any
		want    []string
	}{
		{profile: "entra", scopes: "memory.read memory.write", want: []string{"read", "write"}},
		{profile: "pingone", scopes: "ping.read ping.write", want: []string{"read", "write"}},
		{profile: "generic", scopes: []string{"generic.read", "generic.write"}, want: []string{"read", "write"}},
	}
	for _, test := range tests {
		t.Run(test.profile, func(t *testing.T) {
			claims := fixture.claims(test.profile)
			claims[fixture.scopeClaim(test.profile)] = test.scopes
			claims["tenant"] = "team-a"
			validated, err := validator.Validate(t.Context(), fixture.token(t, test.profile, "primary", claims))
			require.NoError(t, err)
			require.Equal(t, test.profile, validated.ProfileName)
			require.Equal(t, fixture.issuer(test.profile), validated.Issuer)
			require.Equal(t, test.want, validated.Scopes)
			require.Equal(t, "team-a", validated.Team)
			require.Equal(t, fixture.now.Add(5*time.Minute), validated.ExpiresAt)
		})
	}
}

func TestOAuthProtectedResourceValidatorRejectsInvalidTokensWithTypedErrors(t *testing.T) {
	fixture := newOAuthCompatibilityFixture(t)
	validator := fixture.validator(t)
	valid := fixture.claims("entra")

	for name, mutate := range map[string]func(map[string]any){
		"missing issuer":  func(claims map[string]any) { delete(claims, "iss") },
		"wrong issuer":    func(claims map[string]any) { claims["iss"] = fixture.issuer("entra") + "/" },
		"missing subject": func(claims map[string]any) { delete(claims, "sub") },
		"blank subject":   func(claims map[string]any) { claims["sub"] = " " },
		"missing audience": func(claims map[string]any) {
			delete(claims, "aud")
		},
		"wrong audience": func(claims map[string]any) { claims["aud"] = "wrong" },
		"missing expiry": func(claims map[string]any) { delete(claims, "exp") },
		"future nbf":     func(claims map[string]any) { claims["nbf"] = fixture.now.Add(2 * time.Minute).Unix() },
		"future iat":     func(claims map[string]any) { claims["iat"] = fixture.now.Add(2 * time.Minute).Unix() },
		"invalid scopes": func(claims map[string]any) { claims["scp"] = []any{"memory.read", 1} },
		"invalid string scope": func(claims map[string]any) {
			claims["scp"] = "memory.read ☃"
		},
		"blank team": func(claims map[string]any) { claims["tenant"] = " " },
	} {
		t.Run(name, func(t *testing.T) {
			claims := cloneOAuthClaims(valid)
			mutate(claims)
			_, err := validator.Validate(t.Context(), fixture.token(t, "entra", "primary", claims))
			require.ErrorIs(t, err, ErrOAuthTokenInvalid)
			var typed OAuthTokenInvalidError
			require.True(t, errors.As(err, &typed))
		})
	}

	expired := cloneOAuthClaims(valid)
	expired["exp"] = fixture.now.Add(-2 * time.Minute).Unix()
	_, err := validator.Validate(t.Context(), fixture.token(t, "entra", "primary", expired))
	require.ErrorIs(t, err, ErrOAuthTokenExpired)
	var typedExpired OAuthTokenExpiredError
	require.True(t, errors.As(err, &typedExpired))

	for name, raw := range map[string]string{
		"malformed":        "not.a.jwt",
		"duplicate header": duplicateOAuthJWT(`{"alg":"RS256","alg":"RS256","kid":"primary"}`, `{"iss":"x"}`),
		"duplicate claim":  duplicateOAuthJWT(`{"alg":"RS256","kid":"primary"}`, `{"iss":"x","iss":"x"}`),
		"oversized header": base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", oauthJWTHeaderLimit+1))) + ".e30.signature",
		"oversized claims": "e30." + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", oauthJWTClaimsLimit+1))) + ".signature",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validator.Validate(t.Context(), raw)
			require.ErrorIs(t, err, ErrOAuthTokenInvalid)
		})
	}

	psClaims := fixture.claims("entra")
	_, err = validator.Validate(t.Context(), fixture.tokenWithAlgorithm(t, "entra", "primary", jose.PS256, psClaims))
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)
}

func TestOAuthProtectedResourceValidatorRotatesKeysAndBoundsOutageRefreshes(t *testing.T) {
	fixture := newOAuthCompatibilityFixture(t)
	validator := fixture.validator(t)

	_, err := validator.Validate(t.Context(), fixture.token(t, "generic", "primary", fixture.claims("generic")))
	require.NoError(t, err)
	require.Equal(t, 1, fixture.jwksRequests("generic"))

	fixture.setActiveKey("generic", "secondary")
	fixture.advance(2 * time.Second)
	rotated := fixture.token(t, "generic", "secondary", fixture.claims("generic"))
	_, err = validator.Validate(t.Context(), rotated)
	require.NoError(t, err)
	require.Equal(t, 2, fixture.jwksRequests("generic"))

	fixture.setOutage("generic", true)
	_, err = validator.Validate(t.Context(), rotated)
	require.NoError(t, err, "a cached known key must remain usable during an outage")

	fixture.advance(2 * time.Second)
	unknown := fixture.token(t, "generic", "future", fixture.claims("generic"))
	_, err = validator.Validate(t.Context(), unknown)
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	var typed OAuthProviderUnavailableError
	require.True(t, errors.As(err, &typed))
	hitsAfterFailure := fixture.jwksRequests("generic")
	_, err = validator.Validate(t.Context(), unknown)
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	require.Equal(t, hitsAfterFailure, fixture.jwksRequests("generic"), "refresh failures must be throttled")
}

func TestOAuthProtectedResourceValidatorSharedRefreshSurvivesCallerCancellation(t *testing.T) {
	fixture := newOAuthCompatibilityFixture(t)
	profile := fixture.profiles()[1]
	validator := fixture.validatorForProfiles(t, []domain.OAuthProtectedResourceProfile{profile})
	cache := validator.cacheForProfile(profile.Name)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := validator.loadOAuthJWKS(canceled, profile, cache, false)
	require.NoError(t, err)

	set, fromCache, err := validator.loadOAuthJWKS(t.Context(), profile, cache, false)
	require.NoError(t, err)
	require.True(t, fromCache)
	require.NotEmpty(t, set.Keys)
	require.Equal(t, 1, fixture.jwksRequests(profile.Name))
}

func TestOAuthProtectedResourceValidatorInvalidatesCacheWhenSourceChanges(t *testing.T) {
	fixture := newOAuthCompatibilityFixture(t)
	profiles := fixture.profiles()
	validator := fixture.validatorForProfiles(t, profiles)

	_, err := validator.Validate(t.Context(), fixture.token(t, "entra", "primary", fixture.claims("entra")))
	require.NoError(t, err)

	fixture.setActiveKey("entra", "secondary")
	profiles[0].ProtectedResource.JWKSSource = "static"
	profiles[0].ProtectedResource.JWKSURI = fixture.server.URL + "/entra/alternate-jwks"
	require.NoError(t, validator.Configure(profiles))
	_, err = validator.Validate(t.Context(), fixture.token(t, "entra", "secondary", fixture.claims("entra")))
	require.NoError(t, err, "source and URI changes must not reuse the discovery cache")

	profiles[0].Issuer = fixture.server.URL + "/entra-moved"
	profiles[0].ProtectedResource.JWKSURI = fixture.server.URL + "/entra/moved-jwks"
	require.NoError(t, validator.Configure(profiles))
	movedClaims := fixture.claims("entra")
	movedClaims["iss"] = profiles[0].Issuer
	_, err = validator.Validate(t.Context(), fixture.token(t, "entra", "secondary", movedClaims))
	require.NoError(t, err, "issuer changes must not reuse a cache with the old fingerprint")
	_, err = validator.Validate(t.Context(), fixture.token(t, "entra", "secondary", fixture.claims("entra")))
	require.ErrorIs(t, err, ErrOAuthTokenInvalid, "issuer identifiers must compare exactly")
}

func TestOAuthProtectedResourceValidatorRejectsInvalidConfiguration(t *testing.T) {
	fixture := newOAuthCompatibilityFixture(t)
	base := fixture.profiles()[0]

	mutations := map[string]func(*domain.OAuthProtectedResourceProfile){
		"http issuer":       func(profile *domain.OAuthProtectedResourceProfile) { profile.Issuer = "http://issuer.example" },
		"issuer whitespace": func(profile *domain.OAuthProtectedResourceProfile) { profile.Issuer += " " },
		"no audiences":      func(profile *domain.OAuthProtectedResourceProfile) { profile.ProtectedResource.Audiences = nil },
		"unknown source":    func(profile *domain.OAuthProtectedResourceProfile) { profile.ProtectedResource.JWKSSource = "inline" },
		"static without URI": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.JWKSSource = "static"
			profile.ProtectedResource.JWKSURI = ""
		},
		"http JWKS": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.JWKSSource = "static"
			profile.ProtectedResource.JWKSURI = "http://issuer.example/jwks"
		},
		"unsafe algorithm": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.Algorithms = []string{"none"}
		},
		"duplicate mapping": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.ScopeMappings = append(profile.ProtectedResource.ScopeMappings, profile.ProtectedResource.ScopeMappings[0])
		},
		"invalid claim": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.ScopeClaim = "scope claim"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			profile := base
			profile.ProtectedResource = cloneProtectedResourceConfig(base.ProtectedResource)
			mutate(&profile)
			_, err := NewOAuthProtectedResourceValidator([]domain.OAuthProtectedResourceProfile{profile}, OAuthProtectedResourceValidatorOptions{HTTPClient: fixture.server.Client()})
			require.Error(t, err)
		})
	}

	duplicate := base
	duplicate.ProtectedResource = cloneProtectedResourceConfig(base.ProtectedResource)
	_, err := NewOAuthProtectedResourceValidator([]domain.OAuthProtectedResourceProfile{base, duplicate}, OAuthProtectedResourceValidatorOptions{HTTPClient: fixture.server.Client()})
	require.Error(t, err)
}

func TestOAuthProtectedResourceValidatorBoundsProviderDocumentsAndClosesBodies(t *testing.T) {
	fixture := newOAuthCompatibilityFixture(t)
	profile := fixture.profiles()[0]

	closed := false
	client := &http.Client{Transport: oauthValidatorRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: &oauthTrackingBody{
				Reader: strings.NewReader(strings.Repeat("x", oauthProviderDocumentLimit+1)),
				closed: &closed,
			},
		}, nil
	})}
	validator, err := NewOAuthProtectedResourceValidator([]domain.OAuthProtectedResourceProfile{profile}, OAuthProtectedResourceValidatorOptions{HTTPClient: client, Now: func() time.Time { return fixture.now }})
	require.NoError(t, err)
	_, err = validator.Validate(t.Context(), fixture.token(t, "entra", "primary", fixture.claims("entra")))
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	require.True(t, closed)

	fixture.setDuplicateDiscovery("entra", true)
	validator = fixture.validator(t)
	_, err = validator.Validate(t.Context(), fixture.token(t, "entra", "primary", fixture.claims("entra")))
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
}

func cloneOAuthClaims(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneProtectedResourceConfig(source domain.OAuthProtectedResourceConfig) domain.OAuthProtectedResourceConfig {
	clone := source
	clone.Audiences = append([]string(nil), source.Audiences...)
	clone.Algorithms = append([]string(nil), source.Algorithms...)
	clone.ScopeMappings = append([]domain.OAuthScopeMapping(nil), source.ScopeMappings...)
	for index := range clone.ScopeMappings {
		clone.ScopeMappings[index].InternalScopes = append([]string(nil), source.ScopeMappings[index].InternalScopes...)
	}
	return clone
}

func duplicateOAuthJWT(header, claims string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature"
}

type oauthValidatorRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip oauthValidatorRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type oauthTrackingBody struct {
	io.Reader
	closed *bool
}

func (body *oauthTrackingBody) Close() error {
	*body.closed = true
	return nil
}

type oauthCompatibilityFixture struct {
	server         *httptest.Server
	now            time.Time
	mu             sync.Mutex
	providerStates map[string]*oauthFixtureProfile
}

type oauthFixtureProfile struct {
	algorithm          jose.SignatureAlgorithm
	audience           string
	activeKey          string
	keys               map[string]any
	outage             bool
	duplicateDiscovery bool
	jwksRequests       int
}

func newOAuthCompatibilityFixture(t *testing.T) *oauthCompatibilityFixture {
	t.Helper()
	rsaPrimary, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	rsaSecondary, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	rsaFuture, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ecPrimary, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecSecondary, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecFuture, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	fixture := &oauthCompatibilityFixture{
		now: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
		providerStates: map[string]*oauthFixtureProfile{
			"entra":   {algorithm: jose.RS256, audience: "dense-mem-entra", activeKey: "primary", keys: map[string]any{"primary": rsaPrimary, "secondary": rsaSecondary, "future": rsaFuture}},
			"pingone": {algorithm: jose.PS256, audience: "urn:dense-mem:pingone", activeKey: "primary", keys: map[string]any{"primary": rsaPrimary, "secondary": rsaSecondary, "future": rsaFuture}},
			"generic": {algorithm: jose.ES256, audience: "https://dense-mem.example.test/mcp", activeKey: "primary", keys: map[string]any{"primary": ecPrimary, "secondary": ecSecondary, "future": ecFuture}},
		},
	}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *oauthCompatibilityFixture) profiles() []domain.OAuthProtectedResourceProfile {
	return []domain.OAuthProtectedResourceProfile{
		{Name: "entra", Issuer: fixture.issuer("entra"), ProtectedResource: domain.OAuthProtectedResourceConfig{
			Audiences: []string{"dense-mem-entra"}, JWKSSource: "discovery", Algorithms: []string{"RS256"}, ScopeClaim: "scp", TeamClaim: "tenant",
			ScopeMappings: []domain.OAuthScopeMapping{{ExternalScope: "memory.read", InternalScopes: []string{"read"}}, {ExternalScope: "memory.write", InternalScopes: []string{"write"}}},
		}},
		{Name: "pingone", Issuer: fixture.issuer("pingone"), ProtectedResource: domain.OAuthProtectedResourceConfig{
			Audiences: []string{"urn:dense-mem:pingone"}, JWKSSource: "static", JWKSURI: fixture.server.URL + "/pingone/jwks", Algorithms: []string{"PS256"}, ScopeClaim: "scope", TeamClaim: "tenant",
			ScopeMappings: []domain.OAuthScopeMapping{{ExternalScope: "ping.read", InternalScopes: []string{"read"}}, {ExternalScope: "ping.write", InternalScopes: []string{"write"}}},
		}},
		{Name: "generic", Issuer: fixture.issuer("generic"), ProtectedResource: domain.OAuthProtectedResourceConfig{
			Audiences: []string{"https://dense-mem.example.test/mcp"}, JWKSSource: "discovery", Algorithms: []string{"ES256"}, ScopeClaim: "permissions", TeamClaim: "tenant",
			ScopeMappings: []domain.OAuthScopeMapping{{ExternalScope: "generic.read", InternalScopes: []string{"read"}}, {ExternalScope: "generic.write", InternalScopes: []string{"write"}}},
		}},
	}
}

func (fixture *oauthCompatibilityFixture) validator(t *testing.T) *OAuthProtectedResourceValidator {
	return fixture.validatorForProfiles(t, fixture.profiles())
}

func (fixture *oauthCompatibilityFixture) validatorForProfiles(t *testing.T, profiles []domain.OAuthProtectedResourceProfile) *OAuthProtectedResourceValidator {
	t.Helper()
	validator, err := NewOAuthProtectedResourceValidator(profiles, OAuthProtectedResourceValidatorOptions{
		HTTPClient: fixture.server.Client(),
		Now:        func() time.Time { return fixture.currentTime() },
	})
	require.NoError(t, err)
	return validator
}

func (fixture *oauthCompatibilityFixture) issuer(profile string) string {
	issuer := fixture.server.URL + "/" + profile
	if profile == "generic" {
		return issuer + "/"
	}
	return issuer
}

func (fixture *oauthCompatibilityFixture) scopeClaim(profile string) string {
	if profile == "entra" {
		return "scp"
	}
	if profile == "pingone" {
		return "scope"
	}
	return "permissions"
}

func (fixture *oauthCompatibilityFixture) claims(profile string) map[string]any {
	return map[string]any{
		"iss": fixture.issuer(profile), "sub": profile + "-user", "aud": fixture.providerStates[profile].audience,
		"iat": fixture.currentTime().Add(-time.Minute).Unix(), "nbf": fixture.currentTime().Add(-time.Second).Unix(), "exp": fixture.currentTime().Add(5 * time.Minute).Unix(),
	}
}

func (fixture *oauthCompatibilityFixture) token(t *testing.T, profile, keyID string, claims map[string]any) string {
	return fixture.tokenWithAlgorithm(t, profile, keyID, fixture.providerStates[profile].algorithm, claims)
}

func (fixture *oauthCompatibilityFixture) tokenWithAlgorithm(t *testing.T, profile, keyID string, algorithm jose.SignatureAlgorithm, claims map[string]any) string {
	t.Helper()
	fixture.mu.Lock()
	key := fixture.providerStates[profile].keys[keyID]
	fixture.mu.Unlock()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: algorithm, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID))
	require.NoError(t, err)
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

func (fixture *oauthCompatibilityFixture) handle(response http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) < 2 {
		http.NotFound(response, request)
		return
	}
	profile := fixture.providerStates[parts[0]]
	if profile == nil || profile.outage {
		http.Error(response, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if strings.HasSuffix(request.URL.Path, "/.well-known/openid-configuration") {
		issuer := fixture.issuer(parts[0])
		if parts[0] == "entra-moved" {
			issuer = fixture.server.URL + "/entra-moved"
		}
		if profile.duplicateDiscovery {
			_, _ = fmt.Fprintf(response, `{"issuer":%q,"issuer":%q,"jwks_uri":%q}`, issuer, issuer, fixture.server.URL+"/"+parts[0]+"/jwks")
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"issuer": issuer, "jwks_uri": fixture.server.URL + "/" + parts[0] + "/jwks", "authorization_endpoint": fixture.server.URL + "/authorize",
		})
		return
	}
	if strings.HasSuffix(request.URL.Path, "/jwks") || strings.HasSuffix(request.URL.Path, "/alternate-jwks") || strings.HasSuffix(request.URL.Path, "/moved-jwks") {
		profile.jwksRequests++
		keyID := profile.activeKey
		key := profile.keys[keyID]
		public := publicOAuthKey(key)
		_ = json.NewEncoder(response).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: public, KeyID: keyID, Algorithm: string(profile.algorithm), Use: "sig"}}})
		return
	}
	http.NotFound(response, request)
}

func publicOAuthKey(key any) any {
	switch typed := key.(type) {
	case *rsa.PrivateKey:
		return &typed.PublicKey
	case *ecdsa.PrivateKey:
		return &typed.PublicKey
	default:
		panic("unsupported test key")
	}
}

func (fixture *oauthCompatibilityFixture) setActiveKey(profile, keyID string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.providerStates[profile].activeKey = keyID
}

func (fixture *oauthCompatibilityFixture) setOutage(profile string, outage bool) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.providerStates[profile].outage = outage
}

func (fixture *oauthCompatibilityFixture) setDuplicateDiscovery(profile string, duplicate bool) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.providerStates[profile].duplicateDiscovery = duplicate
}

func (fixture *oauthCompatibilityFixture) jwksRequests(profile string) int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.providerStates[profile].jwksRequests
}

func (fixture *oauthCompatibilityFixture) advance(duration time.Duration) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.now = fixture.now.Add(duration)
}

func (fixture *oauthCompatibilityFixture) currentTime() time.Time {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.now
}
