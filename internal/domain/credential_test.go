package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCredentialGettersExposeCanonicalFields(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	identityID := uuid.New()
	membershipID := uuid.New()
	ownerID := uuid.New()
	lastUsed := time.Now().UTC().Add(-time.Hour)
	expires := time.Now().UTC().Add(time.Hour)
	created := time.Now().UTC().Add(-24 * time.Hour)
	revoked := time.Now().UTC()

	key := &Credential{
		ID:              keyID,
		ActorIdentityID: identityID,
		MembershipID:    membershipID,
		OwnerID:         ownerID,
		TeamID:          teamID,
		Name:            "primary key",
		KeyHash:         "hash",
		KeyPrefix:       "dm_123",
		KeySuffix:       "abcd",
		Scopes:          []string{"memory:read"},
		Role:            "manager",
		RateLimit:       120,
		LastUsedAt:      &lastUsed,
		ExpiresAt:       &expires,
		CreatedAt:       created,
		RevokedAt:       &revoked,
	}

	require.Equal(t, keyID, key.GetID())
	require.Equal(t, identityID, key.GetActorIdentityID())
	require.Equal(t, membershipID, key.GetMembershipID())
	require.Equal(t, ownerID, key.GetOwnerID())
	require.Equal(t, teamID, key.GetTeamID())
	require.Equal(t, "primary key", key.GetName())
	require.Equal(t, "hash", key.GetKeyHash())
	require.Equal(t, "dm_123", key.GetKeyPrefix())
	require.Equal(t, "abcd", key.GetKeySuffix())
	require.Equal(t, []string{"memory:read"}, key.GetScopes())
	require.Equal(t, "manager", key.GetRole())
	require.Equal(t, 120, key.GetRateLimit())
	require.Equal(t, &lastUsed, key.GetLastUsedAt())
	require.Equal(t, &expires, key.GetExpiresAt())
	require.Equal(t, created, key.GetCreatedAt())
	require.Equal(t, &revoked, key.GetRevokedAt())

	require.Equal(t, "member", (&Credential{}).GetRole())
}

func TestAuthenticatedActorFromCredential(t *testing.T) {
	require.Nil(t, AuthenticatedActorFromCredential(nil))

	for _, test := range []struct {
		name     string
		role     string
		wantRole string
	}{
		{name: "default role", wantRole: "member"},
		{name: "member", role: "member", wantRole: "member"},
		{name: "manager", role: "manager", wantRole: "manager"},
	} {
		t.Run(test.name, func(t *testing.T) {
			teamID, credentialID, identityID, membershipID, ownerID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			credential := &Credential{
				ID: credentialID, TeamID: teamID, TeamName: "team",
				ActorIdentityID: identityID, MembershipID: membershipID, OwnerID: ownerID,
				Name: "automation", Role: test.role, Scopes: []string{"read", "write"},
			}

			actor := AuthenticatedActorFromCredential(credential)
			require.NotNil(t, actor)
			require.Equal(t, Team{ID: teamID, Name: "team"}, actor.Team)
			require.Equal(t, ActorIdentity{ID: identityID, Kind: "api_client", DisplayName: "automation"}, actor.Identity)
			require.Equal(t, Membership{
				ID: membershipID, ActorIdentityID: identityID, TeamID: teamID, OwnerID: ownerID,
				Name: "automation", Grants: []string{"read", "write"}, Role: test.wantRole, Status: "active",
			}, actor.Membership)
			require.Equal(t, ownerID, actor.OwnerID)
			require.Same(t, credential, actor.Credential)

			actor.Membership.Grants[0] = "changed actor grant"
			require.Equal(t, []string{"read", "write"}, credential.Scopes)
			credential.Scopes[1] = "changed credential scope"
			require.Equal(t, []string{"changed actor grant", "write"}, actor.Membership.Grants)
		})
	}

	for _, scopes := range [][]string{nil, {}} {
		actor := AuthenticatedActorFromCredential(&Credential{Scopes: scopes})
		require.NotNil(t, actor)
		require.Nil(t, actor.Membership.Grants)
	}
}
