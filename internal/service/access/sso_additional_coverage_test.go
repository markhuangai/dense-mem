package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSSOServiceAdditionalBoundaryBranches(t *testing.T) {
	ctx := context.Background()
	service := NewSSOService(nil, SSOConfig{})
	validProvider := domain.SSOProvider{ID: uuid.New(), Name: "Provider", Kind: domain.SSOProviderKindGenericOIDC, IssuerURL: "https://issuer.example", ClientID: "client", Enabled: true}
	if err := service.validateProtectedResourceProviderSet(ctx, &validProvider); err == nil {
		t.Fatal("nil provider repository was accepted")
	}
	if _, err := service.CreateProvider(ctx, validProvider); err == nil {
		t.Fatal("provider creation without repository was accepted")
	}
	if runtime, err := (*SSOService)(nil).runtimeConfig(ctx); err != nil || runtime.SessionTTL <= 0 {
		t.Fatalf("nil runtime config = %+v, %v", runtime, err)
	}
	if _, err := NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{RuntimeConfig: ssoRuntimeConfigStub{err: errors.New("runtime failed")}}).PublicBaseURL(ctx); err == nil {
		t.Fatal("runtime config error was ignored")
	}
	if NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{RuntimeConfig: ssoRuntimeConfigStub{err: errors.New("runtime failed")}}).CookieSecure(ctx) {
		t.Fatal("runtime config failure changed default cookie security")
	}

	service = NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{})
	ctxWithoutDeadline, cancel := service.providerContext(context.Background(), SSORuntimeConfig{})
	cancel()
	if ctxWithoutDeadline == nil {
		t.Fatal("nil provider context was returned")
	}
	ctxNoTimeout, cancel := service.providerContext(context.Background(), SSORuntimeConfig{HTTPTimeout: 0})
	cancel()
	if _, ok := ctxNoTimeout.Deadline(); ok {
		t.Fatal("zero provider timeout installed a deadline")
	}
	parent, parentCancel := context.WithTimeout(context.Background(), time.Hour)
	defer parentCancel()
	ctxWithDeadline, cancel := service.providerContext(parent, SSORuntimeConfig{HTTPTimeout: time.Second})
	cancel()
	if _, ok := ctxWithDeadline.Deadline(); !ok {
		t.Fatal("parent deadline was not preserved")
	}

	if !ssoRuntimeReadyForPublicLogin(SSORuntimeConfig{PublicBaseURL: "https://portal.example"}) || ssoRuntimeReadyForPublicLogin(SSORuntimeConfig{PublicBaseURL: "not a URL"}) {
		t.Fatal("public login runtime readiness was incorrect")
	}
	missingSecret := domain.SSOProvider{Name: "Provider", Kind: domain.SSOProviderKindGenericOIDC, IssuerURL: "https://issuer.example", ClientID: "client", ClientSecretEnv: "DENSE_MEM_TEST_MISSING_SECRET"}
	if ssoProviderReadyForPublicLogin(&missingSecret) {
		t.Fatal("provider with missing client secret was accepted")
	}
	if ssoProviderReadyForPublicLogin(nil) || ssoProviderReadyForPublicLogin(&domain.SSOProvider{}) {
		t.Fatal("invalid public login provider was accepted")
	}

	teamID, identityID, providerID := uuid.New(), uuid.New(), uuid.New()
	team := &domain.SSOTeamMembership{Team: domain.Team{ID: teamID}, Membership: domain.Membership{ID: uuid.New(), TeamID: teamID, OwnerID: uuid.New(), ActorIdentityID: identityID, SSOProviderID: &providerID, SSOSubject: "subject", Status: "active", Grants: []string{CredentialScopeRead}}}
	repo := &ssoRepositoryStub{t: t, providers: map[uuid.UUID]*domain.SSOProvider{providerID: {ID: providerID, Name: "Provider", Kind: domain.SSOProviderKindGenericOIDC, IssuerURL: "https://issuer.example", ClientID: "client", Enabled: true}}, teamProfiles: []*domain.SSOTeamMembership{nil, team}}
	service = NewSSOService(repo, SSOConfig{})
	session := &domain.SSOSession{OwnerID: team.Membership.OwnerID, TeamID: teamID, MembershipID: team.Membership.ID, IdentityID: identityID}
	if teams, selected, err := service.validCurrentSessionTeams(ctx, session, repo.teamProfiles); err != nil || len(teams) != 0 || selected != nil {
		t.Fatalf("valid current teams = %#v, %#v, %v", teams, selected, err)
	}
	repo.getSSOProfileErr = errors.New("membership lookup failed")
	if _, err := service.AuthenticateSession(ctx, "missing", "", false); err == nil {
		t.Fatal("missing session was accepted")
	}
	repo.getSSOProfileErr = nil
	repo.teamProfiles = []*domain.SSOTeamMembership{team}
	repo.listTeamProfilesErr = errors.New("teams failed")
	if _, err := service.SwitchSessionTeam(ctx, "missing", teamID); err == nil {
		t.Fatal("missing switch session was accepted")
	}
	repo.listTeamProfilesErr = nil

	if _, err := service.directoryEntitledTeams(ctx, providerID, identityID); err != nil {
		t.Fatalf("directory entitled teams: %v", err)
	}
	repo.directoryProfileEntitledErr = errors.New("directory entitlement failed")
	if _, err := service.directoryEntitledTeams(ctx, providerID, identityID); err == nil {
		t.Fatal("directory entitlement error was ignored")
	}
	repo.directoryProfileEntitledErr = nil
	repo.listTeamProfilesErr = errors.New("directory list failed")
	if _, err := service.directoryEntitledTeams(ctx, providerID, identityID); err == nil {
		t.Fatal("directory list error was ignored")
	}

	if err := service.storeEntitlementCacheWithTTL(ctx, time.Minute, providerID, "subject", []string{"g", "g"}, "active", "message"); err != nil {
		t.Fatalf("cache store: %v", err)
	}
	repo.setCacheErr = errors.New("cache failed")
	if err := service.storeEntitlementCacheWithTTL(ctx, time.Minute, providerID, "subject", nil, "active", "message"); err == nil {
		t.Fatal("cache error was ignored")
	}
}
