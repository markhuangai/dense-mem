package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

func TestUserPortalQueryAndPrincipalHelpersCoverBounds(t *testing.T) {
	for path, want := range map[string]int{"/": 0, "/?limit=%204%20": 4} {
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, path, nil), httptest.NewRecorder())
		got, err := userPortalOptionalIntQuery(c, "limit")
		if err != nil || got != want {
			t.Fatalf("optional int %q = %d, %v", path, got, err)
		}
	}
	for _, raw := range []string{"-1", "bad"} {
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/?limit="+raw, nil), httptest.NewRecorder())
		if _, err := userPortalOptionalIntQuery(c, "limit"); err == nil {
			t.Fatalf("invalid optional int %q was accepted", raw)
		}
	}
	if userPortalGraphTypes("") != nil || strings.Join(userPortalGraphTypes("entity, value\tclaim"), ",") != "entity,value,claim" {
		t.Fatal("graph type parsing was incorrect")
	}
	teamID := uuid.New()
	ownerID := uuid.New()
	credentialID := uuid.New()
	principal := userPortalPrincipal(teamID, credentialID, "", []string{"read"}, "api_key")
	principal.OwnerID = ownerID
	if got := userPortalMembershipFromPrincipal(principal); got.Role != accessservice.CredentialRoleMember || got.TeamID != teamID {
		t.Fatalf("principal membership = %+v", got)
	}
	if userPortalPrincipalCredentialID(nil) != nil || userPortalPrincipalProfileID(nil) != nil {
		t.Fatal("nil principal IDs were materialized")
	}
	if userPortalPrincipalCredentialID(principal) == nil || userPortalPrincipalProfileID(principal) == nil {
		t.Fatal("principal IDs were not materialized")
	}
	if !userPortalHasGrant([]string{"read", "write"}, "write") || userPortalHasGrant(nil, "write") {
		t.Fatal("grant check was incorrect")
	}
}

func TestUserPortalSessionBoundaryHelpers(t *testing.T) {
	teamID := uuid.New()
	membership := toUserPortalMembership(domain.Membership{TeamID: teamID, Name: "Member"})
	if membership.Role != accessservice.CredentialRoleMember {
		t.Fatalf("membership default role = %q", membership.Role)
	}
	if got := nextSSOOwnedCredentialName("SSO user", []*domain.Credential{{Name: "SSO user"}, {Name: "SSO user (2)"}}); got != "SSO user (3)" {
		t.Fatalf("next credential name = %q", got)
	}
}

func TestUserPortalCSRFAndRotateBodyHelpers(t *testing.T) {
	valid := httptest.NewRequest(http.MethodPost, "/", nil)
	valid.Header.Set(accessservice.SSOCSRFHeaderName, "csrf")
	valid.AddCookie(&http.Cookie{Name: accessservice.SSOCSRFCookieName, Value: "csrf"})
	if err := validateSSOLogoutCSRF(echo.New().NewContext(valid, httptest.NewRecorder())); err != nil {
		t.Fatalf("valid CSRF rejected: %v", err)
	}
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/", nil),
		func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Header.Set(accessservice.SSOCSRFHeaderName, "wrong")
			r.AddCookie(&http.Cookie{Name: accessservice.SSOCSRFCookieName, Value: "csrf"})
			return r
		}(),
	} {
		if err := validateSSOLogoutCSRF(echo.New().NewContext(req, httptest.NewRecorder())); err == nil {
			t.Fatal("invalid CSRF was accepted")
		}
	}
	for body, wantErr := range map[string]bool{"": false, "{}": false, `{ "name": "editable" }`: true, "{": true} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if err := rejectEditableRotateBody(echo.New().NewContext(req, httptest.NewRecorder())); (err != nil) != wantErr {
			t.Fatalf("rotate body %q error = %v, wantErr=%t", body, err, wantErr)
		}
	}
	h := &userPortalHandler{}
	if _, err := h.ssoCallbackURL(context.Background()); err == nil {
		t.Fatal("missing SSO service was accepted")
	}
}

func TestUserPortalTeamConversionUsesRuntimeConfig(t *testing.T) {
	teamID := uuid.New()
	h := &userPortalHandler{appConfig: &controlAppConfigSvc{}}
	converted, err := h.toUserPortalTeam(context.Background(), &domain.Team{ID: teamID, Name: "Team"})
	require.NoError(t, err)
	require.Equal(t, teamID, converted.ID)
	option, err := h.toUserPortalTeamOption(context.Background(), domain.SSOTeamMembership{Team: domain.Team{ID: teamID, Name: "Team"}})
	require.NoError(t, err)
	require.Equal(t, teamID, option.Team.ID)
}
