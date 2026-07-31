package http

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	cryptoutil "github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

func TestDirectorySCIMUserLifecycleAndConnectorIsolation(t *testing.T) {
	t.Parallel()
	connectorID := uuid.New()
	bearerToken := "directory-test-token"
	bearerHash, err := cryptoutil.HashKey(bearerToken)
	require.NoError(t, err)
	repo := &directorySCIMRepositoryStub{
		connector: &domain.DirectoryConnector{
			ID:              connectorID,
			ProviderID:      uuid.New(),
			Status:          domain.DirectoryConnectorObserve,
			BearerTokenHash: bearerHash,
		},
		users:  make(map[uuid.UUID]*domain.DirectoryUser),
		groups: make(map[uuid.UUID]*domain.DirectoryGroup),
	}
	directory := service.NewDirectoryIdentityService(repo, service.DirectoryIdentityConfig{})
	e := echo.New()
	require.NoError(t, RegisterDirectorySCIM(e, directory, DirectorySCIMConfig{}))

	userBody := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"entra-user-1","userName":"alex@example.com","displayName":"Alex Example","active":true,"emails":[{"value":"alex@example.com","type":"work","primary":true}]}`
	request := httptest.NewRequest(nethttp.MethodPost, "/scim/v2/"+connectorID.String()+"/Users", strings.NewReader(userBody))
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	request.Header.Set(echo.HeaderContentType, "application/scim+json")
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusCreated, response.Code, response.Body.String())
	var created struct {
		ID       string `json:"id"`
		External string `json:"externalId"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)
	require.Equal(t, "entra-user-1", created.External)

	request = httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+"/Users/"+created.ID, nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"displayName":"Alex Example"`)

	replacement := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"entra-user-1","userName":"alex@example.com","displayName":"Alex Updated","active":true}`
	request = httptest.NewRequest(nethttp.MethodPut, "/scim/v2/"+connectorID.String()+"/Users/"+created.ID, strings.NewReader(replacement))
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	request.Header.Set(echo.HeaderContentType, "application/scim+json")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())

	request = httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+"/Users?filter=userName%20eq%20%22alex@example.com%22", nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	var listed struct {
		TotalResults int `json:"totalResults"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &listed))
	require.Equal(t, 1, listed.TotalResults)

	patch := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":false}]}`
	request = httptest.NewRequest(nethttp.MethodPatch, "/scim/v2/"+connectorID.String()+"/Users/"+created.ID, strings.NewReader(patch))
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	request.Header.Set(echo.HeaderContentType, "application/scim+json")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	userID, err := uuid.Parse(created.ID)
	require.NoError(t, err)
	require.False(t, repo.users[userID].Active)

	request = httptest.NewRequest(nethttp.MethodDelete, "/scim/v2/"+connectorID.String()+"/Users/"+created.ID, nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusNoContent, response.Code, response.Body.String())

	request = httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+"/Users", nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer wrong-token")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusUnauthorized, response.Code)
	require.Contains(t, response.Header().Get(echo.HeaderContentType), "application/scim+json")
}

func TestDirectorySCIMGroupAndOAuthLifecycle(t *testing.T) {
	t.Parallel()

	connectorID := uuid.New()
	bearerToken := "directory-group-token"
	bearerHash, err := cryptoutil.HashKey(bearerToken)
	require.NoError(t, err)
	clientSecret := "directory-client-secret"
	clientSecretHash, err := cryptoutil.HashKey(clientSecret)
	require.NoError(t, err)
	repo := &directorySCIMRepositoryStub{
		connector: &domain.DirectoryConnector{
			ID:                    connectorID,
			ProviderID:            uuid.New(),
			Status:                domain.DirectoryConnectorObserve,
			BearerTokenHash:       bearerHash,
			OAuthClientID:         "directory-client",
			OAuthClientSecretHash: clientSecretHash,
		},
		users:       make(map[uuid.UUID]*domain.DirectoryUser),
		groups:      make(map[uuid.UUID]*domain.DirectoryGroup),
		oauthTokens: make(map[string]directorySCIMOAuthToken),
	}
	directory := service.NewDirectoryIdentityService(repo, service.DirectoryIdentityConfig{})
	e := echo.New()
	require.NoError(t, RegisterDirectorySCIM(e, directory, DirectorySCIMConfig{PublicBaseURL: "https://scim.example.com"}))

	createUser := func(externalID, userName string) string {
		body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"` + externalID + `","userName":"` + userName + `","displayName":"` + userName + `","active":true}`
		request := httptest.NewRequest(nethttp.MethodPost, "/scim/v2/"+connectorID.String()+"/Users", strings.NewReader(body))
		request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
		request.Header.Set(echo.HeaderContentType, "application/scim+json")
		response := httptest.NewRecorder()
		e.ServeHTTP(response, request)
		require.Equal(t, nethttp.StatusCreated, response.Code, response.Body.String())
		var resource struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &resource))
		return resource.ID
	}

	firstUserID := createUser("entra-user-1", "alex@example.com")
	secondUserID := createUser("entra-user-2", "bea@example.com")
	groupBody := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"externalId":"entra-research-manager","displayName":"ResearchManager","members":[{"value":"` + firstUserID + `"},{"value":"` + secondUserID + `"}]}`
	request := httptest.NewRequest(nethttp.MethodPost, "/scim/v2/"+connectorID.String()+"/Groups", strings.NewReader(groupBody))
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	request.Header.Set(echo.HeaderContentType, "application/scim+json")
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusCreated, response.Code, response.Body.String())
	var groupResource struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &groupResource))
	groupID, err := uuid.Parse(groupResource.ID)
	require.NoError(t, err)
	require.Len(t, repo.groups[groupID].Members, 2)

	request = httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+"/Groups/"+groupResource.ID, nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())

	request = httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+"/Groups?filter=displayName%20eq%20%22ResearchManager%22", nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"totalResults":1`)

	patch := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"displayName","value":"ResearchTeamManager"},{"op":"Remove","path":"members[value eq \"` + secondUserID + `\"]"}]}`
	request = httptest.NewRequest(nethttp.MethodPatch, "/scim/v2/"+connectorID.String()+"/Groups/"+groupResource.ID, strings.NewReader(patch))
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	request.Header.Set(echo.HeaderContentType, "application/scim+json")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "ResearchTeamManager", repo.groups[groupID].DisplayName)
	require.Len(t, repo.groups[groupID].Members, 1)

	replace := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"externalId":"entra-research-manager","displayName":"ResearchManager","members":[{"value":"` + secondUserID + `"}]}`
	request = httptest.NewRequest(nethttp.MethodPut, "/scim/v2/"+connectorID.String()+"/Groups/"+groupResource.ID, strings.NewReader(replace))
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	request.Header.Set(echo.HeaderContentType, "application/scim+json")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "ResearchManager", repo.groups[groupID].DisplayName)
	require.Len(t, repo.groups[groupID].Members, 1)

	request = httptest.NewRequest(nethttp.MethodDelete, "/scim/v2/"+connectorID.String()+"/Groups/"+groupResource.ID, nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusNoContent, response.Code, response.Body.String())
	require.False(t, repo.groups[groupID].Active)

	request = httptest.NewRequest(nethttp.MethodPut, "/scim/v2/"+connectorID.String()+"/Users/"+firstUserID, strings.NewReader(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"entra-user-1","userName":"alex@example.com","displayName":"Alex Updated","active":true}`))
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	request.Header.Set(echo.HeaderContentType, "application/scim+json")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	firstUserUUID := uuid.MustParse(firstUserID)
	require.Equal(t, "Alex Updated", repo.users[firstUserUUID].DisplayName)

	request = httptest.NewRequest(nethttp.MethodDelete, "/scim/v2/"+connectorID.String()+"/Users/"+firstUserID, nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusNoContent, response.Code, response.Body.String())
	require.False(t, repo.users[firstUserUUID].Active)

	request = httptest.NewRequest(nethttp.MethodPost, "/scim/oauth/token", strings.NewReader("grant_type=client_credentials"))
	request.SetBasicAuth("directory-client", clientSecret)
	request.Header.Set(echo.HeaderContentType, "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &tokenResponse))
	require.NotEmpty(t, tokenResponse.AccessToken)

	request = httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+"/ServiceProviderConfig", nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+tokenResponse.AccessToken)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"patch"`)
}

func TestDirectorySCIMRejectsMalformedOrUnauthorizedRequestsWithoutLeakingState(t *testing.T) {
	t.Parallel()

	require.Error(t, RegisterDirectorySCIM(nil, nil, DirectorySCIMConfig{}))
	e := echo.New()
	require.Error(t, RegisterDirectorySCIM(e, nil, DirectorySCIMConfig{}))

	connectorID := uuid.New()
	bearerToken := "directory-safe-error-token"
	bearerHash, err := cryptoutil.HashKey(bearerToken)
	require.NoError(t, err)
	repo := &directorySCIMRepositoryStub{
		connector: &domain.DirectoryConnector{
			ID:              connectorID,
			ProviderID:      uuid.New(),
			Status:          domain.DirectoryConnectorObserve,
			BearerTokenHash: bearerHash,
		},
		users:  make(map[uuid.UUID]*domain.DirectoryUser),
		groups: make(map[uuid.UUID]*domain.DirectoryGroup),
	}
	directory := service.NewDirectoryIdentityService(repo, service.DirectoryIdentityConfig{})
	require.NoError(t, RegisterDirectorySCIM(e, directory, DirectorySCIMConfig{}))

	request := httptest.NewRequest(nethttp.MethodGet, "/scim/v2/not-a-uuid/Users", nil)
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusNotFound, response.Code)

	request = httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+"/Users", nil)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusUnauthorized, response.Code)

	request = httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+"/Users/not-a-uuid", nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusNotFound, response.Code)

	request = httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+"/Users/"+uuid.NewString(), nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusNotFound, response.Code)

	request = httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+"/Groups/"+uuid.NewString(), nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusNotFound, response.Code)

	request = httptest.NewRequest(nethttp.MethodPost, "/scim/oauth/token", strings.NewReader("grant_type=password"))
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "unsupported_grant_type")

	request = httptest.NewRequest(nethttp.MethodPost, "/scim/oauth/token", strings.NewReader("grant_type=client_credentials"))
	request.SetBasicAuth("missing", "wrong")
	request.Header.Set(echo.HeaderContentType, "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusUnauthorized, response.Code)
	require.Contains(t, response.Body.String(), "invalid_client")

	runtimeFailure := echo.New()
	require.NoError(t, RegisterDirectorySCIM(runtimeFailure, directory, DirectorySCIMConfig{RuntimeConfig: controlIdentityHTTPRuntime{err: errors.New("runtime unavailable")}}))
	request = httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+"/ServiceProviderConfig", nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+bearerToken)
	response = httptest.NewRecorder()
	runtimeFailure.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusInternalServerError, response.Code)
}

type directorySCIMRepositoryStub struct {
	repository.DirectoryIdentityRepository
	connector       *domain.DirectoryConnector
	users           map[uuid.UUID]*domain.DirectoryUser
	groups          map[uuid.UUID]*domain.DirectoryGroup
	oauthTokens     map[string]directorySCIMOAuthToken
	appliedPlans    []domain.DirectoryReconcilePlan
	activationPlans []domain.DirectoryReconcilePlan
	adoptions       []directorySCIMTeamAdoption
}

type directorySCIMOAuthToken struct {
	connectorID uuid.UUID
	expiresAt   time.Time
}

type directorySCIMTeamAdoption struct {
	connectorID uuid.UUID
	groupID     uuid.UUID
	teamID      uuid.UUID
}

func (r *directorySCIMRepositoryStub) CreateDirectoryConnector(_ context.Context, connector *domain.DirectoryConnector) error {
	if connector.ID == uuid.Nil {
		connector.ID = uuid.New()
	}
	copy := *connector
	r.connector = &copy
	return nil
}

func (r *directorySCIMRepositoryStub) GetDirectoryConnector(_ context.Context, id uuid.UUID) (*domain.DirectoryConnector, error) {
	if r.connector == nil || r.connector.ID != id {
		return nil, nil
	}
	copy := *r.connector
	return &copy, nil
}

func (r *directorySCIMRepositoryStub) GetDirectoryConnectorByProviderID(_ context.Context, providerID uuid.UUID) (*domain.DirectoryConnector, error) {
	if r.connector == nil || r.connector.ProviderID != providerID {
		return nil, nil
	}
	copy := *r.connector
	return &copy, nil
}

func (r *directorySCIMRepositoryStub) ListDirectoryConnectors(context.Context) ([]*domain.DirectoryConnector, error) {
	if r.connector == nil {
		return []*domain.DirectoryConnector{}, nil
	}
	copy := *r.connector
	return []*domain.DirectoryConnector{&copy}, nil
}

func (r *directorySCIMRepositoryStub) UpdateDirectoryConnector(_ context.Context, connector *domain.DirectoryConnector) error {
	if r.connector == nil || r.connector.ID != connector.ID {
		return nil
	}
	copy := *connector
	r.connector = &copy
	return nil
}

func (r *directorySCIMRepositoryStub) SetDirectoryConnectorStatus(_ context.Context, connectorID uuid.UUID, status domain.DirectoryConnectorStatus, _ *time.Time) error {
	if r.connector != nil && r.connector.ID == connectorID {
		r.connector.Status = status
	}
	return nil
}

func (r *directorySCIMRepositoryStub) SetDirectoryCredentials(_ context.Context, connectorID uuid.UUID, version int, bearerHash, clientID, secretHash string) error {
	if r.connector == nil || r.connector.ID != connectorID {
		return nil
	}
	r.connector.CredentialVersion = version
	r.connector.BearerTokenHash = bearerHash
	r.connector.OAuthClientID = clientID
	r.connector.OAuthClientSecretHash = secretHash
	return nil
}

func (r *directorySCIMRepositoryStub) GetDirectoryOAuthToken(_ context.Context, connectorID uuid.UUID, tokenHash string) (bool, error) {
	token, ok := r.oauthTokens[tokenHash]
	return ok && token.connectorID == connectorID && token.expiresAt.After(time.Now()), nil
}

func (r *directorySCIMRepositoryStub) GetDirectoryConnectorByOAuthClientID(_ context.Context, clientID string) (*domain.DirectoryConnector, error) {
	if r.connector == nil || r.connector.OAuthClientID != clientID {
		return nil, nil
	}
	copy := *r.connector
	return &copy, nil
}

func (r *directorySCIMRepositoryStub) CreateDirectoryOAuthToken(_ context.Context, connectorID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	if r.oauthTokens == nil {
		r.oauthTokens = make(map[string]directorySCIMOAuthToken)
	}
	r.oauthTokens[tokenHash] = directorySCIMOAuthToken{connectorID: connectorID, expiresAt: expiresAt}
	return nil
}

func (r *directorySCIMRepositoryStub) DeleteExpiredDirectoryOAuthTokens(_ context.Context, now time.Time) error {
	for tokenHash, token := range r.oauthTokens {
		if !token.expiresAt.After(now) {
			delete(r.oauthTokens, tokenHash)
		}
	}
	return nil
}

func (r *directorySCIMRepositoryStub) UpsertDirectoryUser(_ context.Context, user domain.DirectoryUser) (*domain.DirectoryUser, error) {
	if user.ID == uuid.Nil {
		for _, existing := range r.users {
			if existing.ExternalID != "" && existing.ExternalID == user.ExternalID {
				user.ID = existing.ID
				break
			}
		}
		if user.ID == uuid.Nil {
			user.ID = uuid.New()
		}
	}
	if user.IdentityID == uuid.Nil {
		user.IdentityID = uuid.New()
	}
	copy := user
	r.users[user.ID] = &copy
	return &copy, nil
}

func (r *directorySCIMRepositoryStub) GetDirectoryUser(_ context.Context, connectorID, userID uuid.UUID) (*domain.DirectoryUser, error) {
	if r.connector == nil || r.connector.ID != connectorID || r.users[userID] == nil {
		return nil, nil
	}
	copy := *r.users[userID]
	return &copy, nil
}

func (r *directorySCIMRepositoryStub) ListDirectoryUsers(_ context.Context, connectorID uuid.UUID) ([]*domain.DirectoryUser, error) {
	if r.connector == nil || r.connector.ID != connectorID {
		return []*domain.DirectoryUser{}, nil
	}
	users := make([]*domain.DirectoryUser, 0, len(r.users))
	for _, user := range r.users {
		copy := *user
		users = append(users, &copy)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].UserName < users[j].UserName })
	return users, nil
}

func (r *directorySCIMRepositoryStub) UpsertDirectoryGroup(_ context.Context, group domain.DirectoryGroup) (*domain.DirectoryGroup, error) {
	if group.ID == uuid.Nil {
		group.ID = uuid.New()
	}
	copy := group
	r.groups[group.ID] = &copy
	return &copy, nil
}

func (r *directorySCIMRepositoryStub) UpsertDirectoryGroupWithMembers(ctx context.Context, group domain.DirectoryGroup, memberIDs []uuid.UUID) (*domain.DirectoryGroup, error) {
	stored, err := r.UpsertDirectoryGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	if err := r.ReplaceDirectoryGroupMembers(ctx, stored.ConnectorID, stored.ID, memberIDs); err != nil {
		return nil, err
	}
	return r.GetDirectoryGroup(ctx, stored.ConnectorID, stored.ID)
}

func (r *directorySCIMRepositoryStub) GetDirectoryGroup(_ context.Context, connectorID, groupID uuid.UUID) (*domain.DirectoryGroup, error) {
	if r.connector == nil || r.connector.ID != connectorID || r.groups[groupID] == nil {
		return nil, nil
	}
	copy := *r.groups[groupID]
	copy.Members = append([]domain.DirectoryUser(nil), r.groups[groupID].Members...)
	return &copy, nil
}

func (r *directorySCIMRepositoryStub) ListDirectoryGroups(_ context.Context, connectorID uuid.UUID) ([]*domain.DirectoryGroup, error) {
	if r.connector == nil || r.connector.ID != connectorID {
		return []*domain.DirectoryGroup{}, nil
	}
	groups := make([]*domain.DirectoryGroup, 0, len(r.groups))
	for _, group := range r.groups {
		copy := *group
		copy.Members = append([]domain.DirectoryUser(nil), group.Members...)
		groups = append(groups, &copy)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].DisplayName < groups[j].DisplayName })
	return groups, nil
}

func (r *directorySCIMRepositoryStub) ReplaceDirectoryGroupMembers(_ context.Context, connectorID, groupID uuid.UUID, memberIDs []uuid.UUID) error {
	if r.connector == nil || r.connector.ID != connectorID || r.groups[groupID] == nil {
		return nil
	}
	members := make([]domain.DirectoryUser, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		if user := r.users[memberID]; user != nil {
			members = append(members, *user)
		}
	}
	r.groups[groupID].Members = members
	return nil
}

func (r *directorySCIMRepositoryStub) DirectoryConnectorSnapshot(_ context.Context, connectorID uuid.UUID) (*domain.DirectoryConnectorSnapshot, error) {
	if r.connector == nil || r.connector.ID != connectorID {
		return nil, nil
	}
	snapshot := &domain.DirectoryConnectorSnapshot{Connector: *r.connector}
	for _, user := range r.users {
		if user.ConnectorID == connectorID {
			snapshot.Users = append(snapshot.Users, *user)
		}
	}
	for _, group := range r.groups {
		if group.ConnectorID == connectorID {
			copy := *group
			copy.Members = append([]domain.DirectoryUser(nil), group.Members...)
			snapshot.Groups = append(snapshot.Groups, copy)
		}
	}
	return snapshot, nil
}

func (r *directorySCIMRepositoryStub) ApplyDirectoryReconcilePlan(_ context.Context, plan domain.DirectoryReconcilePlan) error {
	r.appliedPlans = append(r.appliedPlans, plan)
	return nil
}

func (r *directorySCIMRepositoryStub) ActivateDirectoryConnector(_ context.Context, plan domain.DirectoryReconcilePlan) error {
	if r.connector != nil && r.connector.ID == plan.ConnectorID {
		r.connector.Status = domain.DirectoryConnectorActive
	}
	r.activationPlans = append(r.activationPlans, plan)
	return nil
}

func (r *directorySCIMRepositoryStub) AdoptDirectoryTeam(_ context.Context, connectorID, groupID, teamID uuid.UUID) error {
	r.adoptions = append(r.adoptions, directorySCIMTeamAdoption{connectorID: connectorID, groupID: groupID, teamID: teamID})
	return nil
}
