package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestWrapRegistryConsumesRememberQuotaBeforeInvoke(t *testing.T) {
	q := DefaultQuotas()
	q.WriteAttempts = 1
	q.EvidenceAttempts = 1
	q.EvidenceBytes = 4
	q.VerifierAttempts = 1
	manager := NewQuotaManager(&fakeCounterStore{}, q)
	reg := registry.New()
	calls := 0
	require.NoError(t, reg.Register(registry.Tool{
		Name: "remember",
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			calls++
			return map[string]any{"ok": true}, nil
		},
	}))

	wrapped, err := WrapRegistry(reg, manager)
	require.NoError(t, err)
	remember, ok := wrapped.Get("remember")
	require.True(t, ok)

	input := map[string]any{"evidence": []any{map[string]any{"content": "demo"}}}
	out, err := remember.Invoke(context.Background(), "team-1", input)
	require.NoError(t, err)
	require.Equal(t, true, out["ok"])

	_, err = remember.Invoke(context.Background(), "team-1", input)
	requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
	require.Equal(t, 1, calls)
}

func TestQuotaLimitsUseEvidenceFieldNames(t *testing.T) {
	payload, err := json.Marshal(DefaultQuotas().Limits())
	require.NoError(t, err)

	var values map[string]any
	require.NoError(t, json.Unmarshal(payload, &values))
	require.Contains(t, values, "evidence_attempts")
	require.Contains(t, values, "evidence_bytes")
	require.NotContains(t, values, "fragment_attempts")
	require.NotContains(t, values, "fragment_bytes")
}

func TestWrapRegistryConsumesRecallQuota(t *testing.T) {
	q := DefaultQuotas()
	q.RecallCalls = 1
	manager := NewQuotaManager(&fakeCounterStore{}, q)
	reg := registry.New()
	calls := 0
	require.NoError(t, reg.Register(registry.Tool{
		Name: "recall_memory",
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			calls++
			return map[string]any{"ok": true}, nil
		},
	}))

	wrapped, err := WrapRegistry(reg, manager)
	require.NoError(t, err)
	recall, ok := wrapped.Get("recall_memory")
	require.True(t, ok)

	_, err = recall.Invoke(context.Background(), "team-1", nil)
	require.NoError(t, err)
	_, err = recall.Invoke(context.Background(), "team-1", nil)
	requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
	require.Equal(t, 1, calls)
}

func TestWrapRegistryConsumesRetractEvidenceQuota(t *testing.T) {
	q := DefaultQuotas()
	q.WriteAttempts = 1
	manager := NewQuotaManager(&fakeCounterStore{}, q)
	reg := registry.New()
	calls := 0
	require.NoError(t, reg.Register(registry.Tool{
		Name: registry.ToolRetractEvidence,
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			calls++
			return map[string]any{"ok": true}, nil
		},
	}))

	wrapped, err := WrapRegistry(reg, manager)
	require.NoError(t, err)
	retract, ok := wrapped.Get(registry.ToolRetractEvidence)
	require.True(t, ok)

	_, err = retract.Invoke(context.Background(), "team-1", nil)
	require.NoError(t, err)
	_, err = retract.Invoke(context.Background(), "team-1", nil)
	requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
	require.Equal(t, 1, calls)
}

func TestRequestQuotaMiddlewareConsumesResolvedTeam(t *testing.T) {
	q := DefaultQuotas()
	q.TotalRequests = 1
	manager := NewQuotaManager(&fakeCounterStore{}, q)
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(RequestQuotaMiddleware(manager))
	e.GET("/", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})
	teamID := uuid.New()

	for i, want := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		rec := httptest.NewRecorder()
		ctx := httpmw.SetResolvedTeamIDForTest(context.Background(), teamID)
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		e.ServeHTTP(rec, req)
		require.Equal(t, want, rec.Code, "request %d", i+1)
	}
}

type fakeCounterStore struct {
	mu     sync.Mutex
	values map[string]int64
}

func (s *fakeCounterStore) IncrWithExpire(ctx context.Context, key string, expireSeconds int64) (int64, error) {
	return s.AddWithExpire(ctx, key, 1, expireSeconds)
}

func (s *fakeCounterStore) AddWithExpire(_ context.Context, key string, delta int64, _ int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]int64{}
	}
	s.values[key] += delta
	return s.values[key], nil
}

func requireAPIErrorCode(t *testing.T, err error, code httperr.ErrorCode) {
	t.Helper()
	var apiErr *httperr.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, code, apiErr.Code)
}
