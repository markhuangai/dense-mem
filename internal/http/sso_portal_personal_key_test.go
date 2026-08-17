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
	fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())

	c, _ := userSSOContext(nethttp.MethodGet, "/ui/api/session", "", fixture.sessionToken)
	session, err := fixture.handler.currentSSOSession(c)
	require.NoError(t, err)
	require.Nil(t, session.Credential)
	require.Nil(t, session.PersonalCredential)
	require.Equal(t, []string{service.CredentialScopeRead, service.CredentialScopeWrite}, session.Membership.Grants)

	c, rec := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential", `{"name":"Owned","scopes":["read","write"],"rate_limit":90}`, fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.NoError(t, fixture.handler.createSSOCredential(c))
	require.Equal(t, nethttp.StatusCreated, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"dm_created_plaintext"`)
	require.Equal(t, "Owned", fixture.keySvc.lastCreateReq.Name)
	require.Equal(t, []string{service.CredentialScopeRead, service.CredentialScopeWrite}, fixture.keySvc.lastCreateReq.Scopes)
	require.NotNil(t, fixture.keySvc.lastCreateReq.OwnerIdentityID)
	require.Equal(t, fixture.identityID, *fixture.keySvc.lastCreateReq.OwnerIdentityID)
	require.Len(t, fixture.keySvc.keys, 1)

	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/session", "", fixture.sessionToken)
	session, err = fixture.handler.currentSSOSession(c)
	require.NoError(t, err)
	require.NotNil(t, session.PersonalCredential)
	require.Contains(t, session.PersonalCredential.Scopes, service.CredentialScopeWrite)

	c, rec = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential/rotate", `{}`, fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.NoError(t, fixture.handler.rotateSSOCredential(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"dm_rotated_plaintext"`)
	require.Equal(t, fixture.teamID, fixture.keySvc.rotateProfileID)
	require.Equal(t, fixture.keySvc.keys[0].ID, fixture.keySvc.rotateKeyID)
}

func TestUserPortalSSOCreateKeyRequestValidation(t *testing.T) {
	expiresAt := "2026-06-13T12:30:00Z"
	identity := domain.SSOIdentity{Email: "user@example.com"}

	t.Run("defaults empty name and write entitlement to read write", func(t *testing.T) {
		req, err := userPortalSSOCreateCredentialRequest(userPortalCreateSSOCredentialRequest{
			RateLimit: 30,
			ExpiresAt: &expiresAt,
		}, identity, service.StandardCredentialScopes())
		require.NoError(t, err)
		require.Equal(t, "SSO user@example.com", req.Name)
		require.Equal(t, service.StandardCredentialScopes(), req.Scopes)
		require.Equal(t, 30, req.RateLimit)
		require.NotNil(t, req.ExpiresAt)
		require.Equal(t, service.CredentialRoleMember, req.Role)
	})

	t.Run("rejects missing read entitlement", func(t *testing.T) {
		_, err := userPortalSSOCreateCredentialRequest(userPortalCreateSSOCredentialRequest{RateLimit: 30}, identity, []string{service.CredentialScopeWrite})
		require.ErrorContains(t, err, "sso access denied")
	})

	t.Run("rejects write above entitlement", func(t *testing.T) {
		_, err := userPortalSSOCreateCredentialRequest(userPortalCreateSSOCredentialRequest{
			Scopes:    service.StandardCredentialScopes(),
			RateLimit: 30,
		}, identity, []string{service.CredentialScopeRead})
		require.ErrorContains(t, err, "cannot create credential above sso entitlement")
	})

	t.Run("caps feedback scope to entitlement", func(t *testing.T) {
		req, err := userPortalSSOCreateCredentialRequest(userPortalCreateSSOCredentialRequest{
			Scopes:    []string{service.CredentialScopeRead, service.CredentialScopeFeedbackRead},
			RateLimit: 30,
		}, identity, []string{service.CredentialScopeRead, service.CredentialScopeFeedbackRead})
		require.NoError(t, err)
		require.Equal(t, []string{service.CredentialScopeRead, service.CredentialScopeFeedbackRead}, req.Scopes)

		_, err = userPortalSSOCreateCredentialRequest(userPortalCreateSSOCredentialRequest{
			Scopes:    []string{service.CredentialScopeRead, service.CredentialScopeFeedbackRead},
			RateLimit: 30,
		}, identity, []string{service.CredentialScopeRead})
		require.ErrorContains(t, err, "cannot create credential above sso entitlement")
	})

	t.Run("rejects bad rate limit and expiry", func(t *testing.T) {
		_, err := userPortalSSOCreateCredentialRequest(userPortalCreateSSOCredentialRequest{RateLimit: 0}, identity, []string{service.CredentialScopeRead})
		require.ErrorContains(t, err, "rate_limit must be greater than zero")

		badExpiresAt := "not-a-date"
		_, err = userPortalSSOCreateCredentialRequest(userPortalCreateSSOCredentialRequest{
			RateLimit: 30,
			ExpiresAt: &badExpiresAt,
		}, identity, []string{service.CredentialScopeRead})
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
		fixture := newSSOPersonalKeyFixture(service.CredentialRoleManager, service.StandardCredentialScopes())
		c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential", `{"rate_limit":30}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.createSSOCredential(c), "sso managers should create credentials from the team section")

		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential/rotate", `{}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.rotateSSOCredential(c), "sso managers should rotate credentials from the team section")
	})

	t.Run("create requires key service and no existing owned key", func(t *testing.T) {
		fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())
		fixture.handler.credentials = nil
		c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential", `{"rate_limit":30}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.createSSOCredential(c), "credential service unavailable")

		fixture = newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())
		fixture.keySvc.keys = append(fixture.keySvc.keys, ssoOwnedCredential(fixture.teamID, fixture.identityID, service.StandardCredentialScopes()))
		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential", `{"rate_limit":30}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.createSSOCredential(c), "sso-owned credential already exists for this team")

		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential", `{"rate_limit":30,"memory_binding":"credential_private"}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.createSSOCredential(c), "sso-owned credential already exists for this team")
	})

	t.Run("rotate requires owned writable key and writable entitlement", func(t *testing.T) {
		fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())
		c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential/rotate", `{}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.rotateSSOCredential(c), "sso-owned credential not found")

		fixture.keySvc.keys = append(fixture.keySvc.keys, ssoOwnedCredential(fixture.teamID, fixture.identityID, []string{service.CredentialScopeRead}))
		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential/rotate", `{}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.rotateSSOCredential(c), "sso-owned credential cannot be rotated")

		readOnlyFixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, []string{service.CredentialScopeRead})
		readOnlyFixture.keySvc.keys = append(readOnlyFixture.keySvc.keys, ssoOwnedCredential(readOnlyFixture.teamID, readOnlyFixture.identityID, service.StandardCredentialScopes()))
		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential/rotate", `{}`, readOnlyFixture.sessionToken)
		setUserSSOPrincipal(c, readOnlyFixture.profileID, readOnlyFixture.teamID)
		require.ErrorContains(t, readOnlyFixture.handler.rotateSSOCredential(c), "sso-owned credential cannot be rotated")
	})
}

func TestUserPortalSSORequestSessionGuards(t *testing.T) {
	handler := &userPortalHandler{}
	c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential", `{}`, "")
	_, _, err := handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "sso is not configured")

	fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())
	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential", `{}`, fixture.sessionToken)
	_, _, err = fixture.handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "authentication required")

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential", `{}`, fixture.sessionToken)
	setUserCredentialPrincipal(c, fixture.profileID, fixture.teamID)
	_, _, err = fixture.handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "sso session required")

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credential", `{}`, "")
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	_, _, err = fixture.handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "authentication required")
}

func newSSOPersonalKeyFixture(role string, scopes []string) ssoPersonalKeyFixture {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	identityID := uuid.New()
	teamID := uuid.New()
	membershipID := uuid.New()
	profileID := uuid.New()
	sessionToken := "session-token"
	mapping := httpSSOMapping(providerID, teamID, "Team One", "group-one", role, now)
	mapping.Scopes = append([]string{}, scopes...)
	profile := httpSSOTeamMembership(identityID, providerID, membershipID, profileID, teamID, "Team One", role, now)
	profile.Membership.Grants = append([]string{}, scopes...)
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
				SessionHash:  service.HashSSOToken(sessionToken),
				IdentityID:   identityID,
				ProviderID:   providerID,
				MembershipID: membershipID,
				OwnerID:      profileID,
				TeamID:       teamID,
				ExpiresAt:    now.Add(time.Hour),
				CreatedAt:    now,
			},
		},
		teamProfiles: []*domain.SSOTeamMembership{profile},
		now:          now,
	}
	keySvc := &userPortalKeySvc{}
	return ssoPersonalKeyFixture{
		handler: &userPortalHandler{
			credentials: keySvc,
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

func ssoOwnedCredential(teamID, identityID uuid.UUID, scopes []string) *domain.Credential {
	return &domain.Credential{
		ID:              uuid.New(),
		TeamID:          teamID,
		Name:            "Owned",
		KeySuffix:       "owned1",
		Scopes:          append([]string{}, scopes...),
		Role:            service.CredentialRoleMember,
		RateLimit:       30,
		CreatedAt:       time.Now().UTC().Truncate(time.Second),
		OwnerIdentityID: &identityID,
	}
}
