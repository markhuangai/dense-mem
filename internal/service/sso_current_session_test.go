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

func TestSSOCurrentSessionMaterializesNewMappedTeamFromCachedGroups(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	identityID := uuid.New()
	teamAID := uuid.New()
	teamBID := uuid.New()
	profileAID := uuid.New()
	sessionToken := "session-token"
	sessionHash := HashSSOToken(sessionToken)

	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {ID: providerID, Name: "Enterprise", Enabled: true},
		},
		cache: &domain.SSOEntitlementCache{
			ProviderID: providerID,
			Subject:    "subject-123",
			Groups:     []string{"group-a", "group-b"},
			Status:     "active",
			CheckedAt:  now.Add(-time.Minute),
			ExpiresAt:  now.Add(time.Minute),
		},
		mappings: []*domain.SSOGroupMapping{
			{ProviderID: providerID, TeamID: teamAID, TeamName: "Team A", GroupID: "group-a", Scopes: []string{CredentialScopeRead}, Role: CredentialRoleMember, Enabled: true},
			{ProviderID: providerID, TeamID: teamBID, TeamName: "Team B", GroupID: "group-b", Scopes: []string{CredentialScopeRead, CredentialScopeWrite}, Role: CredentialRoleManager, Enabled: true},
		},
		identities: map[uuid.UUID]*domain.SSOIdentity{
			identityID: {ID: identityID, ProviderID: providerID, Subject: "subject-123", Email: "ada@example.com", DisplayName: "Ada Lovelace"},
		},
		sessions: map[string]*domain.SSOSession{
			sessionHash: {
				SessionHash: sessionHash,
				IdentityID:  identityID,
				ProviderID:  providerID,
				OwnerID:     profileAID,
				TeamID:      teamAID,
				ExpiresAt:   now.Add(time.Hour),
			},
		},
	}
	repo.teamProfiles = []*domain.SSOTeamMembership{
		ssoTeamProfile(identityID, providerID, "subject-123", teamAID, profileAID, "Team A", []string{CredentialScopeRead}, CredentialRoleMember),
	}
	repo.ssoProfiles = map[uuid.UUID]*domain.SSOTeamMembership{
		profileAID: repo.teamProfiles[0],
	}
	svc := NewSSOService(repo, SSOConfig{
		GroupResolver: &ssoGroupResolverStub{t: t, unexpected: true},
		Now:           func() time.Time { return now },
	})

	info, err := svc.CurrentSession(ctx, sessionToken)

	require.NoError(t, err)
	assert.Equal(t, profileAID, info.Selected.Membership.OwnerID)
	require.Len(t, info.Teams, 2)
	assert.ElementsMatch(t, []uuid.UUID{teamAID, teamBID}, []uuid.UUID{info.Teams[0].Team.ID, info.Teams[1].Team.ID})
	require.Len(t, repo.teamProfiles, 2)
	assert.Equal(t, teamBID, repo.teamProfiles[1].Team.ID)
	assert.Equal(t, "Team B", repo.teamProfiles[1].Team.Name)
	assert.Equal(t, []string{CredentialScopeRead, CredentialScopeWrite}, repo.teamProfiles[1].Membership.Grants)
	assert.Equal(t, CredentialRoleManager, repo.teamProfiles[1].Membership.Role)

	_, err = svc.CurrentSession(ctx, sessionToken)
	require.NoError(t, err)
	assert.Len(t, repo.teamProfiles, 2)
}
