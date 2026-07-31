package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestDirectoryReconcileCreatesAndArchivesOnlyDirectoryManagedTeam(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	ssoRepo := NewSSORepository(appDB, rls)
	provider := &domain.SSOProvider{
		Name:         "directory integration provider",
		Kind:         domain.SSOProviderKindGenericOIDC,
		IssuerURL:    "https://idp.example.test",
		ClientID:     "directory-client",
		GroupsScopes: []string{},
		Enabled:      true,
	}
	require.NoError(t, ssoRepo.CreateProvider(ctx, provider))

	directoryRepo := NewDirectoryIdentityRepository(appDB, rls)
	connector := &domain.DirectoryConnector{
		ProviderID:        provider.ID,
		Status:            domain.DirectoryConnectorActive,
		GroupPattern:      "^(?P<team>.+?)(?P<role>Manager)$",
		RoleEntitlements:  map[string]domain.DirectoryRoleEntitlement{"Manager": {Role: "manager", Scopes: []string{"read", "write", "feedback:read"}}},
		MaxAutoTeams:      5,
		CredentialVersion: 1,
	}
	require.NoError(t, directoryRepo.CreateDirectoryConnector(ctx, connector))

	user, err := directoryRepo.UpsertDirectoryUser(ctx, domain.DirectoryUser{
		ConnectorID: connector.ID,
		ExternalID:  "entra-user-id",
		UserName:    "directory.manager@example.test",
		Email:       "directory.manager@example.test",
		DisplayName: "Directory Manager",
		Active:      true,
	})
	require.NoError(t, err)
	group, err := directoryRepo.UpsertDirectoryGroup(ctx, domain.DirectoryGroup{
		ConnectorID: connector.ID,
		ExternalID:  "entra-group-id",
		DisplayName: "ResearchManager",
		Active:      true,
	})
	require.NoError(t, err)
	require.NoError(t, directoryRepo.ReplaceDirectoryGroupMembers(ctx, connector.ID, group.ID, []uuid.UUID{user.ID}))

	teamID := uuid.New()
	createPlan := domain.DirectoryReconcilePlan{
		ConnectorID:          connector.ID,
		ProviderID:           provider.ID,
		DirectoryIdentityIDs: []uuid.UUID{user.IdentityID},
		Bindings: []domain.DirectoryBindingAction{{
			GroupID:         group.ID,
			GroupExternalID: group.ExternalID,
			TeamID:          teamID,
			TeamName:        "Research",
			CreateTeam:      true,
			Origin:          domain.DirectoryBindingCreated,
			Entitlement:     domain.DirectoryRoleEntitlement{Role: "manager", Scopes: []string{"read", "write", "feedback:read"}},
		}},
		ProfileGrants: []domain.DirectoryProfileGrant{{
			IdentityID:      user.IdentityID,
			TeamID:          teamID,
			Subject:         user.ExternalID,
			Email:           user.Email,
			DisplayName:     user.DisplayName,
			GroupExternalID: group.ExternalID,
			Entitlement:     domain.DirectoryRoleEntitlement{Role: "manager", Scopes: []string{"read", "write", "feedback:read"}},
		}},
	}
	require.NoError(t, directoryRepo.ApplyDirectoryReconcilePlan(ctx, createPlan))

	directoryAuthority, err := ssoRepo.DirectoryAuthorityActive(ctx, provider.ID)
	require.NoError(t, err)
	require.True(t, directoryAuthority)
	profiles, err := ssoRepo.ListTeamProfilesForIdentity(ctx, user.IdentityID)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	entitled, err := ssoRepo.DirectoryTeamProfileEntitled(ctx, profiles[0].Profile.ID, provider.ID, user.IdentityID, teamID, group.ExternalID)
	require.NoError(t, err)
	require.True(t, entitled)
	require.NoError(t, directoryRepo.ReplaceDirectoryGroupMembers(ctx, connector.ID, group.ID, nil))
	entitled, err = ssoRepo.DirectoryTeamProfileEntitled(ctx, profiles[0].Profile.ID, provider.ID, user.IdentityID, teamID, group.ExternalID)
	require.NoError(t, err)
	require.False(t, entitled)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var managed, profileActive bool
		if err := tx.Raw(`SELECT directory_managed FROM teams WHERE id = $1`, teamID).Row().Scan(&managed); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT revoked_at IS NULL
			FROM team_profiles
			WHERE team_id = $1 AND sso_identity_id = $2
		`, teamID, user.IdentityID).Row().Scan(&profileActive); err != nil {
			return err
		}
		require.True(t, managed)
		require.True(t, profileActive)
		return nil
	}))

	removePlan := domain.DirectoryReconcilePlan{
		ConnectorID:              connector.ID,
		ProviderID:               provider.ID,
		DirectoryIdentityIDs:     []uuid.UUID{user.IdentityID},
		DisableDirectoryGroupIDs: []string{group.ExternalID},
		ArchiveDirectoryTeamIDs:  []uuid.UUID{teamID},
	}
	require.NoError(t, directoryRepo.ApplyDirectoryReconcilePlan(ctx, removePlan))

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var status string
		var profileRevoked bool
		if err := tx.Raw(`SELECT status FROM teams WHERE id = $1`, teamID).Row().Scan(&status); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT revoked_at IS NOT NULL
			FROM team_profiles
			WHERE team_id = $1 AND sso_identity_id = $2
		`, teamID, user.IdentityID).Row().Scan(&profileRevoked); err != nil {
			return err
		}
		require.Equal(t, "archived", status)
		require.True(t, profileRevoked)
		return nil
	}))

	restorePlan := domain.DirectoryReconcilePlan{
		ConnectorID:             connector.ID,
		ProviderID:              provider.ID,
		RestoreDirectoryTeamIDs: []uuid.UUID{teamID},
		Bindings: []domain.DirectoryBindingAction{{
			GroupID:         group.ID,
			GroupExternalID: group.ExternalID,
			TeamID:          teamID,
			TeamName:        "Research",
			Origin:          domain.DirectoryBindingCreated,
			Entitlement:     domain.DirectoryRoleEntitlement{Role: "manager", Scopes: []string{"read", "write", "feedback:read"}},
		}},
	}
	require.NoError(t, directoryRepo.ApplyDirectoryReconcilePlan(ctx, restorePlan))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var status string
		if err := tx.Raw(`SELECT status FROM teams WHERE id = $1`, teamID).Row().Scan(&status); err != nil {
			return err
		}
		require.Equal(t, "active", status)
		return nil
	}))

	adoptGroup, err := directoryRepo.UpsertDirectoryGroup(ctx, domain.DirectoryGroup{
		ConnectorID: connector.ID,
		ExternalID:  "entra-adopt-group-id",
		DisplayName: "OperationsManager",
		Active:      true,
	})
	require.NoError(t, err)
	adoptTeamID := uuid.New()
	require.NoError(t, directoryRepo.ApplyDirectoryReconcilePlan(ctx, domain.DirectoryReconcilePlan{
		ConnectorID: connector.ID,
		ProviderID:  provider.ID,
		Bindings: []domain.DirectoryBindingAction{{
			GroupID:         adoptGroup.ID,
			GroupExternalID: adoptGroup.ExternalID,
			TeamID:          adoptTeamID,
			TeamName:        "Operations",
			CreateTeam:      true,
			Origin:          domain.DirectoryBindingCreated,
			Entitlement:     domain.DirectoryRoleEntitlement{Role: "manager", Scopes: []string{"read", "write"}},
		}},
	}))
	require.NoError(t, directoryRepo.AdoptDirectoryTeam(ctx, connector.ID, adoptGroup.ID, adoptTeamID))
	require.NoError(t, directoryRepo.ApplyDirectoryReconcilePlan(ctx, domain.DirectoryReconcilePlan{
		ConnectorID:              connector.ID,
		ProviderID:               provider.ID,
		DisableDirectoryGroupIDs: []string{adoptGroup.ExternalID},
		ArchiveDirectoryTeamIDs:  []uuid.UUID{adoptTeamID},
	}))

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var status, origin string
		var managed bool
		if err := tx.Raw(`SELECT status, directory_managed FROM teams WHERE id = $1`, adoptTeamID).Row().Scan(&status, &managed); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT origin
			FROM sso_directory_group_bindings
			WHERE connector_id = $1 AND group_id = $2
		`, connector.ID, adoptGroup.ID).Row().Scan(&origin); err != nil {
			return err
		}
		require.Equal(t, "active", status)
		require.False(t, managed)
		require.Equal(t, string(domain.DirectoryBindingAdopted), origin)
		return nil
	}))
}

func TestDirectoryCredentialsAndProviderRetirementRevokeOAuthAccess(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	ssoRepo := NewSSORepository(appDB, rls)
	provider := &domain.SSOProvider{
		Name:         "directory retirement provider",
		Kind:         domain.SSOProviderKindGenericOIDC,
		IssuerURL:    "https://idp.example.test",
		ClientID:     "directory-retirement-client",
		Enabled:      true,
		GroupClaims:  []string{"groups"},
		GroupsScopes: []string{},
	}
	require.NoError(t, ssoRepo.CreateProvider(ctx, provider))

	directoryRepo := NewDirectoryIdentityRepository(appDB, rls)
	connector := &domain.DirectoryConnector{
		ProviderID:            provider.ID,
		Status:                domain.DirectoryConnectorActive,
		GroupPattern:          "^(?P<team>.+?)(?P<role>Member)$",
		RoleEntitlements:      map[string]domain.DirectoryRoleEntitlement{"Member": {Role: "member", Scopes: []string{"read"}}},
		MaxAutoTeams:          5,
		CredentialVersion:     1,
		BearerTokenHash:       "old-bearer-hash",
		OAuthClientID:         "old-client-id",
		OAuthClientSecretHash: "old-client-secret-hash",
	}
	require.NoError(t, directoryRepo.CreateDirectoryConnector(ctx, connector))

	expiresAt := time.Now().UTC().Add(time.Hour)
	rotatedTokenHash := strings.Repeat("a", 64)
	retiredTokenHash := strings.Repeat("b", 64)
	require.NoError(t, directoryRepo.CreateDirectoryOAuthToken(ctx, connector.ID, rotatedTokenHash, expiresAt))
	require.NoError(t, directoryRepo.SetDirectoryCredentials(ctx, connector.ID, 2, "new-bearer-hash", "new-client-id", "new-client-secret-hash"))
	valid, err := directoryRepo.GetDirectoryOAuthToken(ctx, connector.ID, rotatedTokenHash)
	require.NoError(t, err)
	require.False(t, valid)

	require.NoError(t, directoryRepo.CreateDirectoryOAuthToken(ctx, connector.ID, retiredTokenHash, expiresAt))
	require.NoError(t, ssoRepo.DeleteProvider(ctx, provider.ID))
	stored, err := directoryRepo.GetDirectoryConnector(ctx, connector.ID)
	require.NoError(t, err)
	require.Equal(t, domain.DirectoryConnectorDisabled, stored.Status)
	valid, err = directoryRepo.GetDirectoryOAuthToken(ctx, connector.ID, retiredTokenHash)
	require.NoError(t, err)
	require.False(t, valid)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var enabled bool
		if err := tx.Raw(`SELECT enabled FROM sso_providers WHERE id = $1`, provider.ID).Row().Scan(&enabled); err != nil {
			return err
		}
		require.False(t, enabled)
		return nil
	}))
}

func TestDirectoryGroupAndMembershipUpsertIsAtomic(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	ssoRepo := NewSSORepository(appDB, rls)
	provider := &domain.SSOProvider{
		Name:         "directory atomic group provider",
		Kind:         domain.SSOProviderKindGenericOIDC,
		IssuerURL:    "https://idp.example.test",
		ClientID:     "directory-atomic-client",
		Enabled:      true,
		GroupsScopes: []string{},
	}
	require.NoError(t, ssoRepo.CreateProvider(ctx, provider))

	directoryRepo := NewDirectoryIdentityRepository(appDB, rls)
	connector := &domain.DirectoryConnector{
		ProviderID:        provider.ID,
		Status:            domain.DirectoryConnectorObserve,
		GroupPattern:      "^(?P<team>.+?)(?P<role>Member)$",
		RoleEntitlements:  map[string]domain.DirectoryRoleEntitlement{"Member": {Role: "member", Scopes: []string{"read"}}},
		MaxAutoTeams:      5,
		CredentialVersion: 1,
	}
	require.NoError(t, directoryRepo.CreateDirectoryConnector(ctx, connector))
	user, err := directoryRepo.UpsertDirectoryUser(ctx, domain.DirectoryUser{
		ConnectorID: connector.ID,
		ExternalID:  "atomic-user",
		UserName:    "atomic@example.test",
		Active:      true,
	})
	require.NoError(t, err)

	_, err = directoryRepo.UpsertDirectoryGroupWithMembers(ctx, domain.DirectoryGroup{
		ConnectorID: connector.ID,
		ExternalID:  "atomic-group",
		DisplayName: "ResearchMember",
		Active:      true,
	}, []uuid.UUID{uuid.New()})
	require.ErrorContains(t, err, "members must belong")
	groups, err := directoryRepo.ListDirectoryGroups(ctx, connector.ID)
	require.NoError(t, err)
	require.Empty(t, groups)

	group, err := directoryRepo.UpsertDirectoryGroupWithMembers(ctx, domain.DirectoryGroup{
		ConnectorID: connector.ID,
		ExternalID:  "atomic-group",
		DisplayName: "ResearchMember",
		Active:      true,
	}, []uuid.UUID{user.ID})
	require.NoError(t, err)
	stored, err := directoryRepo.GetDirectoryGroup(ctx, connector.ID, group.ID)
	require.NoError(t, err)
	require.Len(t, stored.Members, 1)
	require.Equal(t, user.ID, stored.Members[0].ID)
}
