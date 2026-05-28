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
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// mockVerifyClaimService implements claimservice.VerifyClaimService for testing.
type mockVerifyClaimService struct {
	verifyFunc func(ctx context.Context, profileID string, claimID string) (*domain.Claim, error)
}

func (m *mockVerifyClaimService) Verify(ctx context.Context, profileID string, claimID string) (*domain.Claim, error) {
	if m.verifyFunc != nil {
		return m.verifyFunc(ctx, profileID, claimID)
	}
	now := time.Now()
	return &domain.Claim{
		ClaimID:           claimID,
		ProfileID:         profileID,
		EntailmentVerdict: domain.VerdictEntailed,
		Status:            domain.StatusValidated,
		VerifiedAt:        &now,
	}, nil
}

// TestClaimVerifyHandler_Returns200OnSuccess verifies a successful verification returns 200.
func TestClaimVerifyHandler_Returns200OnSuccess(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	claimID := "claim-xyz"
	now := time.Now()

	svc := &mockVerifyClaimService{
		verifyFunc: func(ctx context.Context, pid, cid string) (*domain.Claim, error) {
			return &domain.Claim{
				ClaimID:              cid,
				ProfileID:            pid,
				EntailmentVerdict:    domain.VerdictEntailed,
				Status:               domain.StatusValidated,
				VerifiedAt:           &now,
				VerifierModel:        "gpt-4o-mini",
				LastVerifierResponse: "entailed with high confidence",
			}, nil
		},
	}
	h := NewClaimVerifyHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/verify", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/"+claimID+"/verify", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}

	var resp dto.VerifyClaimResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if resp.ClaimID != claimID {
		t.Errorf("claim_id = %q; want %q", resp.ClaimID, claimID)
	}
	if resp.EntailmentVerdict != "entailed" {
		t.Errorf("entailment_verdict = %q; want %q", resp.EntailmentVerdict, "entailed")
	}
	if resp.Status != "validated" {
		t.Errorf("status = %q; want %q", resp.Status, "validated")
	}
}

// TestClaimVerifyHandler_Returns404OnNotFound verifies missing claim → 404.
func TestClaimVerifyHandler_Returns404OnNotFound(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	svc := &mockVerifyClaimService{
		verifyFunc: func(ctx context.Context, pid, cid string) (*domain.Claim, error) {
			return nil, claimservice.ErrClaimNotFound
		},
	}
	h := NewClaimVerifyHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/verify", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/nonexistent/verify", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimVerifyHandler_Returns404OnSupportingFragmentMissing verifies
// missing or retracted support is surfaced as a stable public 404.
func TestClaimVerifyHandler_Returns404OnSupportingFragmentMissing(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	svc := &mockVerifyClaimService{
		verifyFunc: func(ctx context.Context, pid, cid string) (*domain.Claim, error) {
			return nil, claimservice.ErrSupportingFragmentMissing
		},
	}
	h := NewClaimVerifyHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/verify", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/claim-missing-support/verify", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404. body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), string(httperr.ErrSupportingFragmentMissing)) {
		t.Errorf("body must contain supporting_fragment_missing; got: %s", rec.Body.String())
	}
}

// TestClaimVerifyHandler_Returns400WhenProfileMissing verifies missing profile → 400.
func TestClaimVerifyHandler_Returns400WhenProfileMissing(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	h := NewClaimVerifyHandler(&mockVerifyClaimService{})
	// No middleware injecting profile ID.
	e.POST("/api/v1/claims/:id/verify", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/some-id/verify", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimVerifyHandler_Returns429OnVerifierRateLimit verifies rate-limit → 429 with Retry-After.
func TestClaimVerifyHandler_Returns429OnVerifierRateLimit(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	svc := &mockVerifyClaimService{
		verifyFunc: func(ctx context.Context, pid, cid string) (*domain.Claim, error) {
			return nil, &verifier.RateLimitError{
				Provider:   "openai",
				Message:    "rate limited",
				RetryAfter: 30,
			}
		},
	}
	h := NewClaimVerifyHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/verify", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/claim-abc/verify", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d; want 429. body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q; want %q", got, "30")
	}
}

// TestClaimVerifyHandler_Returns504OnVerifierTimeout verifies timeout → 504.
func TestClaimVerifyHandler_Returns504OnVerifierTimeout(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	svc := &mockVerifyClaimService{
		verifyFunc: func(ctx context.Context, pid, cid string) (*domain.Claim, error) {
			return nil, &verifier.TimeoutError{Provider: "openai", Message: "deadline exceeded"}
		},
	}
	h := NewClaimVerifyHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/verify", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/claim-abc/verify", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d; want 504. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimVerifyHandler_Returns503OnVerifierProvider verifies provider error → 503.
func TestClaimVerifyHandler_Returns503OnVerifierProvider(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	svc := &mockVerifyClaimService{
		verifyFunc: func(ctx context.Context, pid, cid string) (*domain.Claim, error) {
			return nil, &verifier.ProviderError{Provider: "openai", Message: "upstream error"}
		},
	}
	h := NewClaimVerifyHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/verify", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/claim-abc/verify", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimVerifyHandler_Returns502OnMalformedResponse verifies malformed response → 502.
func TestClaimVerifyHandler_Returns502OnMalformedResponse(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	svc := &mockVerifyClaimService{
		verifyFunc: func(ctx context.Context, pid, cid string) (*domain.Claim, error) {
			return nil, &verifier.MalformedResponseError{Provider: "openai", Message: "invalid json"}
		},
	}
	h := NewClaimVerifyHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/claims/:id/verify", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/claim-abc/verify", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d; want 502. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimVerifyHandler_CrossProfileIsolation verifies that a claim owned by
// profile B is not accessible when requesting as profile A — the service
// returns ErrClaimNotFound for cross-profile reads, and the handler surfaces
// that as 404 (no existence leak).
func TestClaimVerifyHandler_CrossProfileIsolation(t *testing.T) {
	e := echo.New()
	profileA := uuid.New()
	profileB := uuid.New()
	e.HTTPErrorHandler = httperr.ErrorHandler

	claimIDOwnedByB := "claim-owned-by-b"

	svc := &mockVerifyClaimService{
		verifyFunc: func(ctx context.Context, pid, cid string) (*domain.Claim, error) {
			// Isolation: only profileB can access its own claim.
			if pid == profileB.String() && cid == claimIDOwnedByB {
				now := time.Now()
				return &domain.Claim{
					ClaimID:           cid,
					ProfileID:         pid,
					EntailmentVerdict: domain.VerdictEntailed,
					Status:            domain.StatusValidated,
					VerifiedAt:        &now,
				}, nil
			}
			// All other profiles (including profileA) must get not-found.
			return nil, claimservice.ErrClaimNotFound
		},
	}
	h := NewClaimVerifyHandler(svc)

	// Request as profileA attempting to verify profileB's claim.
	e.Use(injectProfileMiddleware(profileA))
	e.POST("/api/v1/claims/:id/verify", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/claims/"+claimIDOwnedByB+"/verify", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-profile verify returned %d; want 404 (isolation must not leak existence)", rec.Code)
	}

	// Verify profileB's ID does NOT appear in profileA's response.
	body := rec.Body.String()
	if strings.Contains(body, profileB.String()) {
		t.Errorf("response must not contain profileB ID; got: %s", body)
	}
}

// Compile-time companion interface check.
var _ ClaimVerifyHandlerInterface = (*ClaimVerifyHandler)(nil)
