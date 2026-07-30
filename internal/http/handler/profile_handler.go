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
	"github.com/markhuangai/dense-mem/internal/service"
)

// ProfileServiceInterface defines the interface for profile service operations.
// This allows mocking in tests and decouples the handler from concrete implementations.
type ProfileServiceInterface interface {
	Create(ctx context.Context, req service.CreateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.Profile, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error)
	List(ctx context.Context, limit, offset int) ([]*domain.Profile, error)
	Count(ctx context.Context) (int64, error)
	Update(ctx context.Context, id uuid.UUID, req service.UpdateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error)
	Delete(ctx context.Context, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error
}

// ProfileHandler handles HTTP requests for profile operations.
type ProfileHandler struct {
	svc ProfileServiceInterface
}

// ProfileHandlerInterface is the companion interface for ProfileHandler.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type ProfileHandlerInterface interface {
	Get(c echo.Context) error
	Patch(c echo.Context) error
	Delete(c echo.Context) error
}

// Ensure ProfileHandler implements ProfileHandlerInterface
var _ ProfileHandlerInterface = (*ProfileHandler)(nil)

// NewProfileHandler creates a new profile handler with the given service.
func NewProfileHandler(svc ProfileServiceInterface) *ProfileHandler {
	return &ProfileHandler{svc: svc}
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
func (h *ProfileHandler) Get(c echo.Context) error {
	ctx := c.Request().Context()

	profileIDStr := authenticatedTeamID(c)
	if profileIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate UUID format
	profileID, err := uuid.Parse(profileIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	// Get profile
	profile, err := h.svc.Get(ctx, profileID)
	if err != nil {
		return err
	}

	// Check if profile exists (Get returns NOT_FOUND error for deleted profiles)
	if profile == nil {
		return httperr.New(httperr.NOT_FOUND, "team not found")
	}

	// Return 200 with profile data
	return response.SuccessOK(c, toProfileResponse(profile))
}

// Patch handles PATCH /ui/api/team.
// Returns 200 with the updated team data.
func (h *ProfileHandler) Patch(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	profileIDStr := authenticatedTeamID(c)
	if profileIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate UUID format
	profileID, err := uuid.Parse(profileIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	// Get validated request body
	body, ok := middleware.GetValidatedBody[dto.UpdateProfileRequest](ctx, middleware.UpdateProfileBodyKey)
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
	req := service.UpdateProfileRequest{
		Name:        namePtr,
		Description: descPtr,
		Metadata:    body.Metadata,
		Config:      body.Config,
	}

	// Get actor metadata from principal
	var actorKeyID *string
	actorRole := "standard"
	if principal != nil {
		keyIDStr := principal.KeyID.String()
		actorKeyID = &keyIDStr
		actorRole = principal.Role
	}

	// Update profile
	profile, err := h.svc.Update(ctx, profileID, req, actorKeyID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
	if err != nil {
		return err
	}

	// Return 200 with updated profile data
	return response.SuccessOK(c, toProfileResponse(profile))
}

// Delete handles DELETE /ui/api/team.
// Returns 200 with { "status": "deleted" }.
func (h *ProfileHandler) Delete(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	profileIDStr := authenticatedTeamID(c)
	if profileIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate UUID format
	profileID, err := uuid.Parse(profileIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	// Get actor metadata from principal
	var actorKeyID *string
	actorRole := "standard"
	if principal != nil {
		keyIDStr := principal.KeyID.String()
		actorKeyID = &keyIDStr
		actorRole = principal.Role
	}

	// Delete profile
	err = h.svc.Delete(ctx, profileID, actorKeyID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
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

// toProfileResponse converts a domain.Team to dto.ProfileResponse.
func toProfileResponse(p *domain.Profile) dto.ProfileResponse {
	return dto.ProfileResponse{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Metadata:    p.Metadata,
		Config:      p.Config,
		CreatedAt:   p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   p.UpdatedAt.Format("2006-01-02T15:04:05Z"),
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
