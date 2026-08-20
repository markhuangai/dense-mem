package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSSOProviderProtectedResourceConfigRoundTripAndStrictRead(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewSSORepository(appDB, rls)
	provider := &domain.SSOProvider{
		Name:          "oauth protected resource provider",
		Kind:          domain.SSOProviderKindGenericOIDC,
		IssuerURL:     "https://issuer.example.test/",
		IdentityClaim: "sub",
		ClientID:      "oauth-client",
		GroupsScopes:  []string{},
		ProtectedResource: domain.SSOProtectedResourceConfig{
			Enabled: true,
			OAuthProtectedResourceConfig: domain.OAuthProtectedResourceConfig{
				Audiences:  []string{"https://memory.example.test/mcp"},
				JWKSSource: "static",
				JWKSURI:    "https://issuer.example.test/jwks",
				Algorithms: []string{"RS256"},
				ScopeClaim: "scope",
				ScopeMappings: []domain.OAuthScopeMapping{
					{ExternalScope: "memory.read", InternalScopes: []string{"read"}},
					{ExternalScope: "memory.write", InternalScopes: []string{"write"}},
				},
				TeamClaim: "dense_mem_team_id",
			},
		},
		Enabled: true,
	}

	require.NoError(t, repo.CreateProvider(ctx, provider))
	loaded, err := repo.GetProvider(ctx, provider.ID)
	require.NoError(t, err)
	require.Equal(t, provider.IssuerURL, loaded.IssuerURL)
	require.Equal(t, provider.ProtectedResource, loaded.ProtectedResource)

	provider.ProtectedResource.TeamClaim = "tenant_id"
	provider.ProtectedResource.ScopeMappings[0].InternalScopes = []string{"read", "feedback:read"}
	require.NoError(t, repo.UpdateProvider(ctx, provider))
	loaded, err = repo.GetProvider(ctx, provider.ID)
	require.NoError(t, err)
	require.Equal(t, provider.ProtectedResource, loaded.ProtectedResource)

	providers, err := repo.ListEnabledProviders(ctx)
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, provider.ID, providers[0].ID)

	var directCount int64
	require.NoError(t, appDB.WithContext(ctx).Raw(`SELECT count(*) FROM sso_providers WHERE id = $1`, provider.ID).Scan(&directCount).Error)
	require.Zero(t, directCount, "provider configuration must remain hidden outside a system transaction")

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE sso_providers
			SET protected_resource_config = protected_resource_config || '{"unknown_contract_field":true}'::jsonb
			WHERE id = $1
		`, provider.ID).Error
	}))
	_, err = repo.GetProvider(ctx, provider.ID)
	require.ErrorContains(t, err, "unknown field")
}

func TestSSOProviderProtectedResourceDefaultIsDormant(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	providerID := uuid.New()

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id, enabled)
			VALUES ($1, 'dormant oauth provider', 'generic_oidc', 'https://dormant.example.test', 'client', true)
		`, providerID).Error
	}))

	loaded, err := NewSSORepository(appDB, rls).GetProvider(ctx, providerID)
	require.NoError(t, err)
	require.False(t, loaded.ProtectedResource.Enabled)
	require.Empty(t, loaded.ProtectedResource.Audiences)
	require.Equal(t, "discovery", loaded.ProtectedResource.JWKSSource)
	require.Equal(t, []string{"RS256"}, loaded.ProtectedResource.Algorithms)
	require.Equal(t, "scope", loaded.ProtectedResource.ScopeClaim)
}
