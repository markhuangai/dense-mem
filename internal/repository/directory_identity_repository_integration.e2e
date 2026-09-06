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
	profiles, err := ssoRepo.ListTeamMembershipsForIdentity(ctx, user.IdentityID)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	require.Equal(t, directoryMembershipName(user.Email, user.DisplayName, user.IdentityID), profiles[0].Membership.Name)
	entitled, err := ssoRepo.DirectoryMembershipEntitled(ctx, provider.ID, user.IdentityID, teamID, group.ExternalID)
	require.NoError(t, err)
	require.True(t, entitled)
	entitled, err = ssoRepo.DirectoryMembershipEntitled(ctx, provider.ID, uuid.New(), teamID, group.ExternalID)
	require.NoError(t, err)
	require.False(t, entitled)
	require.NoError(t, directoryRepo.ReplaceDirectoryGroupMembers(ctx, connector.ID, group.ID, nil))
	entitled, err = ssoRepo.DirectoryMembershipEntitled(ctx, provider.ID, user.IdentityID, teamID, group.ExternalID)
	require.NoError(t, err)
	require.False(t, entitled)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var managed, profileActive bool
		if err := tx.Raw(`SELECT directory_managed FROM teams WHERE id = $1`, teamID).Row().Scan(&managed); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT membership.status = 'active' AND actor.active
			FROM team_memberships AS membership
			JOIN actor_identities AS actor ON actor.id = membership.actor_identity_id
			WHERE membership.team_id = $1 AND membership.actor_identity_id = $2
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
			SELECT status = 'revoked'
			FROM team_memberships
			WHERE team_id = $1 AND actor_identity_id = $2
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

func TestDirectoryPageQueriesAreScopedAndRejectDuplicateCreates(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	ssoRepo := NewSSORepository(appDB, rls)
	provider := &domain.SSOProvider{
		Name:         "directory page provider",
		Kind:         domain.SSOProviderKindGenericOIDC,
		IssuerURL:    "https://idp.example.test",
		ClientID:     "directory-page-client",
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

	alpha, err := directoryRepo.CreateDirectoryUser(ctx, domain.DirectoryUser{
		ConnectorID: connector.ID,
		ExternalID:  "entra-alpha",
		UserName:    "alpha@example.test",
		DisplayName: "Alpha",
		Active:      true,
	})
	require.NoError(t, err)
	beta, err := directoryRepo.CreateDirectoryUser(ctx, domain.DirectoryUser{
		ConnectorID: connector.ID,
		ExternalID:  "entra-beta",
		UserName:    "beta@example.test",
		DisplayName: "Beta",
		Active:      true,
	})
	require.NoError(t, err)
	group, err := directoryRepo.CreateDirectoryGroupWithMembers(ctx, domain.DirectoryGroup{
		ConnectorID: connector.ID,
		ExternalID:  "entra-research",
		DisplayName: "ResearchMember",
		Active:      true,
	}, []uuid.UUID{alpha.ID})
	require.NoError(t, err)

	otherProvider := &domain.SSOProvider{
		Name:         "other directory page provider",
		Kind:         domain.SSOProviderKindGenericOIDC,
		IssuerURL:    "https://other-idp.example.test",
		ClientID:     "other-directory-page-client",
		Enabled:      true,
		GroupsScopes: []string{},
	}
	require.NoError(t, ssoRepo.CreateProvider(ctx, otherProvider))
	otherConnector := &domain.DirectoryConnector{
		ProviderID:        otherProvider.ID,
		Status:            domain.DirectoryConnectorObserve,
		GroupPattern:      "^(?P<team>.+?)(?P<role>Member)$",
		RoleEntitlements:  map[string]domain.DirectoryRoleEntitlement{"Member": {Role: "member", Scopes: []string{"read"}}},
		MaxAutoTeams:      5,
		CredentialVersion: 1,
	}
	require.NoError(t, directoryRepo.CreateDirectoryConnector(ctx, otherConnector))
	_, err = directoryRepo.CreateDirectoryUser(ctx, domain.DirectoryUser{
		ConnectorID: otherConnector.ID,
		ExternalID:  "entra-other",
		UserName:    "other@example.test",
		DisplayName: "Other",
		Active:      true,
	})
	require.NoError(t, err)

	users, total, err := directoryRepo.ListDirectoryUsersPage(ctx, connector.ID, domain.DirectoryPageRequest{
		FilterField: "userName",
		FilterValue: "ALPHA@example.test",
		Limit:       1,
	})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, users, 1)
	require.Equal(t, alpha.ID, users[0].ID)

	users, total, err = directoryRepo.ListDirectoryUsersPage(ctx, connector.ID, domain.DirectoryPageRequest{Offset: 1, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, users, 1)
	require.Equal(t, beta.ID, users[0].ID)

	groups, total, err := directoryRepo.ListDirectoryGroupsPage(ctx, connector.ID, domain.DirectoryPageRequest{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, groups, 1)
	require.Equal(t, group.ID, groups[0].ID)
	require.Equal(t, []domain.DirectoryUser{*alpha}, groups[0].Members)

	groups, total, err = directoryRepo.ListDirectoryGroupsPage(ctx, connector.ID, domain.DirectoryPageRequest{
		FilterField: "displayName",
		FilterValue: "researchmember",
		Limit:       1,
	})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, groups, 1)
	require.Equal(t, group.ID, groups[0].ID)

	_, _, err = directoryRepo.ListDirectoryUsersPage(ctx, connector.ID, domain.DirectoryPageRequest{FilterField: "id", FilterValue: "not-a-uuid", Limit: 1})
	require.ErrorIs(t, err, ErrDirectoryInvalidValue)
	_, err = directoryRepo.CreateDirectoryUser(ctx, domain.DirectoryUser{
		ConnectorID: connector.ID,
		ExternalID:  alpha.ExternalID,
		UserName:    alpha.UserName,
		Active:      true,
	})
	require.ErrorIs(t, err, ErrDirectoryResourceConflict)
	_, err = directoryRepo.CreateDirectoryGroupWithMembers(ctx, domain.DirectoryGroup{
		ConnectorID: connector.ID,
		ExternalID:  group.ExternalID,
		DisplayName: group.DisplayName,
		Active:      true,
	}, nil)
	require.ErrorIs(t, err, ErrDirectoryResourceConflict)
}

func TestSSOListMappingsExcludesRetiredMappings(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	ssoRepo := NewSSORepository(appDB, rls)
	provider := &domain.SSOProvider{
		Name:         "mapping list provider",
		Kind:         domain.SSOProviderKindGenericOIDC,
		IssuerURL:    "https://idp.example.test",
		ClientID:     "mapping-list-client",
		Enabled:      true,
		GroupsScopes: []string{},
	}
	require.NoError(t, ssoRepo.CreateProvider(ctx, provider))

	activeTeamID := uuid.New()
	retiredTeamID := uuid.New()
	activeMappingID := uuid.New()
	retiredMappingID := uuid.New()
	now := time.Now().UTC()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config, status, created_at, updated_at)
			VALUES ($1, 'Active mapping team', '', '{}'::jsonb, '{}'::jsonb, 'active', $2, $2),
			       ($3, 'Retired mapping team', '', '{}'::jsonb, '{}'::jsonb, 'active', $2, $2)
		`, activeTeamID, now, retiredTeamID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO sso_group_mappings (
				id, provider_id, team_id, group_id, group_name, scopes, role, enabled, origin, retired_at, created_at, updated_at
			) VALUES
				($1, $2, $3, 'active-group', 'Active group', ARRAY['read']::text[], 'member', true, 'manual', NULL, $5, $5),
				($4, $2, $6, 'retired-group', 'Retired group', ARRAY['read']::text[], 'member', false, 'manual', $5, $5, $5)
		`, activeMappingID, provider.ID, activeTeamID, retiredMappingID, now, retiredTeamID).Error
	}))

	mappings, err := ssoRepo.ListMappings(ctx, provider.ID)
	require.NoError(t, err)
	require.Len(t, mappings, 1)
	require.Equal(t, activeMappingID, mappings[0].ID)
}

func TestDirectoryReconcileFenceAndDisableRetireOnlyDirectoryGrants(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	ssoRepo := NewSSORepository(appDB, rls)
	provider := &domain.SSOProvider{
		Name:         "directory fence provider",
		Kind:         domain.SSOProviderKindGenericOIDC,
		IssuerURL:    "https://idp.example.test",
		ClientID:     "directory-fence-client",
		Enabled:      true,
		GroupsScopes: []string{},
	}
	require.NoError(t, ssoRepo.CreateProvider(ctx, provider))
	directoryRepo := NewDirectoryIdentityRepository(appDB, rls)
	connector := &domain.DirectoryConnector{
		ProviderID:        provider.ID,
		Status:            domain.DirectoryConnectorActive,
		GroupPattern:      "^(?P<team>.+?)(?P<role>Manager)$",
		RoleEntitlements:  map[string]domain.DirectoryRoleEntitlement{"Manager": {Role: "manager", Scopes: []string{"read", "write"}}},
		MaxAutoTeams:      5,
		CredentialVersion: 1,
	}
	require.NoError(t, directoryRepo.CreateDirectoryConnector(ctx, connector))
	staleVersion := connector.ReconcileVersion
	user, err := directoryRepo.UpsertDirectoryUser(ctx, domain.DirectoryUser{
		ConnectorID: connector.ID,
		ExternalID:  "entra-fence-user",
		UserName:    "fence@example.test",
		DisplayName: "Fence User",
		Active:      true,
	})
	require.NoError(t, err)
	require.ErrorIs(t, directoryRepo.ApplyDirectoryReconcilePlan(ctx, domain.DirectoryReconcilePlan{
		ConnectorID:      connector.ID,
		ProviderID:       provider.ID,
		ReconcileVersion: staleVersion,
	}), ErrDirectoryReconcileStale)

	group, err := directoryRepo.UpsertDirectoryGroup(ctx, domain.DirectoryGroup{
		ConnectorID: connector.ID,
		ExternalID:  "entra-fence-group",
		DisplayName: "ResearchManager",
		Active:      true,
	})
	require.NoError(t, err)
	require.NoError(t, directoryRepo.ReplaceDirectoryGroupMembers(ctx, connector.ID, group.ID, []uuid.UUID{user.ID}))
	current, err := directoryRepo.GetDirectoryConnector(ctx, connector.ID)
	require.NoError(t, err)
	directoryTeamID := uuid.New()
	require.NoError(t, directoryRepo.ApplyDirectoryReconcilePlan(ctx, domain.DirectoryReconcilePlan{
		ConnectorID:          connector.ID,
		ProviderID:           provider.ID,
		ReconcileVersion:     current.ReconcileVersion,
		DirectoryIdentityIDs: []uuid.UUID{user.IdentityID},
		Bindings: []domain.DirectoryBindingAction{{
			GroupID:         group.ID,
			GroupExternalID: group.ExternalID,
			TeamID:          directoryTeamID,
			TeamName:        "Directory Research",
			CreateTeam:      true,
			Origin:          domain.DirectoryBindingCreated,
			Entitlement:     domain.DirectoryRoleEntitlement{Role: "manager", Scopes: []string{"read", "write"}},
		}},
		ProfileGrants: []domain.DirectoryProfileGrant{{
			IdentityID:      user.IdentityID,
			TeamID:          directoryTeamID,
			Subject:         user.ExternalID,
			DisplayName:     user.DisplayName,
			GroupExternalID: group.ExternalID,
			Entitlement:     domain.DirectoryRoleEntitlement{Role: "manager", Scopes: []string{"read", "write"}},
		}},
	}))

	manualTeamID := uuid.New()
	manualMappingID := uuid.New()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config, status, created_at, updated_at)
			VALUES ($1, 'Manual directory exception', '', '{}'::jsonb, '{}'::jsonb, 'active', $2, $2)
		`, manualTeamID, now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO sso_group_mappings (
				id, provider_id, team_id, group_id, group_name, scopes, role, enabled, origin, retired_at, created_at, updated_at
			) VALUES ($1, $2, $3, 'manual-exception', 'Manual exception', ARRAY['read']::text[], 'member', true, 'manual', NULL, $4, $4)
		`, manualMappingID, provider.ID, manualTeamID, now).Error
	}))

	require.NoError(t, directoryRepo.SetDirectoryConnectorStatus(ctx, connector.ID, domain.DirectoryConnectorDisabled, nil))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var directoryEnabled, directoryRetired, manualEnabled, manualRetired, profileRevoked bool
		if err := tx.Raw(`
			SELECT enabled, retired_at IS NOT NULL
			FROM sso_group_mappings
			WHERE provider_id = $1 AND team_id = $2 AND group_id = $3
		`, provider.ID, directoryTeamID, group.ExternalID).Row().Scan(&directoryEnabled, &directoryRetired); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT enabled, retired_at IS NOT NULL
			FROM sso_group_mappings
			WHERE id = $1
		`, manualMappingID).Row().Scan(&manualEnabled, &manualRetired); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status = 'revoked'
			FROM team_memberships
			WHERE team_id = $1 AND actor_identity_id = $2
		`, directoryTeamID, user.IdentityID).Row().Scan(&profileRevoked); err != nil {
			return err
		}
		require.False(t, directoryEnabled)
		require.True(t, directoryRetired)
		require.True(t, manualEnabled)
		require.False(t, manualRetired)
		require.True(t, profileRevoked)
		return nil
	}))

	stored, err := directoryRepo.GetDirectoryConnector(ctx, connector.ID)
	require.NoError(t, err)
	require.Equal(t, domain.DirectoryConnectorDisabled, stored.Status)
	require.Greater(t, stored.ReconcileVersion, current.ReconcileVersion)
}
