package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	cryptoutil "github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestDirectoryIdentityServiceLifecycleCredentialsAndReconciliation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	repo := newDirectoryIdentityServiceRepository()
	repo.now = func() time.Time { return now }
	svc := NewDirectoryIdentityService(repo, DirectoryIdentityConfig{
		OAuthTokenTTL: time.Hour,
		Now:           func() time.Time { return now },
	})
	providerID := uuid.New()
	created, credential, err := svc.CreateConnector(ctx, directoryConnectorForServiceTest(providerID))
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, created.ID)
	require.Equal(t, domain.DirectoryConnectorDisabled, created.Status)
	require.Equal(t, 1, credential.CredentialVersion)
	require.Equal(t, created.ID, credential.ConnectorID)
	require.True(t, cryptoutil.VerifyKey(credential.BearerToken, created.BearerTokenHash))
	require.True(t, cryptoutil.VerifyKey(credential.OAuthClientSecret, created.OAuthClientSecretHash))

	_, err = svc.AuthenticateSCIM(ctx, created.ID, credential.BearerToken)
	require.ErrorIs(t, err, ErrDirectoryConnectorDisabled)

	listed, err := svc.ListConnectors(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	byProvider, err := svc.GetConnectorForProvider(ctx, providerID)
	require.NoError(t, err)
	require.Equal(t, created.ID, byProvider.ID)

	updated := *created
	updated.MaxAutoTeams = 12
	updated.GroupPattern = "^(?P<team>.+?)(?P<role>Readonly|Member|Manager)$"
	updatedConnector, err := svc.UpdateConnector(ctx, updated)
	require.NoError(t, err)
	require.Equal(t, 12, updatedConnector.MaxAutoTeams)
	require.Equal(t, providerID, updatedConnector.ProviderID)

	observed, err := svc.SetConnectorStatus(ctx, created.ID, domain.DirectoryConnectorObserve, "")
	require.NoError(t, err)
	require.Equal(t, domain.DirectoryConnectorObserve, observed.Status)

	valid, err := svc.AuthenticateSCIM(ctx, created.ID, credential.BearerToken)
	require.NoError(t, err)
	require.True(t, valid)
	rotatedCredential, err := svc.RotateCredentials(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, 2, rotatedCredential.CredentialVersion)
	_, err = svc.AuthenticateSCIM(ctx, created.ID, credential.BearerToken)
	require.ErrorIs(t, err, ErrDirectoryCredentialInvalid)
	oauthToken, expiresAt, err := svc.IssueOAuthToken(ctx, rotatedCredential.OAuthClientID, rotatedCredential.OAuthClientSecret)
	require.NoError(t, err)
	require.NotEmpty(t, oauthToken)
	require.Equal(t, now.Add(time.Hour), expiresAt)
	valid, err = svc.AuthenticateSCIM(ctx, created.ID, oauthToken)
	require.NoError(t, err)
	require.True(t, valid)

	user, err := svc.UpsertUser(ctx, created.ID, domain.DirectoryUser{
		ExternalID:  "  entra-user-1  ",
		UserName:    "  alex@example.com ",
		Email:       "  alex@example.com ",
		DisplayName: "  Alex Example ",
		Active:      true,
		IdentityID:  uuid.New(),
	})
	require.NoError(t, err)
	require.Equal(t, "entra-user-1", user.ExternalID)
	require.Equal(t, "alex@example.com", user.UserName)

	group, err := svc.UpsertGroup(ctx, created.ID, domain.DirectoryGroup{
		ExternalID:  "entra-research-manager",
		DisplayName: "ResearchManager",
		Active:      true,
	})
	require.NoError(t, err)
	require.NoError(t, svc.ReplaceGroupMembers(ctx, created.ID, group.ID, []uuid.UUID{user.ID, user.ID}))
	users, err := svc.ListUsers(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, users, 1)
	groups, err := svc.ListGroups(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, groups, 1)

	preview, err := svc.Preview(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, preview.Candidates, 1)
	require.Equal(t, "Research", preview.Candidates[0].TeamName)
	require.NotEmpty(t, preview.Version)

	active, err := svc.SetConnectorStatus(ctx, created.ID, domain.DirectoryConnectorActive, preview.Version)
	require.NoError(t, err)
	require.Equal(t, domain.DirectoryConnectorActive, active.Status)
	require.Len(t, repo.activationPlans, 1)

	updatedGroup, err := svc.UpsertGroupWithMembers(ctx, created.ID, domain.DirectoryGroup{
		ID:          group.ID,
		ExternalID:  group.ExternalID,
		DisplayName: "ResearchManager",
		Active:      true,
	}, []uuid.UUID{user.ID})
	require.NoError(t, err)
	require.Equal(t, group.ID, updatedGroup.ID)
	require.Len(t, repo.appliedPlans, 1)

	require.NoError(t, svc.DeactivateUser(ctx, created.ID, user.ID))
	storedUser, err := svc.GetUser(ctx, created.ID, user.ID)
	require.NoError(t, err)
	require.False(t, storedUser.Active)

	require.NoError(t, svc.DeactivateGroup(ctx, created.ID, group.ID))
	storedGroup, err := svc.GetGroup(ctx, created.ID, group.ID)
	require.NoError(t, err)
	require.False(t, storedGroup.Active)

	teamID := uuid.New()
	require.NoError(t, svc.AdoptTeam(ctx, created.ID, group.ID, teamID))
	require.Equal(t, []directoryTeamAdoption{{connectorID: created.ID, groupID: group.ID, teamID: teamID}}, repo.adoptions)
}

func TestDirectoryIdentityServiceRejectsUnsafeLifecycleAndCredentials(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := newDirectoryIdentityServiceRepository()
	svc := NewDirectoryIdentityService(repo, DirectoryIdentityConfig{})
	providerID := uuid.New()
	connector, credential, err := svc.CreateConnector(ctx, directoryConnectorForServiceTest(providerID))
	require.NoError(t, err)

	_, _, err = svc.IssueOAuthToken(ctx, credential.OAuthClientID, "wrong-secret")
	require.ErrorIs(t, err, ErrDirectoryCredentialInvalid)
	_, err = svc.AuthenticateSCIM(ctx, connector.ID, "")
	require.ErrorIs(t, err, ErrDirectoryConnectorDisabled)

	_, err = svc.SetConnectorStatus(ctx, connector.ID, domain.DirectoryConnectorActive, "")
	require.ErrorContains(t, err, "transition through observe")

	_, err = svc.SetConnectorStatus(ctx, connector.ID, domain.DirectoryConnectorObserve, "")
	require.NoError(t, err)
	preview, err := svc.Preview(ctx, connector.ID)
	require.NoError(t, err)
	_, err = svc.SetConnectorStatus(ctx, connector.ID, domain.DirectoryConnectorActive, preview.Version+"stale")
	require.ErrorIs(t, err, ErrDirectoryPreviewStale)

	current, err := svc.GetConnector(ctx, connector.ID)
	require.NoError(t, err)
	current.Status = domain.DirectoryConnectorActive
	repo.connectors[connector.ID] = cloneDirectoryConnector(current)
	updated := *current
	updated.GroupPattern = "^(?P<team>.+?)(?P<role>Manager)$"
	_, err = svc.UpdateConnector(ctx, updated)
	require.ErrorContains(t, err, "disable the directory connector")

	_, err = svc.GetConnector(ctx, uuid.New())
	require.ErrorContains(t, err, "not found")
	_, err = svc.GetUser(ctx, connector.ID, uuid.New())
	require.ErrorContains(t, err, "not found")
	_, err = svc.GetGroup(ctx, connector.ID, uuid.New())
	require.ErrorContains(t, err, "not found")
	_, err = svc.GetConnectorForProvider(ctx, uuid.Nil)
	require.ErrorContains(t, err, "required")

	var unavailable *DirectoryIdentityService
	_, _, err = unavailable.CreateConnector(ctx, directoryConnectorForServiceTest(providerID))
	require.ErrorContains(t, err, "unavailable")
}

func TestDirectoryReconcilePlanReportsInvalidCapacityAndStrongestGrant(t *testing.T) {
	t.Parallel()

	connectorID := uuid.New()
	providerID := uuid.New()
	identityID := uuid.New()
	firstGroupID := uuid.New()
	secondGroupID := uuid.New()
	snapshot := domain.DirectoryConnectorSnapshot{
		Connector: domain.DirectoryConnector{
			ID:           connectorID,
			ProviderID:   providerID,
			Status:       domain.DirectoryConnectorObserve,
			GroupPattern: "^(?P<team>.+?)(?P<role>Member|Manager)$",
			RoleEntitlements: map[string]domain.DirectoryRoleEntitlement{
				"Member":  {Role: APIKeyRoleMember, Scopes: []string{APIKeyScopeRead}},
				"Manager": {Role: APIKeyRoleManager, Scopes: []string{APIKeyScopeRead, APIKeyScopeWrite}},
			},
			MaxAutoTeams: 1,
		},
		Groups: []domain.DirectoryGroup{
			{ID: uuid.New(), DisplayName: "does not match", Active: true},
			{
				ID:          firstGroupID,
				DisplayName: "ResearchMember",
				Active:      true,
				Members:     []domain.DirectoryUser{{ID: uuid.New(), IdentityID: identityID, ExternalID: "entra-user", Active: true}},
			},
			{
				ID:          secondGroupID,
				DisplayName: "OperationsManager",
				Active:      true,
				Members:     []domain.DirectoryUser{{ID: uuid.New(), IdentityID: identityID, ExternalID: "entra-user", Active: true}},
			},
		},
	}

	plan, preview, err := buildDirectoryReconcilePlan(snapshot)
	require.NoError(t, err)
	require.Len(t, plan.Bindings, 1)
	require.Len(t, plan.Issues, 2)
	require.Equal(t, domain.DirectoryIssueInvalidGroup, plan.Issues[0].Kind)
	require.Equal(t, domain.DirectoryIssueCapacity, plan.Issues[1].Kind)
	require.Len(t, preview.Candidates, 1)
	require.Equal(t, firstGroupID, preview.Candidates[0].GroupID)

	member := domain.DirectoryProfileGrant{Entitlement: domain.DirectoryRoleEntitlement{Role: APIKeyRoleMember, Scopes: []string{APIKeyScopeRead}}}
	manager := domain.DirectoryProfileGrant{Entitlement: domain.DirectoryRoleEntitlement{Role: APIKeyRoleManager, Scopes: []string{APIKeyScopeRead, APIKeyScopeWrite}}}
	require.Equal(t, manager, strongestDirectoryGrant(member, manager))
	require.Equal(t, manager, strongestDirectoryGrant(manager, member))
}

func TestDirectoryIdentityServiceProvisioningGuardsAndDeterministicHelpers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	providerID := uuid.New()
	repo := newDirectoryIdentityServiceRepository()
	svc := NewDirectoryIdentityService(repo, DirectoryIdentityConfig{})
	connector, _, err := svc.CreateConnector(ctx, directoryConnectorForServiceTest(providerID))
	require.NoError(t, err)
	_, err = svc.UpsertUser(ctx, connector.ID, domain.DirectoryUser{UserName: "blocked@example.test"})
	require.ErrorIs(t, err, ErrDirectoryConnectorDisabled)
	_, err = svc.UpsertGroup(ctx, connector.ID, domain.DirectoryGroup{DisplayName: "BlockedManager"})
	require.ErrorIs(t, err, ErrDirectoryConnectorDisabled)
	require.NoError(t, svc.reconcile(ctx, connector.ID))

	current := *repo.connectors[connector.ID]
	updated := current
	require.False(t, directoryConnectorPolicyChanged(current, updated))
	updated.RoleEntitlements = map[string]domain.DirectoryRoleEntitlement{
		"Manager": {Role: APIKeyRoleManager, Scopes: []string{APIKeyScopeRead, APIKeyScopeWrite}},
	}
	require.True(t, directoryConnectorPolicyChanged(current, updated))
	require.Equal(t, "", normalizeDirectoryTeamName(strings.Repeat("a", 101)))
	require.Equal(t, "Research Team", normalizeDirectoryTeamName("  Research\tTeam  "))

	identityOne := uuid.New()
	identityTwo := uuid.New()
	teamOne := uuid.New()
	teamTwo := uuid.New()
	grants := directoryProfileGrants(map[string]domain.DirectoryProfileGrant{
		"second": {IdentityID: identityTwo, TeamID: teamTwo},
		"first":  {IdentityID: identityOne, TeamID: teamOne},
	})
	require.Len(t, grants, 2)
	require.Equal(t, uniqueDirectoryIdentityIDs([]uuid.UUID{identityOne, identityOne, uuid.Nil, identityTwo}), []uuid.UUID{identityOne, identityTwo})
	require.Equal(t, []string{"a", "b"}, uniqueDirectoryStrings([]string{" a ", "", "a", "b"}))
}

func TestDirectoryIdentityServiceSurfacesRepositoryFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	assertFailure := func(method string, invoke func(*DirectoryIdentityService, *directoryIdentityServiceRepository, uuid.UUID) error) {
		t.Helper()
		svc, repo, connectorID := directoryFailureService(method, domain.DirectoryConnectorObserve)
		err := invoke(svc, repo, connectorID)
		require.ErrorContains(t, err, method+" failed")
	}
	assertFailure("create connector", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, _ uuid.UUID) error {
		_, _, err := svc.CreateConnector(ctx, directoryConnectorForServiceTest(uuid.New()))
		return err
	})
	assertFailure("get connector", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.GetConnector(ctx, connectorID)
		return err
	})
	assertFailure("get connector provider", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, _ uuid.UUID) error {
		_, err := svc.GetConnectorForProvider(ctx, uuid.New())
		return err
	})
	assertFailure("list connectors", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, _ uuid.UUID) error {
		_, err := svc.ListConnectors(ctx)
		return err
	})
	assertFailure("update connector", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.UpdateConnector(ctx, domain.DirectoryConnector{ID: connectorID, GroupPattern: "^(?P<team>.+?)(?P<role>Readonly|Member|Manager)$", RoleEntitlements: directoryConnectorForPolicyTest().RoleEntitlements, MaxAutoTeams: 10})
		return err
	})
	assertFailure("set credentials", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.RotateCredentials(ctx, connectorID)
		return err
	})
	assertFailure("snapshot", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.Preview(ctx, connectorID)
		return err
	})
	assertFailure("set status", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.SetConnectorStatus(ctx, connectorID, domain.DirectoryConnectorDisabled, "")
		return err
	})
	assertFailure("get oauth token", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.AuthenticateSCIM(ctx, connectorID, "opaque-token")
		return err
	})
	assertFailure("get connector oauth", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, _ uuid.UUID) error {
		_, _, err := svc.IssueOAuthToken(ctx, "client", "secret")
		return err
	})
	assertFailure("delete oauth tokens", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, _ uuid.UUID) error {
		_, _, err := svc.IssueOAuthToken(ctx, "client", "secret")
		return err
	})
	assertFailure("create oauth token", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, _ uuid.UUID) error {
		_, _, err := svc.IssueOAuthToken(ctx, "client", "secret")
		return err
	})
	assertFailure("upsert user", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.UpsertUser(ctx, connectorID, domain.DirectoryUser{UserName: "alex@example.test"})
		return err
	})
	assertFailure("get user", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.GetUser(ctx, connectorID, uuid.New())
		return err
	})
	assertFailure("list users", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.ListUsers(ctx, connectorID)
		return err
	})
	assertFailure("upsert group", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.UpsertGroup(ctx, connectorID, domain.DirectoryGroup{DisplayName: "ResearchManager"})
		return err
	})
	assertFailure("get group", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.GetGroup(ctx, connectorID, uuid.New())
		return err
	})
	assertFailure("list groups", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		_, err := svc.ListGroups(ctx, connectorID)
		return err
	})
	assertFailure("replace members", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		return svc.ReplaceGroupMembers(ctx, connectorID, uuid.New(), nil)
	})
	assertFailure("adopt team", func(svc *DirectoryIdentityService, _ *directoryIdentityServiceRepository, connectorID uuid.UUID) error {
		return svc.AdoptTeam(ctx, connectorID, uuid.New(), uuid.New())
	})

	svc, _, connectorID := directoryFailureService("activate connector", domain.DirectoryConnectorObserve)
	preview, err := svc.Preview(ctx, connectorID)
	require.NoError(t, err)
	_, err = svc.SetConnectorStatus(ctx, connectorID, domain.DirectoryConnectorActive, preview.Version)
	require.ErrorContains(t, err, "activate connector failed")

	svc, _, connectorID = directoryFailureService("apply plan", domain.DirectoryConnectorActive)
	_, err = svc.UpsertUser(ctx, connectorID, domain.DirectoryUser{UserName: "alex@example.test"})
	require.ErrorContains(t, err, "apply plan failed")
}

func directoryFailureService(method string, status domain.DirectoryConnectorStatus) (*DirectoryIdentityService, *directoryIdentityServiceRepository, uuid.UUID) {
	repo := newDirectoryIdentityServiceRepository()
	connectorID := uuid.New()
	providerID := uuid.New()
	connector := directoryConnectorForServiceTest(providerID)
	connector.ID = connectorID
	connector.Status = status
	connector.OAuthClientID = "client"
	connector.OAuthClientSecretHash, _ = cryptoutil.HashKey("secret")
	repo.connectors[connectorID] = cloneDirectoryConnector(&connector)
	repo.failureMethod = method
	return NewDirectoryIdentityService(repo, DirectoryIdentityConfig{}), repo, connectorID
}

func directoryConnectorForServiceTest(providerID uuid.UUID) domain.DirectoryConnector {
	return domain.DirectoryConnector{
		ProviderID:       providerID,
		GroupPattern:     "^(?P<team>.+?)(?P<role>Readonly|Member|Manager)$",
		RoleEntitlements: directoryConnectorForPolicyTest().RoleEntitlements,
		MaxAutoTeams:     10,
	}
}

type directoryIdentityServiceRepository struct {
	repository.DirectoryIdentityRepository
	connectors      map[uuid.UUID]*domain.DirectoryConnector
	users           map[uuid.UUID]*domain.DirectoryUser
	groups          map[uuid.UUID]*domain.DirectoryGroup
	oauthTokens     map[string]directoryOAuthToken
	now             func() time.Time
	appliedPlans    []domain.DirectoryReconcilePlan
	activationPlans []domain.DirectoryReconcilePlan
	adoptions       []directoryTeamAdoption
	failureMethod   string
}

type directoryOAuthToken struct {
	connectorID uuid.UUID
	expiresAt   time.Time
}

type directoryTeamAdoption struct {
	connectorID uuid.UUID
	groupID     uuid.UUID
	teamID      uuid.UUID
}

var _ repository.DirectoryIdentityRepository = (*directoryIdentityServiceRepository)(nil)

func newDirectoryIdentityServiceRepository() *directoryIdentityServiceRepository {
	return &directoryIdentityServiceRepository{
		connectors:  make(map[uuid.UUID]*domain.DirectoryConnector),
		users:       make(map[uuid.UUID]*domain.DirectoryUser),
		groups:      make(map[uuid.UUID]*domain.DirectoryGroup),
		oauthTokens: make(map[string]directoryOAuthToken),
		now:         time.Now,
	}
}

func (r *directoryIdentityServiceRepository) failure(method string) error {
	if r.failureMethod == method {
		return errors.New(method + " failed")
	}
	return nil
}

func (r *directoryIdentityServiceRepository) CreateDirectoryConnector(_ context.Context, connector *domain.DirectoryConnector) error {
	if err := r.failure("create connector"); err != nil {
		return err
	}
	if connector.ID == uuid.Nil {
		connector.ID = uuid.New()
	}
	r.connectors[connector.ID] = cloneDirectoryConnector(connector)
	return nil
}

func (r *directoryIdentityServiceRepository) GetDirectoryConnector(_ context.Context, id uuid.UUID) (*domain.DirectoryConnector, error) {
	if err := r.failure("get connector"); err != nil {
		return nil, err
	}
	return cloneDirectoryConnector(r.connectors[id]), nil
}

func (r *directoryIdentityServiceRepository) GetDirectoryConnectorByProviderID(_ context.Context, providerID uuid.UUID) (*domain.DirectoryConnector, error) {
	if err := r.failure("get connector provider"); err != nil {
		return nil, err
	}
	for _, connector := range r.connectors {
		if connector.ProviderID == providerID {
			return cloneDirectoryConnector(connector), nil
		}
	}
	return nil, nil
}

func (r *directoryIdentityServiceRepository) GetDirectoryConnectorByOAuthClientID(_ context.Context, clientID string) (*domain.DirectoryConnector, error) {
	if err := r.failure("get connector oauth"); err != nil {
		return nil, err
	}
	for _, connector := range r.connectors {
		if connector.OAuthClientID == clientID {
			return cloneDirectoryConnector(connector), nil
		}
	}
	return nil, nil
}

func (r *directoryIdentityServiceRepository) ListDirectoryConnectors(context.Context) ([]*domain.DirectoryConnector, error) {
	if err := r.failure("list connectors"); err != nil {
		return nil, err
	}
	items := make([]*domain.DirectoryConnector, 0, len(r.connectors))
	for _, connector := range r.connectors {
		items = append(items, cloneDirectoryConnector(connector))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID.String() < items[j].ID.String() })
	return items, nil
}

func (r *directoryIdentityServiceRepository) UpdateDirectoryConnector(_ context.Context, connector *domain.DirectoryConnector) error {
	if err := r.failure("update connector"); err != nil {
		return err
	}
	if r.connectors[connector.ID] == nil {
		return nil
	}
	r.connectors[connector.ID] = cloneDirectoryConnector(connector)
	return nil
}

func (r *directoryIdentityServiceRepository) SetDirectoryConnectorStatus(_ context.Context, id uuid.UUID, status domain.DirectoryConnectorStatus, _ *time.Time) error {
	if err := r.failure("set status"); err != nil {
		return err
	}
	if connector := r.connectors[id]; connector != nil {
		connector.Status = status
	}
	return nil
}

func (r *directoryIdentityServiceRepository) SetDirectoryCredentials(_ context.Context, id uuid.UUID, version int, bearerHash, clientID, secretHash string) error {
	if err := r.failure("set credentials"); err != nil {
		return err
	}
	connector := r.connectors[id]
	if connector == nil {
		return nil
	}
	connector.CredentialVersion = version
	connector.BearerTokenHash = bearerHash
	connector.OAuthClientID = clientID
	connector.OAuthClientSecretHash = secretHash
	return nil
}

func (r *directoryIdentityServiceRepository) CreateDirectoryOAuthToken(_ context.Context, connectorID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	if err := r.failure("create oauth token"); err != nil {
		return err
	}
	r.oauthTokens[tokenHash] = directoryOAuthToken{connectorID: connectorID, expiresAt: expiresAt}
	return nil
}

func (r *directoryIdentityServiceRepository) GetDirectoryOAuthToken(_ context.Context, connectorID uuid.UUID, tokenHash string) (bool, error) {
	if err := r.failure("get oauth token"); err != nil {
		return false, err
	}
	token, ok := r.oauthTokens[tokenHash]
	return ok && token.connectorID == connectorID && token.expiresAt.After(r.now()), nil
}

func (r *directoryIdentityServiceRepository) DeleteExpiredDirectoryOAuthTokens(_ context.Context, now time.Time) error {
	if err := r.failure("delete oauth tokens"); err != nil {
		return err
	}
	for tokenHash, token := range r.oauthTokens {
		if !token.expiresAt.After(now) {
			delete(r.oauthTokens, tokenHash)
		}
	}
	return nil
}

func (r *directoryIdentityServiceRepository) UpsertDirectoryUser(_ context.Context, user domain.DirectoryUser) (*domain.DirectoryUser, error) {
	if err := r.failure("upsert user"); err != nil {
		return nil, err
	}
	if user.ID == uuid.Nil {
		for _, existing := range r.users {
			if existing.ConnectorID == user.ConnectorID && existing.ExternalID == user.ExternalID && user.ExternalID != "" {
				user.ID = existing.ID
				user.IdentityID = existing.IdentityID
				break
			}
		}
	}
	if user.ID == uuid.Nil {
		user.ID = uuid.New()
	}
	if user.IdentityID == uuid.Nil {
		user.IdentityID = uuid.New()
	}
	r.users[user.ID] = cloneDirectoryUser(&user)
	return cloneDirectoryUser(&user), nil
}

func (r *directoryIdentityServiceRepository) GetDirectoryUser(_ context.Context, connectorID, userID uuid.UUID) (*domain.DirectoryUser, error) {
	if err := r.failure("get user"); err != nil {
		return nil, err
	}
	user := r.users[userID]
	if user == nil || user.ConnectorID != connectorID {
		return nil, nil
	}
	return cloneDirectoryUser(user), nil
}

func (r *directoryIdentityServiceRepository) ListDirectoryUsers(_ context.Context, connectorID uuid.UUID) ([]*domain.DirectoryUser, error) {
	if err := r.failure("list users"); err != nil {
		return nil, err
	}
	items := make([]*domain.DirectoryUser, 0)
	for _, user := range r.users {
		if user.ConnectorID == connectorID {
			items = append(items, cloneDirectoryUser(user))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UserName < items[j].UserName })
	return items, nil
}

func (r *directoryIdentityServiceRepository) UpsertDirectoryGroup(_ context.Context, group domain.DirectoryGroup) (*domain.DirectoryGroup, error) {
	if err := r.failure("upsert group"); err != nil {
		return nil, err
	}
	if group.ID == uuid.Nil {
		for _, existing := range r.groups {
			if existing.ConnectorID == group.ConnectorID && existing.ExternalID == group.ExternalID && group.ExternalID != "" {
				group.ID = existing.ID
				group.Members = append([]domain.DirectoryUser(nil), existing.Members...)
				break
			}
		}
	}
	if group.ID == uuid.Nil {
		group.ID = uuid.New()
	}
	r.groups[group.ID] = cloneDirectoryGroup(&group)
	return cloneDirectoryGroup(&group), nil
}

func (r *directoryIdentityServiceRepository) UpsertDirectoryGroupWithMembers(ctx context.Context, group domain.DirectoryGroup, memberIDs []uuid.UUID) (*domain.DirectoryGroup, error) {
	stored, err := r.UpsertDirectoryGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	if err := r.ReplaceDirectoryGroupMembers(ctx, stored.ConnectorID, stored.ID, memberIDs); err != nil {
		return nil, err
	}
	return r.GetDirectoryGroup(ctx, stored.ConnectorID, stored.ID)
}

func (r *directoryIdentityServiceRepository) GetDirectoryGroup(_ context.Context, connectorID, groupID uuid.UUID) (*domain.DirectoryGroup, error) {
	if err := r.failure("get group"); err != nil {
		return nil, err
	}
	group := r.groups[groupID]
	if group == nil || group.ConnectorID != connectorID {
		return nil, nil
	}
	return cloneDirectoryGroup(group), nil
}

func (r *directoryIdentityServiceRepository) ListDirectoryGroups(_ context.Context, connectorID uuid.UUID) ([]*domain.DirectoryGroup, error) {
	if err := r.failure("list groups"); err != nil {
		return nil, err
	}
	items := make([]*domain.DirectoryGroup, 0)
	for _, group := range r.groups {
		if group.ConnectorID == connectorID {
			items = append(items, cloneDirectoryGroup(group))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DisplayName < items[j].DisplayName })
	return items, nil
}

func (r *directoryIdentityServiceRepository) ReplaceDirectoryGroupMembers(_ context.Context, connectorID, groupID uuid.UUID, memberIDs []uuid.UUID) error {
	if err := r.failure("replace members"); err != nil {
		return err
	}
	group := r.groups[groupID]
	if group == nil || group.ConnectorID != connectorID {
		return nil
	}
	group.Members = make([]domain.DirectoryUser, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		if user := r.users[memberID]; user != nil && user.ConnectorID == connectorID {
			group.Members = append(group.Members, *cloneDirectoryUser(user))
		}
	}
	return nil
}

func (r *directoryIdentityServiceRepository) DirectoryConnectorSnapshot(_ context.Context, connectorID uuid.UUID) (*domain.DirectoryConnectorSnapshot, error) {
	if err := r.failure("snapshot"); err != nil {
		return nil, err
	}
	connector := r.connectors[connectorID]
	if connector == nil {
		return nil, nil
	}
	snapshot := &domain.DirectoryConnectorSnapshot{Connector: *cloneDirectoryConnector(connector)}
	for _, user := range r.users {
		if user.ConnectorID == connectorID {
			snapshot.Users = append(snapshot.Users, *cloneDirectoryUser(user))
		}
	}
	for _, group := range r.groups {
		if group.ConnectorID == connectorID {
			snapshot.Groups = append(snapshot.Groups, *cloneDirectoryGroup(group))
		}
	}
	return snapshot, nil
}

func (r *directoryIdentityServiceRepository) ApplyDirectoryReconcilePlan(_ context.Context, plan domain.DirectoryReconcilePlan) error {
	if err := r.failure("apply plan"); err != nil {
		return err
	}
	r.appliedPlans = append(r.appliedPlans, plan)
	return nil
}

func (r *directoryIdentityServiceRepository) ActivateDirectoryConnector(_ context.Context, plan domain.DirectoryReconcilePlan) error {
	if err := r.failure("activate connector"); err != nil {
		return err
	}
	connector := r.connectors[plan.ConnectorID]
	if connector != nil {
		connector.Status = domain.DirectoryConnectorActive
	}
	r.activationPlans = append(r.activationPlans, plan)
	return nil
}

func (r *directoryIdentityServiceRepository) AdoptDirectoryTeam(_ context.Context, connectorID, groupID, teamID uuid.UUID) error {
	if err := r.failure("adopt team"); err != nil {
		return err
	}
	r.adoptions = append(r.adoptions, directoryTeamAdoption{connectorID: connectorID, groupID: groupID, teamID: teamID})
	return nil
}

func cloneDirectoryConnector(connector *domain.DirectoryConnector) *domain.DirectoryConnector {
	if connector == nil {
		return nil
	}
	copy := *connector
	copy.RoleEntitlements = make(map[string]domain.DirectoryRoleEntitlement, len(connector.RoleEntitlements))
	for key, entitlement := range connector.RoleEntitlements {
		copy.RoleEntitlements[key] = domain.DirectoryRoleEntitlement{Role: entitlement.Role, Scopes: append([]string(nil), entitlement.Scopes...)}
	}
	return &copy
}

func cloneDirectoryUser(user *domain.DirectoryUser) *domain.DirectoryUser {
	if user == nil {
		return nil
	}
	copy := *user
	return &copy
}

func cloneDirectoryGroup(group *domain.DirectoryGroup) *domain.DirectoryGroup {
	if group == nil {
		return nil
	}
	copy := *group
	copy.Members = append([]domain.DirectoryUser(nil), group.Members...)
	return &copy
}
