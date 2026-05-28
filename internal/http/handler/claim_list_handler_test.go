package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
)

// mockListClaimsService implements claimservice.ListClaimsService for testing.
type mockListClaimsService struct {
	listFunc func(ctx context.Context, profileID string, limit, offset int) ([]*domain.Claim, int, error)
}

func (m *mockListClaimsService) List(ctx context.Context, profileID string, limit, offset int) ([]*domain.Claim, int, error) {
	if m.listFunc != nil {
		return m.listFunc(ctx, profileID, limit, offset)
	}
	return []*domain.Claim{
		{ClaimID: "c1", ProfileID: profileID},
		{ClaimID: "c2", ProfileID: profileID},
	}, 2, nil
}

type mockListClaimsFilteredService struct {
	listFunc func(ctx context.Context, profileID string, opts claimservice.ListClaimOptions) (*claimservice.ListClaimsResult, error)
}

func (m *mockListClaimsFilteredService) List(ctx context.Context, profileID string, opts claimservice.ListClaimOptions) (*claimservice.ListClaimsResult, error) {
	if m.listFunc != nil {
		return m.listFunc(ctx, profileID, opts)
	}
	return &claimservice.ListClaimsResult{}, nil
}

// TestClaimListHandler_Returns200WithItems verifies a successful list returns 200 with items.
func TestClaimListHandler_Returns200WithItems(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &mockListClaimsService{}
	h := NewClaimListHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/claims", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/claims", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}

	var resp dto.ListClaimsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if len(resp.Items) != 2 {
		t.Errorf("items count = %d; want 2", len(resp.Items))
	}
}

// TestClaimListHandler_Returns200EmptyList verifies an empty result is handled correctly.
func TestClaimListHandler_Returns200EmptyList(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &mockListClaimsService{
		listFunc: func(ctx context.Context, pid string, limit, offset int) ([]*domain.Claim, int, error) {
			return []*domain.Claim{}, 0, nil
		},
	}
	h := NewClaimListHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/claims", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/claims", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}

	var resp dto.ListClaimsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if len(resp.Items) != 0 {
		t.Errorf("items count = %d; want 0", len(resp.Items))
	}
	if resp.HasMore {
		t.Error("has_more should be false for empty result")
	}
}

// TestClaimListHandler_HasMoreWhenMoreResultsExist verifies next_cursor is set when total > page size.
func TestClaimListHandler_HasMoreWhenMoreResultsExist(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &mockListClaimsService{
		listFunc: func(ctx context.Context, pid string, limit, offset int) ([]*domain.Claim, int, error) {
			claims := []*domain.Claim{
				{ClaimID: "c1", ProfileID: pid},
			}
			// total=5 means there are more results beyond offset+len(claims)
			return claims, 5, nil
		},
	}
	h := NewClaimListHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/claims", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/claims?limit=1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}

	var resp dto.ListClaimsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if !resp.HasMore {
		t.Error("has_more should be true when more results exist")
	}
	if resp.NextCursor == "" {
		t.Error("next_cursor should be non-empty when has_more is true")
	}
}

func TestClaimListHandler_UsesFilteredKeysetServiceForNewCursors(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	compatCalled := false
	compat := &mockListClaimsService{
		listFunc: func(ctx context.Context, pid string, limit, offset int) ([]*domain.Claim, int, error) {
			compatCalled = true
			return nil, 0, nil
		},
	}
	var captured claimservice.ListClaimOptions
	filtered := &mockListClaimsFilteredService{
		listFunc: func(ctx context.Context, pid string, opts claimservice.ListClaimOptions) (*claimservice.ListClaimsResult, error) {
			if pid != profileID.String() {
				t.Fatalf("profileID = %q; want %q", pid, profileID.String())
			}
			captured = opts
			return &claimservice.ListClaimsResult{
				Items:      []*domain.Claim{{ClaimID: "c1", ProfileID: pid}},
				NextCursor: "keyset-cursor",
				HasMore:    true,
			}, nil
		},
	}
	h := NewClaimListHandlerWithFiltered(compat, filtered)

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/claims", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/claims?limit=1&status=validated&modality=assertion", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	if compatCalled {
		t.Fatal("legacy offset service should not be called for keyset listing")
	}
	if captured.Limit != 1 || captured.Status != "validated" || captured.Modality != "assertion" {
		t.Fatalf("filtered opts = %+v; want limit/status/modality propagated", captured)
	}
	var resp dto.ListClaimsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if resp.NextCursor != "keyset-cursor" || !resp.HasMore {
		t.Fatalf("next_cursor/has_more = %q/%v; want keyset-cursor/true", resp.NextCursor, resp.HasMore)
	}
}

func TestClaimListHandler_NumericCursorUsesLegacyOffsetFallback(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	var capturedLimit, capturedOffset int
	compat := &mockListClaimsService{
		listFunc: func(ctx context.Context, pid string, limit, offset int) ([]*domain.Claim, int, error) {
			capturedLimit = limit
			capturedOffset = offset
			return []*domain.Claim{}, offset, nil
		},
	}
	filteredCalled := false
	filtered := &mockListClaimsFilteredService{
		listFunc: func(ctx context.Context, profileID string, opts claimservice.ListClaimOptions) (*claimservice.ListClaimsResult, error) {
			filteredCalled = true
			return &claimservice.ListClaimsResult{}, nil
		},
	}
	h := NewClaimListHandlerWithFiltered(compat, filtered)

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/claims", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/claims?limit=5&cursor=20", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	if filteredCalled {
		t.Fatal("filtered service should not be called for legacy numeric cursor")
	}
	if capturedLimit != 5 || capturedOffset != 20 {
		t.Fatalf("legacy limit/offset = %d/%d; want 5/20", capturedLimit, capturedOffset)
	}
}

// TestClaimListHandler_Returns422OnInvalidCursor verifies an unparseable cursor → 422.
func TestClaimListHandler_Returns422OnInvalidCursor(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &mockListClaimsService{}
	h := NewClaimListHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/claims", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/claims?cursor=not-a-number", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d; want 422. body=%s", rec.Code, rec.Body.String())
	}
}

func TestClaimListHandler_Returns422OnInvalidKeysetCursor(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	h := NewClaimListHandlerWithFiltered(&mockListClaimsService{}, &mockListClaimsFilteredService{
		listFunc: func(ctx context.Context, profileID string, opts claimservice.ListClaimOptions) (*claimservice.ListClaimsResult, error) {
			return nil, claimservice.ErrInvalidClaimCursor
		},
	})
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/claims", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/claims?cursor=not-a-number", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d; want 422. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimListHandler_Returns400WhenProfileMissing verifies missing profile → 400.
func TestClaimListHandler_Returns400WhenProfileMissing(t *testing.T) {
	e := echo.New()
	h := NewClaimListHandler(&mockListClaimsService{})
	e.HTTPErrorHandler = httperr.ErrorHandler

	// No middleware injecting profile ID.
	e.GET("/api/v1/claims", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/claims", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400. body=%s", rec.Code, rec.Body.String())
	}
}

// TestClaimListHandler_CrossProfileIsolation verifies the service only receives the resolved profileID.
func TestClaimListHandler_CrossProfileIsolation(t *testing.T) {
	e := echo.New()
	profileA := uuid.New()
	profileB := uuid.New()

	var capturedProfileID string
	svc := &mockListClaimsService{
		listFunc: func(ctx context.Context, pid string, limit, offset int) ([]*domain.Claim, int, error) {
			capturedProfileID = pid
			return []*domain.Claim{}, 0, nil
		},
	}
	h := NewClaimListHandler(svc)

	// Only profileA is injected.
	e.Use(injectProfileMiddleware(profileA))
	e.GET("/api/v1/claims", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/claims", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	if capturedProfileID != profileA.String() {
		t.Errorf("service received profileID %q; want %q", capturedProfileID, profileA.String())
	}
	if capturedProfileID == profileB.String() {
		t.Error("service received profileB ID — cross-profile isolation violated")
	}
}

// TestClaimListHandler_Returns422OnLimitExceedsMax verifies that limit > 100 is rejected by
// the struct validator (DTO tag: validate:"max=100") before the service is called.
func TestClaimListHandler_Returns422OnLimitExceedsMax(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &mockListClaimsService{}
	h := NewClaimListHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/claims", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/claims?limit=101", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d; want 422 (limit exceeds maxClaimListLimit=%d). body=%s",
			rec.Code, maxClaimListLimit, rec.Body.String())
	}
}

// Compile-time companion interface check.
var _ ClaimListHandlerInterface = (*ClaimListHandler)(nil)

// Ensure claimservice import is not flagged unused by confirming usage of package symbol.
var _ claimservice.ListClaimsService = (*mockListClaimsService)(nil)
