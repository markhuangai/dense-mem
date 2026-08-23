package access

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestDirectoryReconcilePlanPreservesManualBindingsAndTeamLifecycle(t *testing.T) {
	t.Parallel()

	connectorID := uuid.New()
	providerID := uuid.New()
	manualTeamID := uuid.New()
	exactTeamID := uuid.New()
	directoryTeamID := uuid.New()
	inactiveGroupID := uuid.New()
	manualGroupID := uuid.New()
	existingGroupID := uuid.New()
	exactGroupID := uuid.New()
	collisionGroupID := uuid.New()
	invalidEntitlementGroupID := uuid.New()
	identityID := uuid.New()

	member := domain.DirectoryUser{
		ID:          uuid.New(),
		IdentityID:  identityID,
		ExternalID:  "entra-user-1",
		Email:       "alex@example.test",
		DisplayName: "Alex Example",
		Active:      true,
	}
	snapshot := domain.DirectoryConnectorSnapshot{
		Connector: domain.DirectoryConnector{
			ID:           connectorID,
			ProviderID:   providerID,
			Status:       domain.DirectoryConnectorActive,
			GroupPattern: "^(?P<team>.+?)(?P<role>Member|Manager)$",
			RoleEntitlements: map[string]domain.DirectoryRoleEntitlement{
				"Member":  {Role: CredentialRoleMember, Scopes: []string{CredentialScopeRead}},
				"Manager": {Role: CredentialRoleManager, Scopes: []string{CredentialScopeRead, CredentialScopeWrite}},
			},
			MaxAutoTeams: 10,
		},
		Users: []domain.DirectoryUser{{IdentityID: identityID}, {}},
		Groups: []domain.DirectoryGroup{
			{ID: inactiveGroupID, ExternalID: "entra-inactive", DisplayName: "InactiveManager", Active: false},
			{ID: manualGroupID, ExternalID: "entra-manual", DisplayName: "IgnoredManager", Active: true, Members: []domain.DirectoryUser{member, {ID: uuid.New(), Active: false}}},
			{ID: existingGroupID, ExternalID: "entra-existing", DisplayName: "ExistingManager", Active: true, Members: []domain.DirectoryUser{member}},
			{ID: exactGroupID, ExternalID: "entra-exact", DisplayName: "ExactMember", Active: true, Members: []domain.DirectoryUser{member}},
			{ID: collisionGroupID, ExternalID: "entra-collision", DisplayName: "CollisionManager", Active: true},
			{ID: invalidEntitlementGroupID, ExternalID: "entra-invalid-entitlement", DisplayName: "UnknownRole", Active: true},
		},
		Bindings: []domain.DirectoryGroupBinding{
			{ConnectorID: connectorID, GroupID: inactiveGroupID, TeamID: directoryTeamID, Origin: domain.DirectoryBindingCreated, Role: CredentialRoleManager, Scopes: []string{CredentialScopeRead, CredentialScopeWrite}},
			{ConnectorID: connectorID, GroupID: existingGroupID, TeamID: exactTeamID, Origin: domain.DirectoryBindingAdopted, Role: CredentialRoleMember, Scopes: []string{CredentialScopeRead}},
		},
		ManualMappings: []domain.SSOGroupMapping{{ProviderID: providerID, GroupID: "entra-manual", TeamID: manualTeamID, Role: CredentialRoleManager, Scopes: []string{CredentialScopeRead, CredentialScopeWrite}}},
		Teams: []domain.DirectoryTeam{
			{ID: manualTeamID, Name: "Manual", Status: "active"},
			{ID: exactTeamID, Name: "Exact", Status: "active"},
			{ID: directoryTeamID, Name: "Inactive", Status: "active", DirectoryManaged: true},
			{ID: uuid.New(), Name: "Collision", Status: "active", DirectoryManaged: true},
		},
	}

	plan, preview, err := buildDirectoryReconcilePlan(snapshot)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{identityID}, plan.DirectoryIdentityIDs)
	require.ElementsMatch(t, []string{"entra-inactive", "entra-manual"}, plan.DisableDirectoryGroupIDs)
	require.Equal(t, []uuid.UUID{directoryTeamID}, plan.ArchiveDirectoryTeamIDs)
	require.Len(t, plan.Bindings, 2)

	bindings := make(map[uuid.UUID]domain.DirectoryBindingAction, len(plan.Bindings))
	for _, binding := range plan.Bindings {
		bindings[binding.GroupID] = binding
	}
	require.Equal(t, domain.DirectoryBindingAdopted, bindings[existingGroupID].Origin)
	require.Equal(t, "Exact", bindings[existingGroupID].TeamName)
	require.Equal(t, domain.DirectoryBindingExactName, bindings[exactGroupID].Origin)
	require.Equal(t, exactTeamID, bindings[exactGroupID].TeamID)
	require.False(t, bindings[exactGroupID].CreateTeam)

	require.Len(t, plan.ProfileGrants, 2)
	grants := make(map[uuid.UUID]domain.DirectoryProfileGrant, len(plan.ProfileGrants))
	for _, grant := range plan.ProfileGrants {
		grants[grant.TeamID] = grant
	}
	require.Equal(t, CredentialRoleManager, grants[manualTeamID].Entitlement.Role)
	require.Equal(t, CredentialRoleMember, grants[exactTeamID].Entitlement.Role)
	require.Equal(t, "entra-user-1", grants[manualTeamID].Subject)

	require.Len(t, preview.Candidates, len(plan.Bindings))
	require.Len(t, plan.Issues, 2)
	issues := make(map[uuid.UUID]domain.DirectoryIssueKind, len(plan.Issues))
	for _, issue := range plan.Issues {
		require.NotNil(t, issue.GroupID)
		issues[*issue.GroupID] = issue.Kind
	}
	require.Equal(t, domain.DirectoryIssueCollision, issues[collisionGroupID])
	require.Equal(t, domain.DirectoryIssueInvalidGroup, issues[invalidEntitlementGroupID])
}

func TestDirectoryReconcilePlanUsesDirectoryFallbackSubjectAndRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	connectorID := uuid.New()
	groupID := uuid.New()
	identityID := uuid.New()
	base := domain.DirectoryConnectorSnapshot{
		Connector: domain.DirectoryConnector{
			ID:           connectorID,
			ProviderID:   uuid.New(),
			GroupPattern: "^(?P<team>.+?)(?P<role>Member)$",
			RoleEntitlements: map[string]domain.DirectoryRoleEntitlement{
				"Member": {Role: CredentialRoleMember, Scopes: []string{CredentialScopeRead}},
			},
			MaxAutoTeams: 1,
		},
		Groups: []domain.DirectoryGroup{{
			ID:          groupID,
			DisplayName: "ResearchMember",
			Active:      true,
			Members:     []domain.DirectoryUser{{ID: uuid.New(), IdentityID: identityID, Active: true}},
		}},
	}

	plan, _, err := buildDirectoryReconcilePlan(base)
	require.NoError(t, err)
	require.Len(t, plan.ProfileGrants, 1)
	require.Equal(t, "scim:"+base.Groups[0].Members[0].ID.String(), plan.ProfileGrants[0].Subject)
	require.True(t, plan.Bindings[0].CreateTeam)

	invalid := base
	invalid.Connector.GroupPattern = "["
	_, _, err = buildDirectoryReconcilePlan(invalid)
	require.Error(t, err)
	invalid = base
	invalid.Connector.GroupPattern = "^(?P<team>.+)$"
	_, _, err = buildDirectoryReconcilePlan(invalid)
	require.Error(t, err)
	invalid = base
	invalid.Connector.RoleEntitlements = nil
	_, _, err = buildDirectoryReconcilePlan(invalid)
	require.Error(t, err)
}

func TestDirectoryReconcilePlanReusesTeamCapturesAndRestoresOnlyDirectoryTeams(t *testing.T) {
	t.Parallel()

	connectorID := uuid.New()
	providerID := uuid.New()
	identityID := uuid.New()
	member := domain.DirectoryUser{ID: uuid.New(), IdentityID: identityID, ExternalID: "entra-user", Active: true}
	base := domain.DirectoryConnector{
		ID:           connectorID,
		ProviderID:   providerID,
		Status:       domain.DirectoryConnectorActive,
		GroupPattern: "^(?P<team>.+?)(?P<role>Member|Manager)$",
		RoleEntitlements: map[string]domain.DirectoryRoleEntitlement{
			"Member":  {Role: CredentialRoleMember, Scopes: []string{CredentialScopeRead}},
			"Manager": {Role: CredentialRoleManager, Scopes: []string{CredentialScopeRead, CredentialScopeWrite}},
		},
		MaxAutoTeams: 2,
	}

	memberGroupID := uuid.New()
	managerGroupID := uuid.New()
	plan, preview, err := buildDirectoryReconcilePlan(domain.DirectoryConnectorSnapshot{
		Connector: base,
		Groups: []domain.DirectoryGroup{
			{ID: memberGroupID, ExternalID: "research-member", DisplayName: "ResearchMember", Active: true, Members: []domain.DirectoryUser{member}},
			{ID: managerGroupID, ExternalID: "research-manager", DisplayName: "ResearchManager", Active: true, Members: []domain.DirectoryUser{member}},
		},
	})
	require.NoError(t, err)
	require.Len(t, plan.Bindings, 2)
	require.True(t, plan.Bindings[0].CreateTeam)
	require.False(t, plan.Bindings[1].CreateTeam)
	require.Equal(t, plan.Bindings[0].TeamID, plan.Bindings[1].TeamID)
	require.Len(t, plan.ProfileGrants, 1)
	require.Equal(t, CredentialRoleManager, plan.ProfileGrants[0].Entitlement.Role)
	require.Equal(t, "ResearchMember", preview.Candidates[0].DisplayName)
	require.Equal(t, "ResearchManager", preview.Candidates[1].DisplayName)

	directoryTeamID := uuid.New()
	plan, _, err = buildDirectoryReconcilePlan(domain.DirectoryConnectorSnapshot{
		Connector: base,
		Groups: []domain.DirectoryGroup{
			{ID: memberGroupID, ExternalID: "research-member", DisplayName: "ResearchMember", Active: false},
			{ID: managerGroupID, ExternalID: "research-manager", DisplayName: "ResearchManager", Active: true, Members: []domain.DirectoryUser{member}},
		},
		Bindings: []domain.DirectoryGroupBinding{
			{ConnectorID: connectorID, GroupID: memberGroupID, TeamID: directoryTeamID, Origin: domain.DirectoryBindingCreated, Role: CredentialRoleMember, Scopes: []string{CredentialScopeRead}},
			{ConnectorID: connectorID, GroupID: managerGroupID, TeamID: directoryTeamID, Origin: domain.DirectoryBindingCreated, Role: CredentialRoleManager, Scopes: []string{CredentialScopeRead, CredentialScopeWrite}},
		},
		Teams: []domain.DirectoryTeam{{ID: directoryTeamID, Name: "Research", Status: "archived", DirectoryManaged: true}},
	})
	require.NoError(t, err)
	require.Empty(t, plan.ArchiveDirectoryTeamIDs)
	require.Equal(t, []uuid.UUID{directoryTeamID}, plan.RestoreDirectoryTeamIDs)

	plan, _, err = buildDirectoryReconcilePlan(domain.DirectoryConnectorSnapshot{
		Connector: base,
		Groups:    []domain.DirectoryGroup{{ID: memberGroupID, ExternalID: "research-member", DisplayName: "ResearchMember", Active: true}},
		Teams:     []domain.DirectoryTeam{{ID: uuid.New(), Name: "Research", Status: "archived"}},
	})
	require.NoError(t, err)
	require.Empty(t, plan.Bindings)
	require.Len(t, plan.Issues, 1)
	require.Equal(t, domain.DirectoryIssueCollision, plan.Issues[0].Kind)
}
