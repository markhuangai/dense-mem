package http

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	mw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/ontology"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func TestOntologyManagementHTTPClosedInputsAndPermissions(t *testing.T) {
	for _, tc := range []struct {
		method, path, body, role string
		grants                   []string
		status                   int
	}{
		{"GET", "/status", "", "member", []string{"read", "write"}, 403},
		{"GET", "/status", "", "manager", []string{"write"}, 403},
		{"POST", "/drafts", `{}`, "manager", []string{"read"}, 403},
		{"POST", "/drafts", `{}`, "manager", []string{"write"}, 422},
		{"POST", "/drafts", `{"operation_key":"save","expected_version":0,"expected_revision":0,"reason":"test","edits":[],"team_id":"foreign"}`, "manager", []string{"write"}, 422},
		{"POST", "/drafts/id/preview", `{"expected_version":1,"owner_profile_id":"other"}`, "manager", []string{"read"}, 422},
		{"POST", "/drafts/id/preview", `{}`, "manager", []string{"read"}, 422},
		{"GET", "/status?team_id=other", "", "manager", []string{"read"}, 422},
		{"GET", "/records?limit=1&limit=2", "", "manager", []string{"read"}, 422},
		{"GET", "/records?limit=201", "", "manager", []string{"read"}, 422},
		{"GET", "/records/id?version=-1", "", "manager", []string{"read"}, 422},
		{"GET", "/records?limit=bad", "", "manager", []string{"read"}, 422},
		{"POST", "/drafts/id/preview", `{"expected_version":1}`, "manager", []string{"read"}, 503},
	} {
		t.Run(tc.method+tc.path+tc.role+tc.body, func(t *testing.T) {
			router := echo.New()
			router.HTTPErrorHandler = httperr.ErrorHandler
			registerOntologyManagementRoutes(router.Group("/ui/api"), ontology.NewManagementService(nil, nil))
			req := httptest.NewRequest(tc.method, "/ui/api/ontology"+tc.path, strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			ctx := mw.SetPrincipalForTest(req.Context(), &mw.Principal{Role: tc.role, Grants: tc.grants})
			ctx = requestctx.WithActor(ctx, requestctx.Actor{TeamID: uuid.New(), OwnerID: uuid.New(), Role: tc.role, Grants: tc.grants})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req.WithContext(ctx))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
		})
	}
}

func TestOntologyManagementHTTPBoundedErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{contract.ErrUnauthorized, 403}, {contract.ErrNotFound, 404}, {contract.ErrConflict, 409}, {contract.ErrSourceStale, 409},
		{contract.ErrInvalid, 422}, {contract.ErrOverride, 422}, {contract.ErrAuditUnavailable, 503}, {errors.New("storage detail"), 503},
	} {
		router := echo.New()
		router.HTTPErrorHandler = httperr.ErrorHandler
		router.GET("/", func(c echo.Context) error {
			return managementResponse(c, nil, fmt.Errorf("private canary: %w", tc.err))
		})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		require.Equal(t, tc.status, rec.Code)
		require.NotContains(t, rec.Body.String(), "private canary")
		require.NotContains(t, rec.Body.String(), "storage detail")
	}
}
