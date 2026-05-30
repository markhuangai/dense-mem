package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
)

// mockGetFactService implements factservice.GetFactService for testing.
type mockGetFactService struct {
	getFunc func(ctx context.Context, profileID, factID string) (*domain.Fact, error)
}

func (m *mockGetFactService) Get(ctx context.Context, profileID, factID string) (*domain.Fact, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, profileID, factID)
	}
	return nil, factservice.ErrFactNotFound
}

// Ensure mockGetFactService satisfies the interface.
var _ factservice.GetFactService = (*mockGetFactService)(nil)

// TestFactReadHandler_Returns200InScope verifies a successful in-scope read returns 200.
func TestFactReadHandler_Returns200InScope(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &mockGetFactService{
		getFunc: func(ctx context.Context, pid, fid string) (*domain.Fact, error) {
			if pid != profileID.String() {
				t.Errorf("service got profileID %q; want %q", pid, profileID.String())
			}
			return &domain.Fact{
				FactID:    fid,
				ProfileID: pid,
				Subject:   "sky",
				Predicate: "is",
				Object:    "blue",
				Status:    domain.FactStatusActive,
			}, nil
		},
	}
	h := NewFactReadHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/facts/:id", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/facts/fact-1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
}

// TestFactReadHandler_Returns404ForCrossProfile — profile isolation: cross-profile reads return 404.
func TestFactReadHandler_Returns404ForCrossProfile(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &mockGetFactService{
		getFunc: func(ctx context.Context, pid, fid string) (*domain.Fact, error) {
			// Service returns ErrFactNotFound for cross-profile reads (existence leakage prevention).
			return nil, factservice.ErrFactNotFound
		},
	}
	h := NewFactReadHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/facts/:id", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/facts/other-profile-fact", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404. body=%s", rec.Code, rec.Body.String())
	}
}

// TestFactReadHandler_Returns404OnMissing verifies a missing fact returns 404.
func TestFactReadHandler_Returns404OnMissing(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &mockGetFactService{} // default returns ErrFactNotFound
	h := NewFactReadHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/facts/:id", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/facts/does-not-exist", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", rec.Code)
	}
}

func TestFactReadHandler_TemporalFilterReturns404WhenFactOutsideWindow(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	validFrom := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	validTo := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	svc := &mockGetFactService{
		getFunc: func(ctx context.Context, pid, fid string) (*domain.Fact, error) {
			return &domain.Fact{
				FactID:    fid,
				ProfileID: pid,
				Status:    domain.FactStatusActive,
				ValidFrom: &validFrom,
				ValidTo:   &validTo,
			}, nil
		},
	}
	h := NewFactReadHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/facts/:id", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/facts/fact-1?valid_at=2024-03-01T00:00:00Z", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404. body=%s", rec.Code, rec.Body.String())
	}
}

func TestFactMatchesTemporalWindow(t *testing.T) {
	validFrom := time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC)
	validTo := time.Date(2024, 2, 10, 0, 0, 0, 0, time.UTC)
	recordedAt := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	recordedTo := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	beforeValidFrom := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	atValidTo := validTo
	beforeRecordedAt := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	atRecordedTo := recordedTo
	inWindowValid := time.Date(2024, 1, 20, 0, 0, 0, 0, time.UTC)
	inWindowKnown := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)

	fact := &domain.Fact{
		ValidFrom:  &validFrom,
		ValidTo:    &validTo,
		RecordedAt: recordedAt,
		RecordedTo: &recordedTo,
	}

	cases := []struct {
		name    string
		fact    *domain.Fact
		validAt *time.Time
		knownAt *time.Time
		want    bool
	}{
		{name: "nil fact", fact: nil, want: false},
		{name: "valid_at before valid_from", fact: fact, validAt: &beforeValidFrom, want: false},
		{name: "valid_at at valid_to is outside", fact: fact, validAt: &atValidTo, want: false},
		{name: "known_at before recorded_at", fact: fact, knownAt: &beforeRecordedAt, want: false},
		{name: "known_at at recorded_to is outside", fact: fact, knownAt: &atRecordedTo, want: false},
		{name: "inside valid and known windows", fact: fact, validAt: &inWindowValid, knownAt: &inWindowKnown, want: true},
		{name: "open ended windows pass", fact: &domain.Fact{RecordedAt: recordedAt}, validAt: &inWindowValid, knownAt: &inWindowKnown, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := factMatchesTemporalWindow(tc.fact, tc.validAt, tc.knownAt)
			if got != tc.want {
				t.Fatalf("factMatchesTemporalWindow() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestFactReadHandler_Returns400WhenProfileMissing verifies missing profile ID returns 400.
func TestFactReadHandler_Returns400WhenProfileMissing(t *testing.T) {
	e := echo.New()
	h := NewFactReadHandler(&mockGetFactService{})
	e.HTTPErrorHandler = httperr.ErrorHandler

	// No middleware injecting profile ID.
	e.GET("/api/v1/facts/:id", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/facts/fact-1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", rec.Code)
	}
}

// TestFactReadHandler_CrossProfileIsolation verifies the handler passes only the
// resolved profileID to the service — never any caller-supplied value.
func TestFactReadHandler_CrossProfileIsolation(t *testing.T) {
	e := echo.New()
	profileA := uuid.New()
	profileB := uuid.New()

	var capturedProfileID string
	svc := &mockGetFactService{
		getFunc: func(ctx context.Context, pid, fid string) (*domain.Fact, error) {
			capturedProfileID = pid
			return &domain.Fact{
				FactID:    fid,
				ProfileID: pid,
				Status:    domain.FactStatusActive,
			}, nil
		},
	}
	h := NewFactReadHandler(svc)

	// Only profileA is injected via middleware.
	e.Use(injectProfileMiddleware(profileA))
	e.GET("/api/v1/facts/:id", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/facts/fact-1", nil)
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

func TestFactReadHandlerValidationAndErrorBranches(t *testing.T) {
	t.Run("empty id", func(t *testing.T) {
		e := echo.New()
		e.HTTPErrorHandler = httperr.ErrorHandler
		profileID := uuid.New()
		h := NewFactReadHandler(&mockGetFactService{})
		e.Use(injectProfileMiddleware(profileID))
		e.GET("/api/v1/facts", h.Handle)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/facts", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d; want 422", rec.Code)
		}
	})

	t.Run("service error", func(t *testing.T) {
		e := echo.New()
		e.HTTPErrorHandler = httperr.ErrorHandler
		profileID := uuid.New()
		h := NewFactReadHandler(&mockGetFactService{getFunc: func(context.Context, string, string) (*domain.Fact, error) {
			return nil, errors.New("neo4j down")
		}})
		e.Use(injectProfileMiddleware(profileID))
		e.GET("/api/v1/facts/:id", h.Handle)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/facts/fact-1", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d; want 500", rec.Code)
		}
	})

	t.Run("invalid temporal query params", func(t *testing.T) {
		cases := []string{
			"/api/v1/facts/fact-1?valid_at=bad",
			"/api/v1/facts/fact-1?known_at=bad",
		}
		for _, path := range cases {
			e := echo.New()
			e.HTTPErrorHandler = httperr.ErrorHandler
			profileID := uuid.New()
			h := NewFactReadHandler(&mockGetFactService{getFunc: func(ctx context.Context, pid, fid string) (*domain.Fact, error) {
				return &domain.Fact{FactID: fid, ProfileID: pid, Status: domain.FactStatusActive}, nil
			}})
			e.Use(injectProfileMiddleware(profileID))
			e.GET("/api/v1/facts/:id", h.Handle)

			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s status = %d; want 422", path, rec.Code)
			}
		}
	})

	t.Run("include evidence preserves evidence", func(t *testing.T) {
		e := echo.New()
		profileID := uuid.New()
		h := NewFactReadHandler(&mockGetFactService{getFunc: func(ctx context.Context, pid, fid string) (*domain.Fact, error) {
			return &domain.Fact{
				FactID:    fid,
				ProfileID: pid,
				Status:    domain.FactStatusActive,
				Evidence:  []domain.Evidence{{FragmentID: "fragment-1"}},
			}, nil
		}})
		e.Use(injectProfileMiddleware(profileID))
		e.GET("/api/v1/facts/:id", h.Handle)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/facts/fact-1?include_evidence=true", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
		}
	})
}

// Compile-time companion interface check.
var _ FactReadHandlerInterface = (*FactReadHandler)(nil)
