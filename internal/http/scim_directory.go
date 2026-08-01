package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/elimity-com/scim"
	scimerrors "github.com/elimity-com/scim/errors"
	"github.com/elimity-com/scim/optional"
	"github.com/elimity-com/scim/schema"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

const directorySCIMMaxResults = 100

type DirectorySCIMConfig struct {
	PublicBaseURL string
	RuntimeConfig service.SSORuntimeConfigProvider
	Security      service.SecurityService
	RateLimitSvc  service.RateLimitServiceInterface
	Config        config.ConfigProvider
}

type directorySCIMContextKey struct{}

type directorySCIMHandler struct {
	directory           *service.DirectoryIdentityService
	publicBaseURL       string
	runtimeConfigSource service.SSORuntimeConfigProvider
	security            service.SecurityService
}

// RegisterDirectorySCIM exposes one authenticated SCIM endpoint per configured provider connector.
func RegisterDirectorySCIM(e *echo.Echo, directory *service.DirectoryIdentityService, cfg DirectorySCIMConfig) error {
	if e == nil {
		return fmt.Errorf("SCIM: echo server is required")
	}
	if directory == nil {
		return fmt.Errorf("SCIM: directory identity service is required")
	}
	h, err := newDirectorySCIMHandler(directory, cfg)
	if err != nil {
		return err
	}
	rateLimit := publicIPRateLimitMiddleware("directory-scim", cfg.RateLimitSvc, cfg.Config)
	e.POST("/scim/oauth/token", h.oauthToken, rateLimit)
	e.Any("/scim/v2/:connectorId", h.serve, rateLimit)
	e.Any("/scim/v2/:connectorId/*", h.serve, rateLimit)
	return nil
}

func newDirectorySCIMHandler(directory *service.DirectoryIdentityService, cfg DirectorySCIMConfig) (*directorySCIMHandler, error) {
	if _, err := newDirectorySCIMProtocolServer(directory, ""); err != nil {
		return nil, err
	}
	return &directorySCIMHandler{
		directory:           directory,
		publicBaseURL:       strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/"),
		runtimeConfigSource: cfg.RuntimeConfig,
		security:            cfg.Security,
	}, nil
}

func newDirectorySCIMProtocolServer(directory *service.DirectoryIdentityService, baseURL string) (scim.Server, error) {
	userHandler := directorySCIMUserResourceHandler{directory: directory}
	groupHandler := directorySCIMGroupResourceHandler{directory: directory}
	config := &scim.ServiceProviderConfig{
		MaxResults:       directorySCIMMaxResults,
		SupportFiltering: true,
		SupportPatch:     true,
		AuthenticationSchemes: []scim.AuthenticationScheme{
			{
				Type:        scim.AuthenticationTypeOauthBearerToken,
				Name:        "Bearer token",
				Description: "Connector-specific SCIM bearer token.",
				Primary:     true,
			},
			{
				Type:        scim.AuthenticationTypeOauth2,
				Name:        "OAuth client credentials",
				Description: "Connector-specific OAuth client credentials.",
			},
		},
	}
	resourceTypes := []scim.ResourceType{
		{
			ID:          optional.NewString("User"),
			Name:        "User",
			Endpoint:    "/Users",
			Description: optional.NewString("Directory user"),
			Schema:      schema.CoreUserSchema(),
			Handler:     userHandler,
		},
		{
			ID:          optional.NewString("Group"),
			Name:        "Group",
			Endpoint:    "/Groups",
			Description: optional.NewString("Directory group"),
			Schema:      schema.CoreGroupSchema(),
			Handler:     groupHandler,
		},
	}
	options := make([]scim.ServerOption, 0, 1)
	if baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/"); baseURL != "" {
		options = append(options, scim.WithBaseURL(baseURL))
	}
	server, err := scim.NewServer(&scim.ServerArgs{ServiceProviderConfig: config, ResourceTypes: resourceTypes}, options...)
	if err != nil {
		return scim.Server{}, fmt.Errorf("SCIM: construct protocol server: %w", err)
	}
	return server, nil
}

func (h *directorySCIMHandler) oauthToken(c echo.Context) error {
	if err := c.Request().ParseForm(); err != nil {
		return directoryOAuthError(c, nethttp.StatusBadRequest, "invalid_request")
	}
	if c.FormValue("grant_type") != "client_credentials" {
		return directoryOAuthError(c, nethttp.StatusBadRequest, "unsupported_grant_type")
	}
	clientID, clientSecret, ok := c.Request().BasicAuth()
	if !ok {
		clientID = c.FormValue("client_id")
		clientSecret = c.FormValue("client_secret")
	}
	token, expiresAt, err := h.directory.IssueOAuthToken(c.Request().Context(), clientID, clientSecret)
	if err != nil {
		if errors.Is(err, service.ErrDirectoryCredentialInvalid) {
			recordDirectoryOAuthAuthFailure(c, h.security)
			return directoryOAuthError(c, nethttp.StatusUnauthorized, "invalid_client")
		}
		return directoryOAuthError(c, nethttp.StatusInternalServerError, "server_error")
	}
	expiresIn := int(time.Until(expiresAt).Seconds())
	if expiresIn < 1 {
		expiresIn = 1
	}
	directoryOAuthNoStore(c)
	return c.JSON(nethttp.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
	})
}

func directoryOAuthError(c echo.Context, status int, code string) error {
	directoryOAuthNoStore(c)
	return c.JSON(status, map[string]string{"error": code})
}

func directoryOAuthNoStore(c echo.Context) {
	c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
	c.Response().Header().Set("Pragma", "no-cache")
}

func recordDirectoryOAuthAuthFailure(c echo.Context, securitySvc service.SecurityService) {
	if securitySvc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := securitySvc.RecordAuthFailure(ctx, c.RealIP(), "scim", "AUTH_INVALID"); err != nil {
		c.Logger().Error("directory oauth security auth failure record failed")
	}
}

func (h *directorySCIMHandler) serve(c echo.Context) error {
	connectorID, err := uuid.Parse(c.Param("connectorId"))
	if err != nil {
		return directorySCIMError(c, scimerrors.ScimError{Status: nethttp.StatusNotFound})
	}
	rawToken, ok := directoryBearerToken(c.Request())
	if !ok {
		return directorySCIMError(c, scimerrors.ScimError{Status: nethttp.StatusUnauthorized})
	}
	valid, err := h.directory.AuthenticateSCIM(c.Request().Context(), connectorID, rawToken)
	if err != nil {
		if errors.Is(err, service.ErrDirectoryCredentialInvalid) || errors.Is(err, service.ErrDirectoryConnectorDisabled) || errors.Is(err, service.ErrDirectoryResourceNotFound) {
			return directorySCIMError(c, scimerrors.ScimError{Status: nethttp.StatusUnauthorized})
		}
		return directorySCIMError(c, scimerrors.ScimErrorInternal)
	}
	if !valid {
		return directorySCIMError(c, scimerrors.ScimError{Status: nethttp.StatusUnauthorized})
	}

	request := c.Request().Clone(context.WithValue(c.Request().Context(), directorySCIMContextKey{}, connectorID))
	requestURL := *c.Request().URL
	request.URL = &requestURL
	resourcePath := strings.TrimPrefix(c.Param("*"), "/")
	if resourcePath == "" {
		request.URL.Path = "/v2"
	} else {
		request.URL.Path = "/v2/" + resourcePath
	}
	publicBaseURL := h.publicBaseURL
	if h.runtimeConfigSource != nil {
		runtime, err := h.runtimeConfigSource.SSORuntimeConfig(c.Request().Context())
		if err != nil {
			return directorySCIMError(c, scimerrors.ScimErrorInternal)
		}
		if runtimeBaseURL := strings.TrimRight(strings.TrimSpace(runtime.SCIMPublicBaseURL), "/"); runtimeBaseURL != "" {
			publicBaseURL = runtimeBaseURL
		}
	}
	baseURL := ""
	if publicBaseURL != "" {
		baseURL = publicBaseURL + "/scim/v2/" + connectorID.String()
	}
	server, err := newDirectorySCIMProtocolServer(h.directory, baseURL)
	if err != nil {
		return directorySCIMError(c, scimerrors.ScimErrorInternal)
	}
	server.ServeHTTP(c.Response(), request)
	return nil
}

func directoryBearerToken(request *nethttp.Request) (string, bool) {
	authorization := strings.TrimSpace(request.Header.Get(echo.HeaderAuthorization))
	if len(authorization) < len("Bearer ") || !strings.EqualFold(authorization[:len("Bearer ")], "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(authorization[len("Bearer "):])
	return token, token != ""
}

func directorySCIMError(c echo.Context, scimErr scimerrors.ScimError) error {
	payload, err := json.Marshal(scimErr)
	if err != nil {
		return err
	}
	return c.Blob(scimErr.Status, "application/scim+json", payload)
}

func directorySCIMConnectorID(request *nethttp.Request) (uuid.UUID, error) {
	if request == nil {
		return uuid.Nil, fmt.Errorf("SCIM request is required")
	}
	connectorID, ok := request.Context().Value(directorySCIMContextKey{}).(uuid.UUID)
	if !ok || connectorID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("SCIM connector context is missing")
	}
	return connectorID, nil
}

type directorySCIMUserResourceHandler struct {
	directory *service.DirectoryIdentityService
}

func (h directorySCIMUserResourceHandler) Create(request *nethttp.Request, attributes scim.ResourceAttributes) (scim.Resource, error) {
	connectorID, err := directorySCIMConnectorID(request)
	if err != nil {
		return scim.Resource{}, scimerrors.ScimErrorInternal
	}
	user, err := directoryUserFromSCIM(attributes, domain.DirectoryUser{})
	if err != nil {
		return scim.Resource{}, err
	}
	stored, err := h.directory.CreateUser(request.Context(), connectorID, user)
	if err != nil {
		return scim.Resource{}, directorySCIMMutationError(err)
	}
	return directorySCIMUserResource(*stored), nil
}

func (h directorySCIMUserResourceHandler) Get(request *nethttp.Request, id string) (scim.Resource, error) {
	connectorID, userID, err := directorySCIMResourceIDs(request, id)
	if err != nil {
		return scim.Resource{}, scimerrors.ScimErrorResourceNotFound(id)
	}
	user, err := h.directory.GetUser(request.Context(), connectorID, userID)
	if err != nil {
		return scim.Resource{}, directorySCIMGetError(id, err)
	}
	return directorySCIMUserResource(*user), nil
}

func (h directorySCIMUserResourceHandler) GetAll(request *nethttp.Request, params scim.ListRequestParams) (scim.Page, error) {
	connectorID, err := directorySCIMConnectorID(request)
	if err != nil {
		return scim.Page{}, scimerrors.ScimErrorInternal
	}
	filter, err := directoryParseSCIMFilter(request, "userName", "externalId", "id")
	if err != nil {
		return scim.Page{}, err
	}
	users, total, err := h.directory.ListUsersPage(request.Context(), connectorID, directorySCIMPageRequest(filter, params))
	if err != nil {
		return scim.Page{}, directorySCIMListError(err)
	}
	resources := make([]scim.Resource, 0, len(users))
	for _, user := range users {
		if user != nil {
			resources = append(resources, directorySCIMUserResource(*user))
		}
	}
	return scim.Page{TotalResults: total, Resources: resources}, nil
}

func (h directorySCIMUserResourceHandler) Replace(request *nethttp.Request, id string, attributes scim.ResourceAttributes) (scim.Resource, error) {
	connectorID, userID, err := directorySCIMResourceIDs(request, id)
	if err != nil {
		return scim.Resource{}, scimerrors.ScimErrorResourceNotFound(id)
	}
	existing, err := h.directory.GetUser(request.Context(), connectorID, userID)
	if err != nil {
		return scim.Resource{}, directorySCIMGetError(id, err)
	}
	user, err := directoryUserFromSCIM(attributes, domain.DirectoryUser{ID: existing.ID, ExternalID: existing.ExternalID, Active: true})
	if err != nil {
		return scim.Resource{}, err
	}
	user.ID = existing.ID
	stored, err := h.directory.UpsertUser(request.Context(), connectorID, user)
	if err != nil {
		return scim.Resource{}, directorySCIMMutationError(err)
	}
	return directorySCIMUserResource(*stored), nil
}

func (h directorySCIMUserResourceHandler) Delete(request *nethttp.Request, id string) error {
	connectorID, userID, err := directorySCIMResourceIDs(request, id)
	if err != nil {
		return scimerrors.ScimErrorResourceNotFound(id)
	}
	if err := h.directory.DeactivateUser(request.Context(), connectorID, userID); err != nil {
		return directorySCIMGetError(id, err)
	}
	return nil
}

func (h directorySCIMUserResourceHandler) Patch(request *nethttp.Request, id string, operations []scim.PatchOperation) (scim.Resource, error) {
	connectorID, userID, err := directorySCIMResourceIDs(request, id)
	if err != nil {
		return scim.Resource{}, scimerrors.ScimErrorResourceNotFound(id)
	}
	user, err := h.directory.GetUser(request.Context(), connectorID, userID)
	if err != nil {
		return scim.Resource{}, directorySCIMGetError(id, err)
	}
	updated, err := directoryPatchUser(*user, operations)
	if err != nil {
		return scim.Resource{}, err
	}
	stored, err := h.directory.UpsertUser(request.Context(), connectorID, updated)
	if err != nil {
		return scim.Resource{}, directorySCIMMutationError(err)
	}
	return directorySCIMUserResource(*stored), nil
}

type directorySCIMGroupResourceHandler struct {
	directory *service.DirectoryIdentityService
}

func (h directorySCIMGroupResourceHandler) Create(request *nethttp.Request, attributes scim.ResourceAttributes) (scim.Resource, error) {
	connectorID, err := directorySCIMConnectorID(request)
	if err != nil {
		return scim.Resource{}, scimerrors.ScimErrorInternal
	}
	group, members, err := directoryGroupFromSCIM(attributes, domain.DirectoryGroup{})
	if err != nil {
		return scim.Resource{}, err
	}
	stored, err := h.directory.CreateGroupWithMembers(request.Context(), connectorID, group, members)
	if err != nil {
		return scim.Resource{}, directorySCIMMutationError(err)
	}
	stored, err = h.directory.GetGroup(request.Context(), connectorID, stored.ID)
	if err != nil {
		return scim.Resource{}, scimerrors.ScimErrorInternal
	}
	return directorySCIMGroupResource(*stored), nil
}

func (h directorySCIMGroupResourceHandler) Get(request *nethttp.Request, id string) (scim.Resource, error) {
	connectorID, groupID, err := directorySCIMResourceIDs(request, id)
	if err != nil {
		return scim.Resource{}, scimerrors.ScimErrorResourceNotFound(id)
	}
	group, err := h.directory.GetGroup(request.Context(), connectorID, groupID)
	if err != nil {
		return scim.Resource{}, directorySCIMGetError(id, err)
	}
	return directorySCIMGroupResource(*group), nil
}

func (h directorySCIMGroupResourceHandler) GetAll(request *nethttp.Request, params scim.ListRequestParams) (scim.Page, error) {
	connectorID, err := directorySCIMConnectorID(request)
	if err != nil {
		return scim.Page{}, scimerrors.ScimErrorInternal
	}
	filter, err := directoryParseSCIMFilter(request, "displayName", "externalId", "id")
	if err != nil {
		return scim.Page{}, err
	}
	groups, total, err := h.directory.ListGroupsPage(request.Context(), connectorID, directorySCIMPageRequest(filter, params))
	if err != nil {
		return scim.Page{}, directorySCIMListError(err)
	}
	resources := make([]scim.Resource, 0, len(groups))
	for _, group := range groups {
		if group != nil {
			resources = append(resources, directorySCIMGroupResource(*group))
		}
	}
	return scim.Page{TotalResults: total, Resources: resources}, nil
}

func (h directorySCIMGroupResourceHandler) Replace(request *nethttp.Request, id string, attributes scim.ResourceAttributes) (scim.Resource, error) {
	connectorID, groupID, err := directorySCIMResourceIDs(request, id)
	if err != nil {
		return scim.Resource{}, scimerrors.ScimErrorResourceNotFound(id)
	}
	existing, err := h.directory.GetGroup(request.Context(), connectorID, groupID)
	if err != nil {
		return scim.Resource{}, directorySCIMGetError(id, err)
	}
	group, members, err := directoryGroupFromSCIM(attributes, domain.DirectoryGroup{ID: existing.ID, ExternalID: existing.ExternalID, Active: true})
	if err != nil {
		return scim.Resource{}, err
	}
	group.ID = existing.ID
	stored, err := h.directory.UpsertGroupWithMembers(request.Context(), connectorID, group, members)
	if err != nil {
		return scim.Resource{}, directorySCIMMutationError(err)
	}
	stored, err = h.directory.GetGroup(request.Context(), connectorID, stored.ID)
	if err != nil {
		return scim.Resource{}, scimerrors.ScimErrorInternal
	}
	return directorySCIMGroupResource(*stored), nil
}

func (h directorySCIMGroupResourceHandler) Delete(request *nethttp.Request, id string) error {
	connectorID, groupID, err := directorySCIMResourceIDs(request, id)
	if err != nil {
		return scimerrors.ScimErrorResourceNotFound(id)
	}
	if err := h.directory.DeactivateGroup(request.Context(), connectorID, groupID); err != nil {
		return directorySCIMGetError(id, err)
	}
	return nil
}

func (h directorySCIMGroupResourceHandler) Patch(request *nethttp.Request, id string, operations []scim.PatchOperation) (scim.Resource, error) {
	connectorID, groupID, err := directorySCIMResourceIDs(request, id)
	if err != nil {
		return scim.Resource{}, scimerrors.ScimErrorResourceNotFound(id)
	}
	group, err := h.directory.GetGroup(request.Context(), connectorID, groupID)
	if err != nil {
		return scim.Resource{}, directorySCIMGetError(id, err)
	}
	updated, memberIDs, err := directoryPatchGroup(*group, operations)
	if err != nil {
		return scim.Resource{}, err
	}
	stored, err := h.directory.UpsertGroupWithMembers(request.Context(), connectorID, updated, memberIDs)
	if err != nil {
		return scim.Resource{}, directorySCIMMutationError(err)
	}
	stored, err = h.directory.GetGroup(request.Context(), connectorID, stored.ID)
	if err != nil {
		return scim.Resource{}, scimerrors.ScimErrorInternal
	}
	return directorySCIMGroupResource(*stored), nil
}

func directorySCIMResourceIDs(request *nethttp.Request, id string) (uuid.UUID, uuid.UUID, error) {
	connectorID, err := directorySCIMConnectorID(request)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	resourceID, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return connectorID, resourceID, nil
}

func directoryUserFromSCIM(attributes scim.ResourceAttributes, base domain.DirectoryUser) (domain.DirectoryUser, error) {
	user := base
	userName, ok := directorySCIMString(attributes["userName"])
	if !ok || userName == "" {
		return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
	}
	user.UserName = userName
	if value, present := attributes["externalId"]; present {
		externalID, ok := directorySCIMString(value)
		if !ok {
			return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
		}
		user.ExternalID = externalID
	}
	if value, present := attributes["active"]; present {
		active, ok := directorySCIMBool(value)
		if !ok {
			return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
		}
		user.Active = active
	} else if base.ID == uuid.Nil {
		user.Active = true
	}
	if value, present := attributes["displayName"]; present {
		displayName, ok := directorySCIMString(value)
		if !ok {
			return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
		}
		user.DisplayName = displayName
	} else if value, present := attributes["name"]; present {
		name, ok := value.(map[string]interface{})
		if !ok {
			return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
		}
		if formattedValue, formattedPresent := name["formatted"]; formattedPresent {
			formatted, ok := directorySCIMString(formattedValue)
			if !ok {
				return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
			}
			user.DisplayName = formatted
		}
	}
	if value, present := attributes["emails"]; present {
		email, ok := directorySCIMEmail(value)
		if !ok {
			return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
		}
		user.Email = email
	}
	return user, nil
}

func directoryGroupFromSCIM(attributes scim.ResourceAttributes, base domain.DirectoryGroup) (domain.DirectoryGroup, []uuid.UUID, error) {
	group := base
	displayName, ok := directorySCIMString(attributes["displayName"])
	if !ok || displayName == "" {
		return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorInvalidValue
	}
	group.DisplayName = displayName
	if value, present := attributes["externalId"]; present {
		externalID, ok := directorySCIMString(value)
		if !ok {
			return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorInvalidValue
		}
		group.ExternalID = externalID
	}
	if value, present := attributes["active"]; present {
		active, ok := directorySCIMBool(value)
		if !ok {
			return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorInvalidValue
		}
		group.Active = active
	} else if base.ID == uuid.Nil {
		group.Active = true
	}
	members, err := directorySCIMMemberIDs(attributes["members"])
	if err != nil {
		return domain.DirectoryGroup{}, nil, err
	}
	return group, members, nil
}

func directoryPatchUser(user domain.DirectoryUser, operations []scim.PatchOperation) (domain.DirectoryUser, error) {
	for _, operation := range operations {
		path := ""
		if operation.Path != nil {
			path = strings.ToLower(operation.Path.String())
		}
		switch path {
		case "":
			attributes, ok := operation.Value.(map[string]interface{})
			if !ok || operation.Op == scim.PatchOperationRemove {
				return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidPath
			}
			updated, err := directoryPatchUserAttributes(user, scim.ResourceAttributes(attributes))
			if err != nil {
				return domain.DirectoryUser{}, err
			}
			user = updated
		case "active":
			if operation.Op == scim.PatchOperationRemove {
				user.Active = false
				continue
			}
			active, ok := directorySCIMBool(operation.Value)
			if !ok {
				return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
			}
			user.Active = active
		case "username":
			value, ok := directorySCIMString(operation.Value)
			if operation.Op == scim.PatchOperationRemove || !ok || value == "" {
				return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
			}
			user.UserName = value
		case "displayname", "name.formatted":
			if operation.Op == scim.PatchOperationRemove {
				user.DisplayName = user.UserName
				continue
			}
			value, ok := directorySCIMString(operation.Value)
			if !ok {
				return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
			}
			user.DisplayName = value
		case "externalid":
			value, ok := directorySCIMString(operation.Value)
			if operation.Op == scim.PatchOperationRemove || !ok {
				return domain.DirectoryUser{}, scimerrors.ScimErrorMutability
			}
			user.ExternalID = value
		case "emails":
			if operation.Op == scim.PatchOperationRemove {
				user.Email = ""
				continue
			}
			email, ok := directorySCIMEmail(operation.Value)
			if !ok {
				return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
			}
			user.Email = email
		default:
			return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidPath
		}
	}
	return user, nil
}

func directoryPatchUserAttributes(user domain.DirectoryUser, attributes scim.ResourceAttributes) (domain.DirectoryUser, error) {
	if value, present := attributes["userName"]; present {
		userName, ok := directorySCIMString(value)
		if !ok || userName == "" {
			return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
		}
		user.UserName = userName
	}
	if value, present := attributes["externalId"]; present {
		externalID, ok := directorySCIMString(value)
		if !ok {
			return domain.DirectoryUser{}, scimerrors.ScimErrorMutability
		}
		user.ExternalID = externalID
	}
	if value, present := attributes["active"]; present {
		active, ok := directorySCIMBool(value)
		if !ok {
			return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
		}
		user.Active = active
	}
	if value, present := attributes["displayName"]; present {
		displayName, ok := directorySCIMString(value)
		if !ok {
			return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
		}
		user.DisplayName = displayName
	} else if value, present := attributes["name"]; present {
		if formatted, ok := directorySCIMNestedString(value, "formatted"); ok {
			user.DisplayName = formatted
		}
	}
	if value, present := attributes["emails"]; present {
		email, ok := directorySCIMEmail(value)
		if !ok {
			return domain.DirectoryUser{}, scimerrors.ScimErrorInvalidValue
		}
		user.Email = email
	}
	return user, nil
}

func directoryPatchGroup(group domain.DirectoryGroup, operations []scim.PatchOperation) (domain.DirectoryGroup, []uuid.UUID, error) {
	members := directoryUserIDs(group.Members)
	for _, operation := range operations {
		path := ""
		if operation.Path != nil {
			path = strings.ToLower(operation.Path.String())
		}
		switch path {
		case "":
			attributes, ok := operation.Value.(map[string]interface{})
			if !ok || operation.Op == scim.PatchOperationRemove {
				return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorInvalidPath
			}
			updated, replacement, err := directoryPatchGroupAttributes(group, members, scim.ResourceAttributes(attributes))
			if err != nil {
				return domain.DirectoryGroup{}, nil, err
			}
			group, members = updated, replacement
		case "displayname":
			value, ok := directorySCIMString(operation.Value)
			if operation.Op == scim.PatchOperationRemove || !ok || value == "" {
				return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorInvalidValue
			}
			group.DisplayName = value
		case "active":
			if operation.Op == scim.PatchOperationRemove {
				group.Active = false
				continue
			}
			value, ok := directorySCIMBool(operation.Value)
			if !ok {
				return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorInvalidValue
			}
			group.Active = value
		case "externalid":
			value, ok := directorySCIMString(operation.Value)
			if operation.Op == scim.PatchOperationRemove || !ok {
				return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorMutability
			}
			group.ExternalID = value
		case "members":
			if operation.Op == scim.PatchOperationRemove {
				members = nil
				continue
			}
			value, err := directorySCIMMemberIDs(operation.Value)
			if err != nil {
				return domain.DirectoryGroup{}, nil, err
			}
			if operation.Op == scim.PatchOperationAdd {
				members = append(members, value...)
			} else {
				members = value
			}
		default:
			if strings.HasPrefix(path, "members[value eq ") && strings.HasSuffix(path, "]") && operation.Op == scim.PatchOperationRemove {
				memberID, err := directoryMemberIDFromPath(path)
				if err != nil {
					return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorInvalidPath
				}
				members = directoryRemoveUserID(members, memberID)
				continue
			}
			return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorInvalidPath
		}
	}
	return group, directoryUniqueUserIDs(members), nil
}

func directoryPatchGroupAttributes(group domain.DirectoryGroup, members []uuid.UUID, attributes scim.ResourceAttributes) (domain.DirectoryGroup, []uuid.UUID, error) {
	if value, present := attributes["displayName"]; present {
		displayName, ok := directorySCIMString(value)
		if !ok || displayName == "" {
			return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorInvalidValue
		}
		group.DisplayName = displayName
	}
	if value, present := attributes["externalId"]; present {
		externalID, ok := directorySCIMString(value)
		if !ok {
			return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorMutability
		}
		group.ExternalID = externalID
	}
	if value, present := attributes["active"]; present {
		active, ok := directorySCIMBool(value)
		if !ok {
			return domain.DirectoryGroup{}, nil, scimerrors.ScimErrorInvalidValue
		}
		group.Active = active
	}
	if value, present := attributes["members"]; present {
		replacement, err := directorySCIMMemberIDs(value)
		if err != nil {
			return domain.DirectoryGroup{}, nil, err
		}
		members = replacement
	}
	return group, directoryUniqueUserIDs(members), nil
}
