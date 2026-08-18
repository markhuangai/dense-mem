package service

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
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
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestAuthenticateOAuthBearerValidatesAndFixesMembershipContext(t *testing.T) {
	fixture := newOAuthProtectedResourceFixture(t)
	token := fixture.token(fixture.key, fixture.keyID, map[string]any{
		"iss":               fixture.issuer,
		"sub":               fixture.identity.Subject,
		"aud":               []string{"unrelated", "api://dense-mem"},
		"exp":               fixture.now.Add(time.Hour).Unix(),
		"nbf":               fixture.now.Add(-time.Minute).Unix(),
		"iat":               fixture.now.Add(-time.Minute).Unix(),
		"scp":               "densemem.read ignored.scope",
		"dense_mem_team_id": fixture.teamID.String(),
	})

	actor, err := fixture.service.AuthenticateOAuthBearer(t.Context(), token, &fixture.teamID)
	require.NoError(t, err)
	require.Equal(t, fixture.teamID, actor.Team.ID)
	require.Equal(t, fixture.identity.ID, actor.Identity.ID)
	require.Equal(t, fixture.membership.Membership.OwnerID, actor.OwnerID)
	require.Equal(t, []string{CredentialScopeRead}, actor.Membership.Grants)
	require.Equal(t, fixture.membership.Membership.MemorySpaceID, actor.Membership.MemorySpaceID)

	otherTeamID := uuid.New()
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), token, &otherTeamID)
	require.ErrorIs(t, err, ErrOAuthAccessDenied)
}

func TestAuthenticateOAuthBearerRequiresUnambiguousTeam(t *testing.T) {
	fixture := newOAuthProtectedResourceFixture(t)
	fixture.repo.teamProfiles = append(fixture.repo.teamProfiles, &domain.SSOTeamMembership{
		Team: domain.Team{ID: uuid.New(), Name: "Other"},
		Membership: domain.Membership{
			ID:              uuid.New(),
			ActorIdentityID: fixture.identity.ID,
			TeamID:          uuid.New(),
			OwnerID:         uuid.New(),
			Grants:          []string{CredentialScopeRead},
			Status:          "active",
		},
	})
	fixture.repo.teamProfiles[1].Membership.TeamID = fixture.repo.teamProfiles[1].Team.ID
	token := fixture.token(fixture.key, fixture.keyID, fixture.claims())

	_, err := fixture.service.AuthenticateOAuthBearer(t.Context(), token, nil)
	require.ErrorIs(t, err, ErrOAuthTeamRequired)
}

func TestAuthenticateOAuthBearerRejectsClaimsBeforeIdentityLookup(t *testing.T) {
	fixture := newOAuthProtectedResourceFixture(t)
	tests := []struct {
		name   string
		claims map[string]any
		want   error
	}{
		{
			name: "wrong audience",
			claims: func() map[string]any {
				claims := fixture.claims()
				claims["aud"] = "wrong-audience"
				return claims
			}(),
			want: ErrOAuthTokenInvalid,
		},
		{
			name: "expired",
			claims: func() map[string]any {
				claims := fixture.claims()
				claims["exp"] = fixture.now.Add(-2 * time.Minute).Unix()
				return claims
			}(),
			want: ErrOAuthTokenExpired,
		},
		{
			name: "future issued at",
			claims: func() map[string]any {
				claims := fixture.claims()
				claims["iat"] = fixture.now.Add(2 * time.Minute).Unix()
				return claims
			}(),
			want: ErrOAuthTokenInvalid,
		},
		{
			name: "malformed team claim",
			claims: func() map[string]any {
				claims := fixture.claims()
				claims["dense_mem_team_id"] = "not-a-uuid"
				return claims
			}(),
			want: ErrOAuthTokenInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token := fixture.token(fixture.key, fixture.keyID, test.claims)
			_, err := fixture.service.AuthenticateOAuthBearer(t.Context(), token, nil)
			require.ErrorIs(t, err, test.want)
		})
	}
}

func TestAuthenticateOAuthBearerRejectsDuplicateClaims(t *testing.T) {
	fixture := newOAuthProtectedResourceFixture(t)
	payload := []byte(`{"iss":"` + fixture.issuer + `","sub":"subject-a","aud":"api://dense-mem","aud":"other","exp":` + jsonNumber(fixture.now.Add(time.Hour).Unix()) + `,"scp":"densemem.read"}`)
	token := signedOAuthRawToken(t, fixture.key, fixture.keyID, payload)

	_, err := fixture.service.AuthenticateOAuthBearer(t.Context(), token, nil)
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)
}

func TestAuthenticateOAuthBearerRefreshesUnknownKeyAndIsolatesOutage(t *testing.T) {
	fixture := newOAuthProtectedResourceFixture(t)
	first := fixture.token(fixture.key, fixture.keyID, fixture.claims())
	_, err := fixture.service.AuthenticateOAuthBearer(t.Context(), first, nil)
	require.NoError(t, err)

	rotatedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	fixture.provider.setKey(rotatedKey, "rotated")
	fixture.now = fixture.now.Add(2 * time.Second)
	rotated := fixture.token(rotatedKey, "rotated", fixture.claims())
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), rotated, nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, fixture.provider.jwksRequests(), 2)

	fixture.provider.setUnavailable(true)
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), rotated, nil)
	require.NoError(t, err, "a cached known key remains usable during a bounded outage")

	unknownKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	fixture.now = fixture.now.Add(2 * time.Second)
	unknown := fixture.token(unknownKey, "unknown", fixture.claims())
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), unknown, nil)
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
}

func TestOAuthJWKSCacheInvalidatesWhenProviderSourceChanges(t *testing.T) {
	fixture := newOAuthProtectedResourceFixture(t)
	provider := *fixture.providerConfig
	provider.ProtectedResource.JWKSSource = "static"
	provider.ProtectedResource.JWKSURI = fixture.provider.server.URL + "/jwks"
	cache := &oauthJWKSProviderCache{}

	_, fromCache, err := fixture.service.loadOAuthJWKS(t.Context(), provider, cache, false)
	require.NoError(t, err)
	require.False(t, fromCache)
	firstRequests := fixture.provider.jwksRequests()

	provider.ProtectedResource.JWKSURI = fixture.provider.server.URL + "/jwks?source=updated"
	_, fromCache, err = fixture.service.loadOAuthJWKS(t.Context(), provider, cache, false)
	require.NoError(t, err)
	require.False(t, fromCache)
	require.Equal(t, firstRequests+1, fixture.provider.jwksRequests())
}

func TestOAuthProtectedResourceMetadataIsSortedAndExcludesDisabledProviders(t *testing.T) {
	fixture := newOAuthProtectedResourceFixture(t)
	second := *fixture.providerConfig
	second.ID = uuid.New()
	second.IssuerURL = "https://z.example.test"
	second.ProtectedResource.ScopeMappings = []domain.OAuthScopeMapping{{ExternalScope: "z.scope", InternalScopes: []string{"read"}}}
	disabled := second
	disabled.ID = uuid.New()
	disabled.IssuerURL = "https://disabled.example.test"
	disabled.ProtectedResource.Enabled = false
	fixture.repo.providerList = []*domain.SSOProvider{&second, fixture.providerConfig, &disabled}

	metadata, err := fixture.service.OAuthProtectedResourceMetadata(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{fixture.issuer, "https://z.example.test"}, metadata.AuthorizationServers)
	require.Equal(t, []string{"densemem.read", "densemem.write", "z.scope"}, metadata.ScopesSupported)
}

func TestMCPPublicBaseURLUsesRuntimeConfiguration(t *testing.T) {
	svc := NewSSOService(nil, SSOConfig{RuntimeConfig: ssoRuntimeConfigStub{cfg: SSORuntimeConfig{
		MCPPublicBaseURL: " https://memory.example.test/ ",
	}}})
	baseURL, err := svc.MCPPublicBaseURL(t.Context())
	require.NoError(t, err)
	require.Equal(t, "https://memory.example.test", baseURL)

	runtimeErr := errors.New("runtime unavailable")
	svc = NewSSOService(nil, SSOConfig{RuntimeConfig: ssoRuntimeConfigStub{err: runtimeErr}})
	_, err = svc.MCPPublicBaseURL(t.Context())
	require.ErrorIs(t, err, runtimeErr)
}

func TestOAuthClaimHelpersRejectMalformedAndAmbiguousValues(t *testing.T) {
	encodedObject := base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	encodedSignature := base64.RawURLEncoding.EncodeToString([]byte("signature"))
	header, payload, err := strictJWTParts(encodedObject + "." + encodedObject + "." + encodedSignature)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(header))
	require.JSONEq(t, `{}`, string(payload))
	for _, raw := range []string{
		"not-a-jwt",
		"..",
		"%." + encodedObject + "." + encodedSignature,
		base64.RawURLEncoding.EncodeToString(make([]byte, 16*1024+1)) + "." + encodedObject + "." + encodedSignature,
		encodedObject + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 256*1024+1)) + "." + encodedSignature,
	} {
		_, _, err := strictJWTParts(raw)
		require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	}

	claims := map[string]json.RawMessage{
		"valid":  json.RawMessage(`" value "`),
		"blank":  json.RawMessage(`"  "`),
		"number": json.RawMessage(`1`),
	}
	value, err := requiredStringClaim(claims, "valid")
	require.NoError(t, err)
	require.Equal(t, "value", value)
	for _, name := range []string{"missing", "blank", "number"} {
		_, err := requiredStringClaim(claims, name)
		require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	}

	scopes, err := oauthScopeClaim(map[string]json.RawMessage{"scp": json.RawMessage(`"read read write"`)}, "scp")
	require.NoError(t, err)
	require.Equal(t, []string{"read", "write"}, scopes)
	scopes, err = oauthScopeClaim(map[string]json.RawMessage{"scp": json.RawMessage(`["write","read"]`)}, "scp")
	require.NoError(t, err)
	require.Equal(t, []string{"read", "write"}, scopes)
	scopes, err = oauthScopeClaim(nil, "scp")
	require.NoError(t, err)
	require.Empty(t, scopes)
	for _, raw := range []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`[""]`), json.RawMessage(`["read write"]`)} {
		_, err := oauthScopeClaim(map[string]json.RawMessage{"scp": raw}, "scp")
		require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	}

	teamID := uuid.New()
	team, err := oauthTeamClaim(map[string]json.RawMessage{"team": json.RawMessage(`"` + teamID.String() + `"`)}, "team")
	require.NoError(t, err)
	require.Equal(t, teamID, *team)
	team, err = oauthTeamClaim(nil, "")
	require.NoError(t, err)
	require.Nil(t, team)
	team, err = oauthTeamClaim(nil, "team")
	require.NoError(t, err)
	require.Nil(t, team)
	for _, raw := range []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`"not-a-uuid"`), json.RawMessage(`"00000000-0000-0000-0000-000000000000"`)} {
		_, err := oauthTeamClaim(map[string]json.RawMessage{"team": raw}, "team")
		require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	}

	membership := &domain.SSOTeamMembership{Team: domain.Team{ID: teamID}}
	selected, err := selectOAuthMembership([]*domain.SSOTeamMembership{membership}, nil, nil)
	require.NoError(t, err)
	require.Same(t, membership, selected)
	_, err = selectOAuthMembership(nil, nil, nil)
	require.ErrorIs(t, err, ErrOAuthAccessDenied)
	_, err = selectOAuthMembership([]*domain.SSOTeamMembership{membership, membership}, nil, nil)
	require.ErrorIs(t, err, ErrOAuthTeamRequired)
	otherTeamID := uuid.New()
	_, err = selectOAuthMembership([]*domain.SSOTeamMembership{membership}, &teamID, &otherTeamID)
	require.ErrorIs(t, err, ErrOAuthAccessDenied)
	_, err = selectOAuthMembership([]*domain.SSOTeamMembership{nil}, &teamID, nil)
	require.ErrorIs(t, err, ErrOAuthAccessDenied)
}

func TestAuthenticateOAuthBearerFailsClosedAcrossIdentityAndProviderErrors(t *testing.T) {
	fixture := newOAuthProtectedResourceFixture(t)
	_, err := (*SSOService)(nil).AuthenticateOAuthBearer(t.Context(), "not-a-jwt", nil)
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), "not-a-jwt", nil)
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)

	for _, mutate := range []func(map[string]any){
		func(claims map[string]any) { delete(claims, "sub") },
		func(claims map[string]any) { delete(claims, "exp") },
		func(claims map[string]any) { delete(claims, "aud") },
		func(claims map[string]any) { claims["scp"] = 1 },
		func(claims map[string]any) { claims["scp"] = []string{"read write"} },
		func(claims map[string]any) { claims["dense_mem_team_id"] = 1 },
	} {
		claims := fixture.claims()
		mutate(claims)
		_, err := fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(fixture.key, fixture.keyID, claims), nil)
		require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	}

	claims := fixture.claims()
	claims["sub"] = "unknown-subject"
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(fixture.key, fixture.keyID, claims), nil)
	require.ErrorIs(t, err, ErrOAuthAccessDenied)
	fixture.identity.Active = false
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(fixture.key, fixture.keyID, fixture.claims()), nil)
	require.ErrorIs(t, err, ErrOAuthAccessDenied)
	fixture.identity.Active = true

	backendErr := errors.New("identity repository unavailable")
	fixture.repo.getIdentityErr = backendErr
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(fixture.key, fixture.keyID, fixture.claims()), nil)
	require.ErrorIs(t, err, backendErr)
	fixture.repo.getIdentityErr = nil
	fixture.repo.listTeamProfilesErr = backendErr
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(fixture.key, fixture.keyID, fixture.claims()), nil)
	require.ErrorIs(t, err, backendErr)
	fixture.repo.listTeamProfilesErr = nil

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(otherKey, fixture.keyID, fixture.claims()), nil)
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)

	fixture.providerConfig.IdentityClaim = "oid"
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(fixture.key, fixture.keyID, fixture.claims()), nil)
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	fixture.providerConfig.IdentityClaim = "sub"

	duplicate := *fixture.providerConfig
	duplicate.ID = uuid.New()
	fixture.repo.providerList = []*domain.SSOProvider{fixture.providerConfig, &duplicate}
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(fixture.key, fixture.keyID, fixture.claims()), nil)
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	fixture.repo.providerList = nil
	fixture.repo.listProvidersErr = backendErr
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(fixture.key, fixture.keyID, fixture.claims()), nil)
	require.ErrorIs(t, err, backendErr)
}

func TestOAuthMetadataAndJWKSDocumentsFailClosed(t *testing.T) {
	metadata, err := (*SSOService)(nil).OAuthProtectedResourceMetadata(t.Context())
	require.NoError(t, err)
	require.Empty(t, metadata.AuthorizationServers)

	fixture := newOAuthProtectedResourceFixture(t)
	_, err = fixture.service.oauthProviderForIssuer(t.Context(), "https://unknown.example")
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	invalidUnrelatedProvider := *fixture.providerConfig
	invalidUnrelatedProvider.ID = uuid.New()
	invalidUnrelatedProvider.IssuerURL = "https://unrelated.example"
	invalidUnrelatedProvider.ProtectedResource.Algorithms = []string{"none"}
	fixture.repo.providerList = []*domain.SSOProvider{&invalidUnrelatedProvider, fixture.providerConfig}
	providerForIssuer, err := fixture.service.oauthProviderForIssuer(t.Context(), fixture.issuer)
	require.NoError(t, err)
	require.Equal(t, fixture.providerConfig.ID, providerForIssuer.ID)
	invalidIssuerProvider := *fixture.providerConfig
	invalidIssuerProvider.ProtectedResource.Algorithms = []string{"none"}
	fixture.repo.providerList = []*domain.SSOProvider{&invalidIssuerProvider}
	_, err = fixture.service.oauthProviderForIssuer(t.Context(), fixture.issuer)
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	fixture.repo.providerList = nil
	backendErr := errors.New("provider repository unavailable")
	fixture.repo.listProvidersErr = backendErr
	_, err = fixture.service.OAuthProtectedResourceMetadata(t.Context())
	require.ErrorIs(t, err, backendErr)
	fixture.repo.listProvidersErr = nil

	retired := *fixture.providerConfig
	now := fixture.now
	retired.RetiredAt = &now
	disabled := *fixture.providerConfig
	disabled.ID = uuid.New()
	disabled.ProtectedResource.Enabled = false
	fixture.repo.providerList = []*domain.SSOProvider{&retired, &disabled}
	metadata, err = fixture.service.OAuthProtectedResourceMetadata(t.Context())
	require.NoError(t, err)
	require.Empty(t, metadata.AuthorizationServers)

	invalid := *fixture.providerConfig
	invalid.ProtectedResource.Algorithms = []string{"none"}
	fixture.repo.providerList = []*domain.SSOProvider{&invalid}
	_, err = fixture.service.OAuthProtectedResourceMetadata(t.Context())
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)

	var server *httptest.Server
	discoveryBody := ""
	jwksBody := ""
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_, _ = io.WriteString(w, discoveryBody)
		case "/jwks":
			_, _ = io.WriteString(w, jwksBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	provider := *fixture.providerConfig
	provider.IssuerURL = server.URL
	provider.ProtectedResource.JWKSSource = "discovery"

	for _, body := range []string{
		`{"issuer":"` + server.URL + `","issuer":"` + server.URL + `","jwks_uri":"` + server.URL + `/jwks"}`,
		`{"issuer":`,
		`{"issuer":"https://wrong.example","jwks_uri":"` + server.URL + `/jwks"}`,
		`{"issuer":"` + server.URL + `","jwks_uri":"javascript:alert(1)"}`,
	} {
		discoveryBody = body
		_, err := fixture.service.discoverOAuthJWKSURI(t.Context(), provider)
		require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	}

	publicJWK := jose.JSONWebKey{Key: &fixture.key.PublicKey, KeyID: fixture.keyID, Algorithm: "RS256", Use: "sig"}
	tooMany := jose.JSONWebKeySet{Keys: make([]jose.JSONWebKey, 101)}
	for index := range tooMany.Keys {
		tooMany.Keys[index] = publicJWK
	}
	tooManyJSON, err := json.Marshal(tooMany)
	require.NoError(t, err)
	privateJSON, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: fixture.key, KeyID: "private", Algorithm: "RS256", Use: "sig"}}})
	require.NoError(t, err)
	for _, body := range []string{
		`{"keys":[],"keys":[]}`,
		`{"keys":[]}`,
		string(tooManyJSON),
		string(privateJSON),
	} {
		jwksBody = body
		_, err := fixture.service.fetchOAuthJWKS(t.Context(), server.URL+"/jwks")
		require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	}

	_, err = fixture.service.getOAuthProviderDocument(t.Context(), "://invalid")
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	emptyServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(emptyServer.Close)
	_, err = fixture.service.getOAuthProviderDocument(t.Context(), emptyServer.URL)
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	largeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", oauthProviderResponseLimit+1))
	}))
	t.Cleanup(largeServer.Close)
	_, err = fixture.service.getOAuthProviderDocument(t.Context(), largeServer.URL)
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)

	readFailure := NewSSOService(fixture.repo, SSOConfig{HTTPClient: &http.Client{Transport: oauthRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(oauthErrorReader{}),
			Header:     make(http.Header),
		}, nil
	})}})
	_, err = readFailure.getOAuthProviderDocument(t.Context(), "https://provider.example/jwks")
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	requestFailure := NewSSOService(fixture.repo, SSOConfig{HTTPClient: &http.Client{Transport: oauthRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}})
	_, err = requestFailure.getOAuthProviderDocument(t.Context(), "https://provider.example/jwks")
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)

	provider.ProtectedResource.JWKSSource = "static"
	provider.ProtectedResource.JWKSURI = server.URL + "/jwks"
	jwksBodyBytes, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{publicJWK}})
	require.NoError(t, err)
	jwksBody = string(jwksBodyBytes)
	cache := &oauthJWKSProviderCache{}
	set, fromCache, err := fixture.service.loadOAuthJWKS(t.Context(), provider, cache, false)
	require.NoError(t, err)
	require.False(t, fromCache)
	require.Len(t, set.Keys, 1)
	_, fromCache, err = fixture.service.loadOAuthJWKS(t.Context(), provider, cache, true)
	require.NoError(t, err)
	require.True(t, fromCache)
	provider.ID = uuid.New()
	_, err = fixture.service.oauthVerificationKey(t.Context(), provider, "missing", "RS256")
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)

	unavailableRuntime := NewSSOService(fixture.repo, SSOConfig{RuntimeConfig: ssoRuntimeConfigStub{err: backendErr}})
	_, _, err = unavailableRuntime.loadOAuthJWKS(t.Context(), provider, &oauthJWKSProviderCache{}, false)
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)

	set.Keys = append(set.Keys,
		jose.JSONWebKey{Key: &fixture.key.PublicKey, KeyID: "encryption", Algorithm: "RS256", Use: "enc"},
		jose.JSONWebKey{Key: &fixture.key.PublicKey, KeyID: "wrong-algorithm", Algorithm: "RS512", Use: "sig"},
	)
	require.Len(t, oauthCandidateKeys(set, fixture.keyID, "RS256"), 1)
	require.Len(t, oauthCandidateKeys(set, "", "RS256"), 1)
}

func TestOAuthProtectedResourceRejectsExcessiveScopeMappings(t *testing.T) {
	fixture := newOAuthProtectedResourceFixture(t)
	provider := *fixture.providerConfig
	provider.ProtectedResource.ScopeMappings = make([]domain.OAuthScopeMapping, oauthMaximumScopeMappings+1)
	for index := range provider.ProtectedResource.ScopeMappings {
		provider.ProtectedResource.ScopeMappings[index] = domain.OAuthScopeMapping{
			ExternalScope:  fmt.Sprintf("scope.%d", index),
			InternalScopes: []string{CredentialScopeRead},
		}
	}

	require.ErrorContains(t, normalizeSSOProviderForWrite(&provider), "at most 16")
}

type oauthRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f oauthRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type oauthErrorReader struct{}

func (oauthErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

type oauthProtectedResourceFixture struct {
	service        *SSOService
	repo           *ssoRepositoryStub
	provider       *oauthTestProvider
	providerConfig *domain.SSOProvider
	identity       *domain.SSOIdentity
	membership     *domain.SSOTeamMembership
	issuer         string
	key            *rsa.PrivateKey
	keyID          string
	teamID         uuid.UUID
	now            time.Time
}

func newOAuthProtectedResourceFixture(t *testing.T) *oauthProtectedResourceFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	provider := newOAuthTestProvider(t, key, "initial")
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	teamID := uuid.New()
	identity := &domain.SSOIdentity{ID: uuid.New(), ProviderID: providerID, Subject: "subject-a", DisplayName: "OAuth User", Active: true}
	membership := &domain.SSOTeamMembership{
		Team: domain.Team{ID: teamID, Name: "Research"},
		Membership: domain.Membership{
			ID:              uuid.New(),
			MemorySpaceID:   uuid.New(),
			ActorIdentityID: identity.ID,
			TeamID:          teamID,
			OwnerID:         uuid.New(),
			Grants:          []string{CredentialScopeRead, CredentialScopeWrite},
			Role:            CredentialRoleMember,
			Status:          "active",
		},
	}
	providerConfig := &domain.SSOProvider{
		ID:            providerID,
		Name:          "Generic",
		Kind:          domain.SSOProviderKindGenericOIDC,
		IssuerURL:     provider.server.URL,
		IdentityClaim: "sub",
		ClientID:      "browser-client",
		Enabled:       true,
		ProtectedResource: domain.OAuthProtectedResourceConfig{
			Enabled:    true,
			Audiences:  []string{"api://dense-mem"},
			JWKSSource: "discovery",
			Algorithms: []string{"RS256"},
			ScopeClaim: "scp",
			ScopeMappings: []domain.OAuthScopeMapping{
				{ExternalScope: "densemem.read", InternalScopes: []string{CredentialScopeRead}},
				{ExternalScope: "densemem.write", InternalScopes: []string{CredentialScopeWrite}},
			},
			TeamClaim: "dense_mem_team_id",
		},
	}
	repo := &ssoRepositoryStub{
		t:            t,
		providers:    map[uuid.UUID]*domain.SSOProvider{providerID: providerConfig},
		identities:   map[uuid.UUID]*domain.SSOIdentity{identity.ID: identity},
		teamProfiles: []*domain.SSOTeamMembership{membership},
	}
	fixture := &oauthProtectedResourceFixture{
		repo: repo, provider: provider, providerConfig: providerConfig, identity: identity,
		membership: membership, issuer: provider.server.URL, key: key, keyID: "initial", teamID: teamID, now: now,
	}
	fixture.service = NewSSOService(repo, SSOConfig{Now: func() time.Time { return fixture.now }})
	return fixture
}

func (f *oauthProtectedResourceFixture) claims() map[string]any {
	return map[string]any{
		"iss": f.issuer, "sub": f.identity.Subject, "aud": "api://dense-mem",
		"exp": f.now.Add(time.Hour).Unix(), "iat": f.now.Add(-time.Minute).Unix(), "scp": "densemem.read",
	}
}

func (f *oauthProtectedResourceFixture) token(key *rsa.PrivateKey, keyID string, claims map[string]any) string {
	return signedOIDCToken(f.repo.t, key, keyID, claims)
}

type oauthTestProvider struct {
	server      *httptest.Server
	mu          sync.RWMutex
	key         *rsa.PrivateKey
	keyID       string
	unavailable bool
	jwksHits    int
}

func newOAuthTestProvider(t *testing.T, key *rsa.PrivateKey, keyID string) *oauthTestProvider {
	t.Helper()
	provider := &oauthTestProvider{key: key, keyID: keyID}
	provider.server = httptest.NewServer(http.HandlerFunc(provider.handle))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *oauthTestProvider) handle(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unavailable {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": p.server.URL, "jwks_uri": p.server.URL + "/jwks"})
	case "/jwks":
		p.jwksHits++
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{rsaJWK(&p.key.PublicKey, p.keyID)}})
	default:
		http.NotFound(w, r)
	}
}

func (p *oauthTestProvider) setKey(key *rsa.PrivateKey, keyID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.key = key
	p.keyID = keyID
}

func (p *oauthTestProvider) setUnavailable(value bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unavailable = value
}

func (p *oauthTestProvider) jwksRequests() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.jwksHits
}

func signedOAuthRawToken(t *testing.T, key *rsa.PrivateKey, keyID string, claimsJSON []byte) string {
	t.Helper()
	headerJSON, err := json.Marshal(map[string]any{"alg": "RS256", "kid": keyID, "typ": "JWT"})
	require.NoError(t, err)
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func jsonNumber(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
