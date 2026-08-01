package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestNormalizeDirectoryConnectorRequiresAnchoredNamedGroupRule(t *testing.T) {
	t.Parallel()

	valid := directoryConnectorForPolicyTest()
	require.NoError(t, normalizeDirectoryConnector(&valid))
	require.Equal(t, "^gAD7485(?P<team>.+?)(?P<role>Readonly|Member|Manager)Permission$", valid.GroupPattern)

	for _, pattern := range []string{
		"gAD7485(?P<team>.+?)(?P<role>Readonly|Member|Manager)Permission$",
		"^gAD7485(?P<team>.+?)Permission$",
		"^gAD7485(?P<role>Readonly|Member|Manager)Permission$",
		"^(?P<team>.+?)(?P<role>Manager)",
	} {
		connector := directoryConnectorForPolicyTest()
		connector.GroupPattern = pattern
		require.Error(t, normalizeDirectoryConnector(&connector), pattern)
	}
}

func TestNormalizeDirectoryConnectorValidatesRoleEntitlements(t *testing.T) {
	t.Parallel()

	connector := directoryConnectorForPolicyTest()
	connector.RoleEntitlements["Manager"] = domain.DirectoryRoleEntitlement{Role: "member", Scopes: []string{"read"}}
	require.ErrorContains(t, normalizeDirectoryConnector(&connector), "must map to manager")

	connector = directoryConnectorForPolicyTest()
	connector.RoleEntitlements["Readonly"] = domain.DirectoryRoleEntitlement{Role: "member", Scopes: []string{"read", "write"}}
	require.ErrorContains(t, normalizeDirectoryConnector(&connector), "must not grant write")
}

func TestDirectoryConnectorTransitionRequiresCurrentPreview(t *testing.T) {
	t.Parallel()

	connector := directoryConnectorForPolicyTest()
	connector.Status = domain.DirectoryConnectorObserve
	preview := domain.DirectoryPreview{Version: "current-preview"}

	require.ErrorIs(t, validateDirectoryConnectorTransition(connector, domain.DirectoryConnectorActive, "stale-preview", preview), ErrDirectoryPreviewStale)
	require.NoError(t, validateDirectoryConnectorTransition(connector, domain.DirectoryConnectorActive, "current-preview", preview))
	require.ErrorContains(t, validateDirectoryConnectorTransition(connector, domain.DirectoryConnectorActive, "", preview), "preview version is required")
}

func TestDirectoryPolicyRejectsUnsafeConfigurationAndTransitions(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, normalizeDirectoryConnector(nil), "required")
	missingProvider := directoryConnectorForPolicyTest()
	missingProvider.ProviderID = uuid.Nil
	require.ErrorContains(t, normalizeDirectoryConnector(&missingProvider), "provider ID")
	invalidStatus := directoryConnectorForPolicyTest()
	invalidStatus.Status = "unexpected"
	require.ErrorContains(t, normalizeDirectoryConnector(&invalidStatus), "status")
	invalidPattern := directoryConnectorForPolicyTest()
	invalidPattern.GroupPattern = "^(?P<team>[)(?P<role>Manager)$"
	require.ErrorContains(t, normalizeDirectoryConnector(&invalidPattern), "invalid")
	invalidCap := directoryConnectorForPolicyTest()
	invalidCap.MaxAutoTeams = 1001
	require.ErrorContains(t, normalizeDirectoryConnector(&invalidCap), "between 1 and 1000")

	_, err := normalizeDirectoryRoleEntitlements(map[string]domain.DirectoryRoleEntitlement{"": {Role: APIKeyRoleMember, Scopes: []string{APIKeyScopeRead}}})
	require.ErrorContains(t, err, "capture")
	_, err = normalizeDirectoryRoleEntitlements(map[string]domain.DirectoryRoleEntitlement{"Member": {Role: "unknown", Scopes: []string{APIKeyScopeRead}}})
	require.Error(t, err)
	_, err = normalizeDirectoryRoleEntitlements(map[string]domain.DirectoryRoleEntitlement{"Member": {Role: APIKeyRoleMember, Scopes: nil}})
	require.ErrorContains(t, err, "at least one")

	connector := directoryConnectorForPolicyTest()
	connector.Status = domain.DirectoryConnectorActive
	require.NoError(t, validateDirectoryConnectorTransition(connector, domain.DirectoryConnectorDisabled, "", domain.DirectoryPreview{}))
	require.ErrorContains(t, validateDirectoryConnectorTransition(connector, domain.DirectoryConnectorObserve, "", domain.DirectoryPreview{}), "transition")
	connector.Status = domain.DirectoryConnectorObserve
	require.NoError(t, validateDirectoryConnectorTransition(connector, domain.DirectoryConnectorDisabled, "", domain.DirectoryPreview{}))
	require.ErrorContains(t, validateDirectoryConnectorTransition(connector, "", "", domain.DirectoryPreview{}), "status is required")
}

func TestBuildDirectoryReconcilePlanUsesDurableBindingsAndManualMappings(t *testing.T) {
	t.Parallel()

	connectorID := uuid.New()
	providerID := uuid.New()
	groupID := uuid.New()
	identityID := uuid.New()
	teamID := uuid.New()
	snapshot := domain.DirectoryConnectorSnapshot{
		Connector: domain.DirectoryConnector{
			ID:               connectorID,
			ProviderID:       providerID,
			Status:           domain.DirectoryConnectorObserve,
			GroupPattern:     "^gAD7485(?P<team>.+?)(?P<role>Readonly|Member|Manager)Permission$",
			RoleEntitlements: directoryConnectorForPolicyTest().RoleEntitlements,
			MaxAutoTeams:     10,
		},
		Groups: []domain.DirectoryGroup{{
			ID:          groupID,
			ExternalID:  "entra-group-1",
			DisplayName: "gAD7485ResearchManagerPermission",
			Active:      true,
			Members: []domain.DirectoryUser{{
				ID:          uuid.New(),
				ExternalID:  "entra-user-1",
				IdentityID:  identityID,
				DisplayName: "Directory Manager",
				Active:      true,
			}},
		}},
	}

	plan, preview, err := buildDirectoryReconcilePlan(snapshot)
	require.NoError(t, err)
	require.Len(t, plan.Bindings, 1)
	require.True(t, plan.Bindings[0].CreateTeam)
	require.Equal(t, domain.DirectoryBindingCreated, plan.Bindings[0].Origin)
	require.Equal(t, "Research", plan.Bindings[0].TeamName)
	require.Equal(t, "manager", plan.Bindings[0].Entitlement.Role)
	require.Len(t, plan.ProfileGrants, 1)
	require.Equal(t, identityID, plan.ProfileGrants[0].IdentityID)
	require.NotEmpty(t, preview.Version)

	secondPlan, secondPreview, err := buildDirectoryReconcilePlan(snapshot)
	require.NoError(t, err)
	require.Equal(t, plan.Bindings[0].TeamID, secondPlan.Bindings[0].TeamID)
	require.Equal(t, preview.Version, secondPreview.Version)

	snapshot.ManualMappings = []domain.SSOGroupMapping{{
		ProviderID: providerID,
		TeamID:     teamID,
		GroupID:    "entra-group-1",
		Scopes:     []string{"read"},
		Role:       "member",
		Enabled:    true,
		Origin:     "manual",
	}}
	plan, _, err = buildDirectoryReconcilePlan(snapshot)
	require.NoError(t, err)
	require.Empty(t, plan.Bindings)
	require.Equal(t, []string{"entra-group-1"}, plan.DisableDirectoryGroupIDs)
	require.Len(t, plan.ProfileGrants, 1)
	require.Equal(t, teamID, plan.ProfileGrants[0].TeamID)
}

func TestBuildDirectoryReconcilePlanArchivesOnlyDirectoryCreatedTeams(t *testing.T) {
	t.Parallel()

	connectorID := uuid.New()
	groupID := uuid.New()
	directoryTeamID := uuid.New()
	snapshot := domain.DirectoryConnectorSnapshot{
		Connector: domain.DirectoryConnector{
			ID:               connectorID,
			ProviderID:       uuid.New(),
			Status:           domain.DirectoryConnectorActive,
			GroupPattern:     "^(?P<team>.+?)(?P<role>Manager)$",
			RoleEntitlements: directoryConnectorForPolicyTest().RoleEntitlements,
			MaxAutoTeams:     10,
		},
		Groups: []domain.DirectoryGroup{{ID: groupID, ExternalID: "removed-group", DisplayName: "ResearchManager", Active: false}},
		Bindings: []domain.DirectoryGroupBinding{{
			ConnectorID: connectorID,
			GroupID:     groupID,
			TeamID:      directoryTeamID,
			Origin:      domain.DirectoryBindingCreated,
			Scopes:      []string{"read", "write", "feedback:read"},
			Role:        "manager",
		}},
		Teams: []domain.DirectoryTeam{{ID: directoryTeamID, DirectoryManaged: true}},
	}

	plan, _, err := buildDirectoryReconcilePlan(snapshot)
	require.NoError(t, err)
	require.Equal(t, []string{"removed-group"}, plan.DisableDirectoryGroupIDs)
	require.Equal(t, []uuid.UUID{directoryTeamID}, plan.ArchiveDirectoryTeamIDs)

	snapshot.Bindings[0].Origin = domain.DirectoryBindingAdopted
	plan, _, err = buildDirectoryReconcilePlan(snapshot)
	require.NoError(t, err)
	require.Empty(t, plan.ArchiveDirectoryTeamIDs)
}

func directoryConnectorForPolicyTest() domain.DirectoryConnector {
	return domain.DirectoryConnector{
		ProviderID:   [16]byte{1},
		Status:       domain.DirectoryConnectorObserve,
		GroupPattern: "^gAD7485(?P<team>.+?)(?P<role>Readonly|Member|Manager)Permission$",
		RoleEntitlements: map[string]domain.DirectoryRoleEntitlement{
			"Readonly": {Role: "member", Scopes: []string{"read"}},
			"Member":   {Role: "member", Scopes: []string{"read", "write"}},
			"Manager":  {Role: "manager", Scopes: []string{"read", "write", "feedback:read"}},
		},
		MaxAutoTeams: 100,
	}
}
