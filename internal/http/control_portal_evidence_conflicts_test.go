package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/conflict/evidence"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

func TestEvidenceOnlyUnitControlEvidenceConflictsValidatePublicInputs(t *testing.T) {
	team, conflict := uuid.NewString(), uuid.NewString()
	base := "/control/api/teams/" + team + "/evidence-conflicts"
	server, err := newControlPortalServerWithMetricsAndTelemetry(&config.Config{ControlHTTPAddr: "127.0.0.1:8090", ControlPortalToken: "secret"}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{EvidenceConflicts: evidenceconflict.New(&evidenceConflictReaderStub{})}, HealthConfig{}, nil)
	require.NoError(t, err)
	for _, test := range []struct {
		path   string
		status int
	}{
		{base, http.StatusOK}, {base + "?limit=20", http.StatusOK},
		{base + "?limit=invalid", http.StatusUnprocessableEntity}, {base + "?limit=101", http.StatusUnprocessableEntity}, {base + "?status=invalid", http.StatusUnprocessableEntity}, {base + "?cursor=invalid", http.StatusUnprocessableEntity},
		{"/control/api/teams/invalid/evidence-conflicts", http.StatusBadRequest},
		{base + "/invalid", http.StatusBadRequest}, {base + "/" + conflict + "?event_limit=invalid", http.StatusUnprocessableEntity}, {base + "/" + conflict + "?event_limit=101", http.StatusUnprocessableEntity},
	} {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		out := httptest.NewRecorder()
		server.ServeHTTP(out, req)
		require.Equal(t, test.status, out.Code, test.path)
		require.Equal(t, "no-store", out.Header().Get("Cache-Control"))
		require.NotContains(t, out.Body.String(), "stack")
	}
	for _, test := range []struct {
		preferred string
		status    int
	}{{"invalid", http.StatusUnprocessableEntity}, {"  ", http.StatusOK}} {
		body := `{"expected_version":1,"decision":"dismiss","reason":"reviewed","preferred_position_id":"` + test.preferred + `"}`
		req := httptest.NewRequest(http.MethodPost, base+"/"+conflict+"/resolution", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		server.ServeHTTP(out, req)
		require.Equal(t, test.status, out.Code)
	}
}

func TestEvidenceOnlyUnitControlEvidenceConflictsBoundApplicationFailures(t *testing.T) {
	for _, test := range []struct {
		cause  error
		status int
	}{
		{evidenceconflict.ErrInvalid, http.StatusUnprocessableEntity}, {evidenceconflict.ErrNotFound, http.StatusNotFound}, {evidenceconflict.ErrNotOpen, http.StatusConflict}, {evidenceconflict.ErrUnavailable, http.StatusServiceUnavailable}, {errors.New("private database detail"), http.StatusInternalServerError},
	} {
		reader := &evidenceConflictReaderStub{resolveErr: test.cause}
		server, err := newControlPortalServerWithMetricsAndTelemetry(&config.Config{ControlHTTPAddr: "127.0.0.1:8090", ControlPortalToken: "secret"}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{EvidenceConflicts: evidenceconflict.New(reader)}, HealthConfig{}, nil)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/control/api/teams/"+uuid.NewString()+"/evidence-conflicts/"+uuid.NewString()+"/resolution", strings.NewReader(`{"expected_version":1,"decision":"dismiss","reason":"reviewed"}`))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		server.ServeHTTP(out, req)
		require.Equal(t, test.status, out.Code)
		require.Equal(t, "no-store", out.Header().Get("Cache-Control"))
		require.NotContains(t, out.Body.String(), "private database detail")
	}
}

type evidenceConflictReaderStub struct {
	resolveInput knowledgecontract.EvidenceConflictResolutionInput
	resolveErr   error
}

func (s *evidenceConflictReaderStub) ListEvidenceConflicts(context.Context, knowledgecontract.EvidenceConflictListInput) (*knowledgecontract.EvidenceConflictListResult, error) {
	return &knowledgecontract.EvidenceConflictListResult{Items: []knowledgecontract.EvidenceConflictCaseRecord{}, NextCursor: nil}, nil
}

func (s *evidenceConflictReaderStub) GetEvidenceConflict(context.Context, knowledgecontract.EvidenceConflictGetInput) (*knowledgecontract.EvidenceConflictGetResult, error) {
	return nil, knowledgecontract.ErrEvidenceConflictNotFound
}

func (s *evidenceConflictReaderStub) ResolveEvidenceConflict(_ context.Context, input knowledgecontract.EvidenceConflictResolutionInput) (*knowledgecontract.EvidenceConflictCaseRecord, error) {
	s.resolveInput = input
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	return &knowledgecontract.EvidenceConflictCaseRecord{ConflictID: input.ConflictID, Status: "resolved", Version: input.ExpectedVersion + 1, Positions: []knowledgecontract.EvidenceConflictPositionRecord{}}, nil
}

func TestControlPortalEvidenceConflictStaticTokenCarriesActorAndNoStore(t *testing.T) {
	reader := &evidenceConflictReaderStub{}
	server, err := newControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr: "127.0.0.1:8090", ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{EvidenceConflicts: evidenceconflict.New(reader)}, HealthConfig{}, nil)
	require.NoError(t, err)

	teamID, conflictID := uuid.NewString(), uuid.NewString()
	req := httptest.NewRequest(http.MethodPost, "/control/api/teams/"+teamID+"/evidence-conflicts/"+conflictID+"/resolution", strings.NewReader(`{"expected_version":3,"decision":"resolve","reason":"reviewed"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "control_portal:authorization-bearer", reader.resolveInput.ActorID)
	require.Equal(t, "control", reader.resolveInput.ActorKind)
}

func TestControlPortalEvidenceConflictMapsStaleResolutionToConflict(t *testing.T) {
	reader := &evidenceConflictReaderStub{resolveErr: evidenceconflict.ErrVersionStale}
	server, err := newControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr: "127.0.0.1:8090", ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{EvidenceConflicts: evidenceconflict.New(reader)}, HealthConfig{}, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/control/api/teams/"+uuid.NewString()+"/evidence-conflicts/"+uuid.NewString()+"/resolution", strings.NewReader(`{"expected_version":1,"decision":"dismiss","reason":"not supported"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusConflict, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "stack")
}

func TestControlPortalEvidenceConflictUnknownDetailIsNotFound(t *testing.T) {
	server, err := newControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr: "127.0.0.1:8090", ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{EvidenceConflicts: evidenceconflict.New(&evidenceConflictReaderStub{})}, HealthConfig{}, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/control/api/teams/"+uuid.NewString()+"/evidence-conflicts/"+uuid.NewString(), nil)
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "stack")
}
