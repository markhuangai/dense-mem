package http

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

func (h *controlPortalHandler) listSSOProviders(c echo.Context) error {
	if h.sso == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "sso service unavailable")
	}
	providers, err := h.sso.ListProviders(c.Request().Context())
	if err != nil {
		return err
	}
	items := make([]controlSSOProviderResponse, 0, len(providers))
	for _, provider := range providers {
		items = append(items, toControlSSOProvider(provider))
	}
	return c.JSON(200, map[string]any{"data": items})
}

func (h *controlPortalHandler) createSSOProvider(c echo.Context) error {
	if h.sso == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "sso service unavailable")
	}
	var body controlSSOProviderRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	provider, err := h.sso.CreateProvider(c.Request().Context(), body.toDomain(uuid.Nil))
	if err != nil {
		return controlSSOServiceError(err)
	}
	return c.JSON(201, map[string]any{"data": toControlSSOProvider(provider)})
}

func (h *controlPortalHandler) updateSSOProvider(c echo.Context) error {
	if h.sso == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "sso service unavailable")
	}
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	var body controlSSOProviderRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	provider, err := h.sso.UpdateProvider(c.Request().Context(), body.toDomain(providerID))
	if err != nil {
		return controlSSOServiceError(err)
	}
	return c.JSON(200, map[string]any{"data": toControlSSOProvider(provider)})
}

func (h *controlPortalHandler) deleteSSOProvider(c echo.Context) error {
	if h.sso == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "sso service unavailable")
	}
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	if err := h.sso.DeleteProvider(c.Request().Context(), providerID); err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"data": map[string]string{"status": "deleted"}})
}

func (h *controlPortalHandler) listSSOMappings(c echo.Context) error {
	if h.sso == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "sso service unavailable")
	}
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	mappings, err := h.sso.ListMappings(c.Request().Context(), providerID)
	if err != nil {
		return err
	}
	items := make([]controlSSOGroupMappingResponse, 0, len(mappings))
	for _, mapping := range mappings {
		items = append(items, toControlSSOGroupMapping(mapping))
	}
	return c.JSON(200, map[string]any{"data": items})
}

func (h *controlPortalHandler) createSSOMapping(c echo.Context) error {
	if h.sso == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "sso service unavailable")
	}
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	var body controlSSOGroupMappingRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	mapping, err := h.sso.CreateMapping(c.Request().Context(), body.toDomain(providerID, uuid.Nil))
	if err != nil {
		return controlSSOServiceError(err)
	}
	return c.JSON(201, map[string]any{"data": toControlSSOGroupMapping(mapping)})
}

func (h *controlPortalHandler) updateSSOMapping(c echo.Context) error {
	if h.sso == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "sso service unavailable")
	}
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	mappingID, err := parseControlUUID(c.Param("mappingId"), "SSO group mapping ID")
	if err != nil {
		return err
	}
	var body controlSSOGroupMappingRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	mapping, err := h.sso.UpdateMapping(c.Request().Context(), body.toDomain(providerID, mappingID))
	if err != nil {
		return controlSSOServiceError(err)
	}
	return c.JSON(200, map[string]any{"data": toControlSSOGroupMapping(mapping)})
}

func (h *controlPortalHandler) deleteSSOMapping(c echo.Context) error {
	if h.sso == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "sso service unavailable")
	}
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	mappingID, err := parseControlUUID(c.Param("mappingId"), "SSO group mapping ID")
	if err != nil {
		return err
	}
	if err := h.sso.DeleteMapping(c.Request().Context(), providerID, mappingID); err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"data": map[string]string{"status": "deleted"}})
}

type controlSSOProviderRequest struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	IssuerURL       string   `json:"issuer_url"`
	TenantID        string   `json:"tenant_id"`
	IdentityClaim   string   `json:"identity_claim"`
	ClientID        string   `json:"client_id"`
	ClientSecretEnv string   `json:"client_secret_env"`
	Scopes          []string `json:"scopes"`
	GroupClaims     []string `json:"group_claims"`
	GroupsEndpoint  string   `json:"groups_endpoint"`
	GroupsScopes    []string `json:"groups_scopes"`
	Enabled         *bool    `json:"enabled"`
}

func (r controlSSOProviderRequest) toDomain(id uuid.UUID) domain.SSOProvider {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	return domain.SSOProvider{
		ID:              id,
		Name:            r.Name,
		Kind:            domain.SSOProviderKind(r.Kind),
		IssuerURL:       r.IssuerURL,
		TenantID:        r.TenantID,
		IdentityClaim:   r.IdentityClaim,
		ClientID:        r.ClientID,
		ClientSecretEnv: r.ClientSecretEnv,
		Scopes:          append([]string(nil), r.Scopes...),
		GroupClaims:     append([]string(nil), r.GroupClaims...),
		GroupsEndpoint:  r.GroupsEndpoint,
		GroupsScopes:    append([]string(nil), r.GroupsScopes...),
		Enabled:         enabled,
	}
}

type controlSSOGroupMappingRequest struct {
	TeamID    uuid.UUID `json:"team_id"`
	GroupID   string    `json:"group_id"`
	GroupName string    `json:"group_name"`
	Scopes    []string  `json:"scopes"`
	Role      string    `json:"role"`
	Enabled   *bool     `json:"enabled"`
}

func (r controlSSOGroupMappingRequest) toDomain(providerID, mappingID uuid.UUID) domain.SSOGroupMapping {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	return domain.SSOGroupMapping{
		ID:         mappingID,
		ProviderID: providerID,
		TeamID:     r.TeamID,
		GroupID:    r.GroupID,
		GroupName:  r.GroupName,
		Scopes:     append([]string(nil), r.Scopes...),
		Role:       r.Role,
		Enabled:    enabled,
	}
}

type controlSSOProviderResponse struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind"`
	IssuerURL       string    `json:"issuer_url"`
	TenantID        string    `json:"tenant_id"`
	IdentityClaim   string    `json:"identity_claim"`
	ClientID        string    `json:"client_id"`
	ClientSecretEnv string    `json:"client_secret_env"`
	Scopes          []string  `json:"scopes"`
	GroupClaims     []string  `json:"group_claims"`
	GroupsEndpoint  string    `json:"groups_endpoint"`
	GroupsScopes    []string  `json:"groups_scopes"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       string    `json:"created_at"`
	UpdatedAt       string    `json:"updated_at"`
}

func toControlSSOProvider(provider *domain.SSOProvider) controlSSOProviderResponse {
	if provider == nil {
		return controlSSOProviderResponse{}
	}
	return controlSSOProviderResponse{
		ID:              provider.ID,
		Name:            provider.Name,
		Kind:            string(provider.Kind),
		IssuerURL:       provider.IssuerURL,
		TenantID:        provider.TenantID,
		IdentityClaim:   provider.IdentityClaim,
		ClientID:        provider.ClientID,
		ClientSecretEnv: provider.ClientSecretEnv,
		Scopes:          append([]string{}, provider.Scopes...),
		GroupClaims:     append([]string{}, provider.GroupClaims...),
		GroupsEndpoint:  provider.GroupsEndpoint,
		GroupsScopes:    append([]string{}, provider.GroupsScopes...),
		Enabled:         provider.Enabled,
		CreatedAt:       provider.CreatedAt.Format(time.RFC3339),
		UpdatedAt:       provider.UpdatedAt.Format(time.RFC3339),
	}
}

type controlSSOGroupMappingResponse struct {
	ID         uuid.UUID `json:"id"`
	ProviderID uuid.UUID `json:"provider_id"`
	TeamID     uuid.UUID `json:"team_id"`
	TeamName   string    `json:"team_name"`
	GroupID    string    `json:"group_id"`
	GroupName  string    `json:"group_name"`
	Scopes     []string  `json:"scopes"`
	Role       string    `json:"role"`
	Enabled    bool      `json:"enabled"`
	Origin     string    `json:"origin"`
	RetiredAt  *string   `json:"retired_at"`
	CreatedAt  string    `json:"created_at"`
	UpdatedAt  string    `json:"updated_at"`
}

func toControlSSOGroupMapping(mapping *domain.SSOGroupMapping) controlSSOGroupMappingResponse {
	if mapping == nil {
		return controlSSOGroupMappingResponse{}
	}
	var retiredAt *string
	if mapping.RetiredAt != nil {
		formatted := mapping.RetiredAt.Format(time.RFC3339)
		retiredAt = &formatted
	}
	return controlSSOGroupMappingResponse{
		ID:         mapping.ID,
		ProviderID: mapping.ProviderID,
		TeamID:     mapping.TeamID,
		TeamName:   mapping.TeamName,
		GroupID:    mapping.GroupID,
		GroupName:  mapping.GroupName,
		Scopes:     append([]string{}, mapping.Scopes...),
		Role:       mapping.Role,
		Enabled:    mapping.Enabled,
		Origin:     mapping.Origin,
		RetiredAt:  retiredAt,
		CreatedAt:  mapping.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  mapping.UpdatedAt.Format(time.RFC3339),
	}
}

func controlSSOServiceError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.HasPrefix(message, "sso ") || strings.Contains(message, "api key scopes") || strings.Contains(message, "api key role") {
		return httperr.New(httperr.VALIDATION_ERROR, message)
	}
	return err
}
