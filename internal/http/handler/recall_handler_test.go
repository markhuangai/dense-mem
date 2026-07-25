package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

type recallDataEnvelope struct {
	Data memoryservice.V2RecallResult `json:"data"`
}

type stubRecallService struct {
	recallFunc func(ctx context.Context, req memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error)
}

func (s *stubRecallService) Recall(ctx context.Context, req memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
	if s.recallFunc != nil {
		return s.recallFunc(ctx, req)
	}
	return nil, nil
}

var _ memoryservice.RecallService = (*stubRecallService)(nil)

func TestRecallHandlerRoutesHTTPToV2Recall(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	profileID := uuid.New()
	var captured memoryservice.V2RecallRequest
	var capturedActor requestctx.ActorProfile
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, req memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
			captured = req
			actor, ok := requestctx.ActorProfileFromContext(ctx)
			if !ok {
				t.Fatal("actor context missing")
			}
			capturedActor = actor
			return &memoryservice.V2RecallResult{
				RecallID:    "rec_v2",
				SearchState: string(domain.V2SearchProjectionCurrent),
				Results: []memoryservice.V2RecallResultItem{{
					EvidenceID:      "ev-1",
					RelationshipIDs: []string{"rel-1"},
					Rank:            2,
					Context:         "Dense-Mem uses PostgreSQL as the durable authority.",
				}},
				DiscoveryPaths: []memoryservice.V2RecallDiscoveryPath{{
					EvidenceIDs: []string{"ev-1"},
					Relationships: []memoryservice.V2RecallRelationshipHandle{{
						RelationshipID: "rel-1",
						Predicate:      "uses",
					}},
				}},
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(teamID))
	e.Use(injectActorMiddleware(teamID, profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=postgres&limit=3&known_evidence_ids=11111111-1111-4111-8111-111111111111&known_relationship_ids=22222222-2222-4222-8222-222222222222&expand_from_entity_ids=33333333-3333-4333-8333-333333333333", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	if captured.ContractVersion != domain.V2ContractVersion {
		t.Fatalf("contract_version = %q; want %q", captured.ContractVersion, domain.V2ContractVersion)
	}
	if captured.Query != "postgres" || captured.Limit != 3 {
		t.Fatalf("captured request = %#v", captured)
	}
	if got := captured.KnownEvidenceIDs; len(got) != 1 || got[0] != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("known_evidence_ids = %#v", got)
	}
	if got := captured.KnownRelationshipIDs; len(got) != 1 || got[0] != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("known_relationship_ids = %#v", got)
	}
	if got := captured.ExpandFromEntityIDs; len(got) != 1 || got[0] != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("expand_from_entity_ids = %#v", got)
	}
	if capturedActor.TeamID != teamID || capturedActor.ProfileID != profileID {
		t.Fatalf("actor = %#v; want team=%s profile=%s", capturedActor, teamID, profileID)
	}

	var resp recallDataEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if resp.Data.RecallID != "rec_v2" {
		t.Fatalf("recall_id = %q; want rec_v2", resp.Data.RecallID)
	}
	if len(resp.Data.Results) != 1 {
		t.Fatalf("result count = %d; want 1. body=%s", len(resp.Data.Results), rec.Body.String())
	}
	item := resp.Data.Results[0]
	if item.EvidenceID != "ev-1" || item.Context == "" {
		t.Fatalf("result = %#v; want ev-1 with context", item)
	}
	if len(item.RelationshipIDs) != 1 || item.RelationshipIDs[0] != "rel-1" {
		t.Fatalf("relationship_ids = %#v; want rel-1", item.RelationshipIDs)
	}
	if len(resp.Data.DiscoveryPaths) != 1 || len(resp.Data.DiscoveryPaths[0].Relationships) != 1 {
		t.Fatalf("discovery_paths = %#v; want one relationship path", resp.Data.DiscoveryPaths)
	}
	if resp.Data.DiscoveryGuidance != "No additional discovery guidance." {
		t.Fatalf("discovery_guidance = %q; want fallback guidance", resp.Data.DiscoveryGuidance)
	}
}

func TestRecallHandlerRequiresTeamContext(t *testing.T) {
	e := echo.New()
	h := NewRecallHandler(&stubRecallService{})
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=postgres", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400. body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "team ID is required") {
		t.Fatalf("body = %s; want team ID message", rec.Body.String())
	}
}

func TestRecallHandlerMapsAuthContextError(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	svc := &stubRecallService{
		recallFunc: func(context.Context, memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
			return nil, memoryservice.ErrRecallAuthContext
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(teamID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=postgres", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403. body=%s", rec.Code, rec.Body.String())
	}
}

func TestRecallHandlerMapsRequiredDegradation(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	profileID := uuid.New()
	svc := &stubRecallService{
		recallFunc: func(context.Context, memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
			return &memoryservice.V2RecallResult{
				Degradation: &memoryservice.V2RecallDegradationResult{
					RequiredFailure: true,
					Message:         "search unavailable",
				},
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(teamID))
	e.Use(injectActorMiddleware(teamID, profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=postgres", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503. body=%s", rec.Code, rec.Body.String())
	}
}

func TestRecallHandlerMapsEmptyRequiredDegradationMessage(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	profileID := uuid.New()
	svc := &stubRecallService{
		recallFunc: func(context.Context, memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
			return &memoryservice.V2RecallResult{
				Degradation: &memoryservice.V2RecallDegradationResult{
					RequiredFailure: true,
				},
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(teamID))
	e.Use(injectActorMiddleware(teamID, profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=postgres", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503. body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "recall unavailable") {
		t.Fatalf("body = %s; want fallback message", rec.Body.String())
	}
}

func TestRecallHandlerMapsGenericError(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	profileID := uuid.New()
	svc := &stubRecallService{
		recallFunc: func(context.Context, memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
			return nil, errors.New("database details must not leak")
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(teamID))
	e.Use(injectActorMiddleware(teamID, profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=postgres", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500. body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body == "" || strings.Contains(body, "database details must not leak") {
		t.Fatalf("unexpected body: %s", body)
	}
}

func injectActorMiddleware(teamID uuid.UUID, profileID uuid.UUID) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := requestctx.WithActorProfile(c.Request().Context(), requestctx.ActorProfile{
				TeamID:    teamID,
				ProfileID: profileID,
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}
