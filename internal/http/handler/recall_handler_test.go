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
	"github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type recallDataEnvelope struct {
	Data recall.RecallResult `json:"data"`
}

type stubRecallService struct {
	recallFunc func(ctx context.Context, req recall.RecallRequest) (*recall.RecallResult, error)
}

func (s *stubRecallService) Recall(ctx context.Context, req recall.RecallRequest) (*recall.RecallResult, error) {
	if s.recallFunc != nil {
		return s.recallFunc(ctx, req)
	}
	return nil, nil
}

var _ recall.RecallService = (*stubRecallService)(nil)

func TestRecallHandlerRoutesHTTPToRecall(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	profileID := uuid.New()
	var captured recall.RecallRequest
	var capturedActor requestctx.Actor
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, req recall.RecallRequest) (*recall.RecallResult, error) {
			captured = req
			actor, ok := requestctx.ActorFromContext(ctx)
			if !ok {
				t.Fatal("actor context missing")
			}
			capturedActor = actor
			return &recall.RecallResult{
				RecallID:    "rec_canonical",
				SearchState: string(domain.SearchProjectionCurrent),
				SearchStates: recall.RecallSearchStates{
					Evidence: string(domain.SearchProjectionCurrent),
				},
				Results: []recall.RecallResultItem{{
					EvidenceID: "ev-1",
					Rank:       2,
					Context:    "Dense-Mem uses PostgreSQL as the durable authority.",
				}},
				RelatedTopics:   []recallcontract.RelatedTopic{{TopicID: "22222222-2222-4222-8222-222222222222", Name: "PostgreSQL", Description: "Durable database Evidence", EligibleEvidenceCount: 4, Rank: 1}},
				TopicsTruncated: true,
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(teamID))
	e.Use(injectActorMiddleware(teamID, profileID))
	e.GET("/ui/api/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/recall?query=postgres&limit=3&known_evidence_ids=11111111-1111-4111-8111-111111111111&known_evidence_ids=33333333-3333-4333-8333-333333333333&topic_ids=22222222-2222-4222-8222-222222222222&topic_ids=44444444-4444-4444-8444-444444444444&topic_limit=1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	if captured.Query != "postgres" || captured.Limit != 3 {
		t.Fatalf("captured request = %#v", captured)
	}
	if got := captured.KnownEvidenceIDs; len(got) != 2 || got[0] != "11111111-1111-4111-8111-111111111111" || got[1] != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("known_evidence_ids = %#v", got)
	}
	if len(captured.TopicIDs) != 2 || captured.TopicIDs[0] != "22222222-2222-4222-8222-222222222222" || captured.TopicIDs[1] != "44444444-4444-4444-8444-444444444444" || captured.TopicLimit == nil || *captured.TopicLimit != 1 {
		t.Fatalf("topic input=%+v", captured)
	}
	if capturedActor.TeamID != teamID || capturedActor.OwnerID != profileID {
		t.Fatalf("actor = %#v; want team=%s profile=%s", capturedActor, teamID, profileID)
	}

	var resp recallDataEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if resp.Data.RecallID != "rec_canonical" {
		t.Fatalf("recall_id = %q; want rec_canonical", resp.Data.RecallID)
	}
	if len(resp.Data.Results) != 1 {
		t.Fatalf("result count = %d; want 1. body=%s", len(resp.Data.Results), rec.Body.String())
	}
	item := resp.Data.Results[0]
	if item.EvidenceID != "ev-1" || item.Context == "" {
		t.Fatalf("result = %#v; want ev-1 with context", item)
	}
	if len(resp.Data.RelatedTopics) != 1 || resp.Data.RelatedTopics[0].EligibleEvidenceCount != 4 || !resp.Data.TopicsTruncated {
		t.Fatalf("topic result=%+v", resp.Data)
	}
	if resp.Data.SearchStates.Evidence != string(domain.SearchProjectionCurrent) {
		t.Fatalf("search states=%+v", resp.Data.SearchStates)
	}
	for _, retired := range []string{"relationship_ids", "related_relationships", "related_communities"} {
		if strings.Contains(rec.Body.String(), `"`+retired+`"`) {
			t.Fatalf("retired field %s in %s", retired, rec.Body.String())
		}
	}

}

func TestRecallHandlerRequiresTeamContext(t *testing.T) {
	e := echo.New()
	h := NewRecallHandler(&stubRecallService{})
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.GET("/ui/api/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/recall?query=postgres", nil)
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
		recallFunc: func(context.Context, recall.RecallRequest) (*recall.RecallResult, error) {
			return nil, recall.ErrRecallAuthContext
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(teamID))
	e.GET("/ui/api/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/recall?query=postgres", nil)
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
		recallFunc: func(context.Context, recall.RecallRequest) (*recall.RecallResult, error) {
			return &recall.RecallResult{
				Degradation: &recall.RecallDegradationResult{
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
	e.GET("/ui/api/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/recall?query=postgres", nil)
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
		recallFunc: func(context.Context, recall.RecallRequest) (*recall.RecallResult, error) {
			return &recall.RecallResult{
				Degradation: &recall.RecallDegradationResult{
					RequiredFailure: true,
				},
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(teamID))
	e.Use(injectActorMiddleware(teamID, profileID))
	e.GET("/ui/api/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/recall?query=postgres", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503. body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"message":"service unavailable"`) {
		t.Fatalf("body = %s; want fallback message", rec.Body.String())
	}
}

func TestRecallHandlerMapsGenericError(t *testing.T) {
	e := echo.New()
	teamID := uuid.New()
	profileID := uuid.New()
	svc := &stubRecallService{
		recallFunc: func(context.Context, recall.RecallRequest) (*recall.RecallResult, error) {
			return nil, errors.New("database details must not leak")
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(teamID))
	e.Use(injectActorMiddleware(teamID, profileID))
	e.GET("/ui/api/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/recall?query=postgres", nil)
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
			ctx := requestctx.WithActor(c.Request().Context(), requestctx.Actor{
				TeamID:  teamID,
				OwnerID: profileID,
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

func TestEvidenceOnlyRecallHTTPRejectsRetiredAndInvalidTopics(t *testing.T) {
	id := uuid.NewString()
	for _, query := range []string{
		"query=memory&relationship_limit=1", "query=memory&community_limit=1",
		"query=memory&expand_from_entity_ids=" + id, "query=memory&known_relationship_ids=" + id,
		"topic_ids=not-a-uuid", "topic_ids=" + uuid.Nil.String(),
		"topic_ids=" + id + "," + uuid.NewString(),
		"query=memory&known_evidence_ids=" + id + "," + uuid.NewString(),
		"topic_ids=" + id + "&topic_ids=" + strings.ToUpper(id),
		"topic_ids=" + id + "&topic_limit=11", "topic_ids=" + id + "&topic_limit=-1",
	} {
		t.Run(query, func(t *testing.T) {
			e := echo.New()
			e.HTTPErrorHandler = httperr.ErrorHandler
			e.Use(injectProfileMiddleware(uuid.New()))
			e.GET("/ui/api/recall", NewRecallHandler(recall.NewRecallService(recall.RecallDependencies{})).Handle)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/api/recall?"+query, nil))
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("invalid query status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestEvidenceOnlyRecallHTTPTopicUnavailableIsBounded(t *testing.T) {
	id := uuid.NewString()
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(uuid.New()))
	h := NewRecallHandler(&stubRecallService{recallFunc: func(_ context.Context, req recall.RecallRequest) (*recall.RecallResult, error) {
		if req.Query != "" || len(req.TopicIDs) != 1 || req.TopicIDs[0] != id {
			t.Fatalf("topic-only input=%+v", req)
		}
		return nil, errors.Join(recallcontract.ErrTopicUnavailable, errors.New("protected membership details "+id))
	}})
	e.GET("/ui/api/recall", h.Handle)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/api/recall?topic_ids="+id, nil))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"reason_code":"topic_unavailable"`) || strings.Contains(rec.Body.String(), id) || strings.Contains(rec.Body.String(), "protected membership") {
		t.Fatalf("unavailable topic response=%d %s", rec.Code, rec.Body.String())
	}
}
