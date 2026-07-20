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
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

type stubV2RecallService struct {
	recallFunc func(ctx context.Context, req memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error)
}

func (s *stubV2RecallService) RecallV2(ctx context.Context, req memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
	if s.recallFunc != nil {
		return s.recallFunc(ctx, req)
	}
	return nil, nil
}

var _ memoryservice.V2RecallService = (*stubV2RecallService)(nil)

func TestV2RecallHandlerRoutesLegacyHTTPToV2Recall(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	profileID := uuid.New()
	var captured memoryservice.V2RecallRequest
	var capturedActor requestctx.ActorProfile
	svc := &stubV2RecallService{
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
			}, nil
		},
	}
	h := NewV2RecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(teamID))
	e.Use(injectActorMiddleware(teamID, profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=postgres&limit=3&use_communities=true", nil)
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
	if capturedActor.TeamID != teamID || capturedActor.ProfileID != profileID {
		t.Fatalf("actor = %#v; want team=%s profile=%s", capturedActor, teamID, profileID)
	}

	var resp recallDataEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data count = %d; want 1. body=%s", len(resp.Data), rec.Body.String())
	}
	item := resp.Data[0]
	if item.Tier != recallservice.TierFragment {
		t.Fatalf("tier = %q; want %q", item.Tier, recallservice.TierFragment)
	}
	if item.Fragment == nil || item.Fragment.FragmentID != "ev-1" || item.Fragment.Content == "" {
		t.Fatalf("fragment = %#v; want ev-1 with context", item.Fragment)
	}
	if got, _ := item.Fragment.Metadata["relationship_ids"].([]any); len(got) == 0 {
		t.Fatalf("relationship_ids metadata missing: %#v", item.Fragment.Metadata)
	}
	if item.Score != 0.5 || item.FinalScore != 0.5 {
		t.Fatalf("score/final_score = %v/%v; want 0.5/0.5", item.Score, item.FinalScore)
	}
}

func TestV2RecallHandlerMapsAuthContextError(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	svc := &stubV2RecallService{
		recallFunc: func(context.Context, memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
			return nil, memoryservice.ErrV2RecallAuthContext
		},
	}
	h := NewV2RecallHandler(svc)
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

func TestV2RecallHandlerMapsRequiredDegradation(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	profileID := uuid.New()
	svc := &stubV2RecallService{
		recallFunc: func(context.Context, memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
			return &memoryservice.V2RecallResult{
				Degradation: &memoryservice.V2RecallDegradationResult{
					RequiredFailure: true,
					Message:         "search unavailable",
				},
			}, nil
		},
	}
	h := NewV2RecallHandler(svc)
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

func TestV2RecallHandlerMapsEmptyRequiredDegradationMessage(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	profileID := uuid.New()
	svc := &stubV2RecallService{
		recallFunc: func(context.Context, memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
			return &memoryservice.V2RecallResult{
				Degradation: &memoryservice.V2RecallDegradationResult{
					RequiredFailure: true,
				},
			}, nil
		},
	}
	h := NewV2RecallHandler(svc)
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

func TestV2RecallHandlerMapsGenericError(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	profileID := uuid.New()
	svc := &stubV2RecallService{
		recallFunc: func(context.Context, memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
			return nil, errors.New("database details must not leak")
		},
	}
	h := NewV2RecallHandler(svc)
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
