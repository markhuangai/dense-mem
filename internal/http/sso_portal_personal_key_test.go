package http

import (
	nethttp "net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

type ssoPersonalKeyFixture struct {
	handler      *userPortalHandler
	keySvc       *userPortalKeySvc
	identityID   uuid.UUID
	teamID       uuid.UUID
	profileID    uuid.UUID
	sessionToken string
}

func TestUserPortalSSOPersonalKeyLifecycle(t *testing.T) {
	fixture := newSSOPersonalKeyFixture(service.APIKeyRoleMember, service.StandardAPIKeyScopes())

	c, _ := userSSOContext(nethttp.MethodGet, "/ui/api/session", "", fixture.sessionToken)
	session, err := fixture.handler.currentSSOSession(c)
	require.NoError(t, err)
	require.True(t, session.CanCreatePersonalKey)
	require.Nil(t, session.PersonalKey)
	require.Equal(t, []string{service.APIKeyScopeRead, service.APIKeyScopeWrite}, session.PersonalKeyMaxScopes)

	c, rec := userSSOContext(nethttp.MethodPost, "/ui/api/sso/key", `{"name":"Owned","scopes":["read","write"],"rate_limit":90}`, fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.NoError(t, fixture.handler.createSSOKey(c))
	require.Equal(t, nethttp.StatusCreated, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"dm_created_plaintext"`)
	require.Equal(t, "Owned", fixture.keySvc.lastCreateReq.Name)
	require.Equal(t, []string{service.APIKeyScopeRead, service.APIKeyScopeWrite}, fixture.keySvc.lastCreateReq.Scopes)
	require.NotNil(t, fixture.keySvc.lastCreateReq.SSOOwnerIdentityID)
	require.Equal(t, fixture.identityID, *fixture.keySvc.lastCreateReq.SSOOwnerIdentityID)
	require.Len(t, fixture.keySvc.keys, 1)

	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/session", "", fixture.sessionToken)
	session, err = fixture.handler.currentSSOSession(c)
	require.NoError(t, err)
	require.NotNil(t, session.PersonalKey)
	require.False(t, session.CanCreatePersonalKey)
	require.True(t, session.CanRotatePersonalKey)

	c, rec = userSSOContext(nethttp.MethodPost, "/ui/api/sso/key/rotate", `{}`, fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.NoError(t, fixture.handler.rotateSSOKey(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"dm_rotated_plaintext"`)
	require.Equal(t, fixture.teamID, fixture.keySvc.rotateProfileID)
	require.Equal(t, fixture.keySvc.keys[0].ID, fixture.keySvc.rotateKeyID)
}

func TestUserPortalSSOCreateKeyRequestValidation(t *testing.T) {
	expiresAt := "2026-06-13T12:30:00Z"
	identity := domain.SSOIdentity{Email: "user@example.com"}

	t.Run("defaults empty name and write entitlement to read write", func(t *testing.T) {
		req, err := userPortalSSOCreateKeyRequest(userPortalCreateSSOKeyRequest{
			RateLimit: 30,
			ExpiresAt: &expiresAt,
		}, identity, service.StandardAPIKeyScopes())
		require.NoError(t, err)
		require.Equal(t, "SSO user@example.com", req.Name)
		require.Equal(t, service.StandardAPIKeyScopes(), req.Scopes)
		require.Equal(t, 30, req.RateLimit)
		require.NotNil(t, req.ExpiresAt)
		require.Equal(t, service.APIKeyRoleMember, req.Role)
	})

	t.Run("rejects missing read entitlement", func(t *testing.T) {
		_, err := userPortalSSOCreateKeyRequest(userPortalCreateSSOKeyRequest{RateLimit: 30}, identity, []string{service.APIKeyScopeWrite})
		require.ErrorContains(t, err, "sso access denied")
	})

	t.Run("rejects write above entitlement", func(t *testing.T) {
		_, err := userPortalSSOCreateKeyRequest(userPortalCreateSSOKeyRequest{
			Scopes:    service.StandardAPIKeyScopes(),
			RateLimit: 30,
		}, identity, []string{service.APIKeyScopeRead})
		require.ErrorContains(t, err, "cannot create api key above sso entitlement")
	})

	t.Run("rejects bad rate limit and expiry", func(t *testing.T) {
		_, err := userPortalSSOCreateKeyRequest(userPortalCreateSSOKeyRequest{RateLimit: 0}, identity, []string{service.APIKeyScopeRead})
		require.ErrorContains(t, err, "rate_limit must be greater than zero")

		badExpiresAt := "not-a-date"
		_, err = userPortalSSOCreateKeyRequest(userPortalCreateSSOKeyRequest{
			RateLimit: 30,
			ExpiresAt: &badExpiresAt,
		}, identity, []string{service.APIKeyScopeRead})
		require.ErrorContains(t, err, "expires_at must be an RFC3339 timestamp")
	})
}

func TestSSOOwnedKeyDefaultName(t *testing.T) {
	require.Equal(t, "SSO user@example.com", ssoOwnedKeyDefaultName(domain.SSOIdentity{Email: " user@example.com "}))
	require.Equal(t, "SSO User Name", ssoOwnedKeyDefaultName(domain.SSOIdentity{DisplayName: " User Name "}))
	require.Equal(t, "SSO subject-1234", ssoOwnedKeyDefaultName(domain.SSOIdentity{Subject: "subject-123456789"}))
	require.Equal(t, "SSO API key", ssoOwnedKeyDefaultName(domain.SSOIdentity{}))
}

func TestUserPortalSSOPersonalKeyDeniedBranches(t *testing.T) {
	t.Run("manager uses team section", func(t *testing.T) {
		fixture := newSSOPersonalKeyFixture(service.APIKeyRoleManager, service.StandardAPIKeyScopes())
		c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/key", `{"rate_limit":30}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.createSSOKey(c), "sso managers should create api keys from the team section")

		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/key/rotate", `{}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.rotateSSOKey(c), "sso managers should rotate api keys from the team section")
	})

	t.Run("create requires key service and no existing owned key", func(t *testing.T) {
		fixture := newSSOPersonalKeyFixture(service.APIKeyRoleMember, service.StandardAPIKeyScopes())
		fixture.handler.keys = nil
		c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/key", `{"rate_limit":30}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.createSSOKey(c), "api key service unavailable")

		fixture = newSSOPersonalKeyFixture(service.APIKeyRoleMember, service.StandardAPIKeyScopes())
		fixture.keySvc.keys = append(fixture.keySvc.keys, ssoOwnedAPIKey(fixture.teamID, fixture.identityID, service.StandardAPIKeyScopes()))
		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/key", `{"rate_limit":30}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.createSSOKey(c), "sso-owned api key already exists for this team")
	})

	t.Run("rotate requires owned writable key and writable entitlement", func(t *testing.T) {
		fixture := newSSOPersonalKeyFixture(service.APIKeyRoleMember, service.StandardAPIKeyScopes())
		c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/key/rotate", `{}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.rotateSSOKey(c), "sso-owned api key not found")

		fixture.keySvc.keys = append(fixture.keySvc.keys, ssoOwnedAPIKey(fixture.teamID, fixture.identityID, []string{service.APIKeyScopeRead}))
		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/key/rotate", `{}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.rotateSSOKey(c), "sso-owned api key cannot be rotated")

		readOnlyFixture := newSSOPersonalKeyFixture(service.APIKeyRoleMember, []string{service.APIKeyScopeRead})
		readOnlyFixture.keySvc.keys = append(readOnlyFixture.keySvc.keys, ssoOwnedAPIKey(readOnlyFixture.teamID, readOnlyFixture.identityID, service.StandardAPIKeyScopes()))
		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/key/rotate", `{}`, readOnlyFixture.sessionToken)
		setUserSSOPrincipal(c, readOnlyFixture.profileID, readOnlyFixture.teamID)
		require.ErrorContains(t, readOnlyFixture.handler.rotateSSOKey(c), "sso-owned api key cannot be rotated")
	})
}

func TestUserPortalSSORequestSessionGuards(t *testing.T) {
	handler := &userPortalHandler{}
	c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/key", `{}`, "")
	_, _, err := handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "sso is not configured")

	fixture := newSSOPersonalKeyFixture(service.APIKeyRoleMember, service.StandardAPIKeyScopes())
	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/key", `{}`, fixture.sessionToken)
	_, _, err = fixture.handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "authentication required")

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/key", `{}`, fixture.sessionToken)
	setUserAPIKeyPrincipal(c, fixture.profileID, fixture.teamID)
	_, _, err = fixture.handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "sso session required")

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/key", `{}`, "")
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	_, _, err = fixture.handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "authentication required")
}

func newSSOPersonalKeyFixture(role string, scopes []string) ssoPersonalKeyFixture {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	identityID := uuid.New()
	teamID := uuid.New()
	profileID := uuid.New()
	sessionToken := "session-token"
	mapping := httpSSOMapping(providerID, teamID, "Team One", "group-one", role, now)
	mapping.Scopes = append([]string{}, scopes...)
	profile := httpSSOTeamProfile(identityID, providerID, profileID, teamID, "Team One", role, now)
	profile.Profile.Scopes = append([]string{}, scopes...)
	repo := &httpSSORepoStub{
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: httpSSOProvider(providerID, "Enterprise IdP", now),
		},
		cache: &domain.SSOEntitlementCache{
			ProviderID: providerID,
			Subject:    "subject-123",
			Groups:     []string{"group-one"},
			Status:     "active",
			CheckedAt:  now,
			ExpiresAt:  now.Add(time.Hour),
		},
		identities: map[uuid.UUID]*domain.SSOIdentity{
			identityID: {
				ID:          identityID,
				ProviderID:  providerID,
				Subject:     "subject-123",
				Email:       "user@example.com",
				DisplayName: "SSO User",
			},
		},
		mappings: []*domain.SSOGroupMapping{mapping},
		sessions: map[string]*domain.SSOSession{
			service.HashSSOToken(sessionToken): {
				SessionHash:   service.HashSSOToken(sessionToken),
				IdentityID:    identityID,
				ProviderID:    providerID,
				TeamProfileID: profileID,
				TeamID:        teamID,
				ExpiresAt:     now.Add(time.Hour),
				CreatedAt:     now,
			},
		},
		teamProfiles: []*domain.SSOTeamProfile{profile},
		now:          now,
	}
	repo.ssoProfiles = map[uuid.UUID]*domain.APIKey{profileID: &repo.teamProfiles[0].Profile}
	keySvc := &userPortalKeySvc{}
	return ssoPersonalKeyFixture{
		handler: &userPortalHandler{
			keys: keySvc,
			sso: service.NewSSOService(repo, service.SSOConfig{
				PublicBaseURL: "https://portal.example.com",
				Now:           func() time.Time { return now },
			}),
		},
		keySvc:       keySvc,
		identityID:   identityID,
		teamID:       teamID,
		profileID:    profileID,
		sessionToken: sessionToken,
	}
}

func ssoOwnedAPIKey(teamID, identityID uuid.UUID, scopes []string) *domain.APIKey {
	return &domain.APIKey{
		ID:                 uuid.New(),
		TeamID:             teamID,
		ProfileID:          teamID,
		Name:               "Owned",
		Label:              "Owned",
		KeySuffix:          "owned1",
		Scopes:             append([]string{}, scopes...),
		Role:               service.APIKeyRoleMember,
		RateLimit:          30,
		CreatedAt:          time.Now().UTC().Truncate(time.Second),
		SSOOwnerIdentityID: &identityID,
	}
}
