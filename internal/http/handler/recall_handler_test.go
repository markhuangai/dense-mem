package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

// stubRecallService implements recallservice.RecallService for testing.
// Mocks go in *_test.go only — cross-pkg consumers use a local stub per
// memory/feedback_mock_placement.md.
type stubRecallService struct {
	recallFunc func(ctx context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error)
}

func (s *stubRecallService) Recall(ctx context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	if s.recallFunc != nil {
		return s.recallFunc(ctx, profileID, req)
	}
	return nil, nil
}

// Compile-time check that stubRecallService satisfies RecallService.
var _ recallservice.RecallService = (*stubRecallService)(nil)

// recallDataEnvelope is the expected response shape for GET /api/v1/recall.
// The handler wraps hits in {"data": [...]} via response.SuccessOK (AC-55, AC-62).
type recallDataEnvelope struct {
	Data []dto.RecallHitResponse `json:"data"`
}

// TestRecallHandler verifies the handler returns 200 with recall hits wrapped in {"data": [...]}.
func TestRecallHandler(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	fragID := "frag-abc"
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
			return []recallservice.RecallHit{
				{
					Fragment:     &domain.Fragment{FragmentID: fragID, ProfileID: pid},
					Tier:         recallservice.TierFragment,
					Score:        0.9,
					SemanticRank: 1,
					KeywordRank:  2,
					FinalScore:   0.016,
				},
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/recall", h.Handle)

	// Use the "query" parameter (not "q") — stable external contract.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=test+query", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
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
		t.Errorf("tier = %q; want %q", item.Tier, recallservice.TierFragment)
	}
	if item.Fragment == nil || item.Fragment.FragmentID != fragID {
		t.Errorf("fragment_id mismatch: got fragment=%v; want fragment_id=%q", item.Fragment, fragID)
	}
}

func TestRecallHandler_ReturnsClaimAndFactHits(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
			return []recallservice.RecallHit{
				{
					Claim: &domain.Claim{
						ClaimID:   "claim-abc",
						ProfileID: pid,
						Subject:   "sky",
						Predicate: "is",
						Object:    "blue",
						Status:    domain.StatusValidated,
					},
					Tier: recallservice.TierValidatedClaim,
				},
				{
					Fact: &domain.Fact{
						FactID:    "fact-abc",
						ProfileID: pid,
						Subject:   "water",
						Predicate: "is",
						Object:    "wet",
						Status:    domain.FactStatusActive,
					},
					Tier: recallservice.TierActiveFact,
				},
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=test+query", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	var resp recallDataEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data count = %d; want 2. body=%s", len(resp.Data), rec.Body.String())
	}
	if resp.Data[0].Claim == nil || resp.Data[0].Claim.ClaimID != "claim-abc" {
		t.Fatalf("claim hit = %#v; want claim-abc", resp.Data[0].Claim)
	}
	if resp.Data[1].Fact == nil || resp.Data[1].Fact.FactID != "fact-abc" {
		t.Fatalf("fact hit = %#v; want fact-abc", resp.Data[1].Fact)
	}
}

// TestRecallHandler_CrossProfileIsolation verifies that recall for profile B
// does not return results belonging to profile A. The service is responsible
// for scoping all DB queries to the injected profileID; the handler must never
// call the service with a different profileID than the one from the auth context.
func TestRecallHandler_CrossProfileIsolation(t *testing.T) {
	e := echo.New()
	profileB := uuid.New()

	fragAID := "frag-owned-by-a"
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
			// Simulate DB-level isolation: the service returns no results when
			// called with profile B's ID (profile A's data is invisible to B).
			if pid == profileB.String() {
				return nil, nil
			}
			return []recallservice.RecallHit{
				{Fragment: &domain.Fragment{FragmentID: fragAID, ProfileID: pid}, Tier: recallservice.TierFragment},
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	// Inject profile B — profile A's fragments must not appear.
	e.Use(injectProfileMiddleware(profileB))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=anything", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	var resp recallDataEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, item := range resp.Data {
		if item.Fragment != nil && item.Fragment.FragmentID == fragAID {
			t.Errorf("profile A fragment %q leaked into profile B recall results (isolation violation)", fragAID)
		}
	}
	// Explicitly verify profileB received no results (stub returns nil for B).
	if len(resp.Data) != 0 {
		t.Errorf("profile B recall returned %d items; want 0 (cross-profile data must be invisible)", len(resp.Data))
	}
}

// TestRecallHandler_Returns400WhenQueryMissing verifies missing query parameter → 400.
// Stable external contract: missing query returns 400 (Bad Request), not 422.
func TestRecallHandler_Returns400WhenQueryMissing(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	h := NewRecallHandler(&stubRecallService{})
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400. body=%s", rec.Code, rec.Body.String())
	}
}

// TestRecallHandler_Returns503WhenEmbeddingUnavailable verifies embedding failure → 503.
func TestRecallHandler_Returns503WhenEmbeddingUnavailable(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
			return nil, recallservice.ErrEmbeddingUnavailable
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=hello", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503", rec.Code)
	}
}

func TestRecallHandler_ServiceErrorMappings(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "keyword unavailable",
			err:  recallservice.ErrKeywordUnavailable,
			want: http.StatusServiceUnavailable,
		},
		{
			name: "generic recall failure",
			err:  errors.New("query failed"),
			want: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			profileID := uuid.New()
			svc := &stubRecallService{
				recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
					return nil, tc.err
				},
			}
			h := NewRecallHandler(svc)
			e.HTTPErrorHandler = httperr.ErrorHandler

			e.Use(injectProfileMiddleware(profileID))
			e.GET("/api/v1/recall", h.Handle)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=hello", nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d; want %d. body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestRecallHandler_Returns400WhenProfileMissing verifies missing profile → 400.
func TestRecallHandler_Returns400WhenProfileMissing(t *testing.T) {
	e := echo.New()
	h := NewRecallHandler(&stubRecallService{})
	e.HTTPErrorHandler = httperr.ErrorHandler

	// No profile middleware injected.
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=hello", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", rec.Code)
	}
}

// Compile-time companion interface check.
var _ RecallHandlerInterface = (*RecallHandler)(nil)
