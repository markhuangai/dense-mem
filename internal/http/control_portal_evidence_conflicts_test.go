package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/evidenceconflict"
)

type evidenceConflictReaderStub struct {
	resolveInput repository.EvidenceConflictResolutionInput
	resolveErr   error
}

func (s *evidenceConflictReaderStub) ListEvidenceConflicts(context.Context, repository.EvidenceConflictListInput) (*repository.EvidenceConflictListResult, error) {
	return &repository.EvidenceConflictListResult{Items: []repository.EvidenceConflictCaseRecord{}, NextCursor: nil}, nil
}

func (s *evidenceConflictReaderStub) GetEvidenceConflict(context.Context, repository.EvidenceConflictGetInput) (*repository.EvidenceConflictGetResult, error) {
	return nil, repository.ErrEvidenceConflictNotFound
}

func (s *evidenceConflictReaderStub) ResolveEvidenceConflict(_ context.Context, input repository.EvidenceConflictResolutionInput) (*repository.EvidenceConflictCaseRecord, error) {
	s.resolveInput = input
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	return &repository.EvidenceConflictCaseRecord{ConflictID: input.ConflictID, Status: "resolved", Version: input.ExpectedVersion + 1, Positions: []repository.EvidenceConflictPositionRecord{}}, nil
}

func TestControlPortalEvidenceConflictStaticTokenCarriesActorAndNoStore(t *testing.T) {
	reader := &evidenceConflictReaderStub{}
	server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
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
	server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
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
	server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
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
