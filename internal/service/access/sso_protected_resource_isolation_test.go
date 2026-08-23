package access

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestOAuthProtectedResourceIsolatesInvalidProviderByIssuer(t *testing.T) {
	fixture := newSSOOAuthFixture(t)
	invalid := *fixture.provider
	invalid.ID = uuid.New()
	invalid.IssuerURL = "https://invalid.example.test"
	invalid.ProtectedResource.Algorithms = []string{"none"}
	fixture.repo.providerList = []*domain.SSOProvider{&invalid, fixture.provider}

	_, err := fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.NoError(t, err)
	metadata, err := fixture.service.OAuthProtectedResourceMetadata(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{fixture.provider.IssuerURL}, metadata.AuthorizationServers)

	invalid.IssuerURL = fixture.provider.IssuerURL
	fixture.repo.providerList = []*domain.SSOProvider{&invalid}
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	_, err = fixture.service.OAuthProtectedResourceMetadata(t.Context())
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
}

func TestOAuthProtectedResourceIsolatesDuplicateIssuerConfiguration(t *testing.T) {
	fixture := newSSOOAuthFixture(t)
	duplicate := *fixture.provider
	duplicate.ID = uuid.New()

	genericProfile := fixture.compatibility.profiles()[2]
	generic := &domain.SSOProvider{
		ID:            uuid.New(),
		Name:          "Generic",
		Kind:          domain.SSOProviderKindGenericOIDC,
		IssuerURL:     genericProfile.Issuer,
		IdentityClaim: "sub",
		ClientID:      "generic-client",
		ProtectedResource: domain.SSOProtectedResourceConfig{
			Enabled:                      true,
			OAuthProtectedResourceConfig: genericProfile.ProtectedResource,
		},
		Enabled: true,
	}
	fixture.repo.providerList = []*domain.SSOProvider{fixture.provider, &duplicate, generic}

	metadata, err := fixture.service.OAuthProtectedResourceMetadata(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{generic.IssuerURL}, metadata.AuthorizationServers)

	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
}
