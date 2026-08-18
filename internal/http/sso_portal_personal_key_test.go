package http

import (
	"context"
	"encoding/json"
	"io"
	nethttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

type ssoPersonalKeyFixture struct {
	handler       *userPortalHandler
	keySvc        *userPortalKeySvc
	privateMemory *privateMemoryServiceStub
	identityID    uuid.UUID
	teamID        uuid.UUID
	profileID     uuid.UUID
	sessionToken  string
}

func TestUserPortalSSOPersonalKeyLifecycle(t *testing.T) {
	fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())

	c, _ := userSSOContext(nethttp.MethodGet, "/ui/api/session", "", fixture.sessionToken)
	session, err := fixture.handler.currentSSOSession(c)
	require.NoError(t, err)
	require.Nil(t, session.Credential)
	require.Empty(t, session.PersonalCredentials)
	require.Equal(t, []string{service.CredentialScopeRead, service.CredentialScopeWrite}, session.Membership.Grants)

	c, rec := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{"name":"Owned","scopes":["read","write"],"rate_limit":90,"memory_binding":"profile_private"}`, fixture.sessionToken)
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
	require.Len(t, session.PersonalCredentials, 1)
	require.Contains(t, session.PersonalCredentials[0].Scopes, service.CredentialScopeWrite)

	credentialID := fixture.keySvc.keys[0].ID
	c, rec = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials/"+credentialID.String()+"/rotate", `{}`, fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.NoError(t, fixture.handler.rotateSSOCredential(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"dm_rotated_plaintext"`)
	require.Equal(t, fixture.teamID, fixture.keySvc.rotateProfileID)
	require.Equal(t, credentialID, fixture.keySvc.rotateKeyID)

	c, rec = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{"name":"Second","scopes":["read"],"rate_limit":90,"memory_binding":"credential_private"}`, fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.NoError(t, fixture.handler.createSSOCredential(c))
	require.Equal(t, nethttp.StatusCreated, rec.Code)
	require.Len(t, fixture.keySvc.keys, 2)

	c, rec = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{"name":"Third","scopes":["read"],"rate_limit":90,"memory_binding":"shared_only"}`, fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.NoError(t, fixture.handler.createSSOCredential(c))
	require.Equal(t, nethttp.StatusCreated, rec.Code)
	require.Len(t, fixture.keySvc.keys, 3)

	c, rec = userSSOContext(nethttp.MethodGet, "/ui/api/sso/credentials", "", fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.NoError(t, fixture.handler.listSSOCredentials(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), credentialID.String())
	require.Contains(t, rec.Body.String(), fixture.keySvc.keys[2].ID.String())

	c, rec = userSSOContext(nethttp.MethodGet, "/ui/api/sso/credentials/"+credentialID.String(), "", fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.NoError(t, fixture.handler.getSSOCredential(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), credentialID.String())

	c, rec = userSSOContext(nethttp.MethodDelete, "/ui/api/sso/credentials/"+fixture.keySvc.keys[0].ID.String(), "", fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.NoError(t, callDeleteSSOCredential(fixture.handler, c))
	require.Equal(t, nethttp.StatusAccepted, rec.Code)
	require.Equal(t, fixture.keySvc.keys[0].ID, fixture.privateMemory.deletedCredentialID)
	require.Contains(t, rec.Body.String(), `"action":"retire_credential"`)
	firstOperationID := privateMemoryOperationID(t, rec.Body.Bytes())
	require.NotNil(t, fixture.keySvc.keys[0].RevokedAt)

	c, rec = userSSOContext(nethttp.MethodDelete, "/ui/api/sso/credentials/"+fixture.keySvc.keys[0].ID.String(), "", fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.NoError(t, callDeleteSSOCredential(fixture.handler, c))
	require.Equal(t, nethttp.StatusAccepted, rec.Code)
	require.Equal(t, firstOperationID, privateMemoryOperationID(t, rec.Body.Bytes()))
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
	identityID := uuid.New()
	existing := []*domain.Credential{
		{Name: "SSO user@example.com", OwnerIdentityID: &identityID},
		{Name: "SSO user@example.com (2)", OwnerIdentityID: &identityID},
	}
	require.Equal(t, "SSO user@example.com (3)", nextSSOOwnedCredentialName("SSO user@example.com", existing))
}

func TestUserPortalSSOPersonalKeyDeniedBranches(t *testing.T) {
	t.Run("manager uses team section", func(t *testing.T) {
		fixture := newSSOPersonalKeyFixture(service.CredentialRoleManager, service.StandardCredentialScopes())
		c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{"rate_limit":30}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.createSSOCredential(c), "sso managers should create credentials from the team section")

		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials/"+uuid.NewString()+"/rotate", `{}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.rotateSSOCredential(c), "sso managers should rotate credentials from the team section")
	})

	t.Run("create requires key service and allows another owned key", func(t *testing.T) {
		fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())
		fixture.handler.credentials = nil
		c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{"rate_limit":30}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.createSSOCredential(c), "credential service unavailable")

		fixture = newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())
		fixture.keySvc.keys = append(fixture.keySvc.keys, ssoOwnedCredential(fixture.teamID, fixture.identityID, service.StandardCredentialScopes()))
		c, rec := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{"rate_limit":30}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.NoError(t, fixture.handler.createSSOCredential(c))
		require.Equal(t, nethttp.StatusCreated, rec.Code)
		require.Len(t, fixture.keySvc.keys, 2)

		c, rec = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{"rate_limit":30,"memory_binding":"credential_private"}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.NoError(t, fixture.handler.createSSOCredential(c))
		require.Equal(t, nethttp.StatusCreated, rec.Code)
		require.Len(t, fixture.keySvc.keys, 3)
	})

	t.Run("rotate requires owned writable key and writable entitlement", func(t *testing.T) {
		fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())
		c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials/"+uuid.NewString()+"/rotate", `{}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.rotateSSOCredential(c), "sso-owned credential not found")

		readOnly := ssoOwnedCredential(fixture.teamID, fixture.identityID, []string{service.CredentialScopeRead})
		fixture.keySvc.keys = append(fixture.keySvc.keys, readOnly)
		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials/"+readOnly.ID.String()+"/rotate", `{}`, fixture.sessionToken)
		setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
		require.ErrorContains(t, fixture.handler.rotateSSOCredential(c), "sso-owned credential cannot be rotated")

		readOnlyFixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, []string{service.CredentialScopeRead})
		readOnlyCredential := ssoOwnedCredential(readOnlyFixture.teamID, readOnlyFixture.identityID, service.StandardCredentialScopes())
		readOnlyFixture.keySvc.keys = append(readOnlyFixture.keySvc.keys, readOnlyCredential)
		c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials/"+readOnlyCredential.ID.String()+"/rotate", `{}`, readOnlyFixture.sessionToken)
		setUserSSOPrincipal(c, readOnlyFixture.profileID, readOnlyFixture.teamID)
		require.ErrorContains(t, readOnlyFixture.handler.rotateSSOCredential(c), "sso-owned credential cannot be rotated")

		c, rec := userSSOContext(nethttp.MethodDelete, "/ui/api/sso/credentials/"+readOnlyCredential.ID.String(), "", readOnlyFixture.sessionToken)
		setUserSSOPrincipal(c, readOnlyFixture.profileID, readOnlyFixture.teamID)
		require.NoError(t, callDeleteSSOCredential(readOnlyFixture.handler, c))
		require.Equal(t, nethttp.StatusAccepted, rec.Code)
		require.Equal(t, readOnlyCredential.ID, readOnlyFixture.privateMemory.deletedCredentialID)
	})
}

func TestUserPortalSSORequestSessionGuards(t *testing.T) {
	handler := &userPortalHandler{}
	c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{}`, "")
	_, _, err := handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "sso is not configured")

	fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())
	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{}`, fixture.sessionToken)
	_, _, err = fixture.handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "authentication required")

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{}`, fixture.sessionToken)
	setUserCredentialPrincipal(c, fixture.profileID, fixture.teamID)
	_, _, err = fixture.handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "sso session required")
	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/sso/credentials", "", fixture.sessionToken)
	setUserCredentialPrincipal(c, fixture.profileID, fixture.teamID)
	require.ErrorContains(t, fixture.handler.listSSOCredentials(c), "sso session required")

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{}`, "")
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	_, _, err = fixture.handler.ssoRequestSession(c)
	require.ErrorContains(t, err, "authentication required")
}

func TestUserPortalSSOOwnedCollectionGuards(t *testing.T) {
	fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())
	owned := ssoOwnedCredential(fixture.teamID, fixture.identityID, service.StandardCredentialScopes())
	fixture.keySvc.keys = append(fixture.keySvc.keys, owned)

	c, _ := userSSOContext(nethttp.MethodGet, "/ui/api/sso/credentials", "", fixture.sessionToken)
	setUserCredentialPrincipal(c, owned.ID, fixture.teamID)
	require.ErrorContains(t, fixture.handler.listSSOCredentials(c), "sso session required")

	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/sso/credentials/not-a-uuid", "", fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	require.ErrorContains(t, fixture.handler.getSSOCredential(c), "invalid credential ID format")

	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/sso/credentials/"+owned.ID.String(), "", fixture.sessionToken)
	setUserCredentialPrincipal(c, owned.ID, fixture.teamID)
	require.ErrorContains(t, fixture.handler.getSSOCredential(c), "sso session required")

	c, _ = userSSOContext(nethttp.MethodDelete, "/ui/api/sso/credentials/"+owned.ID.String(), "", fixture.sessionToken)
	setUserCredentialPrincipal(c, owned.ID, fixture.teamID)
	require.ErrorContains(t, callDeleteSSOCredential(fixture.handler, c), "sso session required")

	manager := newSSOPersonalKeyFixture(service.CredentialRoleManager, service.StandardCredentialScopes())
	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/sso/credentials", "", manager.sessionToken)
	setUserSSOPrincipal(c, manager.profileID, manager.teamID)
	require.ErrorContains(t, manager.handler.listSSOCredentials(c), "sso managers should use the team credentials section")

	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/sso/credentials/"+uuid.NewString(), "", manager.sessionToken)
	setUserSSOPrincipal(c, manager.profileID, manager.teamID)
	require.ErrorContains(t, manager.handler.getSSOCredential(c), "sso managers should use the team credentials section")

	c, _ = userSSOContext(nethttp.MethodDelete, "/ui/api/sso/credentials/"+uuid.NewString(), "", manager.sessionToken)
	setUserSSOPrincipal(c, manager.profileID, manager.teamID)
	require.ErrorContains(t, callDeleteSSOCredential(manager.handler, c), "sso managers should use the team credentials section")
}

func TestUserPortalSSOOwnedOperationsHideForeignCredentials(t *testing.T) {
	fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())
	foreignIdentityID := uuid.New()
	owned := ssoOwnedCredential(fixture.teamID, fixture.identityID, service.StandardCredentialScopes())
	sameTeam := ssoOwnedCredential(fixture.teamID, foreignIdentityID, service.StandardCredentialScopes())
	otherTeam := ssoOwnedCredential(uuid.New(), foreignIdentityID, service.StandardCredentialScopes())
	fixture.keySvc.keys = append(fixture.keySvc.keys, owned, sameTeam, otherTeam)

	c, rec := userSSOContext(nethttp.MethodGet, "/ui/api/sso/credentials", "", fixture.sessionToken)
	setUserSSOPrincipal(c, fixture.identityID, fixture.teamID)
	require.NoError(t, fixture.handler.listSSOCredentials(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), owned.ID.String())
	require.NotContains(t, rec.Body.String(), sameTeam.ID.String())
	require.NotContains(t, rec.Body.String(), otherTeam.ID.String())

	for _, credential := range []*domain.Credential{sameTeam, otherTeam} {
		t.Run(credential.ID.String(), func(t *testing.T) {
			c, _ := userSSOContext(nethttp.MethodGet, "/ui/api/sso/credentials/"+credential.ID.String(), "", fixture.sessionToken)
			setUserSSOPrincipal(c, fixture.identityID, fixture.teamID)
			err := fixture.handler.getSSOCredential(c)
			assertNotFoundWithoutCredentialDetails(t, err, credential.ID)

			c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials/"+credential.ID.String()+"/rotate", `{}`, fixture.sessionToken)
			setUserSSOPrincipal(c, fixture.identityID, fixture.teamID)
			err = fixture.handler.rotateSSOCredential(c)
			assertNotFoundWithoutCredentialDetails(t, err, credential.ID)

			c, _ = userSSOContext(nethttp.MethodDelete, "/ui/api/sso/credentials/"+credential.ID.String(), "", fixture.sessionToken)
			setUserSSOPrincipal(c, fixture.identityID, fixture.teamID)
			err = callDeleteSSOCredential(fixture.handler, c)
			assertNotFoundWithoutCredentialDetails(t, err, credential.ID)
			require.Nil(t, credential.RevokedAt)
		})
	}
}

func TestUserPortalSSOTokenDerivedSessionCannotMintOwnedCredential(t *testing.T) {
	fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, service.StandardCredentialScopes())
	owned := ssoOwnedCredential(fixture.teamID, fixture.identityID, service.StandardCredentialScopes())
	fixture.keySvc.keys = append(fixture.keySvc.keys, owned)

	c, _ := userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials", `{"rate_limit":30}`, fixture.sessionToken)
	setUserCredentialPrincipal(c, owned.ID, fixture.teamID)
	var apiErr *httperr.APIError
	err := fixture.handler.createSSOCredential(c)
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.FORBIDDEN, apiErr.Code)
	require.Equal(t, 1, len(fixture.keySvc.keys))

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/credentials/"+owned.ID.String()+"/rotate", `{}`, fixture.sessionToken)
	setUserCredentialPrincipal(c, owned.ID, fixture.teamID)
	err = fixture.handler.rotateSSOCredential(c)
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.FORBIDDEN, apiErr.Code)

	c, _ = userSSOContext(nethttp.MethodDelete, "/ui/api/sso/credentials/"+owned.ID.String(), "", fixture.sessionToken)
	setUserCredentialPrincipal(c, owned.ID, fixture.teamID)
	err = callDeleteSSOCredential(fixture.handler, c)
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.FORBIDDEN, apiErr.Code)
	require.Nil(t, owned.RevokedAt)
}

func assertNotFoundWithoutCredentialDetails(t *testing.T, err error, credentialID uuid.UUID) {
	t.Helper()
	var apiErr *httperr.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.NOT_FOUND, apiErr.Code)
	require.NotContains(t, apiErr.Message, credentialID.String())
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
	privateMemory := &privateMemoryServiceStub{
		now:         now,
		credentials: keySvc,
		operations:  make(map[string]*domain.PrivateMemoryErasureOperation),
	}
	return ssoPersonalKeyFixture{
		handler: &userPortalHandler{
			credentials:   keySvc,
			privateMemory: privateMemory,
			sso: service.NewSSOService(repo, service.SSOConfig{
				PublicBaseURL: "https://portal.example.com",
				Now:           func() time.Time { return now },
			}),
		},
		keySvc:        keySvc,
		privateMemory: privateMemory,
		identityID:    identityID,
		teamID:        teamID,
		profileID:     profileID,
		sessionToken:  sessionToken,
	}
}

type privateMemoryServiceStub struct {
	PrivateMemoryServiceInterface
	now                 time.Time
	deletedCredentialID uuid.UUID
	credentials         *userPortalKeySvc
	operations          map[string]*domain.PrivateMemoryErasureOperation
}

func (s *privateMemoryServiceStub) DeleteSSOCredential(_ context.Context, teamID, identityID, credentialID uuid.UUID, command service.PrivateMemoryCommand) (*domain.PrivateMemoryErasureOperation, error) {
	scope := teamID.String() + ":" + identityID.String() + ":" + command.IdempotencyKey
	if existing := s.operations[scope]; existing != nil {
		if existing.TargetCredentialID == nil || *existing.TargetCredentialID != credentialID {
			return nil, repository.ErrPrivateMemoryIdempotency
		}
		return existing, nil
	}
	var credential *domain.Credential
	for _, candidate := range s.credentials.keys {
		if candidate.ID == credentialID && candidate.TeamID == teamID && candidate.OwnerIdentityID != nil &&
			*candidate.OwnerIdentityID == identityID && candidate.RevokedAt == nil {
			credential = candidate
			break
		}
	}
	if credential == nil {
		return nil, repository.ErrPrivateMemoryNotFound
	}
	s.deletedCredentialID = credentialID
	now := s.now
	credential.RevokedAt = &now
	operation := &domain.PrivateMemoryErasureOperation{
		ID:                 uuid.New(),
		TeamID:             teamID,
		TargetCredentialID: &credentialID,
		Action:             domain.PrivateMemoryRetireCredential,
		ActorClass:         domain.PrivateMemoryActorOwnerSSO,
		ReasonCode:         "credential_deleted",
		Status:             domain.PrivateMemoryErasureQueued,
		DeletedCounts:      map[string]int64{},
		RequestedAt:        now,
		UpdatedAt:          now,
	}
	s.operations[scope] = operation
	return operation, nil
}

func privateMemoryOperationID(t *testing.T, body []byte) string {
	t.Helper()
	var response struct {
		Data struct {
			OperationID string `json:"operation_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &response))
	require.NotEmpty(t, response.Data.OperationID)
	return response.Data.OperationID
}

func callDeleteSSOCredential(handler *userPortalHandler, c echo.Context) error {
	body := `{"acknowledge_irreversible":true}`
	c.Request().Body = io.NopCloser(strings.NewReader(body))
	c.Request().ContentLength = int64(len(body))
	c.Request().Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c.Request().Header.Set("Idempotency-Key", "delete-owned-credential")
	return httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey)(handler.deleteSSOCredential)(c)
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
