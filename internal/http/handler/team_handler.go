package handler

import (
	"context"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

// TeamServiceInterface defines the interface for team service operations.
// This allows mocking in tests and decouples the handler from concrete implementations.
type TeamServiceInterface interface {
	Create(ctx context.Context, req accessservice.CreateTeamRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Team, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.Team, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error)
	List(ctx context.Context, limit, offset int) ([]*domain.Team, error)
	Count(ctx context.Context) (int64, error)
	Update(ctx context.Context, id uuid.UUID, req accessservice.UpdateTeamRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Team, error)
	Delete(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error
}

// TeamHandler handles HTTP requests for team operations.
type TeamHandler struct {
	svc TeamServiceInterface
}

// TeamHandlerInterface is the companion interface for TeamHandler.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type TeamHandlerInterface interface {
	Get(c echo.Context) error
	Patch(c echo.Context) error
	Delete(c echo.Context) error
}

// Ensure TeamHandler implements TeamHandlerInterface
var _ TeamHandlerInterface = (*TeamHandler)(nil)

// NewTeamHandler creates a new team handler with the given service.
func NewTeamHandler(svc TeamServiceInterface) *TeamHandler {
	return &TeamHandler{svc: svc}
}

// PaginationEnvelope is the JSON envelope for paginated responses.
type PaginationEnvelope struct {
	Data       interface{} `json:"data"`
	Pagination Pagination  `json:"pagination"`
}

// Pagination holds pagination metadata.
type Pagination struct {
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
	Total  int64 `json:"total"`
}

// Get handles GET /ui/api/team.
// Returns 200 with the team data.
func (h *TeamHandler) Get(c echo.Context) error {
	ctx := c.Request().Context()

	teamIDStr := authenticatedTeamID(c)
	if teamIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate UUID format
	teamID, err := uuid.Parse(teamIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	// Get team
	team, err := h.svc.Get(ctx, teamID)
	if err != nil {
		return err
	}

	// Check if team exists (Get returns NOT_FOUND error for deleted teams)
	if team == nil {
		return httperr.New(httperr.NOT_FOUND, "team not found")
	}

	// Return 200 with team data
	return response.SuccessOK(c, toTeamResponse(team))
}

// Patch handles PATCH /ui/api/team.
// Returns 200 with the updated team data.
func (h *TeamHandler) Patch(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	teamIDStr := authenticatedTeamID(c)
	if teamIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate UUID format
	teamID, err := uuid.Parse(teamIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	// Get validated request body
	body, ok := middleware.GetValidatedBody[dto.UpdateTeamRequest](ctx, middleware.UpdateTeamBodyKey)
	if !ok {
		return httperr.New(httperr.VALIDATION_ERROR, "request body not found")
	}

	// Build service request - convert DTO fields to pointers for PATCH semantics
	var namePtr, descPtr *string
	if body.Name != "" {
		namePtr = &body.Name
	}
	if body.Description != "" {
		descPtr = &body.Description
	}
	req := accessservice.UpdateTeamRequest{
		Name:        namePtr,
		Description: descPtr,
		Metadata:    body.Metadata,
		Config:      body.Config,
	}

	// Get actor metadata from principal
	actorKeyID := principalCredentialID(principal)
	actorRole := "standard"
	if principal != nil {
		actorRole = principal.Role
	}

	// Update team
	team, err := h.svc.Update(ctx, teamID, req, actorKeyID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
	if err != nil {
		return err
	}

	// Return 200 with updated team data
	return response.SuccessOK(c, toTeamResponse(team))
}

// Delete handles DELETE /ui/api/team.
// Returns 200 with { "status": "deleted" }.
func (h *TeamHandler) Delete(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	teamIDStr := authenticatedTeamID(c)
	if teamIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate UUID format
	teamID, err := uuid.Parse(teamIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	// Get actor metadata from principal
	actorKeyID := principalCredentialID(principal)
	actorRole := "standard"
	if principal != nil {
		actorRole = principal.Role
	}

	// Delete team
	err = h.svc.Delete(ctx, teamID, actorKeyID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
	if err != nil {
		return err
	}

	// Return 200 with status deleted
	return response.SuccessOK(c, map[string]string{"status": "deleted"})
}

func authenticatedTeamID(c echo.Context) string {
	if principal := middleware.GetPrincipal(c.Request().Context()); principal != nil && principal.GetTeamID() != uuid.Nil {
		return principal.GetTeamID().String()
	}
	return ""
}

// toTeamResponse converts a domain.Team to dto.TeamResponse.
func toTeamResponse(team *domain.Team) dto.TeamResponse {
	return dto.TeamResponse{
		ID:          team.ID,
		Name:        team.Name,
		Description: team.Description,
		Metadata:    team.Metadata,
		Config:      team.Config,
		CreatedAt:   team.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   team.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

// parsePaginationParams extracts limit and offset from query params.
// Defaults: limit=20, offset=0. Max limit=100.
func parsePaginationParams(c echo.Context) (int, int) {
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
