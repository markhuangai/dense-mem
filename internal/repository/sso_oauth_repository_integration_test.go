package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
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

func TestSSOProviderProtectedResourceIssuerUniquenessIsAtomic(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewSSORepository(appDB, rls)
	issuer := "https://" + uuid.NewString() + ".issuer.example.test"
	providers := []*domain.SSOProvider{
		activeOAuthRepositoryProvider(uuid.New(), issuer, "first"),
		activeOAuthRepositoryProvider(uuid.New(), issuer, "second"),
	}

	start := make(chan struct{})
	results := make(chan error, len(providers))
	var workers sync.WaitGroup
	for _, provider := range providers {
		provider := provider
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- repo.CreateProvider(ctx, provider)
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	succeeded := 0
	failed := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else {
			failed++
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, failed)

	listed, err := repo.ListEnabledProviders(ctx)
	require.NoError(t, err)
	matching := 0
	for _, provider := range listed {
		if provider.IssuerURL == issuer && provider.ProtectedResource.Enabled {
			matching++
		}
	}
	require.Equal(t, 1, matching)
}

func TestSSOProviderProtectedResourceOmittedUpdatePreservesConcurrentConfig(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewSSORepository(appDB, rls)
	provider := activeOAuthRepositoryProvider(
		uuid.New(),
		"https://"+uuid.NewString()+".issuer.example.test",
		"preserve",
	)
	require.NoError(t, repo.CreateProvider(ctx, provider))
	staleUpdate := *provider
	staleUpdate.Name = "legacy provider rename"

	concurrentConfig := domain.SSOProtectedResourceConfig{
		Enabled: false,
		OAuthProtectedResourceConfig: domain.OAuthProtectedResourceConfig{
			Audiences:  []string{"concurrent-audience"},
			JWKSSource: "static",
			JWKSURI:    "https://rotated.example.test/jwks",
			Algorithms: []string{"RS256"},
			ScopeClaim: "scope",
		},
	}
	encodedConfig, err := json.Marshal(concurrentConfig)
	require.NoError(t, err)
	configLocked := make(chan struct{})
	releaseConfig := make(chan struct{})
	configResult := make(chan error, 1)
	go func() {
		configResult <- rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			if err := tx.Exec(`
				UPDATE sso_providers
				SET protected_resource_config = $1::jsonb
				WHERE id = $2
			`, string(encodedConfig), provider.ID).Error; err != nil {
				return err
			}
			close(configLocked)
			<-releaseConfig
			return nil
		})
	}()
	<-configLocked

	updateStarted := make(chan struct{})
	updateResult := make(chan error, 1)
	go func() {
		close(updateStarted)
		updateResult <- repo.UpdateProviderPreservingProtectedResource(ctx, &staleUpdate)
	}()
	<-updateStarted
	close(releaseConfig)
	require.NoError(t, <-configResult)
	require.NoError(t, <-updateResult)

	loaded, err := repo.GetProvider(ctx, provider.ID)
	require.NoError(t, err)
	require.Equal(t, "legacy provider rename", loaded.Name)
	require.Equal(t, concurrentConfig, loaded.ProtectedResource)
}

func TestSSOProviderProtectedResourceProfileLimitIsAtomic(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewSSORepository(appDB, rls)

	for index := 0; index < domain.OAuthProtectedResourceMaximumProfiles-1; index++ {
		provider := activeOAuthRepositoryProvider(
			uuid.New(),
			"https://"+uuid.NewString()+".issuer.example.test",
			uuid.NewString(),
		)
		require.NoError(t, repo.CreateProvider(ctx, provider))
	}

	providers := []*domain.SSOProvider{
		activeOAuthRepositoryProvider(uuid.New(), "https://"+uuid.NewString()+".issuer.example.test", "first contender"),
		activeOAuthRepositoryProvider(uuid.New(), "https://"+uuid.NewString()+".issuer.example.test", "second contender"),
	}
	start := make(chan struct{})
	results := make(chan error, len(providers))
	var workers sync.WaitGroup
	for _, provider := range providers {
		provider := provider
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- repo.CreateProvider(ctx, provider)
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	succeeded := 0
	failed := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else {
			require.True(t, errors.Is(err, ErrSSOProtectedResourceProfileLimit), "unexpected create error: %v", err)
			failed++
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, failed)

	listed, err := repo.ListEnabledProviders(ctx)
	require.NoError(t, err)
	activeProfiles := 0
	for _, provider := range listed {
		if provider.ProtectedResource.Enabled {
			activeProfiles++
		}
	}
	require.Equal(t, domain.OAuthProtectedResourceMaximumProfiles, activeProfiles)
}

func activeOAuthRepositoryProvider(id uuid.UUID, issuer, suffix string) *domain.SSOProvider {
	return &domain.SSOProvider{
		ID:            id,
		Name:          "atomic oauth provider " + suffix,
		Kind:          domain.SSOProviderKindGenericOIDC,
		IssuerURL:     issuer,
		IdentityClaim: "sub",
		ClientID:      "client-" + suffix,
		GroupsScopes:  []string{},
		ProtectedResource: domain.SSOProtectedResourceConfig{
			Enabled: true,
		},
		Enabled: true,
	}
}
