package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
)

type mockRetractFactService struct {
	retractFunc func(ctx context.Context, profileID string, factID string) error
}

func (m *mockRetractFactService) Retract(ctx context.Context, profileID string, factID string) error {
	if m.retractFunc != nil {
		return m.retractFunc(ctx, profileID, factID)
	}
	return nil
}

func TestFactRetractHandler(t *testing.T) {
	t.Run("Returns200WithStatusOnSuccess", func(t *testing.T) {
		e := echo.New()
		e.HTTPErrorHandler = httperr.ErrorHandler
		profileID := uuid.New()
		h := NewFactRetractHandler(&mockRetractFactService{})

		e.Use(injectProfileMiddleware(profileID))
		e.POST("/api/v1/facts/:id/retract", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/facts/fact-abc/retract", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
		}
		if body["status"] != "retracted" {
			t.Errorf("body[status] = %q; want retracted", body["status"])
		}
	})

	t.Run("Returns404OnFactNotFound", func(t *testing.T) {
		e := echo.New()
		e.HTTPErrorHandler = httperr.ErrorHandler
		profileID := uuid.New()
		h := NewFactRetractHandler(&mockRetractFactService{
			retractFunc: func(context.Context, string, string) error {
				return factservice.ErrFactNotFound
			},
		})

		e.Use(injectProfileMiddleware(profileID))
		e.POST("/api/v1/facts/:id/retract", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/facts/missing/retract", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d; want 404. body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("Returns403OnOwnerMismatch", func(t *testing.T) {
		e := echo.New()
		e.HTTPErrorHandler = httperr.ErrorHandler
		profileID := uuid.New()
		h := NewFactRetractHandler(&mockRetractFactService{
			retractFunc: func(context.Context, string, string) error {
				return ownership.ErrOwnerMismatch
			},
		})

		e.Use(injectProfileMiddleware(profileID))
		e.POST("/api/v1/facts/:id/retract", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/facts/fact-owned-by-a/retract", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d; want 403. body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("Returns400OnMissingProfileID", func(t *testing.T) {
		e := echo.New()
		e.HTTPErrorHandler = httperr.ErrorHandler
		h := NewFactRetractHandler(&mockRetractFactService{})

		e.POST("/api/v1/facts/:id/retract", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/facts/fact-abc/retract", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d; want 400. body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("Returns500OnServiceError", func(t *testing.T) {
		e := echo.New()
		e.HTTPErrorHandler = httperr.ErrorHandler
		profileID := uuid.New()
		h := NewFactRetractHandler(&mockRetractFactService{
			retractFunc: func(context.Context, string, string) error {
				return context.DeadlineExceeded
			},
		})

		e.Use(injectProfileMiddleware(profileID))
		e.POST("/api/v1/facts/:id/retract", h.Handle)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/facts/fact-abc/retract", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d; want 500. body=%s", rec.Code, rec.Body.String())
		}
	})
}
