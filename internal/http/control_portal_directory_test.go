package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

func TestControlDirectoryConnectorHandlersDriveLifecycle(t *testing.T) {
	t.Parallel()

	repo := &directorySCIMRepositoryStub{
		users:       make(map[uuid.UUID]*domain.DirectoryUser),
		groups:      make(map[uuid.UUID]*domain.DirectoryGroup),
		oauthTokens: make(map[string]directorySCIMOAuthToken),
	}
	directory := service.NewDirectoryIdentityService(repo, service.DirectoryIdentityConfig{})
	handler := &controlPortalHandler{directory: directory}
	providerID := uuid.New()
	connectorBody := `{"group_pattern":"^(?P<team>.+?)(?P<role>Readonly|Member|Manager)$","role_entitlements":{"Readonly":{"role":"member","scopes":["read"]},"Member":{"role":"member","scopes":["read","write"]},"Manager":{"role":"manager","scopes":["read","write","feedback:read"]}},"max_auto_teams":5}`

	c, rec := controlDirectoryContext(nethttp.MethodPost, connectorBody, map[string]string{"providerId": providerID.String()})
	require.NoError(t, handler.createDirectoryConnector(c))
	require.Equal(t, nethttp.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		Data controlDirectoryCredentialResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	connectorID := created.Data.Connector.ID
	require.NotEqual(t, uuid.Nil, connectorID)
	require.Equal(t, connectorID, created.Data.Credential.ConnectorID)
	require.NotEmpty(t, created.Data.Credential.BearerToken)

	c, rec = controlDirectoryContext(nethttp.MethodGet, "", nil)
	require.NoError(t, handler.listDirectoryConnectors(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), connectorID.String())

	c, rec = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"providerId": providerID.String()})
	require.NoError(t, handler.getDirectoryConnector(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"status":"disabled"`)

	updateBody := `{"group_pattern":"^(?P<team>.+?)(?P<role>Readonly|Member|Manager)$","role_entitlements":{"Readonly":{"role":"member","scopes":["read"]},"Member":{"role":"member","scopes":["read","write"]},"Manager":{"role":"manager","scopes":["read","write","feedback:read"]}},"max_auto_teams":8}`
	c, rec = controlDirectoryContext(nethttp.MethodPatch, updateBody, map[string]string{"connectorId": connectorID.String()})
	require.NoError(t, handler.updateDirectoryConnector(c))
	require.Equal(t, nethttp.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"max_auto_teams":8`)

	c, rec = controlDirectoryContext(nethttp.MethodPost, "", map[string]string{"connectorId": connectorID.String()})
	require.NoError(t, handler.rotateDirectoryCredentials(c))
	require.Equal(t, nethttp.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"credential_version":2`)

	identityID := uuid.New()
	user := &domain.DirectoryUser{
		ID:          uuid.New(),
		ConnectorID: connectorID,
		ExternalID:  "entra-user-1",
		UserName:    "alex@example.com",
		DisplayName: "Alex",
		Active:      true,
		IdentityID:  identityID,
	}
	repo.users[user.ID] = user
	groupID := uuid.New()
	repo.groups[groupID] = &domain.DirectoryGroup{
		ID:          groupID,
		ConnectorID: connectorID,
		ExternalID:  "entra-research-manager",
		DisplayName: "ResearchManager",
		Active:      true,
		Members:     []domain.DirectoryUser{*user},
	}

	c, rec = controlDirectoryContext(nethttp.MethodPost, `{"status":"observe"}`, map[string]string{"connectorId": connectorID.String()})
	require.NoError(t, handler.setDirectoryConnectorStatus(c))
	require.Equal(t, nethttp.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"observe"`)

	c, rec = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"connectorId": connectorID.String()})
	require.NoError(t, handler.previewDirectoryConnector(c))
	require.Equal(t, nethttp.StatusOK, rec.Code, rec.Body.String())
	var previewResponse struct {
		Data domain.DirectoryPreview `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &previewResponse))
	require.Len(t, previewResponse.Data.Candidates, 1)
	require.Equal(t, "Research", previewResponse.Data.Candidates[0].TeamName)

	c, rec = controlDirectoryContext(nethttp.MethodPost, `{"status":"active","preview_version":"`+previewResponse.Data.Version+`"}`, map[string]string{"connectorId": connectorID.String()})
	require.NoError(t, handler.setDirectoryConnectorStatus(c))
	require.Equal(t, nethttp.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"active"`)
	require.Len(t, repo.activationPlans, 1)

	teamID := uuid.New()
	c, rec = controlDirectoryContext(nethttp.MethodPost, `{"team_id":"`+teamID.String()+`"}`, map[string]string{"connectorId": connectorID.String(), "groupId": groupID.String()})
	require.NoError(t, handler.adoptDirectoryGroupTeam(c))
	require.Equal(t, nethttp.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []directorySCIMTeamAdoption{{connectorID: connectorID, groupID: groupID, teamID: teamID}}, repo.adoptions)

	c, rec = controlDirectoryContext(nethttp.MethodPost, `{"status":"disabled"}`, map[string]string{"connectorId": connectorID.String()})
	require.NoError(t, handler.setDirectoryConnectorStatus(c))
	require.Equal(t, nethttp.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"disabled"`)
}

func TestControlDirectoryErrorsAndResponseMapping(t *testing.T) {
	t.Parallel()

	handler := &controlPortalHandler{}
	c, _ := controlDirectoryContext(nethttp.MethodGet, "", nil)
	err := handler.listDirectoryConnectors(c)
	require.Error(t, err)
	c, _ = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"providerId": uuid.NewString()})
	require.Error(t, handler.getDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "{}", map[string]string{"providerId": uuid.NewString()})
	require.Error(t, handler.createDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPatch, "{}", map[string]string{"connectorId": uuid.NewString()})
	require.Error(t, handler.updateDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "", map[string]string{"connectorId": uuid.NewString()})
	require.Error(t, handler.rotateDirectoryCredentials(c))
	c, _ = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"connectorId": uuid.NewString()})
	require.Error(t, handler.previewDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "{}", map[string]string{"connectorId": uuid.NewString()})
	require.Error(t, handler.setDirectoryConnectorStatus(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "{}", map[string]string{"connectorId": uuid.NewString(), "groupId": uuid.NewString()})
	require.Error(t, handler.adoptDirectoryGroupTeam(c))

	for _, testCase := range []struct {
		err  error
		code httperr.ErrorCode
	}{
		{service.ErrDirectoryConnectorDisabled, httperr.CONFLICT},
		{assertError("directory connector not found"), httperr.NOT_FOUND},
		{service.ErrDirectoryPreviewStale, httperr.CONFLICT},
		{assertError("directory role entitlement is invalid"), httperr.VALIDATION_ERROR},
		{assertError("storage unavailable"), httperr.INTERNAL_ERROR},
	} {
		mapped := controlDirectoryServiceError(testCase.err)
		httpError, ok := mapped.(*httperr.APIError)
		require.True(t, ok)
		require.Equal(t, testCase.code, httpError.GetCode())
	}

	activated := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	response := toControlDirectoryConnector(&domain.DirectoryConnector{
		ID:                uuid.New(),
		ProviderID:        uuid.New(),
		Status:            domain.DirectoryConnectorActive,
		RoleEntitlements:  map[string]domain.DirectoryRoleEntitlement{"Manager": {Role: "manager", Scopes: []string{"read", "write"}}},
		LastActivationAt:  &activated,
		CreatedAt:         activated,
		UpdatedAt:         activated,
		CredentialVersion: 3,
	})
	require.NotNil(t, response.LastActivationAt)
	require.Equal(t, "manager", response.RoleEntitlements["Manager"].Role)
	require.Equal(t, controlDirectoryConnectorResponse{}, toControlDirectoryConnector(nil))
}

func TestControlDirectoryHandlersValidateIdentifiersBodiesAndServiceFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	unavailable := &controlPortalHandler{directory: service.NewDirectoryIdentityService(nil, service.DirectoryIdentityConfig{})}
	validID := uuid.NewString()

	c, _ := controlDirectoryContext(nethttp.MethodGet, "", nil)
	require.Error(t, unavailable.listDirectoryConnectors(c))
	c, _ = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"providerId": "not-a-uuid"})
	require.Error(t, unavailable.getDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"providerId": validID})
	require.Error(t, unavailable.getDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "", map[string]string{"providerId": "not-a-uuid"})
	require.Error(t, unavailable.createDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "{", map[string]string{"providerId": validID})
	require.Error(t, unavailable.createDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "{}", map[string]string{"providerId": validID})
	require.Error(t, unavailable.createDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPatch, "{}", map[string]string{"connectorId": "not-a-uuid"})
	require.Error(t, unavailable.updateDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPatch, "{}", map[string]string{"connectorId": validID})
	require.Error(t, unavailable.updateDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "", map[string]string{"connectorId": "not-a-uuid"})
	require.Error(t, unavailable.rotateDirectoryCredentials(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "", map[string]string{"connectorId": validID})
	require.Error(t, unavailable.rotateDirectoryCredentials(c))
	c, _ = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"connectorId": "not-a-uuid"})
	require.Error(t, unavailable.previewDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"connectorId": validID})
	require.Error(t, unavailable.previewDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "{}", map[string]string{"connectorId": "not-a-uuid"})
	require.Error(t, unavailable.setDirectoryConnectorStatus(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "{", map[string]string{"connectorId": validID})
	require.Error(t, unavailable.setDirectoryConnectorStatus(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, `{"status":"observe"}`, map[string]string{"connectorId": validID})
	require.Error(t, unavailable.setDirectoryConnectorStatus(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "{}", map[string]string{"connectorId": "not-a-uuid", "groupId": validID})
	require.Error(t, unavailable.adoptDirectoryGroupTeam(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "{}", map[string]string{"connectorId": validID, "groupId": "not-a-uuid"})
	require.Error(t, unavailable.adoptDirectoryGroupTeam(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "{", map[string]string{"connectorId": validID, "groupId": validID})
	require.Error(t, unavailable.adoptDirectoryGroupTeam(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, `{"team_id":"not-a-uuid"}`, map[string]string{"connectorId": validID, "groupId": validID})
	require.Error(t, unavailable.adoptDirectoryGroupTeam(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, `{"team_id":"`+uuid.NewString()+`"}`, map[string]string{"connectorId": validID, "groupId": validID})
	require.Error(t, unavailable.adoptDirectoryGroupTeam(c))

	repo := &directorySCIMRepositoryStub{users: make(map[uuid.UUID]*domain.DirectoryUser), groups: make(map[uuid.UUID]*domain.DirectoryGroup)}
	directory := service.NewDirectoryIdentityService(repo, service.DirectoryIdentityConfig{})
	connector, _, err := directory.CreateConnector(ctx, domain.DirectoryConnector{
		ProviderID:       uuid.New(),
		GroupPattern:     "^(?P<team>.+?)(?P<role>Readonly|Member|Manager)$",
		RoleEntitlements: map[string]domain.DirectoryRoleEntitlement{"Readonly": {Role: service.APIKeyRoleMember, Scopes: []string{service.APIKeyScopeRead}}, "Member": {Role: service.APIKeyRoleMember, Scopes: []string{service.APIKeyScopeRead, service.APIKeyScopeWrite}}, "Manager": {Role: service.APIKeyRoleManager, Scopes: []string{service.APIKeyScopeRead, service.APIKeyScopeWrite}}},
		MaxAutoTeams:     1,
	})
	require.NoError(t, err)
	handler := &controlPortalHandler{directory: directory}
	c, _ = controlDirectoryContext(nethttp.MethodPatch, "{", map[string]string{"connectorId": connector.ID.String()})
	require.Error(t, handler.updateDirectoryConnector(c))
	c, _ = controlDirectoryContext(nethttp.MethodPatch, "{}", map[string]string{"connectorId": connector.ID.String()})
	require.Error(t, handler.updateDirectoryConnector(c))
}

func controlDirectoryContext(method, body string, params map[string]string) (echo.Context, *httptest.ResponseRecorder) {
	request := httptest.NewRequest(method, "/control/api", strings.NewReader(body))
	if body != "" {
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	if len(params) > 0 {
		names := make([]string, 0, len(params))
		values := make([]string, 0, len(params))
		for _, name := range []string{"providerId", "connectorId", "groupId"} {
			if value, found := params[name]; found {
				names = append(names, name)
				values = append(values, value)
			}
		}
		ctx.SetParamNames(names...)
		ctx.SetParamValues(values...)
	}
	return ctx, recorder
}

type assertError string

func (e assertError) Error() string { return string(e) }
