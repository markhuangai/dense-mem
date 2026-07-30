package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

// AuditServiceInterface defines the interface for audit service operations.
// This allows mocking in tests and decouples the handler from concrete implementations.
type AuditServiceInterface interface {
	List(ctx context.Context, profileID string, limit, offset int) ([]service.AuditLogEntry, int, error)
}

// AuditHandler handles HTTP requests for audit log operations.
// Audit log is append-only, so this handler only provides a read endpoint.
type AuditHandler struct {
	svc AuditServiceInterface
}

// AuditHandlerInterface is the companion interface for AuditHandler.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type AuditHandlerInterface interface {
	Get(c echo.Context) error
}

// Ensure AuditHandler implements AuditHandlerInterface
var _ AuditHandlerInterface = (*AuditHandler)(nil)

// NewAuditHandler creates a new audit handler with the given service.
func NewAuditHandler(svc AuditServiceInterface) *AuditHandler {
	return &AuditHandler{svc: svc}
}

// AuditLogResponse is the response format for a single audit log entry.
type AuditLogResponse struct {
	ID            string                 `json:"id"`
	ProfileID     *string                `json:"team_id,omitempty"`
	Timestamp     string                 `json:"timestamp"`
	Operation     string                 `json:"operation"`
	EntityType    string                 `json:"entity_type"`
	EntityID      string                 `json:"entity_id"`
	BeforePayload map[string]interface{} `json:"before_payload,omitempty"`
	AfterPayload  map[string]interface{} `json:"after_payload,omitempty"`
	ActorKeyID    *string                `json:"actor_key_id,omitempty"`
	ActorRole     string                 `json:"actor_role"`
	ClientIP      string                 `json:"client_ip"`
	CorrelationID string                 `json:"correlation_id"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// Get handles GET /ui/api/team/audit-log for the authenticated team.
// No update/delete endpoints are provided - audit log is append-only.
func (h *AuditHandler) Get(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	if h == nil || h.svc == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "audit log unavailable")
	}

	if principal == nil || principal.GetTeamID() == uuid.Nil {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}
	teamID := principal.GetTeamID()

	// Parse pagination params
	limit, offset := parseAuditPaginationParams(c)

	// Get audit log entries scoped to this profile
	entries, total, err := h.svc.List(ctx, teamID.String(), limit, offset)
	if err != nil {
		return err
	}

	// Convert to response format
	data := make([]AuditLogResponse, len(entries))
	for i, e := range entries {
		data[i] = toAuditLogResponse(e)
	}

	// Return 200 with pagination envelope
	return c.JSON(http.StatusOK, PaginationEnvelope{
		Data: data,
		Pagination: Pagination{
			Limit:  limit,
			Offset: offset,
			Total:  int64(total),
		},
	})
}

// toAuditLogResponse converts a service.AuditLogEntry to AuditLogResponse.
func toAuditLogResponse(e service.AuditLogEntry) AuditLogResponse {
	return AuditLogResponse{
		ID:            e.ID,
		ProfileID:     e.ProfileID,
		Timestamp:     e.Timestamp.Format("2006-01-02T15:04:05Z"),
		Operation:     e.Operation,
		EntityType:    e.EntityType,
		EntityID:      e.EntityID,
		BeforePayload: e.BeforePayload,
		AfterPayload:  e.AfterPayload,
		ActorKeyID:    e.ActorKeyID,
		ActorRole:     e.ActorRole,
		ClientIP:      e.ClientIP,
		CorrelationID: e.CorrelationID,
		Metadata:      e.Metadata,
	}
}

// parseAuditPaginationParams extracts limit and offset from query params.
// Defaults: limit=20, offset=0. Max limit=100.
func parseAuditPaginationParams(c echo.Context) (int, int) {
	limit := 20
	offset := 0

	if l := c.QueryParam("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			if parsed > 100 {
				parsed = 100
			}
			limit = parsed
		}
	}

	if o := c.QueryParam("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	return limit, offset
}
