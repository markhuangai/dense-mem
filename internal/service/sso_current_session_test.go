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
			{ProviderID: providerID, TeamID: teamAID, TeamName: "Team A", GroupID: "group-a", Scopes: []string{APIKeyScopeRead}, Role: APIKeyRoleMember, Enabled: true},
			{ProviderID: providerID, TeamID: teamBID, TeamName: "Team B", GroupID: "group-b", Scopes: []string{APIKeyScopeRead, APIKeyScopeWrite}, Role: APIKeyRoleManager, Enabled: true},
		},
		identities: map[uuid.UUID]*domain.SSOIdentity{
			identityID: {ID: identityID, ProviderID: providerID, Subject: "subject-123", Email: "ada@example.com", DisplayName: "Ada Lovelace"},
		},
		sessions: map[string]*domain.SSOSession{
			sessionHash: {
				SessionHash:   sessionHash,
				IdentityID:    identityID,
				ProviderID:    providerID,
				TeamProfileID: profileAID,
				TeamID:        teamAID,
				ExpiresAt:     now.Add(time.Hour),
			},
		},
	}
	repo.teamProfiles = []*domain.SSOTeamProfile{
		ssoTeamProfile(identityID, providerID, "subject-123", teamAID, profileAID, "Team A", []string{APIKeyScopeRead}, APIKeyRoleMember),
	}
	repo.ssoProfiles = map[uuid.UUID]*domain.APIKey{
		profileAID: &repo.teamProfiles[0].Profile,
	}
	svc := NewSSOService(repo, SSOConfig{
		GroupResolver: &ssoGroupResolverStub{t: t, unexpected: true},
		Now:           func() time.Time { return now },
	})

	info, err := svc.CurrentSession(ctx, sessionToken)

	require.NoError(t, err)
	assert.Equal(t, profileAID, info.Selected.Profile.ID)
	require.Len(t, info.Teams, 2)
	assert.ElementsMatch(t, []uuid.UUID{teamAID, teamBID}, []uuid.UUID{info.Teams[0].Team.ID, info.Teams[1].Team.ID})
	require.Len(t, repo.teamProfiles, 2)
	assert.Equal(t, teamBID, repo.teamProfiles[1].Team.ID)
	assert.Equal(t, "Team B", repo.teamProfiles[1].Team.Name)
	assert.Equal(t, []string{APIKeyScopeRead, APIKeyScopeWrite}, repo.teamProfiles[1].Profile.Scopes)
	assert.Equal(t, APIKeyRoleManager, repo.teamProfiles[1].Profile.Role)

	_, err = svc.CurrentSession(ctx, sessionToken)
	require.NoError(t, err)
	assert.Len(t, repo.teamProfiles, 2)
}
