package http

import (
	"context"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/elimity-com/scim"
	scimerrors "github.com/elimity-com/scim/errors"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	filterparser "github.com/scim2/filter-parser/v2"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

func TestDirectorySCIMPatchAndConversionHelpers(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	user := domain.DirectoryUser{ID: userID, ExternalID: "entra-user", UserName: "alex@example.test", Email: "alex@example.test", DisplayName: "Alex", Active: true}
	updatedUser, err := directoryPatchUser(user, []scim.PatchOperation{
		{Op: scim.PatchOperationReplace, Path: directorySCIMTestPath(t, "userName"), Value: "new-alex@example.test"},
		{Op: scim.PatchOperationRemove, Path: directorySCIMTestPath(t, "displayName")},
		{Op: scim.PatchOperationReplace, Path: directorySCIMTestPath(t, "emails"), Value: []interface{}{map[string]interface{}{"value": "new-alex@example.test", "primary": true}}},
	})
	require.NoError(t, err)
	require.Equal(t, "new-alex@example.test", updatedUser.UserName)
	require.Equal(t, "new-alex@example.test", updatedUser.DisplayName)
	require.Equal(t, "new-alex@example.test", updatedUser.Email)

	updatedUser, err = directoryPatchUser(updatedUser, []scim.PatchOperation{{
		Op:    scim.PatchOperationReplace,
		Value: map[string]interface{}{"userName": "replacement@example.test", "name": map[string]interface{}{"formatted": "Replacement"}, "active": false},
	}})
	require.NoError(t, err)
	require.Equal(t, "replacement@example.test", updatedUser.UserName)
	require.Equal(t, "Replacement", updatedUser.DisplayName)
	require.False(t, updatedUser.Active)
	updatedUser, err = directoryPatchUser(updatedUser, []scim.PatchOperation{{
		Op:    scim.PatchOperationReplace,
		Value: map[string]interface{}{"active": true},
	}})
	require.NoError(t, err)
	require.True(t, updatedUser.Active)
	require.Equal(t, "replacement@example.test", updatedUser.UserName)
	require.Equal(t, "Replacement", updatedUser.DisplayName)
	updatedUser, err = directoryPatchUser(updatedUser, []scim.PatchOperation{{
		Op: scim.PatchOperationReplace,
		Value: map[string]interface{}{
			"externalId":  "entra-replacement",
			"displayName": "Replacement Name",
			"emails":      []interface{}{map[string]interface{}{"value": "replacement@example.test", "primary": true}},
		},
	}})
	require.NoError(t, err)
	require.Equal(t, "entra-replacement", updatedUser.ExternalID)
	require.Equal(t, "Replacement Name", updatedUser.DisplayName)
	require.Equal(t, "replacement@example.test", updatedUser.Email)
	for _, attributes := range []map[string]interface{}{
		{"userName": 42},
		{"externalId": 42},
		{"active": "invalid"},
		{"displayName": 42},
		{"emails": "invalid"},
	} {
		_, err = directoryPatchUser(updatedUser, []scim.PatchOperation{{Op: scim.PatchOperationReplace, Value: attributes}})
		require.Error(t, err)
	}
	_, err = directoryPatchUser(user, []scim.PatchOperation{{Op: scim.PatchOperationRemove, Path: directorySCIMTestPath(t, "externalId")}})
	require.Error(t, err)
	_, err = directoryPatchUser(user, []scim.PatchOperation{{Op: scim.PatchOperationReplace, Path: directorySCIMTestPath(t, "unknown"), Value: "value"}})
	require.Error(t, err)

	memberOne := uuid.New()
	memberTwo := uuid.New()
	group := domain.DirectoryGroup{ID: uuid.New(), ExternalID: "entra-research", DisplayName: "Research", Active: true, Members: []domain.DirectoryUser{{ID: memberOne}, {ID: memberTwo}}}
	updatedGroup, members, err := directoryPatchGroup(group, []scim.PatchOperation{
		{Op: scim.PatchOperationReplace, Path: directorySCIMTestPath(t, "displayName"), Value: "Research Team"},
		{Op: scim.PatchOperationRemove, Path: directorySCIMTestPath(t, "members[value eq \""+memberOne.String()+"\"]")},
		{Op: scim.PatchOperationAdd, Path: directorySCIMTestPath(t, "members"), Value: []interface{}{map[string]interface{}{"value": memberOne.String()}}},
	})
	require.NoError(t, err)
	require.Equal(t, "Research Team", updatedGroup.DisplayName)
	require.ElementsMatch(t, []uuid.UUID{memberOne, memberTwo}, members)

	updatedGroup, members, err = directoryPatchGroup(updatedGroup, []scim.PatchOperation{{
		Op:    scim.PatchOperationReplace,
		Value: map[string]interface{}{"displayName": "Replacement Group", "externalId": "entra-replacement", "active": false, "members": []interface{}{map[string]interface{}{"value": memberTwo.String()}}},
	}})
	require.NoError(t, err)
	require.Equal(t, "Replacement Group", updatedGroup.DisplayName)
	require.Equal(t, "entra-replacement", updatedGroup.ExternalID)
	require.False(t, updatedGroup.Active)
	require.Equal(t, []uuid.UUID{memberTwo}, members)
	updatedGroup.Members = []domain.DirectoryUser{{ID: memberTwo}}
	updatedGroup, members, err = directoryPatchGroup(updatedGroup, []scim.PatchOperation{{
		Op:    scim.PatchOperationReplace,
		Value: map[string]interface{}{"displayName": "Renamed Group"},
	}})
	require.NoError(t, err)
	require.Equal(t, "Renamed Group", updatedGroup.DisplayName)
	require.Equal(t, []uuid.UUID{memberTwo}, members)
	for _, attributes := range []map[string]interface{}{
		{"displayName": ""},
		{"externalId": 42},
		{"active": "invalid"},
		{"members": "invalid"},
	} {
		_, _, err = directoryPatchGroup(updatedGroup, []scim.PatchOperation{{Op: scim.PatchOperationReplace, Value: attributes}})
		require.Error(t, err)
	}
	_, _, err = directoryPatchGroup(group, []scim.PatchOperation{{Op: scim.PatchOperationRemove, Path: directorySCIMTestPath(t, "externalId")}})
	require.Error(t, err)

	parsedUser, err := directoryUserFromSCIM(scim.ResourceAttributes{
		"userName":   "  bea@example.test ",
		"externalId": "entra-bea",
		"active":     true,
		"name":       map[string]interface{}{"formatted": "Bea Example"},
		"emails":     []interface{}{map[string]interface{}{"value": "secondary@example.test"}, map[string]interface{}{"value": "bea@example.test", "primary": true}},
	}, domain.DirectoryUser{})
	require.NoError(t, err)
	require.Equal(t, "bea@example.test", parsedUser.Email)
	require.Equal(t, "Bea Example", parsedUser.DisplayName)

	parsedGroup, parsedMembers, err := directoryGroupFromSCIM(scim.ResourceAttributes{
		"displayName": "Research",
		"externalId":  "entra-research",
		"members":     []interface{}{map[string]interface{}{"value": memberOne.String()}, map[string]interface{}{"value": memberOne.String()}},
	}, domain.DirectoryGroup{})
	require.NoError(t, err)
	require.True(t, parsedGroup.Active)
	require.Equal(t, []uuid.UUID{memberOne}, parsedMembers)
	_, _, err = directoryGroupFromSCIM(scim.ResourceAttributes{"displayName": "Research", "members": "invalid"}, domain.DirectoryGroup{})
	require.Error(t, err)
}

func TestDirectorySCIMFilteringErrorsAndSmallHelpers(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	groupID := uuid.New()
	user := domain.DirectoryUser{ID: userID, ExternalID: "entra-user", UserName: "alex@example.test"}
	group := domain.DirectoryGroup{ID: groupID, ExternalID: "entra-group", DisplayName: "Research"}
	request := httptest.NewRequest(nethttp.MethodGet, "/Users?filter=userName%20eq%20%22alex@example.test%22", nil)
	filter, err := directoryParseSCIMFilter(request, "userName", "id")
	require.NoError(t, err)
	require.True(t, directorySCIMUserMatches(user, filter))
	require.False(t, directorySCIMGroupMatches(group, filter))

	request = httptest.NewRequest(nethttp.MethodGet, "/Groups?filter=displayName%20eq%20%22Research%22", nil)
	filter, err = directoryParseSCIMFilter(request, "displayName", "id")
	require.NoError(t, err)
	require.True(t, directorySCIMGroupMatches(group, filter))
	request = httptest.NewRequest(nethttp.MethodGet, "/Groups?filter=unsupported%20eq%20%22Research%22", nil)
	_, err = directoryParseSCIMFilter(request, "displayName", "id")
	require.Error(t, err)

	page := directorySCIMPage([]scim.Resource{{ID: "1"}, {ID: "2"}}, scim.ListRequestParams{StartIndex: 2, Count: 1})
	require.Equal(t, 2, page.TotalResults)
	require.Len(t, page.Resources, 1)
	require.Equal(t, "2", page.Resources[0].ID)
	emptyPage := directorySCIMPage([]scim.Resource{{ID: "1"}}, scim.ListRequestParams{StartIndex: 9, Count: 1})
	require.Empty(t, emptyPage.Resources)

	resource := directorySCIMGroupResource(domain.DirectoryGroup{ID: groupID, ExternalID: group.ExternalID, DisplayName: group.DisplayName, Members: []domain.DirectoryUser{{ID: userID, DisplayName: "Alex"}}})
	require.Equal(t, groupID.String(), resource.ID)
	require.NotNil(t, resource.Attributes["members"])
	require.Equal(t, []uuid.UUID{userID}, directoryUserIDs([]domain.DirectoryUser{{ID: userID}, {ID: userID}, {}}))
	require.Equal(t, []uuid.UUID{userID}, directoryRemoveUserID([]uuid.UUID{userID, groupID}, groupID))
	parsedMemberID, err := directoryMemberIDFromPath("members[value eq \"" + userID.String() + "\"]")
	require.NoError(t, err)
	require.Equal(t, userID, parsedMemberID)

	require.Error(t, directorySCIMGetError("missing", errors.New("user not found")))
	require.Error(t, directorySCIMGetError("missing", errors.New("storage failed")))
	require.Error(t, directorySCIMMutationError(service.ErrDirectoryConnectorDisabled))
	require.Error(t, directorySCIMMutationError(errors.New("external_id is immutable")))
	require.Error(t, directorySCIMMutationError(errors.New("required field")))

	e := echo.New()
	ctx := e.NewContext(httptest.NewRequest(nethttp.MethodPost, "/", nil), httptest.NewRecorder())
	require.NoError(t, directoryOAuthError(ctx, nethttp.StatusBadRequest, "invalid_request"))
	require.Equal(t, nethttp.StatusBadRequest, ctx.Response().Status)
	ctx = e.NewContext(httptest.NewRequest(nethttp.MethodGet, "/", nil), httptest.NewRecorder())
	require.NoError(t, directorySCIMError(ctx, scimErrorStatus(nethttp.StatusUnauthorized)))
	require.Equal(t, nethttp.StatusUnauthorized, ctx.Response().Status)

	require.Equal(t, "token", mustDirectoryBearer(t, "Bearer token"))
	_, ok := directoryBearerToken(httptest.NewRequest(nethttp.MethodGet, "/", nil))
	require.False(t, ok)
	_, err = directorySCIMConnectorID(nil)
	require.Error(t, err)
}

func TestDirectorySCIMHelperValidationBoundaries(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	groupID := uuid.New()
	user := domain.DirectoryUser{ID: userID, ExternalID: "entra-user", UserName: "alex@example.test"}
	group := domain.DirectoryGroup{ID: groupID, ExternalID: "entra-group", DisplayName: "Research"}

	request := httptest.NewRequest(nethttp.MethodGet, "/Users", nil)
	filter, err := directoryParseSCIMFilter(request, "userName", "externalId", "id")
	require.NoError(t, err)
	require.True(t, directorySCIMUserMatches(user, filter))
	require.True(t, directorySCIMGroupMatches(group, directorySCIMFilter{}))
	for _, testCase := range []struct {
		filter directorySCIMFilter
		match  bool
	}{
		{directorySCIMFilter{field: "externalId", value: user.ExternalID}, true},
		{directorySCIMFilter{field: "id", value: user.ID.String()}, true},
		{directorySCIMFilter{field: "unknown", value: "value"}, false},
	} {
		require.Equal(t, testCase.match, directorySCIMUserMatches(user, testCase.filter))
	}
	for _, testCase := range []struct {
		filter directorySCIMFilter
		match  bool
	}{
		{directorySCIMFilter{field: "externalId", value: group.ExternalID}, true},
		{directorySCIMFilter{field: "id", value: group.ID.String()}, true},
		{directorySCIMFilter{field: "unknown", value: "value"}, false},
	} {
		require.Equal(t, testCase.match, directorySCIMGroupMatches(group, testCase.filter))
	}
	for _, raw := range []string{"userName ne \"alex@example.test\"", "userName eq not-a-quoted-value"} {
		request = httptest.NewRequest(nethttp.MethodGet, "/Users?filter="+url.QueryEscape(raw), nil)
		_, err = directoryParseSCIMFilter(request, "userName")
		require.Error(t, err)
	}

	page := directorySCIMPage([]scim.Resource{{ID: "1"}}, scim.ListRequestParams{StartIndex: -2, Count: 1})
	require.Len(t, page.Resources, 1)
	page = directorySCIMPage([]scim.Resource{{ID: "1"}}, scim.ListRequestParams{StartIndex: 1, Count: 0})
	require.Empty(t, page.Resources)
	require.False(t, directorySCIMUserMatches(user, directorySCIMFilter{field: "id", value: groupID.String()}))
	require.False(t, directorySCIMGroupMatches(group, directorySCIMFilter{field: "id", value: userID.String()}))

	_, err = directoryUserFromSCIM(scim.ResourceAttributes{"displayName": "Missing user name"}, domain.DirectoryUser{})
	require.Error(t, err)
	parsedUser, err := directoryUserFromSCIM(scim.ResourceAttributes{"userName": "alex@example.test", "emails": []interface{}{map[string]interface{}{"value": "fallback@example.test"}}}, domain.DirectoryUser{})
	require.NoError(t, err)
	require.Equal(t, "fallback@example.test", parsedUser.Email)
	require.True(t, parsedUser.Active)
	_, err = directoryUserFromSCIM(scim.ResourceAttributes{"userName": "alex@example.test", "active": "yes"}, domain.DirectoryUser{})
	require.NoError(t, err)

	_, _, err = directoryGroupFromSCIM(scim.ResourceAttributes{"externalId": "entra-group"}, domain.DirectoryGroup{})
	require.Error(t, err)
	_, _, err = directoryGroupFromSCIM(scim.ResourceAttributes{"displayName": "Research", "members": []interface{}{map[string]interface{}{"value": "not-a-uuid"}}}, domain.DirectoryGroup{})
	require.Error(t, err)

	_, ok := directorySCIMNestedString("not-an-object", "formatted")
	require.False(t, ok)
	_, ok = directorySCIMEmail("not-an-array")
	require.False(t, ok)
	_, ok = directorySCIMEmail([]interface{}{map[string]interface{}{"value": "  "}, "not-an-object"})
	require.False(t, ok)
	members, err := directorySCIMMemberIDs(nil)
	require.NoError(t, err)
	require.Empty(t, members)
	_, err = directorySCIMMemberIDs([]interface{}{map[string]interface{}{"display": "missing value"}})
	require.Error(t, err)
	_, err = directorySCIMMemberIDs([]interface{}{"not-an-object"})
	require.Error(t, err)

	updatedUser, err := directoryPatchUser(user, []scim.PatchOperation{{Op: scim.PatchOperationRemove, Path: directorySCIMTestPath(t, "active")}, {Op: scim.PatchOperationRemove, Path: directorySCIMTestPath(t, "emails")}})
	require.NoError(t, err)
	require.False(t, updatedUser.Active)
	require.Empty(t, updatedUser.Email)
	for _, operation := range []scim.PatchOperation{
		{Op: scim.PatchOperationReplace, Path: directorySCIMTestPath(t, "active"), Value: "invalid"},
		{Op: scim.PatchOperationRemove, Path: directorySCIMTestPath(t, "userName")},
		{Op: scim.PatchOperationReplace, Path: directorySCIMTestPath(t, "displayName"), Value: 42},
		{Op: scim.PatchOperationReplace, Path: directorySCIMTestPath(t, "emails"), Value: "invalid"},
		{Op: scim.PatchOperationRemove, Value: map[string]interface{}{}},
	} {
		_, err := directoryPatchUser(user, []scim.PatchOperation{operation})
		require.Error(t, err)
	}

	directoryGroup := domain.DirectoryGroup{ID: groupID, ExternalID: group.ExternalID, DisplayName: group.DisplayName, Active: true, Members: []domain.DirectoryUser{{ID: userID}}}
	updatedGroup, members, err := directoryPatchGroup(directoryGroup, []scim.PatchOperation{{Op: scim.PatchOperationRemove, Path: directorySCIMTestPath(t, "active")}, {Op: scim.PatchOperationRemove, Path: directorySCIMTestPath(t, "members")}})
	require.NoError(t, err)
	require.False(t, updatedGroup.Active)
	require.Empty(t, members)
	for _, operation := range []scim.PatchOperation{
		{Op: scim.PatchOperationReplace, Path: directorySCIMTestPath(t, "displayName"), Value: ""},
		{Op: scim.PatchOperationReplace, Path: directorySCIMTestPath(t, "active"), Value: "invalid"},
		{Op: scim.PatchOperationReplace, Path: directorySCIMTestPath(t, "members"), Value: "invalid"},
		{Op: scim.PatchOperationRemove, Path: directorySCIMTestPath(t, "members[value eq \"not-a-uuid\"]")},
		{Op: scim.PatchOperationReplace, Path: directorySCIMTestPath(t, "unknown"), Value: "invalid"},
		{Op: scim.PatchOperationRemove, Value: map[string]interface{}{}},
	} {
		_, _, err := directoryPatchGroup(directoryGroup, []scim.PatchOperation{operation})
		require.Error(t, err)
	}

	require.Nil(t, directorySCIMGetError("missing", nil))
	require.Nil(t, directorySCIMMutationError(nil))
	require.Error(t, directorySCIMMutationError(errors.New("duplicate conflict")))
	require.Equal(t, scimerrors.ScimErrorUniqueness, directorySCIMMutationError(errors.New("duplicate key value violates unique constraint")))
	require.Error(t, directorySCIMMutationError(errors.New("unexpected storage failure")))
	_, err = directoryMemberIDFromPath("members[value eq not-a-string]")
	require.Error(t, err)

	connectorID := uuid.New()
	request = httptest.NewRequest(nethttp.MethodGet, "/", nil).WithContext(context.WithValue(context.Background(), directorySCIMContextKey{}, connectorID))
	resolvedConnectorID, err := directorySCIMConnectorID(request)
	require.NoError(t, err)
	require.Equal(t, connectorID, resolvedConnectorID)
}

func TestDirectorySCIMResourceHandlersRejectInvalidContextAndUnknownResources(t *testing.T) {
	t.Parallel()

	connectorID := uuid.New()
	repo := &directorySCIMRepositoryStub{
		connector: &domain.DirectoryConnector{ID: connectorID, ProviderID: uuid.New(), Status: domain.DirectoryConnectorObserve},
		users:     make(map[uuid.UUID]*domain.DirectoryUser),
		groups:    make(map[uuid.UUID]*domain.DirectoryGroup),
	}
	directory := service.NewDirectoryIdentityService(repo, service.DirectoryIdentityConfig{})
	userHandler := directorySCIMUserResourceHandler{directory: directory}
	groupHandler := directorySCIMGroupResourceHandler{directory: directory}
	noContext := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	withContext := httptest.NewRequest(nethttp.MethodGet, "/", nil).WithContext(context.WithValue(context.Background(), directorySCIMContextKey{}, connectorID))

	_, err := userHandler.Create(noContext, scim.ResourceAttributes{"userName": "alex@example.test"})
	require.Error(t, err)
	_, err = groupHandler.Create(noContext, scim.ResourceAttributes{"displayName": "Research"})
	require.Error(t, err)
	_, err = userHandler.Get(noContext, uuid.NewString())
	require.Error(t, err)
	_, err = groupHandler.Get(noContext, uuid.NewString())
	require.Error(t, err)
	_, err = userHandler.Get(withContext, "not-a-uuid")
	require.Error(t, err)
	_, err = groupHandler.Get(withContext, "not-a-uuid")
	require.Error(t, err)
	_, err = userHandler.Replace(withContext, uuid.NewString(), scim.ResourceAttributes{"userName": "alex@example.test"})
	require.Error(t, err)
	_, err = groupHandler.Replace(withContext, uuid.NewString(), scim.ResourceAttributes{"displayName": "Research"})
	require.Error(t, err)
	require.Error(t, userHandler.Delete(withContext, uuid.NewString()))
	require.Error(t, groupHandler.Delete(withContext, uuid.NewString()))
	_, err = userHandler.Patch(withContext, uuid.NewString(), nil)
	require.Error(t, err)
	_, err = groupHandler.Patch(withContext, uuid.NewString(), nil)
	require.Error(t, err)
	_, err = userHandler.GetAll(noContext, scim.ListRequestParams{})
	require.Error(t, err)
	_, err = groupHandler.GetAll(noContext, scim.ListRequestParams{})
	require.Error(t, err)
}

func directorySCIMTestPath(t *testing.T, raw string) *filterparser.Path {
	t.Helper()
	path, err := filterparser.ParsePath([]byte(raw))
	require.NoError(t, err)
	return &path
}

func mustDirectoryBearer(t *testing.T, authorization string) string {
	t.Helper()
	request := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	request.Header.Set(echo.HeaderAuthorization, authorization)
	token, ok := directoryBearerToken(request)
	require.True(t, ok)
	return token
}

func scimErrorStatus(status int) scimerrors.ScimError {
	return scimerrors.ScimError{Status: status}
}
