package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

// APIKeyServiceInterface defines the interface for API key service operations.
// This allows mocking in tests and decouples the handler from concrete implementations.
type APIKeyServiceInterface interface {
	CreateStandardKey(ctx context.Context, profileID uuid.UUID, req service.CreateAPIKeyRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.APIKey, string, error)
	RotateForProfile(ctx context.Context, profileID, id uuid.UUID, req service.CreateAPIKeyRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.APIKey, string, error)
	ListByProfile(ctx context.Context, profileID uuid.UUID, limit, offset int) ([]*domain.APIKey, error)
	CountByProfile(ctx context.Context, profileID uuid.UUID) (int64, error)
	GetByIDForProfile(ctx context.Context, profileID, id uuid.UUID) (*domain.APIKey, error)
	DeleteForProfile(ctx context.Context, profileID, id uuid.UUID, actorKeyID *string, actorRole, clientIP, correlationID string) error
}

// APIKeyHandler handles HTTP requests for API key operations.
type APIKeyHandler struct {
	svc APIKeyServiceInterface
}

// APIKeyHandlerInterface is the companion interface for APIKeyHandler.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type APIKeyHandlerInterface interface {
	Create(c echo.Context) error
	List(c echo.Context) error
	Get(c echo.Context) error
	Rotate(c echo.Context) error
	Delete(c echo.Context) error
}

// Ensure APIKeyHandler implements APIKeyHandlerInterface
var _ APIKeyHandlerInterface = (*APIKeyHandler)(nil)

// NewAPIKeyHandler creates a new API key handler with the given service.
func NewAPIKeyHandler(svc APIKeyServiceInterface) *APIKeyHandler {
	return &APIKeyHandler{svc: svc}
}

// CreateAPIKeyResponse is the response for creating an API key.
// It includes the plaintext key exactly once.
type CreateAPIKeyResponse struct {
	APIKey string             `json:"api_key"`
	Key    dto.APIKeyResponse `json:"key"`
}

// Create handles POST /api/v1/profiles/:profileId/api-keys.
// Requires 'write' scope.
// Returns 201 with the created key and plaintext api_key.
func (h *APIKeyHandler) Create(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	profileIDStr := teamIDParam(c)
	if profileIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate UUID format
	profileID, err := uuid.Parse(profileIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	// Get validated request body
	body, ok := middleware.GetValidatedBody[dto.CreateAPIKeyRequest](ctx, middleware.CreateAPIKeyBodyKey)
	if !ok {
		return httperr.New(httperr.VALIDATION_ERROR, "request body not found")
	}

	// Build service request
	req := service.CreateAPIKeyRequest{
		Name:      body.Name,
		RateLimit: body.RateLimit,
	}
	if body.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err == nil {
			req.ExpiresAt = &t
		}
	}

	// Get actor metadata from principal
	var actorKeyID *string
	actorRole := "standard"
	if principal != nil {
		keyIDStr := principal.KeyID.String()
		actorKeyID = &keyIDStr
		actorRole = principal.Role
	}

	// Create API key
	key, rawKey, err := h.svc.CreateStandardKey(ctx, profileID, req, actorKeyID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
	if err != nil {
		return err
	}

	// Return 201 with key data and plaintext
	return response.SuccessCreated(c, CreateAPIKeyResponse{
		APIKey: rawKey,
		Key:    toAPIKeyResponse(key),
	})
}

// List handles GET /api/v1/profiles/:profileId/api-keys.
// Requires 'read' scope.
// Returns 200 with paginated list of API keys (never includes key_hash or plaintext).
func (h *APIKeyHandler) List(c echo.Context) error {
	ctx := c.Request().Context()

	profileIDStr := teamIDParam(c)
	if profileIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate UUID format
	profileID, err := uuid.Parse(profileIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	// Parse pagination params
	limit, offset := parsePaginationParams(c)

	// Get API keys
	keys, err := h.svc.ListByProfile(ctx, profileID, limit, offset)
	if err != nil {
		return err
	}

	// Count total keys for the profile (real total, not just this page).
	total, err := h.svc.CountByProfile(ctx, profileID)
	if err != nil {
		return err
	}

	// Convert to response format (never includes key_hash or plaintext)
	data := make([]dto.APIKeyListItem, len(keys))
	for i, k := range keys {
		data[i] = toAPIKeyListItem(k)
	}

	// Return 200 with pagination envelope
	return c.JSON(http.StatusOK, PaginationEnvelope{
		Data: data,
		Pagination: Pagination{
			Limit:  limit,
			Offset: offset,
			Total:  total,
		},
	})
}

// Get handles GET /api/v1/profiles/:profileId/api-keys/:keyId.
// Requires 'read' scope.
// Returns 200 with the API key data (never includes key_hash or plaintext).
// Scoped to the profileId in the path — returns NOT_FOUND for cross-profile ids.
func (h *APIKeyHandler) Get(c echo.Context) error {
	ctx := c.Request().Context()

	profileIDStr := teamIDParam(c)
	if profileIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate profile UUID format
	profileID, err := uuid.Parse(profileIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	keyIDStr := teamProfileIDParam(c)
	if keyIDStr == "" {
		return httperr.New(httperr.NOT_FOUND, "profile ID is required")
	}

	// Validate key UUID format
	keyID, err := uuid.Parse(keyIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid profile ID format")
	}

	// Get API key scoped to profile — returns NOT_FOUND on cross-profile id.
	key, err := h.svc.GetByIDForProfile(ctx, profileID, keyID)
	if err != nil {
		return err
	}

	// Return 200 with key data (never includes key_hash or plaintext)
	return response.SuccessOK(c, toAPIKeyResponse(key))
}

// Rotate handles POST /api/v1/teams/:teamId/profiles/:profileId/rotate.
// Requires 'write' scope. Returns the same profile id with new plaintext key material.
func (h *APIKeyHandler) Rotate(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	profileIDStr := teamIDParam(c)
	if profileIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}
	profileID, err := uuid.Parse(profileIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	keyIDStr := teamProfileIDParam(c)
	if keyIDStr == "" {
		return httperr.New(httperr.NOT_FOUND, "profile ID is required")
	}
	keyID, err := uuid.Parse(keyIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid profile ID format")
	}

	body, ok := middleware.GetValidatedBody[dto.CreateAPIKeyRequest](ctx, middleware.CreateAPIKeyBodyKey)
	if !ok {
		return httperr.New(httperr.VALIDATION_ERROR, "request body not found")
	}

	req := service.CreateAPIKeyRequest{
		Name:      body.Name,
		RateLimit: body.RateLimit,
	}
	if body.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err == nil {
			req.ExpiresAt = &t
		}
	}

	var actorKeyID *string
	actorRole := "standard"
	if principal != nil {
		keyIDStr := principal.KeyID.String()
		actorKeyID = &keyIDStr
		actorRole = principal.Role
	}

	key, rawKey, err := h.svc.RotateForProfile(ctx, profileID, keyID, req, actorKeyID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
	if err != nil {
		return err
	}

	return response.SuccessOK(c, CreateAPIKeyResponse{
		APIKey: rawKey,
		Key:    toAPIKeyResponse(key),
	})
}

// Delete handles DELETE /api/v1/profiles/:profileId/api-keys/:keyId.
// Requires 'write' scope.
// Returns 200 with { "status": "deleted" }.
// Scoped to the profileId in the path — returns NOT_FOUND for cross-profile ids.
func (h *APIKeyHandler) Delete(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	profileIDStr := teamIDParam(c)
	if profileIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate profile UUID format
	profileID, err := uuid.Parse(profileIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	keyIDStr := teamProfileIDParam(c)
	if keyIDStr == "" {
		return httperr.New(httperr.NOT_FOUND, "profile ID is required")
	}

	// Validate key UUID format
	keyID, err := uuid.Parse(keyIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid profile ID format")
	}

	// Get actor metadata from principal
	var actorKeyID *string
	actorRole := "standard"
	if principal != nil {
		keyIDStr := principal.KeyID.String()
		actorKeyID = &keyIDStr
		actorRole = principal.Role
	}

	// Delete the key scoped to profile — NOT_FOUND on cross-profile id.
	err = h.svc.DeleteForProfile(ctx, profileID, keyID, actorKeyID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
	if err != nil {
		return err
	}

	return response.SuccessOK(c, map[string]string{"status": "deleted"})
}

func teamIDParam(c echo.Context) string {
	if v := c.Param("teamId"); v != "" {
		return v
	}
	return c.Param("profileId")
}

func teamProfileIDParam(c echo.Context) string {
	if v := c.Param("keyId"); v != "" {
		return v
	}
	return c.Param("profileId")
}

// toAPIKeyResponse converts a domain.APIKey to dto.APIKeyResponse.
// Never includes key_hash or plaintext.
func toAPIKeyResponse(k *domain.APIKey) dto.APIKeyResponse {
	var lastUsedAt *string
	if k.LastUsedAt != nil {
		formatted := k.LastUsedAt.Format("2006-01-02T15:04:05Z")
		lastUsedAt = &formatted
	}

	var expiresAt *string
	if k.ExpiresAt != nil {
		formatted := k.ExpiresAt.Format("2006-01-02T15:04:05Z")
		expiresAt = &formatted
	}

	return dto.APIKeyResponse{
		ID:         k.ID,
		TeamID:     k.GetTeamID(),
		Name:       k.GetProfileName(),
		KeySuffix:  k.KeySuffix,
		RateLimit:  k.RateLimit,
		LastUsedAt: lastUsedAt,
		ExpiresAt:  expiresAt,
		CreatedAt:  k.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

// toAPIKeyListItem converts a domain.APIKey to dto.APIKeyListItem.
// Never includes key_hash or plaintext.
func toAPIKeyListItem(k *domain.APIKey) dto.APIKeyListItem {
	var lastUsedAt *string
	if k.LastUsedAt != nil {
		formatted := k.LastUsedAt.Format("2006-01-02T15:04:05Z")
		lastUsedAt = &formatted
	}

	var expiresAt *string
	if k.ExpiresAt != nil {
		formatted := k.ExpiresAt.Format("2006-01-02T15:04:05Z")
		expiresAt = &formatted
	}

	return dto.APIKeyListItem{
		ID:         k.ID,
		TeamID:     k.GetTeamID(),
		Name:       k.GetProfileName(),
		KeySuffix:  k.KeySuffix,
		RateLimit:  k.RateLimit,
		LastUsedAt: lastUsedAt,
		ExpiresAt:  expiresAt,
		CreatedAt:  k.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}
