package handler

import (
	"context"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/tools/graphquery"
	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
)

// QueryStreamRequest represents the request body for query/stream.
type QueryStreamRequest struct {
	Query  string         `json:"query" validate:"required"`
	Params map[string]any `json:"params,omitempty"`
}

// QueryStreamHandler handles HTTP requests for the query/stream SSE endpoint.
type QueryStreamHandler struct {
	orchestrator QueryStreamOrchestrator
	lifecycle    sse.StreamLifecycle
}

// QueryStreamHandlerInterface is the companion interface for QueryStreamHandler.
type QueryStreamHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure QueryStreamHandler implements QueryStreamHandlerInterface.
var _ QueryStreamHandlerInterface = (*QueryStreamHandler)(nil)

// NewQueryStreamHandler creates a new query stream handler.
func NewQueryStreamHandler(orchestrator QueryStreamOrchestrator, lifecycle sse.StreamLifecycle) *QueryStreamHandler {
	return &QueryStreamHandler{
		orchestrator: orchestrator,
		lifecycle:    lifecycle,
	}
}

// Handle handles POST /api/v1/profiles/:profileId/query/stream.
// It validates Accept header, starts SSE stream, and orchestrates tool calls.
func (h *QueryStreamHandler) Handle(c echo.Context) error {
	// Check Accept header for text/event-stream
	acceptHeader := c.Request().Header.Get("Accept")
	if acceptHeader != "text/event-stream" {
		return httperr.New(httperr.VALIDATION_ERROR, "Accept header must be text/event-stream for SSE endpoint")
	}

	ctx := c.Request().Context()

	// Get resolved profile ID from context (set by ProfileResolutionMiddleware)
	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	// Bind request body
	var req QueryStreamRequest
	if err := c.Bind(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}

	// Validate required query field
	if req.Query == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "query is required")
	}

	// Create SSE writer
	writer, err := sse.NewSSEWriter(c.Response().Writer)
	if err != nil {
		return httperr.New(httperr.INTERNAL_ERROR, "failed to create SSE stream")
	}

	// Run stream lifecycle with orchestrator work
	err = h.lifecycle.Start(ctx, profileID.String(), writer, func(workCtx context.Context) error {
		return h.orchestrator.Run(workCtx, profileID.String(), req.Query, req.Params, writer)
	})

	// Handle lifecycle errors
	if err != nil {
		// If context cancelled (disconnect), just return without error
		if ctx.Err() != nil {
			return nil
		}
		// Stream terminated by max duration - already sent done event
		if err == sse.ErrStreamTerminated {
			return nil
		}
		// Too many streams - emit error event
		if err == sse.ErrTooManyStreams {
			_ = writer.WriteEvent(sse.EventTypeError, map[string]any{"message": "too many concurrent streams"})
			return nil
		}
		// Other errors - emit error event if stream still open
		_ = writer.WriteEvent(sse.EventTypeError, map[string]any{"message": sanitizeError(err)})
		return nil
	}

	return nil
}

// sanitizeError returns a sanitized error message safe for client display.
func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	// Return generic message for internal errors
	return "internal error"
}

// QueryStreamOrchestrator orchestrates multiple tool calls within a single SSE stream.
type QueryStreamOrchestrator interface {
	Run(ctx context.Context, profileID string, query string, params map[string]any, writer sse.SSEWriter) error
}

// QueryStreamOrchestratorInterface is the companion interface for QueryStreamOrchestrator.
// This is identical to QueryStreamOrchestrator but provided for explicit interface declaration.
type QueryStreamOrchestratorInterface interface {
	Run(ctx context.Context, profileID string, query string, params map[string]any, writer sse.SSEWriter) error
}

// queryStreamOrchestrator implements QueryStreamOrchestrator.
type queryStreamOrchestrator struct {
	graphQueryService     GraphQueryServiceInterface
	keywordSearchService  keywordsearch.KeywordSearchService
	semanticSearchService semanticsearch.SemanticSearchService
}

// Ensure queryStreamOrchestrator implements QueryStreamOrchestrator.
var _ QueryStreamOrchestrator = (*queryStreamOrchestrator)(nil)

// NewQueryStreamOrchestrator creates a new QueryStreamOrchestrator.
func NewQueryStreamOrchestrator(
	graphQueryService GraphQueryServiceInterface,
	keywordSearchService keywordsearch.KeywordSearchService,
	semanticSearchService semanticsearch.SemanticSearchService,
) QueryStreamOrchestrator {
	return &queryStreamOrchestrator{
		graphQueryService:     graphQueryService,
		keywordSearchService:  keywordSearchService,
		semanticSearchService: semanticSearchService,
	}
}

// Run executes the query across multiple tools and emits SSE events.
// Events are emitted in order: tool_call, evidence, done.
func (o *queryStreamOrchestrator) Run(ctx context.Context, profileID string, query string, params map[string]any, writer sse.SSEWriter) error {
	// Execute graph_query tool
	if o.graphQueryService != nil {
		// Emit tool_call event before execution
		err := writer.WriteEvent(sse.EventTypeToolCall, map[string]any{
			"name": "graph_query",
			"args": map[string]any{"query": query, "params": params},
		})
		if err != nil {
			return err
		}

		// Execute graph query
		result, err := o.graphQueryService.Execute(ctx, profileID, query, params)
		if err != nil {
			// Emit error event with sanitized message
			_ = writer.WriteEvent(sse.EventTypeError, map[string]any{"message": sanitizeOrchestratorError(err)})
			return nil // Error already emitted, close stream gracefully
		}

		// Emit evidence event with profile-isolated data
		if result != nil && len(result.Rows) > 0 {
			// Filter rows to ensure profile isolation (defense-in-depth)
			filteredRows := filterRowsByProfile(result.Rows, profileID)
			err = writer.WriteEvent(sse.EventTypeEvidence, map[string]any{
				"tool":    "graph_query",
				"profile": profileID,
				"data":    filteredRows,
				"meta": map[string]any{
					"columns":         result.Columns,
					"row_count":       len(filteredRows),
					"row_cap_applied": result.RowCapApplied,
				},
			})
			if err != nil {
				return err
			}
		}
	}

	// Execute keyword_search tool
	if o.keywordSearchService != nil {
		// Emit tool_call event before execution
		err := writer.WriteEvent(sse.EventTypeToolCall, map[string]any{
			"name": "keyword_search",
			"args": map[string]any{"query": query},
		})
		if err != nil {
			return err
		}

		// Execute keyword search
		result, err := o.keywordSearchService.Search(ctx, profileID, &keywordsearch.KeywordSearchRequest{
			Query: query,
			Limit: 20, // Default limit
		})
		if err != nil {
			// Emit error event with sanitized message
			_ = writer.WriteEvent(sse.EventTypeError, map[string]any{"message": sanitizeOrchestratorError(err)})
			return nil // Error already emitted, close stream gracefully
		}

		// Emit evidence event with profile-isolated data
		if result != nil && len(result.Data) > 0 {
			// Filter evidence to ensure profile isolation (defense-in-depth)
			filteredHits := filterHitsByProfile(result.Data, profileID)
			err = writer.WriteEvent(sse.EventTypeEvidence, map[string]any{
				"tool":    "keyword_search",
				"profile": profileID,
				"data":    filteredHits,
				"meta": map[string]any{
					"limit_applied": result.Meta.LimitApplied,
				},
			})
			if err != nil {
				return err
			}
		}
	}

	// Execute semantic_search tool (if embedding provided in params)
	if o.semanticSearchService != nil {
		embedding, hasEmbedding := extractEmbedding(params)
		if hasEmbedding && len(embedding) > 0 {
			// Emit tool_call event before execution
			err := writer.WriteEvent(sse.EventTypeToolCall, map[string]any{
				"name": "semantic_search",
				"args": map[string]any{"query": query, "embedding_present": true},
			})
			if err != nil {
				return err
			}

			// Execute semantic search
			result, err := o.semanticSearchService.Search(ctx, profileID, &semanticsearch.SemanticSearchRequest{
				Query:     query,
				Embedding: embedding,
				Limit:     10, // Default limit
			})
			if err != nil {
				// Emit error event with sanitized message
				_ = writer.WriteEvent(sse.EventTypeError, map[string]any{"message": sanitizeOrchestratorError(err)})
				return nil // Error already emitted, close stream gracefully
			}

			// Emit evidence event with profile-isolated data
			if result != nil && len(result.Data) > 0 {
				// Filter evidence to ensure profile isolation (defense-in-depth)
				filteredHits := filterSemanticHitsByProfile(result.Data, profileID)
				err = writer.WriteEvent(sse.EventTypeEvidence, map[string]any{
					"tool":    "semantic_search",
					"profile": profileID,
					"data":    filteredHits,
					"meta": map[string]any{
						"limit_applied": result.Meta.LimitApplied,
					},
				})
				if err != nil {
					return err
				}
			}
		}
	}

	// Emit done event (terminal)
	err := writer.WriteEvent(sse.EventTypeDone, map[string]any{})
	if err != nil {
		return err
	}

	return nil
}

// sanitizeOrchestratorError returns a sanitized error message for orchestrator errors.
func sanitizeOrchestratorError(err error) string {
	if err == nil {
		return ""
	}

	// Check for known error types that can be safely reported
	if _, ok := err.(*graphquery.ValidationError); ok ||
		graphquery.IsLimitError(err) ||
		graphquery.IsForbiddenParamError(err) ||
		graphquery.IsSyntaxError(err) {
		return err.Error()
	}

	if keywordsearch.IsValidationError(err) {
		return err.Error()
	}

	if semanticsearch.IsValidationError(err) ||
		semanticsearch.IsDimensionMismatchError(err) ||
		semanticsearch.IsEmbeddingGenerationNotConfiguredError(err) {
		return err.Error()
	}

	// Return generic message for unknown errors
	return "internal error"
}

// filterRowsByProfile filters graph query rows by profile ID (defense-in-depth).
func filterRowsByProfile(rows []map[string]any, profileID string) []map[string]any {
	filtered := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if rowProfile, ok := row["team_id"].(string); ok {
			if rowProfile == profileID {
				filtered = append(filtered, row)
			}
		}
	}
	return filtered
}

// filterHitsByProfile filters keyword search hits by profile ID (defense-in-depth).
func filterHitsByProfile(hits []keywordsearch.SearchHit, profileID string) []keywordsearch.SearchHit {
	filtered := make([]keywordsearch.SearchHit, 0, len(hits))
	for _, hit := range hits {
		if hit.ProfileID == profileID {
			filtered = append(filtered, hit)
		}
	}
	return filtered
}

// filterSemanticHitsByProfile filters semantic search hits by profile ID (defense-in-depth).
func filterSemanticHitsByProfile(hits []semanticsearch.SearchHit, profileID string) []semanticsearch.SearchHit {
	filtered := make([]semanticsearch.SearchHit, 0, len(hits))
	for _, hit := range hits {
		if hit.ProfileID == profileID {
			filtered = append(filtered, hit)
		}
	}
	return filtered
}

// extractEmbedding extracts embedding from params if present.
func extractEmbedding(params map[string]any) ([]float32, bool) {
	if params == nil {
		return nil, false
	}

	emb, ok := params["embedding"]
	if !ok {
		return nil, false
	}

	// Handle different embedding formats
	switch v := emb.(type) {
	case []float32:
		return v, true
	case []any:
		result := make([]float32, len(v))
		for i, f := range v {
			if fv, ok := f.(float32); ok {
				result[i] = fv
			} else if fv, ok := f.(float64); ok {
				result[i] = float32(fv)
			} else {
				return nil, false
			}
		}
		return result, true
	default:
		return nil, false
	}
}

// Ensure imports are used
var _ = uuid.UUID{}
