package access

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSSOEntitlementAndDirectoryTeamFilters(t *testing.T) {
	providerID := uuid.New()
	identityID := uuid.New()
	ownerID := uuid.New()
	allowedTeam := uuid.New()
	teamProfiles := []*domain.SSOTeamMembership{
		{Team: domain.Team{ID: allowedTeam}, Membership: domain.Membership{OwnerID: ownerID, TeamID: allowedTeam, ActorIdentityID: identityID}},
		{Team: domain.Team{ID: uuid.New()}, Membership: domain.Membership{OwnerID: uuid.New(), ActorIdentityID: identityID}},
	}
	repo := &ssoRepositoryStub{t: t, teamProfiles: teamProfiles, directoryProfileEntitled: map[uuid.UUID]bool{identityID: true}}
	svc := NewSSOService(repo, SSOConfig{})
	teams, err := svc.currentEntitledTeams(context.Background(), identityID, map[uuid.UUID]struct{}{ownerID: {}})
	require.NoError(t, err)
	require.Len(t, teams, 1)
	repo.teamProfiles = []*domain.SSOTeamMembership{teamProfiles[0]}
	teams, err = svc.directoryEntitledTeams(context.Background(), providerID, identityID)
	require.NoError(t, err)
	require.Len(t, teams, 1)
	repo.listTeamProfilesErr = errors.New("list failed")
	if _, err := svc.currentEntitledTeams(context.Background(), identityID, nil); err == nil {
		t.Fatal("team listing error was swallowed")
	}
	if _, err := svc.directoryEntitledTeams(context.Background(), providerID, identityID); err == nil {
		t.Fatal("directory team listing error was swallowed")
	}
}

func TestSSOEntitlementMappingAndRuntimeHelpers(t *testing.T) {
	providerID := uuid.New()
	teamID := uuid.New()
	svc := NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{})
	if _, err := svc.entitlementsFromMappings(providerID, "subject", nil); !errors.Is(err, ErrSSOAccessDenied) {
		t.Fatalf("empty mappings error = %v", err)
	}
	if _, err := svc.entitlementsFromMappings(providerID, "subject", []*domain.SSOGroupMapping{{ProviderID: uuid.New(), TeamID: teamID, Enabled: true}}); !errors.Is(err, ErrSSOAccessDenied) {
		t.Fatalf("mismatched mappings error = %v", err)
	}
	items, err := svc.entitlementsFromMappings(providerID, "subject", []*domain.SSOGroupMapping{
		{ProviderID: providerID, TeamID: teamID, TeamName: "Team", GroupID: "g1", Enabled: true, Scopes: []string{CredentialScopeRead}},
		{ProviderID: providerID, TeamID: teamID, TeamName: "Team", GroupID: "g2", Enabled: true, Role: CredentialRoleManager, Scopes: []string{CredentialScopeFeedbackRead}},
	})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, CredentialRoleManager, items[0].Role)
	require.Contains(t, items[0].GroupID, "g1")
	ctx, cancel := svc.providerContext(context.Background(), SSORuntimeConfig{HTTPTimeout: time.Millisecond})
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("provider context did not install a deadline")
	}
	parent, parentCancel := context.WithCancel(context.Background())
	parentCancel()
	ctx, cancel = svc.providerContext(parent, SSORuntimeConfig{HTTPTimeout: time.Second})
	cancel()
	if ctx.Err() == nil {
		t.Fatal("provider context did not preserve canceled parent")
	}
	runtime := normalizeSSORuntimeConfig(SSORuntimeConfig{PublicBaseURL: " https://portal.example/ "})
	if runtime.PublicBaseURL != "https://portal.example" || runtime.SessionTTL <= 0 || runtime.HTTPTimeout <= 0 {
		t.Fatalf("normalized runtime = %+v", runtime)
	}
	if boundedSSOHTTPClient(nil, 0).Timeout != DefaultSSOHTTPTimeout {
		t.Fatal("default SSO HTTP timeout was not applied")
	}
	custom := boundedSSOHTTPClient(&http.Client{Timeout: 0}, time.Second)
	if custom.Timeout != time.Second {
		t.Fatal("custom SSO HTTP timeout was not applied")
	}
}

func TestSSOSessionAndGroupResolutionFailureBranches(t *testing.T) {
	svc := NewSSOService(nil, SSOConfig{})
	if _, err := svc.sessionFromToken(context.Background(), "token"); !errors.Is(err, ErrSSOSessionInvalid) {
		t.Fatalf("nil repo session error = %v", err)
	}
	if _, err := (*SSOService)(nil).sessionFromToken(context.Background(), "token"); !errors.Is(err, ErrSSOSessionInvalid) {
		t.Fatalf("nil service session error = %v", err)
	}
	repo := &ssoRepositoryStub{t: t}
	svc = NewSSOService(repo, SSOConfig{Now: func() time.Time { return time.Now().UTC() }})
	if _, err := svc.sessionFromToken(context.Background(), ""); !errors.Is(err, ErrSSOSessionInvalid) {
		t.Fatalf("empty token session error = %v", err)
	}
	resolver := &ssoGroupResolverStub{groups: []string{"g"}}
	svc.groupResolver = resolver
	groups, err := svc.resolveGroups(context.Background(), SSORuntimeConfig{}, domain.SSOProvider{}, "subject", "token")
	require.NoError(t, err)
	require.Equal(t, []string{"g"}, groups)
}

func TestSSOServiceManagementAndSessionFailureBranches(t *testing.T) {
	ctx := context.Background()
	repo := &ssoRepositoryStub{t: t, providers: map[uuid.UUID]*domain.SSOProvider{}}
	svc := NewSSOService(repo, SSOConfig{})
	if _, err := svc.CreateProvider(ctx, domain.SSOProvider{}); err == nil {
		t.Fatal("empty provider was accepted")
	}
	if err := svc.DeleteProvider(ctx, uuid.Nil); err == nil {
		t.Fatal("empty provider ID was accepted")
	}
	repo.deleteProviderErr = errors.New("delete provider")
	if err := svc.DeleteProvider(ctx, uuid.New()); err == nil {
		t.Fatal("provider delete failure was swallowed")
	}
	if _, err := svc.ListMappings(ctx, uuid.Nil); err == nil {
		t.Fatal("empty mapping provider ID was accepted")
	}
	repo.listMappingsErr = errors.New("list mappings")
	if _, err := svc.ListMappings(ctx, uuid.New()); err == nil {
		t.Fatal("mapping list failure was swallowed")
	}
	if _, err := svc.CreateMapping(ctx, domain.SSOGroupMapping{}); err == nil {
		t.Fatal("empty mapping was accepted")
	}
	validMapping := domain.SSOGroupMapping{ProviderID: uuid.New(), TeamID: uuid.New(), GroupID: "group", Enabled: true}
	repo.createMappingErr = errors.New("create mapping")
	if _, err := svc.CreateMapping(ctx, validMapping); err == nil {
		t.Fatal("mapping create failure was swallowed")
	}
	repo.createMappingErr = nil
	repo.listMappingsErr = errors.New("reload mapping")
	if _, err := svc.CreateMapping(ctx, validMapping); err == nil {
		t.Fatal("mapping reload failure was swallowed")
	}
	if _, err := svc.UpdateMapping(ctx, validMapping); err == nil {
		t.Fatal("mapping without ID was accepted")
	}
	validMapping.ID = uuid.New()
	repo.updateMappingErr = errors.New("update mapping")
	if _, err := svc.UpdateMapping(ctx, validMapping); err == nil {
		t.Fatal("mapping update failure was swallowed")
	}
	if err := svc.DeleteMapping(ctx, uuid.Nil, uuid.New()); err == nil {
		t.Fatal("mapping delete without provider ID was accepted")
	}
	if err := svc.DeleteMapping(ctx, uuid.New(), uuid.Nil); err == nil {
		t.Fatal("mapping delete without mapping ID was accepted")
	}
	repo.deleteMappingErr = errors.New("delete mapping")
	if err := svc.DeleteMapping(ctx, uuid.New(), uuid.New()); err == nil {
		t.Fatal("mapping delete failure was swallowed")
	}

	if _, err := (*SSOService)(nil).BeginLogin(ctx, uuid.New(), "", ""); !errors.Is(err, ErrSSOProviderDisabled) {
		t.Fatalf("nil BeginLogin error = %v", err)
	}
	if _, err := NewSSOService(nil, SSOConfig{}).BeginLogin(ctx, uuid.New(), "", ""); !errors.Is(err, ErrSSOProviderDisabled) {
		t.Fatalf("missing BeginLogin repo error = %v", err)
	}
	runtimeErr := errors.New("runtime config")
	if _, err := NewSSOService(repo, SSOConfig{RuntimeConfig: ssoRuntimeConfigStub{err: runtimeErr}}).BeginLogin(ctx, uuid.New(), "", ""); !errors.Is(err, runtimeErr) {
		t.Fatalf("BeginLogin runtime error = %v", err)
	}
	repo.getProviderErr = errors.New("provider lookup")
	if _, err := svc.BeginLogin(ctx, uuid.New(), "", ""); err == nil {
		t.Fatal("provider lookup failure was swallowed")
	}
	repo.getProviderErr = nil
	if _, err := svc.BeginLogin(ctx, uuid.New(), "", ""); !errors.Is(err, ErrSSOProviderDisabled) {
		t.Fatalf("missing provider error = %v", err)
	}

	if _, err := (*SSOService)(nil).CompleteLogin(ctx, "state", "code", ""); !errors.Is(err, ErrSSOProviderDisabled) {
		t.Fatalf("nil CompleteLogin error = %v", err)
	}
	if _, err := NewSSOService(nil, SSOConfig{}).CompleteLogin(ctx, "state", "code", ""); !errors.Is(err, ErrSSOProviderDisabled) {
		t.Fatalf("missing CompleteLogin repo error = %v", err)
	}
	if _, err := svc.CompleteLogin(ctx, "", "", ""); !errors.Is(err, ErrSSOSessionInvalid) {
		t.Fatalf("missing CompleteLogin state error = %v", err)
	}

	identityID, ownerID, teamID := uuid.New(), uuid.New(), uuid.New()
	sessionToken := "session-token"
	session := &domain.SSOSession{SessionHash: HashSSOToken(sessionToken), IdentityID: identityID, OwnerID: ownerID, TeamID: teamID, MembershipID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour), CSRFHash: HashSSOToken("csrf")}
	repo.sessions = map[string]*domain.SSOSession{session.SessionHash: session}
	if _, err := svc.AuthenticateSession(ctx, sessionToken, "wrong", true); !errors.Is(err, ErrSSOCSRFInvalid) {
		t.Fatalf("invalid CSRF error = %v", err)
	}
	if _, err := svc.AuthenticateSession(ctx, sessionToken, "csrf", false); !errors.Is(err, ErrSSOSessionInvalid) {
		t.Fatalf("missing membership error = %v", err)
	}
	if _, err := svc.CurrentSession(ctx, sessionToken); !errors.Is(err, ErrSSOSessionInvalid) {
		t.Fatalf("missing identity error = %v", err)
	}
	repo.getIdentityErr = errors.New("identity lookup")
	if _, err := svc.CurrentSession(ctx, sessionToken); err == nil {
		t.Fatal("identity lookup failure was swallowed")
	}
	repo.getIdentityErr = nil
	repo.listTeamProfilesErr = errors.New("team list")
	if _, err := svc.CurrentSession(ctx, sessionToken); err == nil {
		t.Fatal("current team list failure was swallowed")
	}
	repo.listTeamProfilesErr = nil
	if _, err := svc.SwitchSessionTeam(ctx, sessionToken, uuid.New()); !errors.Is(err, ErrSSOAccessDenied) {
		t.Fatalf("switch missing team error = %v", err)
	}
	repo.listTeamProfilesErr = errors.New("switch list")
	if _, err := svc.SwitchSessionTeam(ctx, sessionToken, uuid.New()); err == nil {
		t.Fatal("switch team list failure was swallowed")
	}
	if err := svc.Logout(ctx, ""); err != nil {
		t.Fatalf("empty logout error = %v", err)
	}
	repo.listTeamProfilesErr = nil
	repo.deleteSessionErr = errors.New("logout")
	if err := svc.Logout(ctx, sessionToken); err == nil {
		t.Fatal("logout failure was swallowed")
	}
}

func TestSSOCurrentSessionMembershipReconciliationBranches(t *testing.T) {
	ctx := context.Background()
	providerID, identityID, teamID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	identity := domain.SSOIdentity{ID: identityID, ProviderID: providerID, Subject: "subject", Email: "person@example.com"}
	repo := &ssoRepositoryStub{t: t, providers: map[uuid.UUID]*domain.SSOProvider{providerID: {ID: providerID, Enabled: true}}, cache: &domain.SSOEntitlementCache{ProviderID: providerID, Subject: "subject", Groups: []string{"group"}, Status: "active", ExpiresAt: now.Add(time.Hour)}, mappings: []*domain.SSOGroupMapping{{ProviderID: providerID, TeamID: teamID, GroupID: "group", Enabled: true}}}
	svc := NewSSOService(repo, SSOConfig{Now: func() time.Time { return now }})
	if teams, selected, err := svc.validCurrentSessionTeams(ctx, &domain.SSOSession{OwnerID: uuid.New()}, []*domain.SSOTeamMembership{nil, &domain.SSOTeamMembership{}}); err != nil || len(teams) != 0 || selected != nil {
		t.Fatalf("invalid current teams = %#v, %#v, %v", teams, selected, err)
	}
	if changed, err := svc.reconcileCurrentSessionMemberships(ctx, identity, nil); err != nil || !changed {
		t.Fatalf("membership reconciliation = %v, %v", changed, err)
	}
	if len(repo.teamProfiles) != 1 {
		t.Fatalf("reconciled team profiles = %d, want 1", len(repo.teamProfiles))
	}
	repo.directoryAuthorityActive = true
	if changed, err := svc.reconcileCurrentSessionMemberships(ctx, identity, repo.teamProfiles); err != nil || changed {
		t.Fatalf("directory reconciliation = %v, %v", changed, err)
	}
	repo.directoryAuthorityActive = false
	repo.cache = nil
	if changed, err := svc.reconcileCurrentSessionMemberships(ctx, identity, repo.teamProfiles); err != nil || changed {
		t.Fatalf("empty-cache reconciliation = %v, %v", changed, err)
	}
	repo.cache = &domain.SSOEntitlementCache{ProviderID: providerID, Subject: "subject", Groups: []string{"group"}, Status: "active", ExpiresAt: now.Add(time.Hour)}
	repo.mappingsForGroupsErr = errors.New("mapping lookup")
	if _, err := svc.reconcileCurrentSessionMemberships(ctx, identity, repo.teamProfiles); err == nil {
		t.Fatal("mapping lookup failure was swallowed")
	}
}
