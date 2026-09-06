package http

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

func (h *controlPortalHandler) listDirectoryConnectors(c echo.Context) error {
	if h.directory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "directory identity service unavailable")
	}
	connectors, err := h.directory.ListConnectors(c.Request().Context())
	if err != nil {
		return controlDirectoryServiceError(err)
	}
	items := make([]controlDirectoryConnectorResponse, 0, len(connectors))
	for _, connector := range connectors {
		items = append(items, toControlDirectoryConnector(connector))
	}
	return c.JSON(200, map[string]any{"data": items})
}

func (h *controlPortalHandler) getDirectoryConnector(c echo.Context) error {
	if h.directory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "directory identity service unavailable")
	}
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	connector, err := h.directory.GetConnectorForProvider(c.Request().Context(), providerID)
	if err != nil {
		return controlDirectoryServiceError(err)
	}
	return c.JSON(200, map[string]any{"data": toControlDirectoryConnector(connector)})
}

func (h *controlPortalHandler) createDirectoryConnector(c echo.Context) error {
	if h.directory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "directory identity service unavailable")
	}
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	var body controlDirectoryConnectorRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	connector, credential, err := h.directory.CreateConnector(c.Request().Context(), body.toDomain(uuid.Nil, providerID))
	if err != nil {
		return controlDirectoryServiceError(err)
	}
	return c.JSON(201, map[string]any{"data": controlDirectoryCredentialResponse{
		Connector:  toControlDirectoryConnector(connector),
		Credential: *credential,
	}})
}

func (h *controlPortalHandler) updateDirectoryConnector(c echo.Context) error {
	if h.directory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "directory identity service unavailable")
	}
	connectorID, err := parseControlUUID(c.Param("connectorId"), "directory connector ID")
	if err != nil {
		return err
	}
	current, err := h.directory.GetConnector(c.Request().Context(), connectorID)
	if err != nil {
		return controlDirectoryServiceError(err)
	}
	var body controlDirectoryConnectorRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	connector, err := h.directory.UpdateConnector(c.Request().Context(), body.toDomain(connectorID, current.ProviderID))
	if err != nil {
		return controlDirectoryServiceError(err)
	}
	return c.JSON(200, map[string]any{"data": toControlDirectoryConnector(connector)})
}

func (h *controlPortalHandler) rotateDirectoryCredentials(c echo.Context) error {
	if h.directory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "directory identity service unavailable")
	}
	connectorID, err := parseControlUUID(c.Param("connectorId"), "directory connector ID")
	if err != nil {
		return err
	}
	credential, err := h.directory.RotateCredentials(c.Request().Context(), connectorID)
	if err != nil {
		return controlDirectoryServiceError(err)
	}
	return c.JSON(200, map[string]any{"data": credential})
}

func (h *controlPortalHandler) previewDirectoryConnector(c echo.Context) error {
	if h.directory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "directory identity service unavailable")
	}
	connectorID, err := parseControlUUID(c.Param("connectorId"), "directory connector ID")
	if err != nil {
		return err
	}
	preview, err := h.directory.Preview(c.Request().Context(), connectorID)
	if err != nil {
		return controlDirectoryServiceError(err)
	}
	return c.JSON(200, map[string]any{"data": preview})
}

func (h *controlPortalHandler) setDirectoryConnectorStatus(c echo.Context) error {
	if h.directory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "directory identity service unavailable")
	}
	connectorID, err := parseControlUUID(c.Param("connectorId"), "directory connector ID")
	if err != nil {
		return err
	}
	var body controlDirectoryConnectorStatusRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	connector, err := h.directory.SetConnectorStatus(c.Request().Context(), connectorID, domain.DirectoryConnectorStatus(body.Status), body.PreviewVersion)
	if err != nil {
		return controlDirectoryServiceError(err)
	}
	return c.JSON(200, map[string]any{"data": toControlDirectoryConnector(connector)})
}

func (h *controlPortalHandler) adoptDirectoryGroupTeam(c echo.Context) error {
	if h.directory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "directory identity service unavailable")
	}
	connectorID, err := parseControlUUID(c.Param("connectorId"), "directory connector ID")
	if err != nil {
		return err
	}
	groupID, err := parseControlUUID(c.Param("groupId"), "directory group ID")
	if err != nil {
		return err
	}
	var body controlDirectoryAdoptRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	teamID, err := parseControlUUID(body.TeamID, "team ID")
	if err != nil {
		return err
	}
	if err := h.directory.AdoptTeam(c.Request().Context(), connectorID, groupID, teamID); err != nil {
		return controlDirectoryServiceError(err)
	}
	return c.JSON(200, map[string]any{"data": map[string]string{"status": "adopted"}})
}

type controlDirectoryConnectorRequest struct {
	GroupPattern     string                                     `json:"group_pattern"`
	RoleEntitlements map[string]domain.DirectoryRoleEntitlement `json:"role_entitlements"`
	MaxAutoTeams     int                                        `json:"max_auto_teams"`
}

func (r controlDirectoryConnectorRequest) toDomain(id, providerID uuid.UUID) domain.DirectoryConnector {
	return domain.DirectoryConnector{
		ID:               id,
		ProviderID:       providerID,
		GroupPattern:     r.GroupPattern,
		RoleEntitlements: r.RoleEntitlements,
		MaxAutoTeams:     r.MaxAutoTeams,
	}
}

type controlDirectoryConnectorStatusRequest struct {
	Status         string `json:"status"`
	PreviewVersion string `json:"preview_version"`
}

type controlDirectoryAdoptRequest struct {
	TeamID string `json:"team_id"`
}

type controlDirectoryConnectorResponse struct {
	ID                uuid.UUID                                  `json:"id"`
	ProviderID        uuid.UUID                                  `json:"provider_id"`
	Status            string                                     `json:"status"`
	GroupPattern      string                                     `json:"group_pattern"`
	RoleEntitlements  map[string]domain.DirectoryRoleEntitlement `json:"role_entitlements"`
	MaxAutoTeams      int                                        `json:"max_auto_teams"`
	CredentialVersion int                                        `json:"credential_version"`
	SCIMPath          string                                     `json:"scim_path"`
	LastActivationAt  *string                                    `json:"last_activation_at"`
	CreatedAt         string                                     `json:"created_at"`
	UpdatedAt         string                                     `json:"updated_at"`
}

func toControlDirectoryConnector(connector *domain.DirectoryConnector) controlDirectoryConnectorResponse {
	if connector == nil {
		return controlDirectoryConnectorResponse{}
	}
	var activatedAt *string
	if connector.LastActivationAt != nil {
		formatted := connector.LastActivationAt.Format(time.RFC3339)
		activatedAt = &formatted
	}
	entitlements := make(map[string]domain.DirectoryRoleEntitlement, len(connector.RoleEntitlements))
	for capture, entitlement := range connector.RoleEntitlements {
		entitlements[capture] = domain.DirectoryRoleEntitlement{Role: entitlement.Role, Scopes: append([]string(nil), entitlement.Scopes...)}
	}
	return controlDirectoryConnectorResponse{
		ID:                connector.ID,
		ProviderID:        connector.ProviderID,
		Status:            string(connector.Status),
		GroupPattern:      connector.GroupPattern,
		RoleEntitlements:  entitlements,
		MaxAutoTeams:      connector.MaxAutoTeams,
		CredentialVersion: connector.CredentialVersion,
		SCIMPath:          "/scim/v2/" + connector.ID.String(),
		LastActivationAt:  activatedAt,
		CreatedAt:         connector.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         connector.UpdatedAt.Format(time.RFC3339),
	}
}

type controlDirectoryCredentialResponse struct {
	Connector  controlDirectoryConnectorResponse `json:"connector"`
	Credential domain.DirectoryCredential        `json:"credential"`
}

func controlDirectoryServiceError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if errors.Is(err, service.ErrDirectoryConnectorDisabled) {
		return httperr.WithGuidance(httperr.New(httperr.CONFLICT, "directory connector is disabled"), "connector_disabled", "contact_operator", "Contact an operator to enable the directory connector before retrying.", false, nil, "")
	}
	if strings.Contains(message, "not found") {
		return httperr.New(httperr.NOT_FOUND, "directory resource not found")
	}
	if strings.Contains(message, "already") || strings.Contains(message, "preview") || strings.Contains(message, "disable") {
		return httperr.New(httperr.CONFLICT, "directory connector state conflict")
	}
	if strings.Contains(message, "directory") || strings.Contains(message, "sso provider") || strings.Contains(message, "group policy") || strings.Contains(message, "role entitlement") {
		return httperr.New(httperr.VALIDATION_ERROR, "invalid directory connector request")
	}
	return httperr.New(httperr.INTERNAL_ERROR, "directory identity operation failed")
}
