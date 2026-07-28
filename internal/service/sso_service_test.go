package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSSOServiceDeleteMappingRequiresProviderID(t *testing.T) {
	svc := NewSSOService(&ssoRepositoryStub{}, SSOConfig{})
	require.ErrorContains(t, svc.DeleteMapping(context.Background(), uuid.Nil, uuid.New()), "sso provider ID is required")
}

func TestSSOValidateAPIKeyPrincipalUsesFreshCacheMappings(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	teamID := uuid.New()
	keyID := uuid.New()
	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {
				ID:      providerID,
				Name:    "Enterprise IdP",
				Enabled: true,
			},
		},
		cache: &domain.SSOEntitlementCache{
			ProviderID: providerID,
			Subject:    "subject-123",
			Groups:     []string{"group-read", "group-write"},
			Status:     "active",
			CheckedAt:  now.Add(-time.Minute),
			ExpiresAt:  now.Add(time.Minute),
		},
		mappings: []*domain.SSOGroupMapping{
			{
				ProviderID: providerID,
				TeamID:     teamID,
				GroupID:    "group-read",
				Scopes:     []string{APIKeyScopeRead},
				Role:       APIKeyRoleMember,
				Enabled:    true,
			},
			{
				ProviderID: providerID,
				TeamID:     teamID,
				GroupID:    "group-write",
				Scopes:     []string{APIKeyScopeRead, APIKeyScopeWrite},
				Role:       APIKeyRoleManager,
				Enabled:    true,
			},
		},
	}
	resolver := &ssoGroupResolverStub{t: t, unexpected: true}
	svc := NewSSOService(repo, SSOConfig{
		GroupResolver: resolver,
		Now:           func() time.Time { return now },
	})
	key := &domain.APIKey{
		ID:            keyID,
		ProfileID:     teamID,
		TeamID:        teamID,
		Scopes:        []string{APIKeyScopeRead},
		Role:          APIKeyRoleMember,
		SSOProviderID: &providerID,
		SSOSubject:    "subject-123",
	}

	validated, err := svc.ValidateAPIKeyPrincipal(context.Background(), key)

	require.NoError(t, err)
	require.Same(t, key, validated)
	assert.Equal(t, []string{APIKeyScopeRead, APIKeyScopeWrite}, validated.Scopes)
	assert.Equal(t, APIKeyRoleManager, validated.Role)
	assert.Equal(t, "group-read,group-write", validated.SSOGroupID)
	assert.Equal(t, "active", validated.SSOEntitlementStatus)
}

func TestSSOValidateAPIKeyPrincipalFailsClosedWhenRefreshUnavailable(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	teamID := uuid.New()
	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {
				ID:      providerID,
				Name:    "Enterprise IdP",
				Enabled: true,
			},
		},
	}
	resolver := &ssoGroupResolverStub{err: ErrSSOGroupRefreshUnavailable}
	svc := NewSSOService(repo, SSOConfig{
		GroupResolver:       resolver,
		EntitlementCacheTTL: time.Minute,
		Now:                 func() time.Time { return now },
	})
	key := &domain.APIKey{
		ID:            uuid.New(),
		ProfileID:     teamID,
		TeamID:        teamID,
		SSOProviderID: &providerID,
		SSOSubject:    "subject-123",
	}

	validated, err := svc.ValidateAPIKeyPrincipal(context.Background(), key)

	require.ErrorIs(t, err, ErrSSOEntitlementRefreshStale)
	assert.Nil(t, validated)
	require.NotNil(t, repo.savedCache)
	assert.Equal(t, "error", repo.savedCache.Status)
	assert.Equal(t, []string(nil), repo.savedCache.Groups)
	assert.Equal(t, now.Add(time.Minute), repo.savedCache.ExpiresAt)
	assert.Contains(t, repo.savedCache.Error, ErrSSOGroupRefreshUnavailable.Error())
	assert.Equal(t, 1, resolver.calls)
}

func TestSSOValidateAPIKeyPrincipalDeniesIncompleteSSOLinks(t *testing.T) {
	providerID := uuid.New()
	identityID := uuid.New()
	teamID := uuid.New()
	svc := NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{})

	for name, ordinary := range map[string]*domain.APIKey{
		"empty sso fields": {ID: uuid.New(), TeamID: teamID},
		"db unlinked":      {ID: uuid.New(), TeamID: teamID, AuthSource: "api_key", SSOEntitlementStatus: "unlinked"},
	} {
		t.Run(name, func(t *testing.T) {
			validated, err := svc.ValidateAPIKeyPrincipal(context.Background(), ordinary)
			require.NoError(t, err)
			assert.Same(t, ordinary, validated)
		})
	}

	for name, key := range map[string]*domain.APIKey{
		"sso auth source only": {ID: uuid.New(), TeamID: teamID, AuthSource: "sso"},
		"sso identity only":    {ID: uuid.New(), TeamID: teamID, SSOIdentityID: &identityID},
		"provider no subject":  {ID: uuid.New(), TeamID: teamID, AuthSource: "sso", SSOProviderID: &providerID},
		"subject no provider":  {ID: uuid.New(), TeamID: teamID, AuthSource: "sso", SSOSubject: "subject-123"},
	} {
		t.Run(name, func(t *testing.T) {
			validated, err := svc.ValidateAPIKeyPrincipal(context.Background(), key)
			require.ErrorIs(t, err, ErrSSOAccessDenied)
			assert.Nil(t, validated)
		})
	}
}

func TestSSOListEnabledProvidersRequiresPublicLoginReadiness(t *testing.T) {
	ctx := context.Background()
	readyID := uuid.New()
	secretReadyID := uuid.New()
	missingSecretID := uuid.New()
	invalidID := uuid.New()
	disabledID := uuid.New()
	secretEnv := "DENSE_MEM_TEST_SSO_SECRET"
	missingSecretEnv := "DENSE_MEM_TEST_MISSING_SSO_SECRET"
	t.Setenv(secretEnv, "test-secret")
	t.Setenv(missingSecretEnv, "")

	ready := domain.SSOProvider{
		ID:        readyID,
		Name:      "Ready OIDC",
		Kind:      domain.SSOProviderKindGenericOIDC,
		IssuerURL: "https://issuer.example.com",
		ClientID:  "client-id",
		Enabled:   true,
	}
	secretReady := ready
	secretReady.ID = secretReadyID
	secretReady.Name = "Secret OIDC"
	secretReady.ClientSecretEnv = secretEnv
	missingSecret := ready
	missingSecret.ID = missingSecretID
	missingSecret.Name = "Missing Secret"
	missingSecret.ClientSecretEnv = missingSecretEnv
	invalid := ready
	invalid.ID = invalidID
	invalid.Name = "Invalid OIDC"
	invalid.ClientID = ""
	disabled := ready
	disabled.ID = disabledID
	disabled.Name = "Disabled OIDC"
	disabled.Enabled = false

	repo := &ssoRepositoryStub{
		t: t,
		providerList: []*domain.SSOProvider{
			&ready,
			&secretReady,
			&missingSecret,
			&invalid,
			&disabled,
		},
	}

	svc := NewSSOService(repo, SSOConfig{PublicBaseURL: "https://portal.example.com"})
	providers, err := svc.ListEnabledProviders(ctx)
	require.NoError(t, err)
	require.Len(t, providers, 2)
	assert.Equal(t, readyID, providers[0].ID)
	assert.Equal(t, secretReadyID, providers[1].ID)

	withoutBaseURL := NewSSOService(repo, SSOConfig{})
	providers, err = withoutBaseURL.ListEnabledProviders(ctx)
	require.NoError(t, err)
	assert.Empty(t, providers)

	withInvalidBaseURL := NewSSOService(repo, SSOConfig{PublicBaseURL: "portal.example.com"})
	providers, err = withInvalidBaseURL.ListEnabledProviders(ctx)
	require.NoError(t, err)
	assert.Empty(t, providers)
}

type ssoGroupResolverStub struct {
	t          *testing.T
	groups     []string
	err        error
	calls      int
	unexpected bool
}

func (r *ssoGroupResolverStub) ResolveGroups(ctx context.Context, provider domain.SSOProvider, subject, accessToken string) ([]string, error) {
	r.calls++
	if r.unexpected {
		r.t.Helper()
		r.t.Fatalf("ResolveGroups should not be called")
	}
	return r.groups, r.err
}

type ssoRepositoryStub struct {
	t                    *testing.T
	providers            map[uuid.UUID]*domain.SSOProvider
	providerList         []*domain.SSOProvider
	listProvidersErr     error
	getProviderErr       error
	createProviderErr    error
	updateProviderErr    error
	deleteProviderErr    error
	deletedProviderID    uuid.UUID
	cache                *domain.SSOEntitlementCache
	cacheErr             error
	setCacheErr          error
	savedCache           *domain.SSOEntitlementCache
	mappings             []*domain.SSOGroupMapping
	listMappingsErr      error
	createMappingErr     error
	updateMappingErr     error
	deleteMappingErr     error
	mappingsForGroupsErr error
	deletedMappingID     uuid.UUID
	identities           map[uuid.UUID]*domain.SSOIdentity
	getIdentityErr       error
	upsertIdentityErr    error
	upsertProfileErrors  map[uuid.UUID]error
	listTeamProfilesErr  error
	teamProfiles         []*domain.SSOTeamProfile
	ssoProfiles          map[uuid.UUID]*domain.APIKey
	getSSOProfileErr     error
	sessions             map[string]*domain.SSOSession
	getSessionErr        error
	createSessionErr     error
	updateSessionErr     error
	deleteSessionErr     error
	updatedSessionHash   string
	deletedSessionHash   string
	oauthStates          []domain.SSOOAuthState
	createOAuthStateErr  error
	consumeOAuthStateErr error
	consumableState      *domain.SSOOAuthState
	deleteExpiredAt      time.Time
	deleteExpiredErr     error
	createdSession       *domain.SSOSession
}

func (r *ssoRepositoryStub) unexpected(name string) {
	r.t.Helper()
	r.t.Fatalf("%s should not be called", name)
}

func (r *ssoRepositoryStub) ListProviders(ctx context.Context) ([]*domain.SSOProvider, error) {
	if r.listProvidersErr != nil {
		return nil, r.listProvidersErr
	}
	if len(r.providerList) > 0 {
		return copySSOProviders(r.providerList), nil
	}
	items := make([]*domain.SSOProvider, 0, len(r.providers))
	for _, provider := range r.providers {
		copy := *provider
		items = append(items, &copy)
	}
	return items, nil
}

func (r *ssoRepositoryStub) ListEnabledProviders(ctx context.Context) ([]*domain.SSOProvider, error) {
	providers, err := r.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*domain.SSOProvider, 0, len(providers))
	for _, provider := range providers {
		if provider.Enabled {
			items = append(items, provider)
		}
	}
	return items, nil
}

func (r *ssoRepositoryStub) GetProvider(ctx context.Context, id uuid.UUID) (*domain.SSOProvider, error) {
	if r.getProviderErr != nil {
		return nil, r.getProviderErr
	}
	provider := r.providers[id]
	if provider == nil {
		return nil, nil
	}
	copy := *provider
	return &copy, nil
}

func (r *ssoRepositoryStub) CreateProvider(ctx context.Context, provider *domain.SSOProvider) error {
	if r.createProviderErr != nil {
		return r.createProviderErr
	}
	if provider.ID == uuid.Nil {
		provider.ID = uuid.New()
	}
	copy := *provider
	if r.providers == nil {
		r.providers = make(map[uuid.UUID]*domain.SSOProvider)
	}
	r.providers[provider.ID] = &copy
	r.providerList = append(r.providerList, &copy)
	return nil
}

func (r *ssoRepositoryStub) UpdateProvider(ctx context.Context, provider *domain.SSOProvider) error {
	if r.updateProviderErr != nil {
		return r.updateProviderErr
	}
	copy := *provider
	if r.providers == nil {
		r.providers = make(map[uuid.UUID]*domain.SSOProvider)
	}
	r.providers[provider.ID] = &copy
	found := false
	for index, item := range r.providerList {
		if item.ID == provider.ID {
			r.providerList[index] = &copy
			found = true
			break
		}
	}
	if !found {
		r.providerList = append(r.providerList, &copy)
	}
	return nil
}

func (r *ssoRepositoryStub) DeleteProvider(ctx context.Context, id uuid.UUID) error {
	if r.deleteProviderErr != nil {
		return r.deleteProviderErr
	}
	r.deletedProviderID = id
	delete(r.providers, id)
	return nil
}

func (r *ssoRepositoryStub) ListMappings(ctx context.Context, providerID uuid.UUID) ([]*domain.SSOGroupMapping, error) {
	if r.listMappingsErr != nil {
		return nil, r.listMappingsErr
	}
	items := make([]*domain.SSOGroupMapping, 0)
	for _, mapping := range r.mappings {
		if mapping.ProviderID == providerID {
			copy := *mapping
			items = append(items, &copy)
		}
	}
	return items, nil
}

func (r *ssoRepositoryStub) CreateMapping(ctx context.Context, mapping *domain.SSOGroupMapping) error {
	if r.createMappingErr != nil {
		return r.createMappingErr
	}
	if mapping.ID == uuid.Nil {
		mapping.ID = uuid.New()
	}
	copy := *mapping
	r.mappings = append(r.mappings, &copy)
	return nil
}

func (r *ssoRepositoryStub) UpdateMapping(ctx context.Context, mapping *domain.SSOGroupMapping) error {
	if r.updateMappingErr != nil {
		return r.updateMappingErr
	}
	copy := *mapping
	for index, item := range r.mappings {
		if item.ID == mapping.ID {
			r.mappings[index] = &copy
			return nil
		}
	}
	r.mappings = append(r.mappings, &copy)
	return nil
}

func (r *ssoRepositoryStub) DeleteMapping(ctx context.Context, providerID, id uuid.UUID) error {
	if r.deleteMappingErr != nil {
		return r.deleteMappingErr
	}
	r.deletedMappingID = id
	for index, mapping := range r.mappings {
		if mapping.ID == id && mapping.ProviderID == providerID {
			r.mappings = append(r.mappings[:index], r.mappings[index+1:]...)
			break
		}
	}
	return nil
}

func (r *ssoRepositoryStub) ListMappingsForGroups(ctx context.Context, providerID uuid.UUID, groups []string) ([]*domain.SSOGroupMapping, error) {
	if r.mappingsForGroupsErr != nil {
		return nil, r.mappingsForGroupsErr
	}
	groupSet := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		groupSet[group] = struct{}{}
	}
	result := make([]*domain.SSOGroupMapping, 0)
	for _, mapping := range r.mappings {
		if mapping == nil || mapping.ProviderID != providerID {
			continue
		}
		if _, ok := groupSet[mapping.GroupID]; !ok {
			continue
		}
		copy := *mapping
		result = append(result, &copy)
	}
	return result, nil
}

func (r *ssoRepositoryStub) UpsertIdentity(ctx context.Context, identity *domain.SSOIdentity) error {
	if r.upsertIdentityErr != nil {
		return r.upsertIdentityErr
	}
	if identity.ID == uuid.Nil {
		identity.ID = uuid.New()
	}
	copy := *identity
	if r.identities == nil {
		r.identities = make(map[uuid.UUID]*domain.SSOIdentity)
	}
	r.identities[identity.ID] = &copy
	return nil
}

func (r *ssoRepositoryStub) GetIdentity(ctx context.Context, id uuid.UUID) (*domain.SSOIdentity, error) {
	if r.getIdentityErr != nil {
		return nil, r.getIdentityErr
	}
	identity := r.identities[id]
	if identity == nil {
		return nil, nil
	}
	copy := *identity
	return &copy, nil
}

func (r *ssoRepositoryStub) GetIdentityByProviderSubject(ctx context.Context, providerID uuid.UUID, subject string) (*domain.SSOIdentity, error) {
	r.unexpected("GetIdentityByProviderSubject")
	return nil, nil
}

func (r *ssoRepositoryStub) UpsertTeamProfileForMapping(ctx context.Context, identity domain.SSOIdentity, mapping domain.SSOGroupMapping, name string) (*domain.APIKey, error) {
	if err := r.upsertProfileErrors[mapping.TeamID]; err != nil {
		return nil, err
	}
	key := &domain.APIKey{
		ID:            uuid.New(),
		ProfileID:     mapping.TeamID,
		TeamID:        mapping.TeamID,
		TeamName:      mapping.TeamName,
		Name:          name,
		Scopes:        append([]string(nil), mapping.Scopes...),
		Role:          mapping.Role,
		RateLimit:     0,
		SSOIdentityID: &identity.ID,
		SSOProviderID: &identity.ProviderID,
		SSOSubject:    identity.Subject,
		SSOEmail:      identity.Email,
		SSOGroupID:    mapping.GroupID,
	}
	if r.ssoProfiles == nil {
		r.ssoProfiles = make(map[uuid.UUID]*domain.APIKey)
	}
	r.ssoProfiles[key.ID] = key
	r.teamProfiles = append(r.teamProfiles, &domain.SSOTeamProfile{
		Team: domain.Profile{
			ID:   mapping.TeamID,
			Name: mapping.TeamName,
		},
		Profile: *key,
	})
	return key, nil
}

func (r *ssoRepositoryStub) ListTeamProfilesForIdentity(ctx context.Context, identityID uuid.UUID) ([]*domain.SSOTeamProfile, error) {
	if r.listTeamProfilesErr != nil {
		return nil, r.listTeamProfilesErr
	}
	items := make([]*domain.SSOTeamProfile, 0, len(r.teamProfiles))
	for _, item := range r.teamProfiles {
		if item.Profile.SSOIdentityID == nil || *item.Profile.SSOIdentityID != identityID {
			continue
		}
		copy := *item
		copy.Profile.Scopes = append([]string(nil), item.Profile.Scopes...)
		items = append(items, &copy)
	}
	return items, nil
}

func (r *ssoRepositoryStub) GetSSOProfileByID(ctx context.Context, id uuid.UUID) (*domain.APIKey, error) {
	if r.getSSOProfileErr != nil {
		return nil, r.getSSOProfileErr
	}
	key := r.ssoProfiles[id]
	if key == nil {
		return nil, nil
	}
	copy := *key
	copy.Scopes = append([]string(nil), key.Scopes...)
	return &copy, nil
}

func (r *ssoRepositoryStub) GetEntitlementCache(ctx context.Context, providerID uuid.UUID, subject string) (*domain.SSOEntitlementCache, error) {
	if r.cacheErr != nil {
		return nil, r.cacheErr
	}
	if r.cache == nil || r.cache.ProviderID != providerID || r.cache.Subject != subject {
		return nil, nil
	}
	copy := *r.cache
	copy.Groups = append([]string(nil), r.cache.Groups...)
	return &copy, nil
}

func (r *ssoRepositoryStub) SetEntitlementCache(ctx context.Context, cache domain.SSOEntitlementCache) error {
	if r.setCacheErr != nil {
		return r.setCacheErr
	}
	copy := cache
	copy.Groups = append([]string(nil), cache.Groups...)
	r.savedCache = &copy
	r.cache = &copy
	return nil
}

func (r *ssoRepositoryStub) CreateOAuthState(ctx context.Context, state domain.SSOOAuthState) error {
	if r.createOAuthStateErr != nil {
		return r.createOAuthStateErr
	}
	r.oauthStates = append(r.oauthStates, state)
	return nil
}

func (r *ssoRepositoryStub) ConsumeOAuthState(ctx context.Context, stateHash string) (*domain.SSOOAuthState, error) {
	if r.consumeOAuthStateErr != nil {
		return nil, r.consumeOAuthStateErr
	}
	if r.consumableState != nil && r.consumableState.StateHash == stateHash {
		copy := *r.consumableState
		r.consumableState = nil
		return &copy, nil
	}
	return nil, nil
}

func (r *ssoRepositoryStub) DeleteExpiredOAuthStates(ctx context.Context, now time.Time) error {
	r.deleteExpiredAt = now
	return r.deleteExpiredErr
}

func (r *ssoRepositoryStub) CreateSession(ctx context.Context, session domain.SSOSession) error {
	if r.createSessionErr != nil {
		return r.createSessionErr
	}
	copy := session
	r.createdSession = &copy
	if r.sessions == nil {
		r.sessions = make(map[string]*domain.SSOSession)
	}
	r.sessions[session.SessionHash] = &copy
	return nil
}

func (r *ssoRepositoryStub) GetSession(ctx context.Context, sessionHash string) (*domain.SSOSession, error) {
	if r.getSessionErr != nil {
		return nil, r.getSessionErr
	}
	session := r.sessions[sessionHash]
	if session == nil {
		return nil, nil
	}
	copy := *session
	return &copy, nil
}

func (r *ssoRepositoryStub) UpdateSessionTeam(ctx context.Context, sessionHash string, teamProfileID, teamID uuid.UUID) error {
	if r.updateSessionErr != nil {
		return r.updateSessionErr
	}
	r.updatedSessionHash = sessionHash
	if session := r.sessions[sessionHash]; session != nil {
		session.TeamProfileID = teamProfileID
		session.TeamID = teamID
	}
	return nil
}

func (r *ssoRepositoryStub) DeleteSession(ctx context.Context, sessionHash string) error {
	if r.deleteSessionErr != nil {
		return r.deleteSessionErr
	}
	r.deletedSessionHash = sessionHash
	delete(r.sessions, sessionHash)
	return nil
}

func (r *ssoRepositoryStub) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	r.unexpected("DeleteExpiredSessions")
	return nil
}

func copySSOProviders(providers []*domain.SSOProvider) []*domain.SSOProvider {
	items := make([]*domain.SSOProvider, 0, len(providers))
	for _, provider := range providers {
		copy := *provider
		items = append(items, &copy)
	}
	return items
}
