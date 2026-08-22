package access

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestOAuthValidationErrorsAreTypedAndBounded(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{err: OAuthTokenInvalidError{}, want: "oauth access token invalid"},
		{err: OAuthTokenExpiredError{}, want: "oauth access token expired"},
		{err: OAuthProviderUnavailableError{}, want: "oauth provider unavailable"},
	}
	for _, test := range tests {
		require.Equal(t, test.want, test.err.Error())
	}
}

func TestOAuthProtectedResourceValidatorDefaultOptionsAndReconfiguration(t *testing.T) {
	fixture := newOAuthCompatibilityFixture(t)
	profiles := fixture.profiles()
	validator, err := NewOAuthProtectedResourceValidator(profiles[:2], OAuthProtectedResourceValidatorOptions{})
	require.NoError(t, err)
	require.Equal(t, oauthProviderTimeout, validator.httpClient.Timeout)
	require.NotNil(t, validator.now)

	validator = fixture.validatorForProfiles(t, profiles[:2])
	_, err = validator.Validate(t.Context(), fixture.token(t, "entra", "primary", fixture.claims("entra")))
	require.NoError(t, err)
	validator.cachesMu.Lock()
	require.Contains(t, validator.caches, "entra")
	validator.cachesMu.Unlock()

	require.NoError(t, validator.Configure(profiles[1:2]))
	validator.cachesMu.Lock()
	require.NotContains(t, validator.caches, "entra")
	validator.cachesMu.Unlock()

	invalid := cloneOAuthProfile(profiles[1])
	invalid.Issuer = "http://issuer.invalid"
	require.Error(t, validator.Configure([]domain.OAuthProtectedResourceProfile{invalid}))
	_, err = validator.Validate(t.Context(), fixture.token(t, "pingone", "primary", fixture.claims("pingone")))
	require.NoError(t, err, "invalid reconfiguration must not replace the last valid profiles")
}

func TestOAuthProtectedResourceValidatorRejectsEveryBoundedConfigDimension(t *testing.T) {
	fixture := newOAuthCompatibilityFixture(t)
	base := fixture.profiles()[0]

	mutations := map[string]func(*domain.OAuthProtectedResourceProfile){
		"invalid name": func(profile *domain.OAuthProtectedResourceProfile) { profile.Name = "bad\nname" },
		"too many audiences": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.Audiences = numberedOAuthValues("audience", oauthMaximumAudiences+1)
		},
		"duplicate audience": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.Audiences = []string{"audience", "audience"}
		},
		"discovery URI": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.JWKSURI = "https://issuer.example/jwks"
		},
		"too many algorithms": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.Algorithms = []string{"RS256", "PS256", "ES256", "RS256"}
		},
		"duplicate algorithm": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.Algorithms = []string{"RS256", "RS256"}
		},
		"invalid team claim": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.TeamClaim = "team claim"
		},
		"too many mappings": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.ScopeMappings = make([]domain.OAuthScopeMapping, oauthMaximumScopeMappings+1)
			for index := range profile.ProtectedResource.ScopeMappings {
				profile.ProtectedResource.ScopeMappings[index] = domain.OAuthScopeMapping{ExternalScope: fmt.Sprintf("scope.%d", index), InternalScopes: []string{"read"}}
			}
		},
		"missing internal scope": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.ScopeMappings[0].InternalScopes = nil
		},
		"too many internal scopes": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.ScopeMappings[0].InternalScopes = numberedOAuthValues("scope", oauthMaximumMappedScopes+1)
		},
		"duplicate internal scope": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.ScopeMappings[0].InternalScopes = []string{"read", "read"}
		},
		"invalid internal scope": func(profile *domain.OAuthProtectedResourceProfile) {
			profile.ProtectedResource.ScopeMappings[0].InternalScopes = []string{"read scope"}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			profile := cloneOAuthProfile(base)
			mutate(&profile)
			_, err := NewOAuthProtectedResourceValidator([]domain.OAuthProtectedResourceProfile{profile}, OAuthProtectedResourceValidatorOptions{})
			require.Error(t, err)
		})
	}

	_, err := NewOAuthProtectedResourceValidator(nil, OAuthProtectedResourceValidatorOptions{})
	require.Error(t, err)
	tooMany := make([]domain.OAuthProtectedResourceProfile, oauthMaximumProfiles+1)
	_, err = NewOAuthProtectedResourceValidator(tooMany, OAuthProtectedResourceValidatorOptions{})
	require.Error(t, err)

	duplicateIssuer := cloneOAuthProfile(base)
	duplicateIssuer.Name = "other"
	_, err = NewOAuthProtectedResourceValidator([]domain.OAuthProtectedResourceProfile{base, duplicateIssuer}, OAuthProtectedResourceValidatorOptions{})
	require.Error(t, err)
}

func TestValidateOAuthHTTPSURLRequiresUsableAuthority(t *testing.T) {
	for _, raw := range []string{
		"https://:443",
		"https://issuer.example:0",
		"https://issuer.example:65536",
	} {
		t.Run(raw, func(t *testing.T) {
			require.Error(t, validateOAuthHTTPSURL(raw, true))
		})
	}

	for _, raw := range []string{
		"https://issuer.example",
		"https://issuer.example:443/jwks",
		"https://[2001:db8::1]/jwks",
		"https://[2001:db8::1]:65535/jwks",
	} {
		t.Run(raw, func(t *testing.T) {
			require.NoError(t, validateOAuthHTTPSURL(raw, true))
		})
	}
}

func TestBoundedOAuthHTTPClientConstrainsTimeoutsAndRedirects(t *testing.T) {
	previousCalled := false
	client := boundedOAuthHTTPClient(&http.Client{
		Timeout: time.Hour,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			previousCalled = true
			return errors.New("caller rejected redirect")
		},
	})
	require.Equal(t, oauthProviderTimeout, client.Timeout)

	request, err := http.NewRequest(http.MethodGet, "https://issuer.example/jwks", nil)
	require.NoError(t, err)
	require.ErrorContains(t, client.CheckRedirect(request, make([]*http.Request, 4)), "too many")
	insecure, err := http.NewRequest(http.MethodGet, "http://issuer.example/jwks", nil)
	require.NoError(t, err)
	require.Error(t, client.CheckRedirect(insecure, nil))
	require.ErrorContains(t, client.CheckRedirect(request, nil), "caller rejected")
	require.True(t, previousCalled)

	short := boundedOAuthHTTPClient(&http.Client{Timeout: time.Second})
	require.Equal(t, time.Second, short.Timeout)
	require.NoError(t, short.CheckRedirect(request, nil))
}

func TestOAuthJWTAndScopeHelpersRejectAmbiguousValues(t *testing.T) {
	for _, raw := range []string{
		" token.value.signature",
		"token..signature",
		"@@.e30.signature",
		strings.Repeat("x", oauthEncodedTokenLimit+1),
	} {
		_, _, err := strictOAuthJWTParts(raw)
		require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	}

	for _, value := range []string{"", "read scope", "read\n", "snowman-☃"} {
		require.False(t, validOAuthScope(value))
	}
	require.False(t, validNonControlValue("audience\x00value"))
	require.False(t, validOAuthClaimName("scope/claim"))
}

func numberedOAuthValues(prefix string, count int) []string {
	values := make([]string, count)
	for index := range values {
		values[index] = fmt.Sprintf("%s.%d", prefix, index)
	}
	return values
}
