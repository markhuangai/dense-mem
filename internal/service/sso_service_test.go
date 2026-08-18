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

func TestSSOValidateCredentialUsesFreshCacheMappings(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	teamID := uuid.New()
	keyID := uuid.New()
	identityID := uuid.New()
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
				Scopes:     []string{CredentialScopeRead},
				Role:       CredentialRoleMember,
				Enabled:    true,
			},
			{
				ProviderID: providerID,
				TeamID:     teamID,
				GroupID:    "group-write",
				Scopes:     []string{CredentialScopeRead, CredentialScopeWrite},
				Role:       CredentialRoleManager,
				Enabled:    true,
			},
		},
	}
	resolver := &ssoGroupResolverStub{t: t, unexpected: true}
	svc := NewSSOService(repo, SSOConfig{
		GroupResolver: resolver,
		Now:           func() time.Time { return now },
	})
	key := &domain.Credential{
		ID:              keyID,
		TeamID:          teamID,
		Scopes:          []string{CredentialScopeRead},
		Role:            CredentialRoleMember,
		OwnerIdentityID: &identityID,
		SSOProviderID:   &providerID,
		SSOSubject:      "subject-123",
	}

	validated, err := svc.ValidateCredential(context.Background(), key)

	require.NoError(t, err)
	require.Same(t, key, validated)
	assert.Equal(t, []string{CredentialScopeRead}, validated.Scopes)
	assert.Equal(t, CredentialRoleMember, validated.Role)
	assert.Equal(t, "group-read,group-write", validated.SSOGroupID)
	assert.Equal(t, "active", validated.SSOEntitlementStatus)
}

func TestSSOValidateCredentialFailsClosedWhenRefreshUnavailable(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	identityID := uuid.New()
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
	key := &domain.Credential{
		ID:              uuid.New(),
		TeamID:          teamID,
		OwnerIdentityID: &identityID,
		SSOProviderID:   &providerID,
		SSOSubject:      "subject-123",
	}

	validated, err := svc.ValidateCredential(context.Background(), key)

	require.ErrorIs(t, err, ErrSSOEntitlementRefreshStale)
	assert.Nil(t, validated)
	require.NotNil(t, repo.savedCache)
	assert.Equal(t, "error", repo.savedCache.Status)
	assert.Equal(t, []string(nil), repo.savedCache.Groups)
	assert.Equal(t, now.Add(time.Minute), repo.savedCache.ExpiresAt)
	assert.Contains(t, repo.savedCache.Error, ErrSSOGroupRefreshUnavailable.Error())
	assert.Equal(t, 1, resolver.calls)
}

func TestSSOValidateCredentialDeniesIncompleteSSOLinks(t *testing.T) {
	providerID := uuid.New()
	identityID := uuid.New()
	teamID := uuid.New()
	svc := NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{})

	for name, ordinary := range map[string]*domain.Credential{
		"empty sso fields":       {ID: uuid.New(), TeamID: teamID},
		"metadata without owner": {ID: uuid.New(), TeamID: teamID, SSOProviderID: &providerID, SSOSubject: "subject-123"},
	} {
		t.Run(name, func(t *testing.T) {
			validated, err := svc.ValidateCredential(context.Background(), ordinary)
			require.NoError(t, err)
			assert.Same(t, ordinary, validated)
		})
	}

	for name, key := range map[string]*domain.Credential{
		"owner identity only": {ID: uuid.New(), TeamID: teamID, OwnerIdentityID: &identityID},
		"provider no subject": {ID: uuid.New(), TeamID: teamID, OwnerIdentityID: &identityID, SSOProviderID: &providerID},
		"subject no provider": {ID: uuid.New(), TeamID: teamID, OwnerIdentityID: &identityID, SSOSubject: "subject-123"},
	} {
		t.Run(name, func(t *testing.T) {
			validated, err := svc.ValidateCredential(context.Background(), key)
			require.ErrorIs(t, err, ErrSSOAccessDenied)
			assert.Nil(t, validated)
		})
	}
}

func TestSSOListEnabledProvidersRequiresPublicLoginReadiness(t *testing.T) {
	ctx := context.Background()
	readyID := uuid.New()
	secretReadyID := uuid.New()
	legacyAzureID := uuid.New()
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
	legacyAzure := domain.SSOProvider{
		ID:        legacyAzureID,
		Name:      "Legacy Azure",
		Kind:      domain.SSOProviderKindAzureAD,
		IssuerURL: "https://login.microsoftonline.com/tenant/v2.0",
		ClientID:  "legacy-client",
		Enabled:   true,
	}
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
			&legacyAzure,
			&missingSecret,
			&invalid,
			&disabled,
		},
	}

	svc := NewSSOService(repo, SSOConfig{PublicBaseURL: "https://portal.example.com"})
	providers, err := svc.ListEnabledProviders(ctx)
	require.NoError(t, err)
	require.Len(t, providers, 3)
	assert.Equal(t, readyID, providers[0].ID)
	assert.Equal(t, secretReadyID, providers[1].ID)
	assert.Equal(t, legacyAzureID, providers[2].ID)

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
	t                             *testing.T
	providers                     map[uuid.UUID]*domain.SSOProvider
	providerList                  []*domain.SSOProvider
	listProvidersErr              error
	getProviderErr                error
	createProviderErr             error
	updateProviderErr             error
	deleteProviderErr             error
	deletedProviderID             uuid.UUID
	cache                         *domain.SSOEntitlementCache
	cacheErr                      error
	setCacheErr                   error
	savedCache                    *domain.SSOEntitlementCache
	mappings                      []*domain.SSOGroupMapping
	listMappingsErr               error
	createMappingErr              error
	updateMappingErr              error
	deleteMappingErr              error
	mappingsForGroupsErr          error
	mappingLookupCalls            int
	deletedMappingID              uuid.UUID
	identities                    map[uuid.UUID]*domain.SSOIdentity
	getIdentityErr                error
	upsertIdentityErr             error
	upsertIdentityID              uuid.UUID
	upsertProfileErrors           map[uuid.UUID]error
	upsertProfileCalls            int
	listTeamProfilesErr           error
	teamProfiles                  []*domain.SSOTeamMembership
	ssoProfiles                   map[uuid.UUID]*domain.SSOTeamMembership
	getSSOProfileErr              error
	directoryAuthorityActive      bool
	directoryAuthorityErr         error
	directoryProfileEntitled      map[uuid.UUID]bool
	directoryProfileEntitledErr   error
	directoryProfileEntitledCalls []uuid.UUID
	sessions                      map[string]*domain.SSOSession
	getSessionErr                 error
	createSessionErr              error
	updateSessionErr              error
	deleteSessionErr              error
	updatedSessionHash            string
	deletedSessionHash            string
	oauthStates                   []domain.SSOOAuthState
	createOAuthStateErr           error
	consumeOAuthStateErr          error
	consumableState               *domain.SSOOAuthState
	deleteExpiredAt               time.Time
	deleteExpiredErr              error
	createdSession                *domain.SSOSession
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
	r.mappingLookupCalls++
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
	if r.upsertIdentityID != uuid.Nil {
		identity.ID = r.upsertIdentityID
	} else if identity.ID == uuid.Nil {
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
	if r.getIdentityErr != nil {
		return nil, r.getIdentityErr
	}
	for _, identity := range r.identities {
		if identity != nil && identity.ProviderID == providerID && identity.Subject == subject {
			copy := *identity
			return &copy, nil
		}
	}
	return nil, nil
}

func (r *ssoRepositoryStub) UpsertTeamMembershipForMapping(ctx context.Context, identity domain.SSOIdentity, mapping domain.SSOGroupMapping, name string) (*domain.Membership, error) {
	r.upsertProfileCalls++
	if err := r.upsertProfileErrors[mapping.TeamID]; err != nil {
		return nil, err
	}
	membershipID := uuid.New()
	ownerID := membershipID
	providerID := identity.ProviderID
	membership := &domain.Membership{
		ID:              membershipID,
		ActorIdentityID: identity.ID,
		TeamID:          mapping.TeamID,
		OwnerID:         ownerID,
		Name:            name,
		Grants:          append([]string(nil), mapping.Scopes...),
		Role:            mapping.Role,
		Status:          "active",
		SSOProviderID:   &providerID,
		SSOSubject:      identity.Subject,
		SSOEmail:        identity.Email,
		SSOGroupID:      mapping.GroupID,
	}
	if r.ssoProfiles == nil {
		r.ssoProfiles = make(map[uuid.UUID]*domain.SSOTeamMembership)
	}
	item := &domain.SSOTeamMembership{
		Team: domain.Team{
			ID:   mapping.TeamID,
			Name: mapping.TeamName,
		},
		Membership: *membership,
	}
	r.ssoProfiles[ownerID] = item
	r.teamProfiles = append(r.teamProfiles, item)
	return membership, nil
}

func (r *ssoRepositoryStub) DirectoryAuthorityActive(ctx context.Context, providerID uuid.UUID) (bool, error) {
	if r.directoryAuthorityErr != nil {
		return false, r.directoryAuthorityErr
	}
	return r.directoryAuthorityActive, nil
}

func (r *ssoRepositoryStub) DirectoryMembershipEntitled(ctx context.Context, providerID, identityID, teamID uuid.UUID, groupID string) (bool, error) {
	r.directoryProfileEntitledCalls = append(r.directoryProfileEntitledCalls, identityID)
	if r.directoryProfileEntitledErr != nil {
		return false, r.directoryProfileEntitledErr
	}
	return r.directoryProfileEntitled[identityID], nil
}

func (r *ssoRepositoryStub) ListTeamMembershipsForIdentity(ctx context.Context, identityID uuid.UUID) ([]*domain.SSOTeamMembership, error) {
	if r.listTeamProfilesErr != nil {
		return nil, r.listTeamProfilesErr
	}
	items := make([]*domain.SSOTeamMembership, 0, len(r.teamProfiles))
	for _, item := range r.teamProfiles {
		if item.Membership.ActorIdentityID != identityID {
			continue
		}
		copy := *item
		copy.Membership.Grants = append([]string(nil), item.Membership.Grants...)
		items = append(items, &copy)
	}
	return items, nil
}

func (r *ssoRepositoryStub) GetTeamMembershipByOwnerID(ctx context.Context, id uuid.UUID) (*domain.SSOTeamMembership, error) {
	if r.getSSOProfileErr != nil {
		return nil, r.getSSOProfileErr
	}
	item := r.ssoProfiles[id]
	if item == nil {
		return nil, nil
	}
	copy := *item
	copy.Membership.Grants = append([]string(nil), item.Membership.Grants...)
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

func (r *ssoRepositoryStub) UpdateSessionTeam(ctx context.Context, sessionHash string, teamID uuid.UUID) error {
	if r.updateSessionErr != nil {
		return r.updateSessionErr
	}
	r.updatedSessionHash = sessionHash
	if session := r.sessions[sessionHash]; session != nil {
		for _, item := range r.teamProfiles {
			if item.Team.ID == teamID && item.Membership.ActorIdentityID == session.IdentityID {
				session.MembershipID = item.Membership.ID
				session.OwnerID = item.Membership.OwnerID
				session.TeamID = teamID
				break
			}
		}
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
