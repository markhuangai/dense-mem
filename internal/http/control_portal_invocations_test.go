package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

type invocationReaderStub struct {
	page         *rememberapp.RememberInvocationDiagnosticPage
	detail       *rememberapp.RememberInvocationDiagnosticDetail
	filter       rememberapp.RememberInvocationDiagnosticFilter
	teamID       string
	invocationID string
	listErr      error
	detailErr    error
}

func (s *invocationReaderStub) ListRememberInvocationDiagnostics(_ context.Context, filter rememberapp.RememberInvocationDiagnosticFilter) (*rememberapp.RememberInvocationDiagnosticPage, error) {
	s.filter = filter
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.page != nil {
		return s.page, nil
	}
	return &rememberapp.RememberInvocationDiagnosticPage{}, nil
}
func (s *invocationReaderStub) GetRememberInvocationDiagnostic(_ context.Context, teamID, invocationID string) (*rememberapp.RememberInvocationDiagnosticDetail, error) {
	s.teamID, s.invocationID = teamID, invocationID
	return s.detail, s.detailErr
}

func TestControlPortalRememberInvocationRoutes(t *testing.T) {
	teamID, profileID, invocationID, attemptID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	reader := &invocationReaderStub{
		page: &rememberapp.RememberInvocationDiagnosticPage{
			Items: []rememberapp.RememberInvocationDiagnosticSummary{{
				TeamID: teamID.String(), OwnerProfileID: profileID.String(), InvocationID: invocationID.String(),
				CanonicalAttemptID: attemptID.String(), Classification: "replay", Outcome: "replayed", CreatedAt: time.Now().UTC(),
			}},
			Total: 1,
		},
		detail: &rememberapp.RememberInvocationDiagnosticDetail{
			RememberInvocationDiagnosticSummary: rememberapp.RememberInvocationDiagnosticSummary{
				TeamID: teamID.String(), InvocationID: invocationID.String(), Classification: "replay", Outcome: "replayed",
			},
			RequestBody: `{"evidence":[]}`, ProviderExchanges: []rememberapp.RememberInvocationDiagnosticExchange{{Kind: "provider_exchange", Component: "assessor"}},
		},
	}
	server, err := newControlPortalServerWithMetricsAndTelemetry(&config.Config{ControlHTTPAddr: "127.0.0.1:8090", ControlPortalToken: "secret"}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{RememberInvocations: reader}, HealthConfig{}, nil)
	require.NoError(t, err)

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	rec := do("/control/api/remember-invocations?team_id=" + teamID.String() + "&owner_profile_id=" + profileID.String() + "&invocation_id=" + invocationID.String() + "&canonical_attempt_id=" + attemptID.String() + "&request_hash=hash&correlation_id=corr&classification=replay&outcome=replayed&retryable=true&limit=25&offset=2")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"invocation_id":"`+invocationID.String()+`"`)
	require.Equal(t, rememberapp.RememberInvocationDiagnosticFilter{
		TeamID: teamID.String(), OwnerProfileID: profileID.String(), InvocationID: invocationID.String(), CanonicalAttemptID: attemptID.String(),
		RequestHash: "hash", CorrelationID: "corr", Classification: "replay", Outcome: "replayed", Retryable: boolPtr(true), Limit: 25, Offset: 2,
	}, reader.filter)

	rec = do("/control/api/teams/" + teamID.String() + "/remember-invocations/" + invocationID.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"provider_exchanges"`)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, teamID.String(), reader.teamID)
	require.Equal(t, invocationID.String(), reader.invocationID)
}

func TestControlPortalRememberInvocationValidationAndErrors(t *testing.T) {
	e := echo.New()
	newContext := func(path string) echo.Context {
		return e.NewContext(httptest.NewRequest(http.MethodGet, path, nil), httptest.NewRecorder())
	}

	filter, err := controlRememberInvocationDiagnosticFilter(newContext("/?team_id=" + uuid.NewString() + "&owner_profile_id=" + uuid.NewString() + "&invocation_id=" + uuid.NewString() + "&canonical_attempt_id=" + uuid.NewString() + "&request_hash=hash&correlation_id=corr&classification=conflict&outcome=conflict&retryable=false&limit=3&offset=4"))
	require.NoError(t, err)
	require.Equal(t, 3, filter.Limit)
	require.Equal(t, 4, filter.Offset)
	require.NotNil(t, filter.Retryable)
	require.False(t, *filter.Retryable)

	for _, path := range []string{
		"/?limit=101", "/?offset=-1", "/?team_id=bad", "/?request_hash=" + strings.Repeat("x", 257),
		"/?classification=unknown", "/?outcome=unknown", "/?retryable=maybe", "/?retryable=1",
		"/?retryable=0", "/?retryable=t", "/?retryable=f", "/?retryable=TRUE", "/?retryable=False",
	} {
		_, err := controlRememberInvocationDiagnosticFilter(newContext(path))
		require.Error(t, err, path)
	}

	unavailable := &controlPortalHandler{}
	require.ErrorContains(t, unavailable.listRememberInvocationDiagnostics(newContext("/")), "unavailable")
	require.ErrorContains(t, unavailable.getRememberInvocationDiagnostic(withInvocationParams(newContext("/"), uuid.NewString(), uuid.NewString())), "unavailable")

	reader := &invocationReaderStub{listErr: rememberapp.ErrRememberInvocationDiagnosticsUnavailable, detailErr: rememberapp.ErrRememberInvocationDiagnosticsUnavailable}
	h := &controlPortalHandler{rememberInvocations: reader}
	require.ErrorContains(t, h.listRememberInvocationDiagnostics(newContext("/")), "unavailable")
	require.ErrorContains(t, h.getRememberInvocationDiagnostic(withInvocationParams(newContext("/"), "bad", uuid.NewString())), "team ID")
	require.ErrorContains(t, h.getRememberInvocationDiagnostic(withInvocationParams(newContext("/"), uuid.NewString(), "bad")), "invocation ID")
	require.ErrorContains(t, h.getRememberInvocationDiagnostic(withInvocationParams(newContext("/"), uuid.NewString(), uuid.NewString())), "unavailable")

	reader.listErr = errors.New("list failed")
	require.ErrorContains(t, h.listRememberInvocationDiagnostics(newContext("/")), "list failed")
	reader.detailErr = rememberapp.ErrRememberInvocationDiagnosticNotFound
	require.ErrorContains(t, h.getRememberInvocationDiagnostic(withInvocationParams(newContext("/"), uuid.NewString(), uuid.NewString())), "not found")
	reader.detailErr = errors.New("detail failed")
	require.ErrorContains(t, h.getRememberInvocationDiagnostic(withInvocationParams(newContext("/"), uuid.NewString(), uuid.NewString())), "detail failed")
}

func withInvocationParams(ctx echo.Context, teamID, invocationID string) echo.Context {
	ctx.SetParamNames("teamId", "invocationId")
	ctx.SetParamValues(teamID, invocationID)
	return ctx
}

type invocationLogReaderStub struct {
	rows    []domain.OperationLog
	filters []domain.OperationLogFilter
	err     error
}

func (s *invocationLogReaderStub) ListOperationLogs(_ context.Context, filter domain.OperationLogFilter) (*domain.OperationLogPage, error) {
	s.filters = append(s.filters, filter)
	if s.err != nil {
		return nil, s.err
	}
	items := make([]domain.OperationLog, 0, len(s.rows))
	for _, row := range s.rows {
		if filter.InvocationID != "" {
			invocationID, _ := row.Attrs["invocation_id"].(string)
			if invocationID != filter.InvocationID {
				continue
			}
		}
		if filter.CorrelationID != "" && row.CorrelationID != filter.CorrelationID {
			continue
		}
		items = append(items, row)
	}
	return &domain.OperationLogPage{Items: items}, nil
}

func TestInvocationDetailUsesExactInvocationAndSameTeamTransportRows(t *testing.T) {
	teamID := uuid.New()
	invocationID := uuid.New()
	correlationID := "corr-328"
	logs := &invocationLogReaderStub{rows: []domain.OperationLog{
		{Message: "remember_invocation_completed", CorrelationID: correlationID, Attrs: map[string]any{"invocation_id": invocationID.String(), "phase": "replay"}, Error: "protected-cause"},
		{Message: "remember_invocation_completed", CorrelationID: correlationID, Attrs: map[string]any{"invocation_id": invocationID.String(), "phase": "assessment"}, Error: "older-cause"},
		{Message: "remember_invocation_completed", CorrelationID: correlationID, Attrs: map[string]any{"invocation_id": uuid.NewString(), "phase": "wrong-phase"}, Error: "wrong-cause"},
		{Message: "http_request", CorrelationID: correlationID, Attrs: map[string]any{"invocation_id": invocationID.String(), "delivery_stage": "write_observed"}},
	}}
	h := &controlPortalHandler{
		rememberInvocations: &invocationReaderStub{detail: &rememberapp.RememberInvocationDiagnosticDetail{
			RememberInvocationDiagnosticSummary: rememberapp.RememberInvocationDiagnosticSummary{TeamID: teamID.String(), InvocationID: invocationID.String(), CorrelationID: correlationID},
		}},
		operationLogs: logs,
	}
	detail := h.rememberInvocations.(*invocationReaderStub).detail
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := e.NewContext(req, httptest.NewRecorder())
	ctx.SetParamNames("teamId", "invocationId")
	ctx.SetParamValues(teamID.String(), invocationID.String())
	err := h.getRememberInvocationDiagnostic(ctx)
	require.NoError(t, err)
	require.Len(t, logs.filters, 1)
	require.Equal(t, &teamID, logs.filters[0].TeamID)
	require.Equal(t, invocationID.String(), logs.filters[0].InvocationID)
	require.Equal(t, "replay", detail.Phase)
	require.Equal(t, "protected-cause", detail.ProtectedCause)
	require.Equal(t, "write_observed", detail.DeliveryStage)
	require.False(t, detail.EnrichmentUnavailable)
}

func TestInvocationDetailMarksPartialLogEnrichmentUnavailable(t *testing.T) {
	teamID := uuid.New()
	invocationID := uuid.New()
	correlationID := "partial-correlation"
	detail := &rememberapp.RememberInvocationDiagnosticDetail{
		RememberInvocationDiagnosticSummary: rememberapp.RememberInvocationDiagnosticSummary{
			TeamID: teamID.String(), InvocationID: invocationID.String(), CorrelationID: correlationID,
		},
	}
	h := &controlPortalHandler{
		rememberInvocations: &invocationReaderStub{detail: detail},
		operationLogs: &invocationLogReaderStub{rows: []domain.OperationLog{
			{Message: "http_request", CorrelationID: correlationID, Attrs: map[string]any{
				"invocation_id": invocationID.String(), "delivery_stage": "write_observed",
			}},
		}},
	}
	e := echo.New()
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	ctx.SetParamNames("teamId", "invocationId")
	ctx.SetParamValues(teamID.String(), invocationID.String())

	require.NoError(t, h.getRememberInvocationDiagnostic(ctx))
	require.Equal(t, "write_observed", detail.DeliveryStage)
	require.True(t, detail.EnrichmentUnavailable)
}

func TestInvocationDetailSurfacesEnrichmentUnavailable(t *testing.T) {
	teamID := uuid.New()
	invocationID := uuid.New()
	detail := &rememberapp.RememberInvocationDiagnosticDetail{
		RememberInvocationDiagnosticSummary: rememberapp.RememberInvocationDiagnosticSummary{
			TeamID: teamID.String(), InvocationID: invocationID.String(), CorrelationID: "corr-error",
		},
	}
	h := &controlPortalHandler{
		rememberInvocations: &invocationReaderStub{detail: detail},
		operationLogs:       &invocationLogReaderStub{err: errors.New("operation logs unavailable")},
	}
	e := echo.New()
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	ctx.SetParamNames("teamId", "invocationId")
	ctx.SetParamValues(teamID.String(), invocationID.String())
	require.NoError(t, h.getRememberInvocationDiagnostic(ctx))
	require.True(t, detail.EnrichmentUnavailable)
}

func TestInvocationDetailLeavesDeliveryStageUnknownForAmbiguousCorrelation(t *testing.T) {
	teamID := uuid.New()
	invocationID := uuid.New()
	correlationID := "reused-correlation"
	logs := &invocationLogReaderStub{rows: []domain.OperationLog{
		{Message: "http_request", CorrelationID: correlationID, Attrs: map[string]any{"delivery_stage": "write_observed"}},
		{Message: "http_request", CorrelationID: correlationID, Attrs: map[string]any{"delivery_stage": "prepared"}},
	}}
	detail := &rememberapp.RememberInvocationDiagnosticDetail{
		RememberInvocationDiagnosticSummary: rememberapp.RememberInvocationDiagnosticSummary{
			TeamID: teamID.String(), InvocationID: invocationID.String(), CorrelationID: correlationID, DeliveryStage: "unknown_receipt",
		},
	}
	h := &controlPortalHandler{
		rememberInvocations: &invocationReaderStub{detail: detail},
		operationLogs:       logs,
	}
	e := echo.New()
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	ctx.SetParamNames("teamId", "invocationId")
	ctx.SetParamValues(teamID.String(), invocationID.String())
	require.NoError(t, h.getRememberInvocationDiagnostic(ctx))
	require.Equal(t, "unknown_receipt", detail.DeliveryStage)
}

func TestInvocationDetailLeavesDeliveryStageUnknownForCorrelationOnlyTransportRow(t *testing.T) {
	teamID := uuid.New()
	invocationID := uuid.New()
	correlationID := "reused-correlation"
	detail := &rememberapp.RememberInvocationDiagnosticDetail{
		RememberInvocationDiagnosticSummary: rememberapp.RememberInvocationDiagnosticSummary{
			TeamID: teamID.String(), InvocationID: invocationID.String(), CorrelationID: correlationID, DeliveryStage: "unknown_receipt",
		},
	}
	h := &controlPortalHandler{
		rememberInvocations: &invocationReaderStub{detail: detail},
		operationLogs: &invocationLogReaderStub{rows: []domain.OperationLog{
			{Message: "http_request", CorrelationID: correlationID, Attrs: map[string]any{"delivery_stage": "write_observed"}},
		}},
	}
	e := echo.New()
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	ctx.SetParamNames("teamId", "invocationId")
	ctx.SetParamValues(teamID.String(), invocationID.String())
	require.NoError(t, h.getRememberInvocationDiagnostic(ctx))
	require.Equal(t, "unknown_receipt", detail.DeliveryStage)
}

func TestInvocationDetailMarksEmptyLogEnrichmentUnavailable(t *testing.T) {
	teamID := uuid.New()
	detail := &rememberapp.RememberInvocationDiagnosticDetail{
		RememberInvocationDiagnosticSummary: rememberapp.RememberInvocationDiagnosticSummary{
			TeamID: teamID.String(), InvocationID: uuid.NewString(), CorrelationID: "filtered-correlation", DeliveryStage: "unknown_receipt",
		},
	}
	h := &controlPortalHandler{
		rememberInvocations: &invocationReaderStub{detail: detail},
		operationLogs:       &invocationLogReaderStub{},
	}
	e := echo.New()
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	ctx.SetParamNames("teamId", "invocationId")
	ctx.SetParamValues(teamID.String(), detail.InvocationID)
	require.NoError(t, h.getRememberInvocationDiagnostic(ctx))
	require.True(t, detail.EnrichmentUnavailable)
}
