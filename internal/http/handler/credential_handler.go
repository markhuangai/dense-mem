package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// CredentialServiceInterface defines the interface for API credential service operations.
// This allows mocking in tests and decouples the handler from concrete implementations.
type CredentialServiceInterface interface {
	CreateCredential(ctx context.Context, teamID uuid.UUID, req service.CreateCredentialRequest, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, string, error)
	UpdateNameForTeam(ctx context.Context, teamID, id uuid.UUID, name string, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, error)
	UpdateRoleForTeam(ctx context.Context, teamID, id uuid.UUID, role string, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, error)
	UpdateScopesForTeam(ctx context.Context, teamID, id uuid.UUID, scopes []string, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, error)
	RotateForTeam(ctx context.Context, teamID, id uuid.UUID, req service.CreateCredentialRequest, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, string, error)
	ListByTeam(ctx context.Context, teamID uuid.UUID, limit, offset int) ([]*domain.Credential, error)
	CountByTeam(ctx context.Context, teamID uuid.UUID) (int64, error)
	GetByIDForTeam(ctx context.Context, teamID, id uuid.UUID) (*domain.Credential, error)
	GetSSOOwnedCredential(ctx context.Context, teamID, identityID uuid.UUID) (*domain.Credential, error)
	DeleteForTeam(ctx context.Context, teamID, id uuid.UUID, actorCredentialID *string, actorRole, clientIP, correlationID string) error
}

// CredentialHandler handles HTTP requests for API credential operations.
type CredentialHandler struct {
	svc CredentialServiceInterface
}

// CredentialHandlerInterface is the companion interface for CredentialHandler.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type CredentialHandlerInterface interface {
	Create(c echo.Context) error
	List(c echo.Context) error
	Get(c echo.Context) error
	Update(c echo.Context) error
	Rotate(c echo.Context) error
	Delete(c echo.Context) error
}

// Ensure CredentialHandler implements CredentialHandlerInterface
var _ CredentialHandlerInterface = (*CredentialHandler)(nil)

// NewCredentialHandler creates a new API credential handler with the given service.
func NewCredentialHandler(svc CredentialServiceInterface) *CredentialHandler {
	return &CredentialHandler{svc: svc}
}

// CreateCredentialResponse is the response for creating an API credential.
// It includes the plaintext credential exactly once.
type CreateCredentialResponse struct {
	APIKey     string                 `json:"api_key"`
	Credential dto.CredentialResponse `json:"credential"`
}

// Create handles POST /ui/api/team/credentials.
// Requires manager role.
// Returns 201 with the created credential and plaintext api_key.
func (h *CredentialHandler) Create(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	teamIDStr := teamIDParam(c)
	if teamIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate UUID format
	teamID, err := uuid.Parse(teamIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	// Get validated request body
	body, ok := middleware.GetValidatedBody[dto.CreateCredentialRequest](ctx, middleware.CreateCredentialBodyKey)
	if !ok {
		return httperr.New(httperr.VALIDATION_ERROR, "request body not found")
	}

	// Build service request
	req := service.CreateCredentialRequest{
		Name:      body.Name,
		RateLimit: body.RateLimit,
		Role:      service.CredentialRoleMember,
	}
	if body.Scopes != nil {
		if len(*body.Scopes) == 0 {
			return httperr.New(httperr.VALIDATION_ERROR, service.CredentialScopeValidationMessage())
		}
		req.Scopes = append([]string(nil), (*body.Scopes)...)
	}
	if body.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, "expires_at must be in RFC3339 format")
		}
		req.ExpiresAt = &t
	}
	if body.MemoryBinding != nil {
		req.MemoryBinding = *body.MemoryBinding
	}
	if principal != nil && principal.AuthMethod == "sso_session" && principal.IdentityID != uuid.Nil {
		binding := strings.TrimSpace(req.MemoryBinding)
		if binding == "" || binding == service.CredentialBindingProfilePrivate {
			identityID := principal.IdentityID
			req.OwnerIdentityID = &identityID
		}
	}

	// Get actor metadata from principal
	actorCredentialID := principalCredentialID(principal)
	actorRole := service.CredentialRoleMember
	if principal != nil {
		actorRole = principal.Role
	}

	// Create API credential
	credential, rawKey, err := h.svc.CreateCredential(ctx, teamID, req, actorCredentialID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
	if err != nil {
		return err
	}

	// Return 201 with credential data and plaintext
	return response.SuccessCreated(c, CreateCredentialResponse{
		APIKey:     rawKey,
		Credential: toCredentialResponse(credential),
	})
}

// List handles GET /ui/api/team/credentials.
// Requires manager role.
// Returns 200 with paginated list of API credentials (never includes key_hash or plaintext).
func (h *CredentialHandler) List(c echo.Context) error {
	ctx := c.Request().Context()

	teamIDStr := teamIDParam(c)
	if teamIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate UUID format
	teamID, err := uuid.Parse(teamIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	// Parse pagination params
	limit, offset := parsePaginationParams(c)

	// Get API credentials
	credentials, err := h.svc.ListByTeam(ctx, teamID, limit, offset)
	if err != nil {
		return err
	}

	// Count total credentials for the team (real total, not just this page).
	total, err := h.svc.CountByTeam(ctx, teamID)
	if err != nil {
		return err
	}

	// Convert to response format (never includes key_hash or plaintext)
	data := make([]dto.CredentialListItem, len(credentials))
	for i, credential := range credentials {
		data[i] = toCredentialListItem(credential)
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

// Get handles GET /ui/api/team/credentials/:credentialId.
// Requires manager role.
// Returns 200 with the API credential data (never includes key_hash or plaintext).
// Scoped to the credentialId in the path; cross-team IDs return NOT_FOUND.
func (h *CredentialHandler) Get(c echo.Context) error {
	ctx := c.Request().Context()

	teamIDStr := teamIDParam(c)
	if teamIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate team UUID format
	teamID, err := uuid.Parse(teamIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	credentialIDStr := credentialIDParam(c)
	if credentialIDStr == "" {
		return httperr.New(httperr.NOT_FOUND, "credential ID is required")
	}

	// Validate credential UUID format
	credentialID, err := uuid.Parse(credentialIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid credential ID format")
	}

	// Get API credential scoped to team — returns NOT_FOUND on cross-team id.
	credential, err := h.svc.GetByIDForTeam(ctx, teamID, credentialID)
	if err != nil {
		return err
	}

	// Return 200 with credential data (never includes key_hash or plaintext)
	return response.SuccessOK(c, toCredentialResponse(credential))
}

// Update handles PATCH /ui/api/team/credentials/:credentialId.
// Requires manager role and can only update member credentials.
func (h *CredentialHandler) Update(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	teamIDStr := teamIDParam(c)
	if teamIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}
	teamID, err := uuid.Parse(teamIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	credentialIDStr := credentialIDParam(c)
	if credentialIDStr == "" {
		return httperr.New(httperr.NOT_FOUND, "credential ID is required")
	}
	credentialID, err := uuid.Parse(credentialIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid credential ID format")
	}

	body, ok := middleware.GetValidatedBody[dto.UpdateCredentialRequest](ctx, middleware.UpdateCredentialBodyKey)
	if !ok {
		return httperr.New(httperr.VALIDATION_ERROR, "request body not found")
	}
	namePresent := strings.TrimSpace(body.Name) != ""
	scopesPresent := body.Scopes != nil
	if !namePresent && !scopesPresent {
		return httperr.New(httperr.VALIDATION_ERROR, "credential name or scopes is required")
	}
	if namePresent && scopesPresent {
		return httperr.New(httperr.VALIDATION_ERROR, "credential name and scopes must be updated separately")
	}

	if err := h.rejectManagerTarget(ctx, teamID, credentialID); err != nil {
		return err
	}

	actorCredentialID := principalCredentialID(principal)
	actorRole := service.CredentialRoleMember
	if principal != nil {
		actorRole = principal.Role
	}

	var credential *domain.Credential
	if namePresent {
		credential, err = h.svc.UpdateNameForTeam(ctx, teamID, credentialID, body.Name, actorCredentialID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
	} else {
		credential, err = h.svc.UpdateScopesForTeam(ctx, teamID, credentialID, *body.Scopes, actorCredentialID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
	}
	if err != nil {
		return err
	}
	return response.SuccessOK(c, toCredentialResponse(credential))
}

// Rotate handles POST /ui/api/team/credentials/:credentialId/rotate.
// Requires manager role and can only rotate member credentials.
func (h *CredentialHandler) Rotate(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	teamIDStr := teamIDParam(c)
	if teamIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}
	teamID, err := uuid.Parse(teamIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	credentialIDStr := credentialIDParam(c)
	if credentialIDStr == "" {
		return httperr.New(httperr.NOT_FOUND, "credential ID is required")
	}
	credentialID, err := uuid.Parse(credentialIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid credential ID format")
	}

	body, ok := middleware.GetValidatedBody[dto.CreateCredentialRequest](ctx, middleware.CreateCredentialBodyKey)
	if !ok {
		return httperr.New(httperr.VALIDATION_ERROR, "request body not found")
	}
	if body.Scopes != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "scopes cannot be changed by rotating a credential")
	}
	if body.MemoryBinding != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "memory_binding cannot be changed by rotating a credential")
	}

	req := service.CreateCredentialRequest{
		Name:      body.Name,
		RateLimit: body.RateLimit,
	}
	if body.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *body.ExpiresAt)
		if err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, "expires_at must be in RFC3339 format")
		}
		req.ExpiresAt = &t
	}

	actorCredentialID := principalCredentialID(principal)
	if err := h.rejectManagerTarget(ctx, teamID, credentialID); err != nil {
		return err
	}

	actorRole := service.CredentialRoleMember
	if principal != nil {
		actorRole = principal.Role
	}

	credential, rawKey, err := h.svc.RotateForTeam(ctx, teamID, credentialID, req, actorCredentialID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
	if err != nil {
		return err
	}

	return response.SuccessOK(c, CreateCredentialResponse{
		APIKey:     rawKey,
		Credential: toCredentialResponse(credential),
	})
}

// Delete handles DELETE /ui/api/team/credentials/:credentialId.
// Requires manager role and can only delete member credentials.
// Returns 200 with { "status": "deleted" }.
// Scoped to the credentialId in the path; cross-team IDs return NOT_FOUND.
func (h *CredentialHandler) Delete(c echo.Context) error {
	ctx := c.Request().Context()
	principal := middleware.GetPrincipal(ctx)

	teamIDStr := teamIDParam(c)
	if teamIDStr == "" {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
	}

	// Validate team UUID format
	teamID, err := uuid.Parse(teamIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
	}

	credentialIDStr := credentialIDParam(c)
	if credentialIDStr == "" {
		return httperr.New(httperr.NOT_FOUND, "credential ID is required")
	}

	// Validate credential UUID format
	credentialID, err := uuid.Parse(credentialIDStr)
	if err != nil {
		return httperr.New(httperr.INVALID_UUID, "invalid credential ID format")
	}

	// Get actor metadata from principal
	actorCredentialID := principalCredentialID(principal)
	if err := h.rejectManagerTarget(ctx, teamID, credentialID); err != nil {
		return err
	}

	actorRole := service.CredentialRoleMember
	if principal != nil {
		actorRole = principal.Role
	}

	// Delete the credential scoped to team — NOT_FOUND on cross-team id.
	err = h.svc.DeleteForTeam(ctx, teamID, credentialID, actorCredentialID, actorRole, c.RealIP(), middleware.GetCorrelationID(ctx))
	if err != nil {
		return err
	}

	return response.SuccessOK(c, map[string]string{"status": "deleted"})
}

func (h *CredentialHandler) rejectManagerTarget(ctx context.Context, teamID, credentialID uuid.UUID) error {
	credential, err := h.svc.GetByIDForTeam(ctx, teamID, credentialID)
	if err != nil {
		return err
	}
	if credential == nil {
		return httperr.New(httperr.NOT_FOUND, fmt.Sprintf("credential with id '%s' not found", credentialID.String()))
	}
	if credential.GetRole() == service.CredentialRoleManager {
		return httperr.New(httperr.FORBIDDEN, "manager credentials can only be managed from the control portal")
	}
	return nil
}

func teamIDParam(c echo.Context) string {
	if v := c.Param("teamId"); v != "" {
		return v
	}
	if principal := middleware.GetPrincipal(c.Request().Context()); principal != nil && principal.GetTeamID() != uuid.Nil {
		return principal.GetTeamID().String()
	}
	return ""
}

func credentialIDParam(c echo.Context) string {
	return c.Param("credentialId")
}

// toCredentialResponse converts a domain.Credential to dto.CredentialResponse.
// Never includes key_hash or plaintext.
func toCredentialResponse(k *domain.Credential) dto.CredentialResponse {
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

	binding := k.MemoryBinding
	if !binding.Valid() {
		binding = domain.CredentialBindingSharedOnly
	}
	return dto.CredentialResponse{
		ID:              k.ID,
		TeamID:          k.GetTeamID(),
		Name:            k.Name,
		KeySuffix:       k.KeySuffix,
		Scopes:          append([]string{}, k.Scopes...),
		Role:            k.GetRole(),
		RateLimit:       k.RateLimit,
		LastUsedAt:      lastUsedAt,
		ExpiresAt:       expiresAt,
		CreatedAt:       k.CreatedAt.Format("2006-01-02T15:04:05Z"),
		MemoryBinding:   string(binding),
		MemorySpaceKind: string(binding.SpaceKind()),
	}
}

// toCredentialListItem converts a domain.Credential to dto.CredentialListItem.
// Never includes key_hash or plaintext.
func toCredentialListItem(k *domain.Credential) dto.CredentialListItem {
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

	binding := k.MemoryBinding
	if !binding.Valid() {
		binding = domain.CredentialBindingSharedOnly
	}
	return dto.CredentialListItem{
		ID:              k.ID,
		TeamID:          k.GetTeamID(),
		Name:            k.Name,
		KeySuffix:       k.KeySuffix,
		Scopes:          append([]string{}, k.Scopes...),
		Role:            k.GetRole(),
		RateLimit:       k.RateLimit,
		LastUsedAt:      lastUsedAt,
		ExpiresAt:       expiresAt,
		CreatedAt:       k.CreatedAt.Format("2006-01-02T15:04:05Z"),
		MemoryBinding:   string(binding),
		MemorySpaceKind: string(binding.SpaceKind()),
	}
}

func principalCredentialID(principal *middleware.Principal) *string {
	if principal == nil || principal.CredentialID == nil {
		return nil
	}
	value := principal.CredentialID.String()
	return &value
}
