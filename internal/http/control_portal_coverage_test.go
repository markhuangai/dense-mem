package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

func controlCoverageContext(path string) echo.Context {
	return echo.New().NewContext(httptest.NewRequest(http.MethodGet, path, nil), httptest.NewRecorder())
}

func TestControlPortalFilterAndPaginationBranches(t *testing.T) {
	teamID := uuid.New()
	c := controlCoverageContext("/?window_minutes=15&team_id=" + teamID.String())
	if filter, err := controlMetricsFilter(c); err != nil || filter.TeamID == nil || *filter.TeamID != teamID {
		t.Fatalf("metrics filter = %+v, %v", filter, err)
	}
	c = controlCoverageContext("/?scope=profile&team_id=" + teamID.String() + "&profile_id=" + uuid.NewString() + "&window=1h")
	if filter, err := controlTelemetryFilter(c); err != nil || filter.Scope != "profile" || filter.Audience != "operator" {
		t.Fatalf("telemetry filter = %+v, %v", filter, err)
	}
	c = controlCoverageContext("/?limit=12&offset=4&severity=warn&sort=severity&direction=desc&team_id=" + teamID.String() + "&event=event")
	if filter, err := controlOperationLogsFilter(c); err != nil || filter.Limit != 12 || filter.Offset != 4 || filter.Severity != "WARN" {
		t.Fatalf("operation filter = %+v, %v", filter, err)
	}
	c = controlCoverageContext("/?quality=high&include_pending=true&missing_context=false&irrelevant=true&from=2026-01-01T00:00:00Z&to=2026-01-02T00:00:00Z")
	if filter, err := controlRecallFeedbackEventsFilter(c); err != nil || filter.Quality != "high" || !filter.IncludePending {
		t.Fatalf("feedback filter = %+v, %v", filter, err)
	}
	if got, err := controlDreamLimit("10"); err != nil || got != 10 {
		t.Fatalf("dream limit = %d, %v", got, err)
	}
	if _, err := controlDreamLimit("0"); err == nil {
		t.Fatal("invalid dream limit was accepted")
	}
	if _, err := controlDreamListOptions(controlCoverageContext("/?status=invalid")); err == nil {
		t.Fatal("invalid dream status was accepted")
	}
	if got, _ := optionalControlBool("true", "flag"); got == nil || !*got {
		t.Fatal("optional boolean was not parsed")
	}
	if _, err := optionalControlBool("bad", "flag"); err == nil {
		t.Fatal("invalid optional boolean was accepted")
	}
	if got, _ := optionalControlTime("2026-01-01T00:00:00Z", "from"); got == nil || got.Equal(time.Time{}) {
		t.Fatal("optional time was not parsed")
	}
	if _, err := optionalControlTime("bad", "from"); err == nil {
		t.Fatal("invalid optional time was accepted")
	}
	if limit, offset := controlPagination(controlCoverageContext("/?limit=999&offset=-1")); limit != 100 || offset != 0 {
		t.Fatalf("pagination = %d/%d", limit, offset)
	}
	if !parseControlBool(" YES ") || parseControlBool("no") || boolCount(true, false, true) != 2 {
		t.Fatal("control boolean helpers were incorrect")
	}
}

func TestControlPortalConstructorAndHandlerDependencyFailures(t *testing.T) {
	if _, err := NewControlPortalServer(nil, nil, nil, nil); err == nil {
		t.Fatal("nil control config was accepted")
	}
	cfg := &controlCoverageConfig{token: "token"}
	if _, err := NewControlPortalServerWithMetrics(cfg, nil, nil, nil, HealthConfig{}, nil); err != nil {
		t.Fatalf("minimal control portal failed: %v", err)
	}
	if _, err := newControlPortalServerWithMetricsAndTelemetry(cfg, nil, nil, nil, ControlPortalTelemetry{ScrapeHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}, HealthConfig{}, nil); err == nil {
		t.Fatal("telemetry without scrape token was accepted")
	}

	h := &controlPortalHandler{}
	for name, call := range map[string]func(echo.Context) error{
		"metrics":           h.getMetrics,
		"telemetry":         h.getTelemetry,
		"security settings": h.getSecuritySettings,
		"update security":   h.updateSecuritySettings,
		"security bans":     h.listSecurityBans,
		"create ban":        h.createSecurityBan,
		"delete ban":        h.deleteSecurityBan,
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(controlCoverageContext("/")); err == nil {
				t.Fatal("missing handler dependency was accepted")
			}
		})
	}
	invalidTeam := controlCoverageContext("/teams/not-a-uuid")
	invalidTeam.SetParamNames("teamId")
	invalidTeam.SetParamValues("not-a-uuid")
	for name, call := range map[string]func(echo.Context) error{
		"update team":       h.updateTeam,
		"delete team":       h.deleteTeam,
		"list credentials":  h.listCredentials,
		"create credential": h.createCredential,
		"update credential": h.updateCredential,
		"rotate credential": h.rotateCredential,
		"delete credential": h.deleteCredential,
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(invalidTeam); err == nil {
				t.Fatal("invalid team ID was accepted")
			}
		})
	}
	malformed := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("{")), httptest.NewRecorder())
	if err := h.createTeam(malformed); err == nil {
		t.Fatal("malformed team body was accepted")
	}
}

type controlCoverageConfig struct{ token string }

func (*controlCoverageConfig) GetHTTPMaxBodyBytes() int        { return 1 << 20 }
func (*controlCoverageConfig) GetRateLimitPerMinute() int      { return 100 }
func (c *controlCoverageConfig) GetControlPortalToken() string { return c.token }
func (*controlCoverageConfig) GetAIVerifierModel() string      { return "verifier" }
func (*controlCoverageConfig) GetAIEmbeddingModel() string     { return "embedding" }
