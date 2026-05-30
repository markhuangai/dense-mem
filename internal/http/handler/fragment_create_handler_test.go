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
	"github.com/markhuangai/dense-mem/internal/embedding"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
)

// mockCreateFragmentService implements fragmentservice.CreateFragmentService for testing.
type mockCreateFragmentService struct {
	createFunc func(ctx context.Context, profileID string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error)
}

func (m *mockCreateFragmentService) Create(ctx context.Context, profileID string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
	if m.createFunc != nil {
		return m.createFunc(ctx, profileID, req)
	}
	return &fragmentservice.CreateResult{
		Fragment: &domain.Fragment{
			FragmentID:          "frag-new",
			ProfileID:           profileID,
			Content:             req.Content,
			SourceType:          domain.SourceTypeManual,
			ContentHash:         "abc",
			EmbeddingModel:      "m1",
			EmbeddingDimensions: 4,
		},
		Duplicate: false,
	}, nil
}

func injectProfileMiddleware(profileID uuid.UUID) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// TestFragmentCreateHandler_Returns201OnCreate — backpressure test + AC-25 new fragment.
func TestFragmentCreateHandler_Returns201OnCreate(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &mockCreateFragmentService{}
	h := NewFragmentCreateHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/fragments", h.Handle)

	body := `{"content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fragments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d. body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var resp dto.FragmentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if resp.ID == "" {
		t.Error("response missing id")
	}
	// AC-28: embedding vector must not appear in response; FragmentResponse has no vector field,
	// so check that the raw JSON does not contain an embedding array key.
	if strings.Contains(rec.Body.String(), `"embedding":`) {
		t.Errorf("response leaked embedding vector: %s", rec.Body.String())
	}
}

// TestFragmentCreateHandler_Returns200AndReplayHeaderOnDuplicate — AC-25 replay.
func TestFragmentCreateHandler_Returns200AndReplayHeaderOnDuplicate(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &mockCreateFragmentService{
		createFunc: func(ctx context.Context, pid string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
			return &fragmentservice.CreateResult{
				Fragment: &domain.Fragment{
					FragmentID: "frag-existing",
					ProfileID:  pid,
					Content:    req.Content,
					SourceType: domain.SourceTypeManual,
				},
				Duplicate:   true,
				DuplicateOf: "frag-existing",
			}, nil
		},
	}
	h := NewFragmentCreateHandler(svc)

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/fragments", h.Handle)

	body := `{"content":"x","idempotency_key":"k1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fragments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Idempotent-Replay"); got != "true" {
		t.Errorf("X-Idempotent-Replay = %q; want true", got)
	}
}

// TestFragmentCreateHandler_Returns400OnMalformedJSON — AC-17 validation.
func TestFragmentCreateHandler_Returns400OnMalformedJSON(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	h := NewFragmentCreateHandler(&mockCreateFragmentService{})
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/fragments", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/fragments", strings.NewReader(`{malformed`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// VALIDATION_ERROR maps to 422
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d; want %d. body=%s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
}

// TestFragmentCreateHandler_Returns422OnBlankContent — AC-17 validation.
func TestFragmentCreateHandler_Returns422OnBlankContent(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	h := NewFragmentCreateHandler(&mockCreateFragmentService{})
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/fragments", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/fragments", strings.NewReader(`{"content":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d; want %d. body=%s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
}

// TestFragmentCreateHandler_Returns400WhenProfileMissing — AC-17 profile required.
func TestFragmentCreateHandler_Returns400WhenProfileMissing(t *testing.T) {
	e := echo.New()
	h := NewFragmentCreateHandler(&mockCreateFragmentService{})
	e.HTTPErrorHandler = httperr.ErrorHandler

	// No middleware injecting profile ID
	e.POST("/api/v1/fragments", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/fragments", strings.NewReader(`{"content":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (PROFILE_ID_REQUIRED). body=%s", rec.Code, rec.Body.String())
	}
}

// TestFragmentCreateHandler_EmbeddingFailureMapsTo503 — AC-17 error mapping.
func TestFragmentCreateHandler_EmbeddingFailureMapsTo503(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &mockCreateFragmentService{
		createFunc: func(ctx context.Context, pid string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
			return nil, errors.Join(fragmentservice.ErrEmbeddingFailed, embedding.ErrEmbeddingProvider)
		},
	}
	h := NewFragmentCreateHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.POST("/api/v1/fragments", h.Handle)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/fragments", strings.NewReader(`{"content":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503. body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleFragmentCreateErrorMapsKnownErrors(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		code    httperr.ErrorCode
		message string
	}{
		{"nil", nil, "", ""},
		{"embedding timeout", embedding.ErrEmbeddingTimeout, httperr.SERVICE_UNAVAILABLE, "embedding request timed out"},
		{"embedding provider", embedding.ErrEmbeddingProvider, httperr.SERVICE_UNAVAILABLE, "embedding service unavailable"},
		{"embedding rate limit", embedding.ErrEmbeddingRateLimit, httperr.SERVICE_UNAVAILABLE, "embedding service rate limited"},
		{"embedding failed", fragmentservice.ErrEmbeddingFailed, httperr.SERVICE_UNAVAILABLE, "embedding service unavailable"},
		{"context deadline", context.DeadlineExceeded, httperr.SERVICE_UNAVAILABLE, "request timed out"},
		{"default", errors.New("write failed"), httperr.INTERNAL_ERROR, "failed to create fragment"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := handleFragmentCreateError(context.Background(), tc.err)
			if tc.err == nil {
				if got != nil {
					t.Fatalf("handleFragmentCreateError(nil) = %v; want nil", got)
				}
				return
			}
			if got.Code != tc.code {
				t.Fatalf("code = %s; want %s", got.Code, tc.code)
			}
			if got.Message != tc.message {
				t.Fatalf("message = %q; want %q", got.Message, tc.message)
			}
		})
	}
}

// Compile-time companion interface check (already in handler.go)
var _ FragmentCreateHandlerInterface = (*FragmentCreateHandler)(nil)
