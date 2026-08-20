package service

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestAuthenticateOAuthBearerValidatesBeforeFixingMembershipContext(t *testing.T) {
	fixture := newSSOOAuthFixture(t)

	actor, err := fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), &fixture.teamID)
	require.NoError(t, err)
	require.Equal(t, fixture.teamID, actor.Team.ID)
	require.Equal(t, fixture.identity.ID, actor.Identity.ID)
	require.Equal(t, fixture.membership.Membership.OwnerID, actor.OwnerID)
	require.Equal(t, []string{CredentialScopeRead}, actor.Membership.Grants)
	require.Equal(t, fixture.membership.Membership.MemorySpaceID, actor.Membership.MemorySpaceID)
	require.Equal(t, 1, fixture.repo.identityLookupCalls)

	claims := fixture.claims(true)
	claims["aud"] = "wrong-audience"
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.compatibility.token(t, "entra", "primary", claims), nil)
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	require.Equal(t, 1, fixture.repo.identityLookupCalls, "invalid bearer material must not reach identity lookup")

	otherTeamID := uuid.New()
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), &otherTeamID)
	require.ErrorIs(t, err, ErrOAuthAccessDenied)
}

func TestAuthenticateOAuthBearerSupportsSubAndEntraOIDIdentityClaims(t *testing.T) {
	for _, identityClaim := range []string{"sub", "oid"} {
		t.Run(identityClaim, func(t *testing.T) {
			fixture := newSSOOAuthFixture(t)
			fixture.provider.IdentityClaim = identityClaim
			if identityClaim == "sub" {
				fixture.identity.Subject = "entra-user"
				fixture.membership.Membership.SSOSubject = fixture.identity.Subject
				fixture.repo.cache.Subject = fixture.identity.Subject
			}

			actor, err := fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
			require.NoError(t, err)
			require.Equal(t, fixture.identity.ID, actor.Identity.ID)
		})
	}
}

func TestAuthenticateOAuthBearerRequiresUnambiguousVerifiedTeam(t *testing.T) {
	fixture := newSSOOAuthFixture(t)

	actor, err := fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, false), nil)
	require.NoError(t, err)
	require.Equal(t, fixture.teamID, actor.Team.ID)

	otherTeamID := uuid.New()
	other := fixture.addMembership(otherTeamID)
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, false), nil)
	require.ErrorIs(t, err, ErrOAuthTeamRequired)

	actor, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, false), &otherTeamID)
	require.NoError(t, err)
	require.Equal(t, other.Team.ID, actor.Team.ID)

	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), &otherTeamID)
	require.ErrorIs(t, err, ErrOAuthAccessDenied)
}

func TestAuthenticateOAuthBearerFailsClosedForIdentityAndMembershipMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ssoOAuthFixture)
	}{
		{
			name: "disabled identity",
			mutate: func(fixture *ssoOAuthFixture) {
				fixture.identity.Active = false
			},
		},
		{
			name: "missing membership",
			mutate: func(fixture *ssoOAuthFixture) {
				fixture.repo.teamProfiles = nil
			},
		},
		{
			name: "provider mismatch",
			mutate: func(fixture *ssoOAuthFixture) {
				otherProviderID := uuid.New()
				fixture.membership.Membership.SSOProviderID = &otherProviderID
			},
		},
		{
			name: "subject mismatch",
			mutate: func(fixture *ssoOAuthFixture) {
				fixture.membership.Membership.SSOSubject = "other-subject"
			},
		},
		{
			name: "revoked mapping",
			mutate: func(fixture *ssoOAuthFixture) {
				fixture.repo.mappings = nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSSOOAuthFixture(t)
			test.mutate(fixture)

			_, err := fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
			require.ErrorIs(t, err, ErrOAuthAccessDenied)
		})
	}
}

func TestAuthenticateOAuthBearerRevalidatesDirectoryMembership(t *testing.T) {
	fixture := newSSOOAuthFixture(t)
	fixture.repo.directoryAuthorityActive = true
	fixture.repo.directoryProfileEntitled = map[uuid.UUID]bool{fixture.identity.ID: false}

	_, err := fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.ErrorIs(t, err, ErrOAuthAccessDenied)
	require.Equal(t, []uuid.UUID{fixture.identity.ID}, fixture.repo.directoryProfileEntitledCalls)

	fixture.repo.directoryProfileEntitled[fixture.identity.ID] = true
	claims := fixture.claims(true)
	claims["scp"] = "memory.read"
	actor, err := fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.compatibility.token(t, "entra", "primary", claims), nil)
	require.NoError(t, err)
	require.Equal(t, []string{CredentialScopeRead}, actor.Membership.Grants)
}

func TestOAuthProtectedResourceMetadataIsDormantAndSorted(t *testing.T) {
	fixture := newSSOOAuthFixture(t)
	fixture.provider.ProtectedResource.Enabled = false

	metadata, err := fixture.service.OAuthProtectedResourceMetadata(t.Context())
	require.NoError(t, err)
	require.Empty(t, metadata.AuthorizationServers)
	require.Empty(t, metadata.ScopesSupported)

	fixture.provider.ProtectedResource.Enabled = true
	genericProfile := fixture.compatibility.profiles()[2]
	genericProvider := &domain.SSOProvider{
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
	fixture.repo.providerList = []*domain.SSOProvider{genericProvider, fixture.provider}

	metadata, err = fixture.service.OAuthProtectedResourceMetadata(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{fixture.provider.IssuerURL, genericProvider.IssuerURL}, metadata.AuthorizationServers)
	require.Equal(t, []string{"generic.read", "generic.write", "memory.read", "memory.write"}, metadata.ScopesSupported)
}

func TestAuthenticateOAuthBearerReconfiguresWithoutDiscardingCompatibleJWKS(t *testing.T) {
	fixture := newSSOOAuthFixture(t)

	_, err := fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.NoError(t, err)
	initialRequests := fixture.compatibility.jwksRequests("entra")

	fixture.provider.ProtectedResource.ScopeMappings[0].InternalScopes = []string{CredentialScopeRead, CredentialScopeFeedbackRead}
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.NoError(t, err)
	require.Equal(t, initialRequests, fixture.compatibility.jwksRequests("entra"))

	fixture.provider.ProtectedResource.JWKSSource = "static"
	fixture.provider.ProtectedResource.JWKSURI = fixture.compatibility.server.URL + "/entra/alternate-jwks"
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.NoError(t, err)
	require.Equal(t, initialRequests+1, fixture.compatibility.jwksRequests("entra"))
}

func TestOAuthProtectedResourcePublicURLAndErrors(t *testing.T) {
	repo := &ssoRepositoryStub{t: t}
	service := NewSSOService(repo, SSOConfig{
		RuntimeConfig: ssoRuntimeConfigStub{cfg: SSORuntimeConfig{MCPPublicBaseURL: "https://memory.example.test/base/"}},
	})

	publicURL, err := service.MCPPublicBaseURL(t.Context())
	require.NoError(t, err)
	require.Equal(t, "https://memory.example.test/base", publicURL)

	publicURL, err = (*SSOService)(nil).MCPPublicBaseURL(t.Context())
	require.NoError(t, err)
	require.Empty(t, publicURL)

	runtimeErr := errors.New("runtime unavailable")
	service = NewSSOService(repo, SSOConfig{RuntimeConfig: ssoRuntimeConfigStub{err: runtimeErr}})
	_, err = service.MCPPublicBaseURL(t.Context())
	require.ErrorIs(t, err, runtimeErr)
	require.Equal(t, "oauth membership access denied", ErrOAuthAccessDenied.Error())
	require.Equal(t, "oauth team selection required", ErrOAuthTeamRequired.Error())
}

func TestOAuthProtectedResourceFailsClosedBeforeIdentityResolution(t *testing.T) {
	fixture := newSSOOAuthFixture(t)

	_, err := (*SSOService)(nil).AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	_, err = NewSSOService(nil, SSOConfig{}).AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), "not-a-jwt", nil)
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)
	require.Zero(t, fixture.repo.identityLookupCalls)

	backendErr := errors.New("provider list unavailable")
	fixture.repo.listProvidersErr = backendErr
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	metadata, err := fixture.service.OAuthProtectedResourceMetadata(t.Context())
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)
	require.Empty(t, metadata.AuthorizationServers)

	metadata, err = (*SSOService)(nil).OAuthProtectedResourceMetadata(t.Context())
	require.NoError(t, err)
	require.Empty(t, metadata.AuthorizationServers)
}

func TestAuthenticateOAuthBearerPropagatesAuthoritativeLookupFailures(t *testing.T) {
	backendErr := errors.New("authoritative lookup failed")

	fixture := newSSOOAuthFixture(t)
	fixture.repo.getIdentityErr = backendErr
	_, err := fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.ErrorIs(t, err, backendErr)

	fixture = newSSOOAuthFixture(t)
	fixture.repo.listTeamProfilesErr = backendErr
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.ErrorIs(t, err, backendErr)

	fixture = newSSOOAuthFixture(t)
	claims := fixture.claims(false)
	claims["tenant"] = "not-a-team-id"
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.compatibility.token(t, "entra", "primary", claims), nil)
	require.ErrorIs(t, err, ErrOAuthTokenInvalid)

	fixture = newSSOOAuthFixture(t)
	fixture.repo.cache = nil
	fixture.service.groupResolver = &ssoGroupResolverStub{t: t, err: backendErr}
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.ErrorIs(t, err, ErrOAuthProviderUnavailable)

	fixture = newSSOOAuthFixture(t)
	fixture.repo.getProviderErr = backendErr
	_, err = fixture.service.AuthenticateOAuthBearer(t.Context(), fixture.token(t, true), nil)
	require.ErrorIs(t, err, backendErr)
}

type ssoOAuthFixture struct {
	compatibility *oauthCompatibilityFixture
	service       *SSOService
	repo          *ssoRepositoryStub
	provider      *domain.SSOProvider
	identity      *domain.SSOIdentity
	membership    *domain.SSOTeamMembership
	teamID        uuid.UUID
}

func newSSOOAuthFixture(t *testing.T) *ssoOAuthFixture {
	t.Helper()
	compatibility := newOAuthCompatibilityFixture(t)
	profile := compatibility.profiles()[0]
	providerID := uuid.New()
	teamID := uuid.New()
	identity := &domain.SSOIdentity{
		ID:          uuid.New(),
		ProviderID:  providerID,
		Subject:     "entra-object-id",
		DisplayName: "Entra User",
		Active:      true,
	}
	provider := &domain.SSOProvider{
		ID:            providerID,
		Name:          "Entra",
		Kind:          domain.SSOProviderKindAzureAD,
		IssuerURL:     profile.Issuer,
		TenantID:      "tenant",
		IdentityClaim: "oid",
		ClientID:      "entra-client",
		ProtectedResource: domain.SSOProtectedResourceConfig{
			Enabled:                      true,
			OAuthProtectedResourceConfig: profile.ProtectedResource,
		},
		Enabled: true,
	}
	membership := newSSOOAuthMembership(providerID, identity.ID, identity.Subject, teamID)
	repo := &ssoRepositoryStub{
		t:            t,
		providers:    map[uuid.UUID]*domain.SSOProvider{providerID: provider},
		providerList: []*domain.SSOProvider{provider},
		identities:   map[uuid.UUID]*domain.SSOIdentity{identity.ID: identity},
		teamProfiles: []*domain.SSOTeamMembership{membership},
		cache: &domain.SSOEntitlementCache{
			ProviderID: providerID,
			Subject:    identity.Subject,
			Groups:     []string{"memory-users"},
			Status:     "active",
			CheckedAt:  compatibility.currentTime(),
			ExpiresAt:  compatibility.currentTime().Add(time.Hour),
		},
		mappings: []*domain.SSOGroupMapping{newSSOOAuthMapping(providerID, teamID)},
	}
	return &ssoOAuthFixture{
		compatibility: compatibility,
		service: NewSSOService(repo, SSOConfig{
			HTTPClient: compatibility.server.Client(),
			Now:        func() time.Time { return compatibility.currentTime() },
		}),
		repo:       repo,
		provider:   provider,
		identity:   identity,
		membership: membership,
		teamID:     teamID,
	}
}

func (fixture *ssoOAuthFixture) claims(includeTeam bool) map[string]any {
	claims := fixture.compatibility.claims("entra")
	claims["oid"] = fixture.identity.Subject
	claims["scp"] = "memory.read memory.write"
	if includeTeam {
		claims["tenant"] = fixture.teamID.String()
	}
	return claims
}

func (fixture *ssoOAuthFixture) token(t *testing.T, includeTeam bool) string {
	t.Helper()
	return fixture.compatibility.token(t, "entra", "primary", fixture.claims(includeTeam))
}

func (fixture *ssoOAuthFixture) addMembership(teamID uuid.UUID) *domain.SSOTeamMembership {
	membership := newSSOOAuthMembership(fixture.provider.ID, fixture.identity.ID, fixture.identity.Subject, teamID)
	fixture.repo.teamProfiles = append(fixture.repo.teamProfiles, membership)
	fixture.repo.mappings = append(fixture.repo.mappings, newSSOOAuthMapping(fixture.provider.ID, teamID))
	return membership
}

func newSSOOAuthMembership(providerID, identityID uuid.UUID, subject string, teamID uuid.UUID) *domain.SSOTeamMembership {
	return &domain.SSOTeamMembership{
		Team: domain.Team{ID: teamID, Name: "OAuth Team"},
		Membership: domain.Membership{
			ID:              uuid.New(),
			MemorySpaceID:   uuid.New(),
			ActorIdentityID: identityID,
			TeamID:          teamID,
			OwnerID:         uuid.New(),
			Name:            "OAuth User",
			Grants:          []string{CredentialScopeRead, CredentialScopeWrite},
			Role:            CredentialRoleMember,
			Status:          "active",
			SSOProviderID:   &providerID,
			SSOSubject:      subject,
			SSOGroupID:      "memory-users",
		},
	}
}

func newSSOOAuthMapping(providerID, teamID uuid.UUID) *domain.SSOGroupMapping {
	return &domain.SSOGroupMapping{
		ID:         uuid.New(),
		ProviderID: providerID,
		TeamID:     teamID,
		GroupID:    "memory-users",
		Scopes:     []string{CredentialScopeRead},
		Role:       CredentialRoleMember,
		Enabled:    true,
	}
}
