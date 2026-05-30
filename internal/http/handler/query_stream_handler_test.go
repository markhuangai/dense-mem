package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/tools/graphquery"
	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
)

// mockQueryStreamOrchestrator implements QueryStreamOrchestrator for testing.
type mockQueryStreamOrchestrator struct {
	runFunc func(ctx context.Context, profileID string, query string, params map[string]any, writer sse.SSEWriter) error
}

func (m *mockQueryStreamOrchestrator) Run(ctx context.Context, profileID string, query string, params map[string]any, writer sse.SSEWriter) error {
	if m.runFunc != nil {
		return m.runFunc(ctx, profileID, query, params, writer)
	}
	// Default: emit tool_call, evidence, done
	_ = writer.WriteEvent(sse.EventTypeToolCall, map[string]any{"name": "test-tool"})
	_ = writer.WriteEvent(sse.EventTypeEvidence, map[string]any{"content": "test evidence", "team_id": profileID})
	_ = writer.WriteEvent(sse.EventTypeDone, map[string]any{})
	return nil
}

// mockStreamLifecycle implements StreamLifecycle for testing.
type mockStreamLifecycle struct {
	startFunc func(ctx context.Context, profileID string, writer sse.SSEWriter, work func(context.Context) error) error
}

func (m *mockStreamLifecycle) Start(ctx context.Context, profileID string, writer sse.SSEWriter, work func(context.Context) error) error {
	if m.startFunc != nil {
		return m.startFunc(ctx, profileID, writer, work)
	}
	// Default: just run the work function
	return work(ctx)
}

// mockSSEWriter implements SSEWriter for testing.
type mockSSEWriter struct {
	events   []mockEvent
	comments []string
	closed   bool
	writeErr error
	closeErr error
}

type mockEvent struct {
	eventType string
	payload   any
}

func (m *mockSSEWriter) WriteEvent(eventType string, payload any) error {
	if m.writeErr != nil {
		return m.writeErr
	}
	if m.closed {
		return sse.ErrStreamClosed
	}
	m.events = append(m.events, mockEvent{eventType: eventType, payload: payload})
	if eventType == sse.EventTypeDone || eventType == sse.EventTypeError {
		m.closed = true
	}
	return nil
}

func (m *mockSSEWriter) WriteComment(text string) error {
	if m.closed {
		return sse.ErrStreamClosed
	}
	m.comments = append(m.comments, text)
	return nil
}

func (m *mockSSEWriter) Close() error {
	m.closed = true
	if m.closeErr != nil {
		return m.closeErr
	}
	return nil
}

// TestQueryStreamSSEFormat tests that emitted events match SSE wire format with correct ordering.
func TestQueryStreamSSEFormat(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockOrch := &mockQueryStreamOrchestrator{
		runFunc: func(ctx context.Context, pid string, query string, params map[string]any, writer sse.SSEWriter) error {
			// Emit events in correct order
			_ = writer.WriteEvent(sse.EventTypeToolCall, map[string]any{"name": "graph-query", "args": map[string]any{"query": query}})
			_ = writer.WriteEvent(sse.EventTypeEvidence, map[string]any{"tool": "graph-query", "team_id": pid, "data": []map[string]any{{"id": "test"}}})
			_ = writer.WriteEvent(sse.EventTypeDone, map[string]any{})
			return nil
		},
	}

	mockLife := &mockStreamLifecycle{}

	h := NewQueryStreamHandler(mockOrch, mockLife)

	// Set resolved profile ID in context
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/profiles/:profileId/query/stream", h.Handle)

	body := `{"query": "MATCH (n) RETURN n LIMIT 10"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/"+profileID.String()+"/query/stream", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Verify SSE headers
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))

	// Verify SSE event format in body
	bodyStr := rec.Body.String()
	assert.Contains(t, bodyStr, "event: tool_call")
	assert.Contains(t, bodyStr, "event: evidence")
	assert.Contains(t, bodyStr, "event: done")

	// Verify correct ordering: tool_call before evidence, evidence before done
	toolCallIdx := strings.Index(bodyStr, "event: tool_call")
	evidenceIdx := strings.Index(bodyStr, "event: evidence")
	doneIdx := strings.Index(bodyStr, "event: done")

	assert.True(t, toolCallIdx >= 0, "tool_call event should be present")
	assert.True(t, evidenceIdx >= 0, "evidence event should be present")
	assert.True(t, doneIdx >= 0, "done event should be present")
	assert.True(t, toolCallIdx < evidenceIdx, "tool_call should come before evidence")
	assert.True(t, evidenceIdx < doneIdx, "evidence should come before done")
}

// TestQueryStreamProfileIsolation tests that evidence payloads contain only authenticated-profile data.
func TestQueryStreamProfileIsolation(t *testing.T) {
	profileID := uuid.New()
	otherProfileID := uuid.New()

	mockOrch := &queryStreamOrchestrator{
		graphQueryService: &mockGraphQueryService{
			executeFunc: func(ctx context.Context, pid string, query string, params map[string]any) (*graphquery.GraphQueryResult, error) {
				// Return rows from both profiles (simulating leak)
				return &graphquery.GraphQueryResult{
					Columns: []string{"id", "content", "team_id"},
					Rows: []map[string]any{
						{"id": "hit-1", "content": "profile-A content", "team_id": pid},
						{"id": "hit-2", "content": "profile-B content", "team_id": otherProfileID.String()},
					},
					RowCount: 2,
				}, nil
			},
		},
		keywordSearchService: &mockKeywordSearchService{
			searchFunc: func(ctx context.Context, pid string, req *keywordsearch.KeywordSearchRequest) (*keywordsearch.KeywordSearchResult, error) {
				// Return hits from both profiles (simulating leak)
				return &keywordsearch.KeywordSearchResult{
					Data: []keywordsearch.SearchHit{
						{ID: "kw-1", Content: "keyword-A", ProfileID: pid},
						{ID: "kw-2", Content: "keyword-B", ProfileID: otherProfileID.String()},
					},
					Meta: keywordsearch.KeywordSearchMeta{LimitApplied: 20},
				}, nil
			},
		},
		semanticSearchService: &mockSemanticSearchService{
			searchFunc: func(ctx context.Context, pid string, req *semanticsearch.SemanticSearchRequest) (*semanticsearch.SemanticSearchResult, error) {
				// Return hits from both profiles (simulating leak)
				return &semanticsearch.SemanticSearchResult{
					Data: []semanticsearch.SearchHit{
						{ID: "sem-1", Content: "semantic-A", ProfileID: pid},
						{ID: "sem-2", Content: "semantic-B", ProfileID: otherProfileID.String()},
					},
					Meta: semanticsearch.SemanticSearchMeta{LimitApplied: 10},
				}, nil
			},
		},
	}

	writer := &mockSSEWriter{}

	// Run orchestrator with profile isolation test
	err := mockOrch.Run(context.Background(), profileID.String(), "test query", map[string]any{"embedding": []float32{0.1, 0.2}}, writer)
	require.NoError(t, err)

	// Verify evidence events only contain authenticated profile data
	for _, event := range writer.events {
		if event.eventType == sse.EventTypeEvidence {
			payload := event.payload.(map[string]any)
			profileField := payload["profile"].(string)
			assert.Equal(t, profileID.String(), profileField, "evidence profile must match authenticated profile")

			// Verify data items are filtered
			data := payload["data"]
			if data != nil {
				switch v := data.(type) {
				case []keywordsearch.SearchHit:
					for _, hit := range v {
						assert.Equal(t, profileID.String(), hit.ProfileID, "keyword search hit must belong to authenticated profile")
					}
				case []semanticsearch.SearchHit:
					for _, hit := range v {
						assert.Equal(t, profileID.String(), hit.ProfileID, "semantic search hit must belong to authenticated profile")
					}
				case []map[string]any:
					for _, row := range v {
						if rowProfile, ok := row["team_id"].(string); ok {
							assert.Equal(t, profileID.String(), rowProfile, "graph query row team_id must match authenticated profile")
						}
					}
				}
			}
		}
	}
}

// TestQueryStreamDisconnectCleanup tests that disconnect triggers immediate cleanup of stream resources.
func TestQueryStreamDisconnectCleanup(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	cleanupCalled := false
	cleanupCtxReceived := context.Context(nil)

	mockLife := &mockStreamLifecycle{
		startFunc: func(ctx context.Context, pid string, writer sse.SSEWriter, work func(context.Context) error) error {
			// Simulate work that detects disconnect
			workCtx, cancel := context.WithCancel(ctx)
			go func() {
				time.Sleep(10 * time.Millisecond)
				cancel() // Simulate disconnect
			}()

			// Run work which should detect context cancellation
			err := work(workCtx)

			// Cleanup should be triggered on context error
			if workCtx.Err() == context.Canceled {
				cleanupCalled = true
				cleanupCtxReceived = ctx
			}

			return err
		},
	}

	mockOrch := &mockQueryStreamOrchestrator{
		runFunc: func(ctx context.Context, pid string, query string, params map[string]any, writer sse.SSEWriter) error {
			// Emit some events before disconnect
			_ = writer.WriteEvent(sse.EventTypeToolCall, map[string]any{"name": "test-tool"})

			// Wait for context cancellation (disconnect)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
				// Should not reach here in this test
				_ = writer.WriteEvent(sse.EventTypeDone, map[string]any{})
				return nil
			}
		},
	}

	h := NewQueryStreamHandler(mockOrch, mockLife)

	// Set resolved profile ID in context
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/profiles/:profileId/query/stream", h.Handle)

	body := `{"query": "test query"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/"+profileID.String()+"/query/stream", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Verify cleanup was called
	assert.True(t, cleanupCalled, "cleanup should be triggered on disconnect")

	// Verify context was passed for cleanup
	assert.NotNil(t, cleanupCtxReceived, "cleanup should receive context")
}

// TestQueryStreamSSEFormat_AcceptHeaderValidation tests Accept header check.
func TestQueryStreamSSEFormat_AcceptHeaderValidation(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockOrch := &mockQueryStreamOrchestrator{}
	mockLife := &mockStreamLifecycle{}

	h := NewQueryStreamHandler(mockOrch, mockLife)

	// Set resolved profile ID in context
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/profiles/:profileId/query/stream", h.Handle)

	body := `{"query": "test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/"+profileID.String()+"/query/stream", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Missing or wrong Accept header
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Should return 422 for wrong Accept header
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

// TestQueryStreamSSEFormat_ErrorEvent tests error event emission.
func TestQueryStreamSSEFormat_ErrorEvent(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	mockOrch := &mockQueryStreamOrchestrator{
		runFunc: func(ctx context.Context, pid string, query string, params map[string]any, writer sse.SSEWriter) error {
			// Emit tool_call then error
			_ = writer.WriteEvent(sse.EventTypeToolCall, map[string]any{"name": "test-tool"})
			_ = writer.WriteEvent(sse.EventTypeError, map[string]any{"message": "validation error"})
			// Done should not be sent after error (terminal)
			return nil
		},
	}

	mockLife := &mockStreamLifecycle{}

	h := NewQueryStreamHandler(mockOrch, mockLife)

	// Set resolved profile ID in context
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})

	e.POST("/api/v1/profiles/:profileId/query/stream", h.Handle)

	body := `{"query": "test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/"+profileID.String()+"/query/stream", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Verify SSE headers
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	// Verify error event in body
	bodyStr := rec.Body.String()
	assert.Contains(t, bodyStr, "event: error")
	assert.Contains(t, bodyStr, "validation error")

	// Verify error event is terminal (no done event)
	assert.NotContains(t, bodyStr, "event: done")
}

func TestQueryStreamHandlerValidationAndLifecycleBranches(t *testing.T) {
	profileID := uuid.New()

	run := func(lifecycle *mockStreamLifecycle, body string, accept string, withProfile bool) *httptest.ResponseRecorder {
		e := newTestEcho()
		h := NewQueryStreamHandler(&mockQueryStreamOrchestrator{}, lifecycle)
		if withProfile {
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					ctx := middleware.SetResolvedProfileIDForTest(c.Request().Context(), profileID)
					c.SetRequest(c.Request().WithContext(ctx))
					return next(c)
				}
			})
		}
		e.POST("/api/v1/profiles/:profileId/query/stream", h.Handle)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/"+profileID.String()+"/query/stream", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", accept)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	rec := run(&mockStreamLifecycle{}, `{"query":"x"}`, "text/event-stream", false)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = run(&mockStreamLifecycle{}, `{`, "text/event-stream", true)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec = run(&mockStreamLifecycle{}, `{"query":""}`, "text/event-stream", true)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec = run(&mockStreamLifecycle{
		startFunc: func(ctx context.Context, profileID string, writer sse.SSEWriter, work func(context.Context) error) error {
			return sse.ErrTooManyStreams
		},
	}, `{"query":"x"}`, "text/event-stream", true)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "too many concurrent streams")

	rec = run(&mockStreamLifecycle{
		startFunc: func(ctx context.Context, profileID string, writer sse.SSEWriter, work func(context.Context) error) error {
			return errors.New("database details")
		},
	}, `{"query":"x"}`, "text/event-stream", true)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "internal error")
	assert.NotContains(t, rec.Body.String(), "database details")
}

func TestQueryStreamOrchestratorHelpersAndErrorBranches(t *testing.T) {
	t.Run("constructor wires services", func(t *testing.T) {
		orch := NewQueryStreamOrchestrator(&mockGraphQueryService{}, &mockKeywordSearchService{}, &mockSemanticSearchService{})
		require.NotNil(t, orch)
	})

	t.Run("writer error stops first tool call", func(t *testing.T) {
		orch := NewQueryStreamOrchestrator(&mockGraphQueryService{}, nil, nil)
		writer := &mockSSEWriter{writeErr: errors.New("write failed")}

		err := orch.Run(context.Background(), "profile-1", "query", nil, writer)

		require.ErrorContains(t, err, "write failed")
	})

	t.Run("graph error is sanitized into event", func(t *testing.T) {
		orch := NewQueryStreamOrchestrator(&mockGraphQueryService{
			executeFunc: func(ctx context.Context, profileID string, query string, params map[string]any) (*graphquery.GraphQueryResult, error) {
				return nil, &graphquery.ValidationError{Reason: "missing team filter"}
			},
		}, nil, nil)
		writer := &mockSSEWriter{}

		err := orch.Run(context.Background(), "profile-1", "query", nil, writer)

		require.NoError(t, err)
		require.Len(t, writer.events, 2)
		assert.Equal(t, sse.EventTypeError, writer.events[1].eventType)
	})

	t.Run("keyword error is sanitized into event", func(t *testing.T) {
		orch := NewQueryStreamOrchestrator(nil, &mockKeywordSearchService{
			searchFunc: func(ctx context.Context, profileID string, req *keywordsearch.KeywordSearchRequest) (*keywordsearch.KeywordSearchResult, error) {
				return nil, keywordsearch.NewValidationError("query is required")
			},
		}, nil)
		writer := &mockSSEWriter{}

		err := orch.Run(context.Background(), "profile-1", "query", nil, writer)

		require.NoError(t, err)
		require.Len(t, writer.events, 2)
		assert.Equal(t, sse.EventTypeError, writer.events[1].eventType)
	})

	t.Run("semantic error is sanitized into event", func(t *testing.T) {
		orch := NewQueryStreamOrchestrator(nil, nil, &mockSemanticSearchService{
			searchFunc: func(ctx context.Context, profileID string, req *semanticsearch.SemanticSearchRequest) (*semanticsearch.SemanticSearchResult, error) {
				return nil, semanticsearch.NewDimensionMismatchError(3, 2)
			},
		})
		writer := &mockSSEWriter{}

		err := orch.Run(context.Background(), "profile-1", "query", map[string]any{"embedding": []any{1.0, 2.0}}, writer)

		require.NoError(t, err)
		require.Len(t, writer.events, 2)
		assert.Equal(t, sse.EventTypeError, writer.events[1].eventType)
	})

	t.Run("done writer error is returned", func(t *testing.T) {
		orch := NewQueryStreamOrchestrator(nil, nil, nil)
		writer := &mockSSEWriter{writeErr: errors.New("done failed")}

		err := orch.Run(context.Background(), "profile-1", "query", nil, writer)

		require.ErrorContains(t, err, "done failed")
	})

	assert.Equal(t, "", sanitizeError(nil))
	assert.Equal(t, "internal error", sanitizeError(errors.New("secret")))

	sanitizedCases := []error{
		&graphquery.ValidationError{Reason: "bad query"},
		graphquery.NewLimitError("limit exceeded"),
		graphquery.NewForbiddenParamError("profileId"),
		graphquery.NewSyntaxError("syntax error"),
		keywordsearch.NewValidationError("bad keywords"),
		semanticsearch.NewValidationError("bad semantic request"),
		semanticsearch.NewDimensionMismatchError(3, 2),
		semanticsearch.NewEmbeddingGenerationNotConfiguredError("embedding generation not configured"),
	}
	for _, err := range sanitizedCases {
		if got := sanitizeOrchestratorError(err); got != err.Error() {
			t.Fatalf("sanitizeOrchestratorError(%T) = %q, want %q", err, got, err.Error())
		}
	}
	assert.Equal(t, "", sanitizeOrchestratorError(nil))
	assert.Equal(t, "internal error", sanitizeOrchestratorError(errors.New("driver details")))

	embedding, ok := extractEmbedding(map[string]any{"embedding": []any{float64(1), float32(2)}})
	require.True(t, ok)
	assert.Equal(t, []float32{1, 2}, embedding)

	embedding, ok = extractEmbedding(map[string]any{"embedding": []float32{3, 4}})
	require.True(t, ok)
	assert.Equal(t, []float32{3, 4}, embedding)

	_, ok = extractEmbedding(nil)
	assert.False(t, ok)
	_, ok = extractEmbedding(map[string]any{})
	assert.False(t, ok)
	_, ok = extractEmbedding(map[string]any{"embedding": []any{"bad"}})
	assert.False(t, ok)
	_, ok = extractEmbedding(map[string]any{"embedding": "bad"})
	assert.False(t, ok)
}

// mockGraphQueryService for testing
type mockGraphQueryService struct {
	executeFunc func(ctx context.Context, profileID string, query string, params map[string]any) (*graphquery.GraphQueryResult, error)
}

func (m *mockGraphQueryService) Execute(ctx context.Context, profileID string, query string, params map[string]any) (*graphquery.GraphQueryResult, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, profileID, query, params)
	}
	return &graphquery.GraphQueryResult{
		Columns: []string{},
		Rows:    []map[string]any{},
	}, nil
}

// mockSemanticSearchService for testing
type mockSemanticSearchService struct {
	searchFunc func(ctx context.Context, profileID string, req *semanticsearch.SemanticSearchRequest) (*semanticsearch.SemanticSearchResult, error)
}

func (m *mockSemanticSearchService) Search(ctx context.Context, profileID string, req *semanticsearch.SemanticSearchRequest) (*semanticsearch.SemanticSearchResult, error) {
	if m.searchFunc != nil {
		return m.searchFunc(ctx, profileID, req)
	}
	return &semanticsearch.SemanticSearchResult{
		Data: []semanticsearch.SearchHit{},
		Meta: semanticsearch.SemanticSearchMeta{LimitApplied: 10},
	}, nil
}
