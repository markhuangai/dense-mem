package http

import (
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	cryptoutil "github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

func TestDirectorySCIMPaginationContract(t *testing.T) {
	t.Parallel()
	connectorID := uuid.New()
	token := "directory-pagination-test-token"
	hash, err := cryptoutil.HashKey(token)
	require.NoError(t, err)
	repo := &directorySCIMRepositoryStub{
		connector: &domain.DirectoryConnector{ID: connectorID, Status: domain.DirectoryConnectorObserve, BearerTokenHash: hash},
		users:     make(map[uuid.UUID]*domain.DirectoryUser), groups: make(map[uuid.UUID]*domain.DirectoryGroup),
	}
	for i := range 101 {
		id := uuid.New()
		repo.users[id] = &domain.DirectoryUser{ID: id, ConnectorID: connectorID, UserName: fmt.Sprintf("user-%03d@example.test", i)}
		repo.groups[id] = &domain.DirectoryGroup{ID: id, ConnectorID: connectorID, DisplayName: fmt.Sprintf("Group-%03d", i)}
	}
	e := echo.New()
	directory := accessservice.NewDirectoryIdentityService(repo, accessservice.DirectoryIdentityConfig{})
	require.NoError(t, RegisterDirectorySCIM(e, directory, DirectorySCIMConfig{}))
	get := func(path, credential string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(nethttp.MethodGet, "/scim/v2/"+connectorID.String()+path, nil)
		if credential != "" {
			request.Header.Set(echo.HeaderAuthorization, "Bearer "+credential)
		}
		response := httptest.NewRecorder()
		e.ServeHTTP(response, request)
		return response
	}
	config := get("/ServiceProviderConfig", token)
	require.Equal(t, nethttp.StatusOK, config.Code)
	var advertised struct {
		Filter struct {
			MaxResults int `json:"maxResults"`
		} `json:"filter"`
	}
	require.NoError(t, json.Unmarshal(config.Body.Bytes(), &advertised))
	require.Equal(t, 100, advertised.Filter.MaxResults)
	for _, kind := range []string{"Users", "Groups"} {
		t.Run(kind, func(t *testing.T) {
			for _, tc := range []struct {
				query  string
				length int
			}{
				{"", 100}, {"?count=0", 0}, {"?count=-1", 0},
				{"?count=1", 1}, {"?count=100", 100}, {"?count=101", 100},
				{"?startIndex=2&count=1", 1}, {"?startIndex=102&count=1", 0},
			} {
				response := get("/"+kind+tc.query, token)
				require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
				var page struct {
					Total     int `json:"totalResults"`
					Resources []struct {
						ID string `json:"id"`
					} `json:"Resources"`
				}
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
				require.Equal(t, 101, page.Total)
				require.Len(t, page.Resources, tc.length)
			}
			for _, query := range []string{"?count=bad", "?startIndex=bad", "?filter=" + url.QueryEscape(`id eq "not-a-uuid"`), "?filter=" + url.QueryEscape(`email eq "anything"`)} {
				response := get("/"+kind+query, token)
				require.Equal(t, nethttp.StatusBadRequest, response.Code)
				if strings.HasPrefix(query, "?filter=") {
					require.Contains(t, response.Body.String(), `"scimType":"invalidFilter"`)
				}
			}
			for _, credential := range []string{"", "wrong-token"} {
				response := get("/"+kind+"?count=0", credential)
				require.Equal(t, nethttp.StatusUnauthorized, response.Code)
				require.NotContains(t, response.Body.String(), "totalResults")
			}
		})
	}
}
