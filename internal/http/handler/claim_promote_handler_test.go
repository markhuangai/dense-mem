package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
)

// mockPromoteClaimService implements factservice.PromoteClaimService for testing.
type mockPromoteClaimService struct {
	promoteFunc func(ctx context.Context, profileID string, claimID string) (*domain.Fact, error)
}

func (m *mockPromoteClaimService) Promote(ctx context.Context, profileID string, claimID string) (*domain.Fact, error) {
	if m.promoteFunc != nil {
		return m.promoteFunc(ctx, profileID, claimID)
	}
	now := time.Now()
	return &domain.Fact{
		FactID:              uuid.NewString(),
		ProfileID:           profileID,
		PromotedFromClaimID: claimID,
		Status:              domain.FactStatusActive,
		RecordedAt:          now,
	}, nil
}

// TestClaimPromoteHandler verifies that a successful promotion returns 201 with a FactResponse.
func TestClaimPromoteHandler(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	profileID := uuid.New()
	claimID := "claim-abc-123"
	factID := uuid.NewString()
	now := time.Now()

	svc := &mockPromoteClaimService{
		promoteFunc: func(ctx context.Context, pid, cid string) (*domain.Fact, error) {
			return &domain.Fact{
				FactID:              factID,
				ProfileID:           pid,
				Subject:             "Alice",
				Predicate:           "knows",
				Object:              "Bob",
				Status:              domain.FactStatusActive,
				TruthScore:          0.9,
				PromotedFromClaimID: cid,
				RecordedAt:          now,
			}, nil
		},
	}
	h := NewClaimPromoteHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/promote", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/"+claimID+"/promote", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201. body=%s", rec.Code, rec.Body.String())
	}

	var resp dto.FactResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if resp.FactID != factID {
		t.Errorf("fact_id = %q; want %q", resp.FactID, factID)
	}
	if resp.ProfileID != profileID.String() {
		t.Errorf("team_id = %q; want %q", resp.ProfileID, profileID.String())
	}
	if resp.PromotedFromClaimID != claimID {
		t.Errorf("promoted_from_claim_id = %q; want %q", resp.PromotedFromClaimID, claimID)
	}
	if resp.Status != "active" {
		t.Errorf("status = %q; want %q", resp.Status, "active")
	}
}

// TestClaimPromoteHandler_Returns400WhenProfileMissing verifies missing profile → 400.
func TestClaimPromoteHandler_Returns400WhenProfileMissing(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	h := NewClaimPromoteHandler(&mockPromoteClaimService{})
	// No middleware injecting profile ID.
	e.POST("/api/v1/claims/:id/promote", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/some-id/promote", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimPromoteHandler_Returns422OnPredicateNotPoliced verifies unknown predicate → 422.
func TestClaimPromoteHandler_Returns422OnPredicateNotPoliced(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	profileID := uuid.New()

	svc := &mockPromoteClaimService{
		promoteFunc: func(ctx context.Context, pid, cid string) (*domain.Fact, error) {
			return nil, factservice.ErrPredicateNotPoliced
		},
	}
	h := NewClaimPromoteHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/promote", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/claim-xyz/promote", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d; want 422. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimPromoteHandler_Returns409OnClaimNotValidated verifies unvalidated claim → 409.
func TestClaimPromoteHandler_Returns409OnClaimNotValidated(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	profileID := uuid.New()

	svc := &mockPromoteClaimService{
		promoteFunc: func(ctx context.Context, pid, cid string) (*domain.Fact, error) {
			return nil, factservice.ErrClaimNotValidated
		},
	}
	h := NewClaimPromoteHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/promote", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/claim-xyz/promote", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d; want 409. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimPromoteHandler_Returns409OnGateRejected verifies gate failure → 409.
func TestClaimPromoteHandler_Returns409OnGateRejected(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	profileID := uuid.New()

	svc := &mockPromoteClaimService{
		promoteFunc: func(ctx context.Context, pid, cid string) (*domain.Fact, error) {
			return nil, factservice.ErrGateRejected
		},
	}
	h := NewClaimPromoteHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/promote", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/claim-xyz/promote", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d; want 409. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimPromoteHandler_Returns409OnDisputed verifies disputed promotion → 409.
func TestClaimPromoteHandler_Returns409OnDisputed(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	profileID := uuid.New()

	svc := &mockPromoteClaimService{
		promoteFunc: func(ctx context.Context, pid, cid string) (*domain.Fact, error) {
			return nil, factservice.ErrPromotionDeferredDisputed
		},
	}
	h := NewClaimPromoteHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/promote", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/claim-xyz/promote", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d; want 409. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimPromoteHandler_Returns409OnRejectedWeaker verifies weaker claim → 409.
func TestClaimPromoteHandler_Returns409OnRejectedWeaker(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	profileID := uuid.New()

	svc := &mockPromoteClaimService{
		promoteFunc: func(ctx context.Context, pid, cid string) (*domain.Fact, error) {
			return nil, factservice.ErrPromotionRejected
		},
	}
	h := NewClaimPromoteHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/promote", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/claim-xyz/promote", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d; want 409. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimPromoteHandler_CrossProfileIsolation verifies that profile B cannot access
// a claim belonging to profile A. The service denies cross-profile access and
// the handler returns an error response that does not contain profile A's fact data.
func TestClaimPromoteHandler_CrossProfileIsolation(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	profileA := uuid.New()
	profileB := uuid.New()

	claimOwnedByA := "claim-owned-by-a"
	factIDForA := uuid.NewString()
	now := time.Now()

	svc := &mockPromoteClaimService{
		promoteFunc: func(ctx context.Context, pid, cid string) (*domain.Fact, error) {
			// Profile isolation: only profileA can promote its own claim.
			if pid == profileA.String() && cid == claimOwnedByA {
				return &domain.Fact{
					FactID:              factIDForA,
					ProfileID:           pid,
					PromotedFromClaimID: cid,
					Status:              domain.FactStatusActive,
					RecordedAt:          now,
				}, nil
			}
			// All other profiles receive a denial — indistinguishable from
			// "not found" so no cross-profile existence is leaked.
			return nil, factservice.ErrClaimNotFound
		},
	}
	h := NewClaimPromoteHandler(svc)

	// Request as profileB attempting to promote profileA's claim.
	e.Use(injectProfileMiddleware(profileB))
	e.POST("/api/v1/claims/:id/promote", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/"+claimOwnedByA+"/promote", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-profile promote returned %d; want 404 (isolation must not leak existence). body=%s", rec.Code, rec.Body.String())
	}

	// Profile A's fact ID must not appear in profile B's response.
	body := rec.Body.String()
	if strings.Contains(body, factIDForA) {
		t.Errorf("response must not contain profileA's fact ID; got: %s", body)
	}
}

// Compile-time companion interface check.
var _ ClaimPromoteHandlerInterface = (*ClaimPromoteHandler)(nil)
