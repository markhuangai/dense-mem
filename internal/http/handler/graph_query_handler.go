package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	httpvalidation "github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/tools/graphquery"
)

// GraphQueryResponse represents the response for graph-query.
type GraphQueryResponse struct {
	Data GraphQueryData `json:"data"`
	Meta GraphQueryMeta `json:"meta"`
}

// GraphQueryData represents the data portion of the response.
type GraphQueryData struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
}

// GraphQueryMeta represents the metadata portion of the response.
type GraphQueryMeta struct {
	RowCount      int  `json:"row_count"`
	RowCapApplied bool `json:"row_cap_applied"`
}

// GraphQueryServiceInterface defines the interface for graph query service.
type GraphQueryServiceInterface interface {
	Execute(ctx context.Context, profileID string, query string, params map[string]any) (*graphquery.GraphQueryResult, error)
}

// GraphQueryHandler handles HTTP requests for graph-query operations.
type GraphQueryHandler struct {
	svc        GraphQueryServiceInterface
	maxTimeout time.Duration
}

// GraphQueryHandlerInterface is the companion interface for GraphQueryHandler.
type GraphQueryHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure GraphQueryHandler implements GraphQueryHandlerInterface.
var _ GraphQueryHandlerInterface = (*GraphQueryHandler)(nil)

// NewGraphQueryHandler creates a new graph query handler.
func NewGraphQueryHandler(svc GraphQueryServiceInterface) *GraphQueryHandler {
	return &GraphQueryHandler{svc: svc}
}

func NewGraphQueryHandlerWithTimeouts(svc GraphQueryServiceInterface, maxTimeout time.Duration) *GraphQueryHandler {
	return &GraphQueryHandler{svc: svc, maxTimeout: maxTimeout}
}

// Handle handles POST /api/v1/tools/graph-query.
// It validates the request, executes the query, and returns results.
func (h *GraphQueryHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	// Get resolved profile ID from context (set by ProfileResolutionMiddleware)
	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	// Bind request body
	var req dto.GraphQueryRequest
	if err := c.Bind(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}

	if err := httpvalidation.ValidateStruct(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}
	if req.TimeoutSeconds < 0 {
		return httperr.New(httperr.VALIDATION_ERROR, "timeout_seconds must be greater than or equal to 0")
	}

	// Apply timeout if specified
	if req.TimeoutSeconds > 0 {
		requested := time.Duration(req.TimeoutSeconds) * time.Second
		if h.maxTimeout > 0 && requested > h.maxTimeout {
			return httperr.New(httperr.VALIDATION_ERROR, "timeout_seconds exceeds maximum")
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, requested)
		defer cancel()
	}

	// Execute query
	result, err := h.svc.Execute(ctx, profileID.String(), req.Query, req.Parameters)
	if err != nil {
		return handleGraphQueryError(err)
	}

	// Build response
	response := GraphQueryResponse{
		Data: GraphQueryData{
			Columns: result.Columns,
			Rows:    result.Rows,
		},
		Meta: GraphQueryMeta{
			RowCount:      result.RowCount,
			RowCapApplied: result.RowCapApplied,
		},
	}

	return c.JSON(http.StatusOK, response)
}

// handleGraphQueryError converts service errors to HTTP errors.
func handleGraphQueryError(err error) *httperr.APIError {
	if err == nil {
		return nil
	}

	// Check for specific error types
	if graphquery.IsLimitError(err) {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	if graphquery.IsForbiddenParamError(err) {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	if graphquery.IsSyntaxError(err) {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	// Check for validation errors from CypherValidator
	if _, ok := err.(*graphquery.ValidationError); ok {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	// Default to internal error
	return httperr.New(httperr.INTERNAL_ERROR, "failed to execute query")
}

// Ensure imports are used
var _ = uuid.UUID{}
