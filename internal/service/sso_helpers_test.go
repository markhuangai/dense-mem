package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSSOTokenAndRedirectHelpers(t *testing.T) {
	hash := HashSSOToken("token")
	assert.Len(t, hash, 64)
	assert.True(t, hashMatches("token", hash))
	assert.False(t, hashMatches("other", hash))
	assert.False(t, hashMatches("", hash))

	randomToken, err := secureRandomToken(16)
	require.NoError(t, err)
	assert.NotEmpty(t, randomToken)
	assert.NotEmpty(t, pkceChallenge("verifier"))

	assert.Equal(t, "/ui", safeRedirectPath(""))
	assert.Equal(t, "/ui", safeRedirectPath("https://evil.example"))
	assert.Equal(t, "/ui", safeRedirectPath("//evil.example/path"))
	assert.Equal(t, "/ui/team", safeRedirectPath("/ui/team"))
}

func TestSSOClaimAndGroupHelpers(t *testing.T) {
	raw := map[string]json.RawMessage{
		"email":         json.RawMessage(`"ada@example.com"`),
		"groups":        json.RawMessage(`["g2","g1","g1"]`),
		"roles":         json.RawMessage(`"role-1"`),
		"zitadel_roles": json.RawMessage(`{"dense-mem-admin":{"org-id":"example.zitadel.cloud"},"dense-mem-member":{}}`),
	}

	assert.Equal(t, "ada@example.com", firstClaimString(raw, "missing", "email"))
	assert.Equal(t, []string{"dense-mem-admin", "dense-mem-member", "g1", "g2", "role-1"}, groupsFromRawClaims(raw, []string{"groups", "roles", "zitadel_roles"}))
	assert.Equal(t, []string{"a", "b"}, dedupeStrings([]string{" b ", "", "a", "b"}))
	assert.True(t, containsString([]string{"openid", "email"}, "openid"))
	assert.False(t, containsString([]string{"email"}, "profile"))

	payload := map[string]any{
		"value": []any{
			map[string]any{"id": "group-a"},
			map[string]any{"groupId": "group-b"},
		},
	}
	assert.Equal(t, []string{"group-a", "group-b"}, extractGroupsFromPayload(payload))
	assert.Equal(t, []string{"x", "y"}, extractGroupsFromPayload([]any{"y", "x", "x"}))
	assert.Equal(t, []string{"embedded-a"}, extractGroupsFromPayload(map[string]any{
		"_embedded": map[string]any{
			"groups": []any{map[string]any{"group_id": "embedded-a"}},
		},
	}))
	assert.Nil(t, extractGroupsFromPayload(map[string]any{"value": []any{map[string]any{"name": "ignored"}}}))
}

func TestSSOProfileAndEntitlementHelpers(t *testing.T) {
	identityID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	assert.Equal(t, "SSO ada@example.com 11111111", ssoProfileName("ada@example.com", "Ada", identityID))
	assert.Equal(t, "SSO Ada 11111111", ssoProfileName("", "Ada", identityID))
	assert.Equal(t, "SSO 11111111-1111-4111-8111-111111111111 11111111", ssoProfileName("", "", identityID))
	longName := ssoProfileName("averyveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryveryverylong@example.com", "", identityID)
	assert.LessOrEqual(t, len([]rune(longName)), 100)

	teamID := uuid.New()
	otherTeamID := uuid.New()
	mappings := []*domain.SSOGroupMapping{
		{TeamID: teamID, GroupID: "g1", Scopes: []string{APIKeyScopeRead}, Role: APIKeyRoleMember, Enabled: true},
		{TeamID: teamID, GroupID: "g2", Scopes: []string{APIKeyScopeRead, APIKeyScopeWrite}, Role: APIKeyRoleManager, Enabled: true},
		{TeamID: otherTeamID, GroupID: "g3", Scopes: []string{APIKeyScopeRead}, Role: APIKeyRoleMember, Enabled: true},
	}

	assert.True(t, hasMappingForTeam(mappings, teamID))
	entitlement, ok := mergedEntitlementForTeam(mappings, teamID)
	require.True(t, ok)
	assert.Equal(t, []string{APIKeyScopeRead, APIKeyScopeWrite}, entitlement.Scopes)
	assert.Equal(t, APIKeyRoleManager, entitlement.Role)
	assert.Equal(t, "g1,g2", entitlement.GroupID)
	_, ok = mergedEntitlementForTeam(mappings, uuid.New())
	assert.False(t, ok)

	entitlement, ok = mergedEntitlementForTeam([]*domain.SSOGroupMapping{{TeamID: teamID, GroupID: "g4", Enabled: true}}, teamID)
	require.True(t, ok)
	assert.Equal(t, []string{APIKeyScopeRead}, entitlement.Scopes)
}

func TestNormalizeSSOProviderAndMapping(t *testing.T) {
	provider := domain.SSOProvider{
		Name:      " Enterprise ",
		Kind:      domain.SSOProviderKindAzureAD,
		IssuerURL: "https://login.example.com/",
		ClientID:  " client-id ",
		Scopes:    []string{"email", "openid", "email"},
	}
	require.NoError(t, normalizeSSOProvider(&provider))
	assert.Equal(t, "Enterprise", provider.Name)
	assert.Equal(t, "https://login.example.com", provider.IssuerURL)
	assert.Equal(t, "client-id", provider.ClientID)
	assert.Equal(t, []string{"email", "openid"}, provider.Scopes)
	assert.Equal(t, []string{"groups"}, provider.GroupClaims)

	badProvider := domain.SSOProvider{Name: "Bad", Kind: domain.SSOProviderKindAzureAD, IssuerURL: "http://example.com", ClientID: "client"}
	require.Error(t, normalizeSSOProvider(&badProvider))
	badProvider = domain.SSOProvider{Name: "Bad", Kind: "saml", IssuerURL: "https://login.example.com", ClientID: "client"}
	require.Error(t, normalizeSSOProvider(&badProvider))
	badProvider = domain.SSOProvider{Name: "Bad", Kind: domain.SSOProviderKindAzureAD, IssuerURL: "://bad", ClientID: "client"}
	require.Error(t, normalizeSSOProvider(&badProvider))
	badProvider = domain.SSOProvider{Name: "Bad", Kind: domain.SSOProviderKindAzureAD, IssuerURL: "https://login.example.com"}
	require.Error(t, normalizeSSOProvider(&badProvider))

	provider = domain.SSOProvider{Name: "Ping", Kind: domain.SSOProviderKindPingOne, IssuerURL: "https://ping.example.com", ClientID: "client", Scopes: []string{"email"}}
	require.NoError(t, normalizeSSOProvider(&provider))
	assert.Equal(t, []string{"openid", "email"}, provider.Scopes)

	mapping := domain.SSOGroupMapping{
		ProviderID: uuid.New(),
		TeamID:     uuid.New(),
		GroupID:    " group-1 ",
	}
	require.NoError(t, normalizeSSOGroupMapping(&mapping))
	assert.Equal(t, "group-1", mapping.GroupID)
	assert.Equal(t, []string{APIKeyScopeRead}, mapping.Scopes)
	assert.Equal(t, APIKeyRoleMember, mapping.Role)

	invalidMapping := domain.SSOGroupMapping{ProviderID: uuid.New(), TeamID: uuid.New()}
	require.Error(t, normalizeSSOGroupMapping(&invalidMapping))
	invalidMapping = domain.SSOGroupMapping{ProviderID: uuid.New(), GroupID: "group-a"}
	require.Error(t, normalizeSSOGroupMapping(&invalidMapping))
	invalidMapping = domain.SSOGroupMapping{ProviderID: uuid.New(), TeamID: uuid.New(), GroupID: "group-a", Scopes: []string{"admin"}}
	require.Error(t, normalizeSSOGroupMapping(&invalidMapping))
	invalidMapping = domain.SSOGroupMapping{ProviderID: uuid.New(), TeamID: uuid.New(), GroupID: "group-a", Role: "owner"}
	require.Error(t, normalizeSSOGroupMapping(&invalidMapping))
}

func TestHTTPSSOGroupResolverResolveGroups(t *testing.T) {
	var gotMethod, gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]string{{"id": "group-a"}, {"groupId": "group-b"}},
		}))
	}))
	defer server.Close()

	resolver := &HTTPSSOGroupResolver{HTTPClient: server.Client()}
	groups, err := resolver.ResolveGroups(context.Background(), domain.SSOProvider{
		Kind:           domain.SSOProviderKindGenericOIDC,
		GroupsEndpoint: server.URL + "/users/{subject}/groups",
	}, "user@example.com", "access-token")

	require.NoError(t, err)
	assert.Equal(t, []string{"group-a", "group-b"}, groups)
	assert.Equal(t, http.MethodGet, gotMethod)
	assert.Equal(t, "Bearer access-token", gotAuth)
	assert.Equal(t, "/users/user@example.com/groups", gotPath)
}

func TestHTTPSSOGroupResolverAzureGetMemberGroupsUsesPost(t *testing.T) {
	var gotMethod, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotBody = string(body)
		require.NoError(t, json.NewEncoder(w).Encode([]string{"group-z"}))
	}))
	defer server.Close()

	resolver := &HTTPSSOGroupResolver{HTTPClient: server.Client()}
	groups, err := resolver.ResolveGroups(context.Background(), domain.SSOProvider{
		Kind:           domain.SSOProviderKindAzureAD,
		GroupsEndpoint: server.URL + "/me/getMemberGroups",
	}, "subject", "access-token")

	require.NoError(t, err)
	assert.Equal(t, []string{"group-z"}, groups)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.JSONEq(t, `{"securityEnabledOnly":false}`, gotBody)
}

func TestHTTPSSOGroupResolverErrors(t *testing.T) {
	resolver := &HTTPSSOGroupResolver{}
	_, err := resolver.ResolveGroups(context.Background(), domain.SSOProvider{}, "subject", "access-token")
	require.ErrorIs(t, err, ErrSSOGroupRefreshUnavailable)

	_, err = resolver.ResolveGroups(context.Background(), domain.SSOProvider{GroupsEndpoint: "http://example.com/groups"}, "subject", "")
	require.ErrorIs(t, err, ErrSSOGroupRefreshUnavailable)

	_, err = resolver.ResolveGroups(context.Background(), domain.SSOProvider{GroupsEndpoint: "://bad"}, "subject", "access-token")
	require.Error(t, err)

	for name, handler := range map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusBadGateway)
		},
		"json": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("{"))
		},
		"groups": func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"value": []any{}}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			resolver := &HTTPSSOGroupResolver{HTTPClient: server.Client()}
			_, err := resolver.ResolveGroups(context.Background(), domain.SSOProvider{GroupsEndpoint: server.URL}, "subject", "access-token")
			require.Error(t, err)
		})
	}
}

func TestHTTPSSOGroupResolverClientCredentialsToken(t *testing.T) {
	var oidcServer *httptest.Server
	oidcServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 oidcServer.URL,
				"authorization_endpoint": oidcServer.URL + "/authorize",
				"token_endpoint":         oidcServer.URL + "/token",
				"jwks_uri":               oidcServer.URL + "/jwks",
			}))
		case "/token":
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "client_credentials", r.Form.Get("grant_type"))
			assert.Equal(t, "group.read", r.Form.Get("scope"))
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"access_token": "credential-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oidcServer.Close()

	t.Setenv("SSO_CLIENT_SECRET", "secret-value")
	resolver := &HTTPSSOGroupResolver{HTTPClient: oidcServer.Client()}
	token, err := resolver.clientCredentialsToken(context.Background(), domain.SSOProvider{
		IssuerURL:       oidcServer.URL,
		ClientID:        "client-id",
		ClientSecretEnv: "SSO_CLIENT_SECRET",
		GroupsScopes:    []string{"group.read"},
	})

	require.NoError(t, err)
	assert.Equal(t, "credential-token", token)
}

func TestHTTPSSOGroupResolverClientCredentialsUnavailable(t *testing.T) {
	resolver := &HTTPSSOGroupResolver{}
	_, err := resolver.clientCredentialsToken(context.Background(), domain.SSOProvider{})
	require.ErrorIs(t, err, ErrSSOGroupRefreshUnavailable)

	_, err = resolver.clientCredentialsToken(context.Background(), domain.SSOProvider{
		ClientSecretEnv: "MISSING_SECRET",
		GroupsScopes:    []string{"group.read"},
	})
	require.ErrorIs(t, err, ErrSSOGroupRefreshUnavailable)

	t.Setenv("EMPTY_SECRET", "")
	_, err = resolver.clientCredentialsToken(context.Background(), domain.SSOProvider{
		ClientSecretEnv: "EMPTY_SECRET",
		GroupsScopes:    []string{"group.read"},
	})
	require.ErrorIs(t, err, ErrSSOGroupRefreshUnavailable)
}
