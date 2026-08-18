package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

type privateMemoryHTTPServiceStub struct {
	PrivateMemoryServiceInterface
	operation           *domain.PrivateMemoryErasureOperation
	operations          []domain.PrivateMemoryErasureOperation
	spaces              []domain.PrivateMemorySpaceMetadata
	hold                *domain.PrivateMemoryLegalHold
	holdChanged         bool
	retentionRun        *domain.PrivateMemoryRetentionRun
	retentionRuns       []domain.PrivateMemoryRetentionRun
	credentialCommand   service.PrivateMemoryCommand
	credentialTeamID    uuid.UUID
	credentialID        uuid.UUID
	profileCommand      service.PrivateMemoryCommand
	profileTeamID       uuid.UUID
	profileIdentityID   uuid.UUID
	controlCommand      service.PrivateMemoryCommand
	controlSpaceID      uuid.UUID
	holdSpaceID         uuid.UUID
	holdReason          string
	retentionCommand    service.PrivateMemoryCommand
	retentionActorClass domain.PrivateMemoryActorClass
	requestErr          error
}

func (s *privateMemoryHTTPServiceStub) RequestSSOProfileErasure(_ context.Context, teamID, identityID uuid.UUID, command service.PrivateMemoryCommand) (*domain.PrivateMemoryErasureOperation, error) {
	s.profileCommand = command
	s.profileTeamID = teamID
	s.profileIdentityID = identityID
	return s.operation, s.requestErr
}

func (s *privateMemoryHTTPServiceStub) RequestCredentialErasure(_ context.Context, teamID, credentialID uuid.UUID, command service.PrivateMemoryCommand) (*domain.PrivateMemoryErasureOperation, error) {
	s.credentialCommand = command
	s.credentialTeamID = teamID
	s.credentialID = credentialID
	if !command.AcknowledgeIrreversible {
		return nil, service.ErrPrivateMemoryAcknowledgementRequired
	}
	if strings.TrimSpace(command.IdempotencyKey) == "" {
		return nil, service.ErrPrivateMemoryIdempotencyKeyRequired
	}
	if s.requestErr != nil {
		return nil, s.requestErr
	}
	return s.operation, nil
}

func (s *privateMemoryHTTPServiceStub) GetOwnerOperation(_ context.Context, _, _ uuid.UUID, _, _ *uuid.UUID) (*domain.PrivateMemoryErasureOperation, error) {
	return s.operation, s.requestErr
}

func (s *privateMemoryHTTPServiceStub) GetOperation(_ context.Context, _ uuid.UUID) (*domain.PrivateMemoryErasureOperation, error) {
	return s.operation, s.requestErr
}

func (s *privateMemoryHTTPServiceStub) RequestControlErasure(_ context.Context, spaceID uuid.UUID, command service.PrivateMemoryCommand) (*domain.PrivateMemoryErasureOperation, error) {
	s.controlSpaceID = spaceID
	s.controlCommand = command
	return s.operation, s.requestErr
}

func (s *privateMemoryHTTPServiceStub) ListOperations(context.Context, int, int) ([]domain.PrivateMemoryErasureOperation, error) {
	return s.operations, s.requestErr
}

func (s *privateMemoryHTTPServiceStub) ListSpaces(context.Context, int, int) ([]domain.PrivateMemorySpaceMetadata, error) {
	return s.spaces, s.requestErr
}

func (s *privateMemoryHTTPServiceStub) PlaceLegalHold(_ context.Context, spaceID uuid.UUID, reason string) (*domain.PrivateMemoryLegalHold, bool, error) {
	s.holdSpaceID = spaceID
	s.holdReason = reason
	return s.hold, s.holdChanged, s.requestErr
}

func (s *privateMemoryHTTPServiceStub) ReleaseLegalHold(_ context.Context, spaceID uuid.UUID) (*domain.PrivateMemoryLegalHold, bool, error) {
	s.holdSpaceID = spaceID
	return s.hold, s.holdChanged, s.requestErr
}

func (s *privateMemoryHTTPServiceStub) RunRetention(_ context.Context, command service.PrivateMemoryCommand, actorClass domain.PrivateMemoryActorClass) (*domain.PrivateMemoryRetentionRun, error) {
	s.retentionCommand = command
	s.retentionActorClass = actorClass
	return s.retentionRun, s.requestErr
}

func (s *privateMemoryHTTPServiceStub) ListRetentionRuns(context.Context, int, int) ([]domain.PrivateMemoryRetentionRun, error) {
	return s.retentionRuns, s.requestErr
}

func TestCredentialPrivateMemoryHTTPContract(t *testing.T) {
	teamID := uuid.New()
	credentialID := uuid.New()
	operation := privateMemoryHTTPTestOperation(teamID, credentialID)
	stub := &privateMemoryHTTPServiceStub{operation: operation}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := httpmw.SetPrincipalForTest(c.Request().Context(), &httpmw.Principal{
				TeamID: teamID, IdentityID: credentialID, OwnerID: credentialID,
				CredentialID: &credentialID, AuthMethod: "api_key", Grants: []string{"read"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	handler := &userPortalHandler{privateMemory: stub}
	e.DELETE("/ui/api/credential/private-memory", handler.eraseCredentialPrivateMemory,
		httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey))

	tests := []struct {
		name       string
		body       string
		key        string
		wantStatus int
	}{
		{name: "unknown field", body: `{"acknowledge_irreversible":true,"extra":true}`, key: "request-1", wantStatus: http.StatusUnprocessableEntity},
		{name: "reason override", body: `{"acknowledge_irreversible":true,"reason_code":"custom"}`, key: "request-1", wantStatus: http.StatusUnprocessableEntity},
		{name: "acknowledgement false", body: `{"acknowledge_irreversible":false}`, key: "request-2", wantStatus: http.StatusUnprocessableEntity},
		{name: "idempotency missing", body: `{"acknowledge_irreversible":true}`, wantStatus: http.StatusUnprocessableEntity},
		{name: "accepted", body: `{"acknowledge_irreversible":true}`, key: "request-3", wantStatus: http.StatusAccepted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, "/ui/api/credential/private-memory", strings.NewReader(test.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			if test.key != "" {
				req.Header.Set("Idempotency-Key", test.key)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, test.wantStatus, rec.Code, rec.Body.String())
		})
	}
	require.Equal(t, teamID, stub.credentialTeamID)
	require.Equal(t, credentialID, stub.credentialID)
	require.Equal(t, "request-3", stub.credentialCommand.IdempotencyKey)
	require.Equal(t, domain.PrivateMemoryEraseCredentialPrivate, operation.Action)
}

func TestOwnerErasureStatusOmitsControlIdentifiers(t *testing.T) {
	teamID := uuid.New()
	credentialID := uuid.New()
	operation := privateMemoryHTTPTestOperation(teamID, credentialID)
	handler := &userPortalHandler{privateMemory: &privateMemoryHTTPServiceStub{operation: operation}}
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ui/api/private-memory/erasures/"+operation.ID.String(), nil)
	ctx := httpmw.SetPrincipalForTest(req.Context(), &httpmw.Principal{
		TeamID: teamID, IdentityID: credentialID, OwnerID: credentialID,
		CredentialID: &credentialID, AuthMethod: "api_key", Grants: []string{"read"},
	})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/ui/api/private-memory/erasures/:operationId")
	c.SetParamNames("operationId")
	c.SetParamValues(operation.ID.String())

	require.NoError(t, handler.getOwnerPrivateMemoryErasure(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), operation.ID.String())
	require.NotContains(t, rec.Body.String(), teamID.String())
	require.NotContains(t, rec.Body.String(), operation.SpaceID.String())
	require.NotContains(t, rec.Body.String(), credentialID.String())
}

func TestControlErasureStatusIncludesGovernanceIdentifiers(t *testing.T) {
	teamID := uuid.New()
	credentialID := uuid.New()
	operation := privateMemoryHTTPTestOperation(teamID, credentialID)
	handler := &controlPortalHandler{privateMemory: &privateMemoryHTTPServiceStub{operation: operation}}
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/control/api/private-memory/erasures/"+operation.ID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/control/api/private-memory/erasures/:operationId")
	c.SetParamNames("operationId")
	c.SetParamValues(operation.ID.String())

	require.NoError(t, handler.getPrivateMemoryErasure(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), teamID.String())
	require.Contains(t, rec.Body.String(), operation.SpaceID.String())
	require.Contains(t, rec.Body.String(), credentialID.String())
}

func TestPrivateMemoryHTTPErrorsAreBounded(t *testing.T) {
	apiErr, ok := privateMemoryHTTPError(service.ErrPrivateMemoryAcknowledgementRequired).(*httperr.APIError)
	require.True(t, ok)
	require.Equal(t, httperr.VALIDATION_ERROR, apiErr.Code)
}

func TestSSOOwnerPrivateMemoryErasureHTTPContract(t *testing.T) {
	fixture := newSSOPersonalKeyFixture(service.CredentialRoleMember, []string{service.CredentialScopeRead})
	operation := privateMemoryHTTPTestOperation(fixture.teamID, fixture.identityID)
	stub := &privateMemoryHTTPServiceStub{operation: operation}
	fixture.handler.privateMemory = stub
	c, rec := userSSOContext(
		http.MethodDelete,
		"/ui/api/sso/private-memory",
		`{"acknowledge_irreversible":true}`,
		fixture.sessionToken,
	)
	setUserSSOPrincipal(c, fixture.profileID, fixture.teamID)
	c.Request().Header.Set("Idempotency-Key", "profile-request-1")

	handler := httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey)(fixture.handler.eraseSSOPrivateMemory)
	require.NoError(t, handler(c))
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Equal(t, fixture.teamID, stub.profileTeamID)
	require.Equal(t, fixture.identityID, stub.profileIdentityID)
	require.Equal(t, "profile-request-1", stub.profileCommand.IdempotencyKey)
	require.NotContains(t, rec.Body.String(), fixture.teamID.String())
}

func TestPrivateMemoryControlHTTPRoutes(t *testing.T) {
	now := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
	teamID := uuid.New()
	credentialID := uuid.New()
	ownerProfileID := uuid.New()
	spaceID := uuid.New()
	operation := privateMemoryHTTPTestOperation(teamID, credentialID)
	operation.SpaceID = &spaceID
	releasedAt := now.Add(time.Hour)
	hold := &domain.PrivateMemoryLegalHold{
		ID: uuid.New(), TeamID: teamID, SpaceID: spaceID, ReasonCode: "legal_hold",
		ActorClass: "control", PlacedAt: now, ReleasedAt: &releasedAt,
	}
	completedAt := now.Add(2 * time.Hour)
	run := domain.PrivateMemoryRetentionRun{
		ID: uuid.New(), ActorClass: domain.PrivateMemoryActorControl,
		Cutoff: now.Add(-30 * 24 * time.Hour), RetentionDays: 30, QueuedCount: 2,
		Status: "completed", StartedAt: now, CompletedAt: &completedAt,
	}
	stub := &privateMemoryHTTPServiceStub{
		operation: operation, operations: []domain.PrivateMemoryErasureOperation{*operation},
		spaces: []domain.PrivateMemorySpaceMetadata{{
			Space: domain.MemorySpace{
				ID: spaceID, TeamID: teamID, Kind: domain.MemorySpaceProfilePrivate,
				OwnerProfileID: &ownerProfileID, Generation: 2,
				LifecycleState: domain.MemorySpaceActive, PrivateContentAt: &now,
				CreatedAt: now, UpdatedAt: now,
			},
			ActiveHold: hold,
		}},
		hold: hold, holdChanged: true, retentionRun: &run,
		retentionRuns: []domain.PrivateMemoryRetentionRun{run},
	}
	handler := &controlPortalHandler{privateMemory: stub}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.GET("/control/api/private-memory/spaces", handler.listPrivateMemorySpaces)
	e.POST(
		"/control/api/private-memory/spaces/:spaceId/legal-hold",
		handler.placePrivateMemoryLegalHold,
		httpmw.BindAndValidateStrict[dto.PrivateMemoryLegalHoldRequest](privateMemoryLegalHoldBodyKey),
	)
	e.DELETE("/control/api/private-memory/spaces/:spaceId/legal-hold", handler.releasePrivateMemoryLegalHold)
	e.POST(
		"/control/api/private-memory/spaces/:spaceId/erasures",
		handler.requestControlPrivateMemoryErasure,
		httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey),
	)
	e.GET("/control/api/private-memory/erasures", handler.listPrivateMemoryErasures)
	e.GET("/control/api/private-memory/erasures/:operationId", handler.getPrivateMemoryErasure)
	e.POST(
		"/control/api/private-memory/retention-runs",
		handler.runPrivateMemoryRetention,
		httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey),
	)
	e.GET("/control/api/private-memory/retention-runs", handler.listPrivateMemoryRetentionRuns)

	call := func(method, target, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	rec := call(http.MethodGet, "/control/api/private-memory/spaces?limit=20&offset=5", "", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), ownerProfileID.String())
	require.Contains(t, rec.Body.String(), hold.ID.String())

	rec = call(http.MethodPost, "/control/api/private-memory/spaces/"+spaceID.String()+"/legal-hold", `{"reason_code":"legal_hold"}`, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, spaceID, stub.holdSpaceID)
	require.Equal(t, "legal_hold", stub.holdReason)

	rec = call(http.MethodDelete, "/control/api/private-memory/spaces/"+spaceID.String()+"/legal-hold", "", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"released":true`)

	rec = call(http.MethodPost, "/control/api/private-memory/spaces/"+spaceID.String()+"/erasures", `{"acknowledge_irreversible":true}`, "erase-1")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Equal(t, spaceID, stub.controlSpaceID)
	require.Equal(t, "erase-1", stub.controlCommand.IdempotencyKey)
	require.Contains(t, rec.Body.String(), teamID.String())

	rec = call(http.MethodGet, "/control/api/private-memory/erasures?limit=10&offset=1", "", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), operation.ID.String())
	rec = call(http.MethodGet, "/control/api/private-memory/erasures/"+operation.ID.String(), "", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = call(http.MethodPost, "/control/api/private-memory/retention-runs", `{"acknowledge_irreversible":true}`, "retention-1")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Equal(t, domain.PrivateMemoryActorControl, stub.retentionActorClass)
	require.Equal(t, "retention-1", stub.retentionCommand.IdempotencyKey)
	rec = call(http.MethodGet, "/control/api/private-memory/retention-runs?limit=10&offset=1", "", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), run.ID.String())
}

func TestPrivateMemoryResponseHelpersAndBoundedErrors(t *testing.T) {
	now := time.Date(2026, 8, 18, 1, 2, 3, 4, time.FixedZone("offset", 3600))
	teamID := uuid.New()
	credentialID := uuid.New()
	operation := privateMemoryHTTPTestOperation(teamID, credentialID)
	operation.StartedAt = &now
	operation.CompletedAt = &now
	operation.DeletedCounts = map[string]int64{"knowledge_ingests": 2}
	operation.AttemptCount = 3
	operation.LastErrorCode = "database_operation"

	require.Empty(t, toPrivateMemoryOperation(nil, false).OperationID)
	response := toPrivateMemoryOperation(operation, true)
	require.Equal(t, teamID.String(), response.TeamID)
	require.Equal(t, 3, response.AttemptCount)
	require.NotNil(t, response.StartedAt)
	response.DeletedCounts["knowledge_ingests"] = 9
	require.Equal(t, int64(2), operation.DeletedCounts["knowledge_ingests"])
	require.Nil(t, privateMemoryTime(nil))
	require.NotNil(t, privateMemoryTime(&now))
	require.Empty(t, toPrivateMemoryLegalHold(nil).ID)
	require.Empty(t, toPrivateMemoryRetentionRun(nil).ID)
	require.Empty(t, toControlPrivateMemoryConfig(nil).Items)
	require.Equal(t, 5, newPrivateMemoryPage([]string{"item"}, 5, 2).Pagination.Limit)

	apiErr := httperr.New(httperr.FORBIDDEN, "forbidden")
	require.Same(t, apiErr, privateMemoryHTTPError(apiErr))
	unknown := errors.New("backend failed")
	require.ErrorIs(t, privateMemoryHTTPError(unknown), unknown)
	for err, code := range map[error]httperr.ErrorCode{
		service.ErrPrivateMemoryAcknowledgementRequired: httperr.VALIDATION_ERROR,
		service.ErrPrivateMemoryIdempotencyKeyRequired:  httperr.VALIDATION_ERROR,
		service.ErrPrivateMemoryInvalidReason:           httperr.VALIDATION_ERROR,
		repository.ErrPrivateMemoryNotFound:             httperr.NOT_FOUND,
		repository.ErrPrivateMemoryLegalHold:            httperr.CONFLICT,
		repository.ErrPrivateMemoryIdempotency:          httperr.CONFLICT,
		repository.ErrPrivateMemoryOperationConflict:    httperr.CONFLICT,
		repository.ErrPrivateMemoryRetentionDisabled:    httperr.CONFLICT,
		repository.ErrPrivateMemoryHoldConflict:         httperr.CONFLICT,
		repository.ErrPrivateMemoryManifest:             httperr.SERVICE_UNAVAILABLE,
	} {
		mapped, ok := privateMemoryHTTPError(err).(*httperr.APIError)
		require.True(t, ok)
		require.Equal(t, code, mapped.Code)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c := e.NewContext(req, httptest.NewRecorder())
	c.SetParamNames("id")
	c.SetParamValues(uuid.Nil.String())
	_, err := privateMemoryUUIDParam(c, "id", "space ID")
	require.Error(t, err)
	c.SetParamValues(uuid.New().String())
	_, err = privateMemoryUUIDParam(c, "id", "space ID")
	require.NoError(t, err)
}

func TestPrivateMemoryOwnerHandlersRejectUnavailableOrWrongPrincipals(t *testing.T) {
	e := echo.New()
	operationID := uuid.New()
	newContext := func(method, target string, principal *httpmw.Principal) echo.Context {
		t.Helper()
		req := httptest.NewRequest(method, target, nil)
		if principal != nil {
			req = req.WithContext(httpmw.SetPrincipalForTest(req.Context(), principal))
		}
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath("/ui/api/private-memory/erasures/:operationId")
		c.SetParamNames("operationId")
		c.SetParamValues(operationID.String())
		return c
	}
	assertCode := func(err error, code httperr.ErrorCode) {
		t.Helper()
		var apiErr *httperr.APIError
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, code, apiErr.Code)
	}

	unavailable := &userPortalHandler{}
	assertCode(unavailable.eraseSSOPrivateMemory(newContext(http.MethodDelete, "/ui/api/sso/private-memory", nil)), httperr.SERVICE_UNAVAILABLE)
	assertCode(unavailable.eraseCredentialPrivateMemory(newContext(http.MethodDelete, "/ui/api/credential/private-memory", nil)), httperr.SERVICE_UNAVAILABLE)
	assertCode(unavailable.deleteSSOCredential(newContext(http.MethodDelete, "/ui/api/sso/credentials/"+uuid.NewString(), nil)), httperr.SERVICE_UNAVAILABLE)
	assertCode(unavailable.getOwnerPrivateMemoryErasure(newContext(http.MethodGet, "/ui/api/private-memory/erasures/"+operationID.String(), nil)), httperr.SERVICE_UNAVAILABLE)

	stub := &privateMemoryHTTPServiceStub{}
	handler := &userPortalHandler{privateMemory: stub}
	assertCode(handler.eraseSSOPrivateMemory(newContext(http.MethodDelete, "/ui/api/sso/private-memory", nil)), httperr.NOT_FOUND)
	assertCode(handler.deleteSSOCredential(newContext(http.MethodDelete, "/ui/api/sso/credentials/"+uuid.NewString(), nil)), httperr.NOT_FOUND)
	assertCode(handler.eraseCredentialPrivateMemory(newContext(http.MethodDelete, "/ui/api/credential/private-memory", nil)), httperr.FORBIDDEN)

	ssoPrincipal := &httpmw.Principal{AuthMethod: "sso_session"}
	assertCode(handler.eraseCredentialPrivateMemory(newContext(http.MethodDelete, "/ui/api/credential/private-memory", ssoPrincipal)), httperr.FORBIDDEN)
	apiPrincipal := &httpmw.Principal{AuthMethod: "api_key", TeamID: uuid.New()}
	assertCode(handler.eraseCredentialPrivateMemory(newContext(http.MethodDelete, "/ui/api/credential/private-memory", apiPrincipal)), httperr.FORBIDDEN)

	assertCode(handler.getOwnerPrivateMemoryErasure(newContext(http.MethodGet, "/ui/api/private-memory/erasures/"+operationID.String(), nil)), httperr.FORBIDDEN)
	assertCode(handler.getOwnerPrivateMemoryErasure(newContext(http.MethodGet, "/ui/api/private-memory/erasures/"+operationID.String(), ssoPrincipal)), httperr.NOT_FOUND)
	assertCode(handler.getOwnerPrivateMemoryErasure(newContext(http.MethodGet, "/ui/api/private-memory/erasures/"+operationID.String(), apiPrincipal)), httperr.FORBIDDEN)

	credentialID := uuid.New()
	apiPrincipal.CredentialID = &credentialID
	assertCode(handler.getOwnerPrivateMemoryErasure(newContext(http.MethodGet, "/ui/api/private-memory/erasures/"+operationID.String(), apiPrincipal)), httperr.NOT_FOUND)
	stub.requestErr = repository.ErrPrivateMemoryLegalHold
	assertCode(handler.getOwnerPrivateMemoryErasure(newContext(http.MethodGet, "/ui/api/private-memory/erasures/"+operationID.String(), apiPrincipal)), httperr.CONFLICT)
}

func TestPrivateMemoryControlHandlersPropagateRepositoryFailures(t *testing.T) {
	backendErr := errors.New("repository unavailable")
	stub := &privateMemoryHTTPServiceStub{requestErr: backendErr}
	handler := &controlPortalHandler{privateMemory: stub}
	e := echo.New()
	newContext := func(method, target, path, name, value string) echo.Context {
		t.Helper()
		c := e.NewContext(httptest.NewRequest(method, target, nil), httptest.NewRecorder())
		c.SetPath(path)
		if name != "" {
			c.SetParamNames(name)
			c.SetParamValues(value)
		}
		return c
	}

	require.ErrorIs(t, handler.listPrivateMemorySpaces(newContext(http.MethodGet, "/control/api/private-memory/spaces", "", "", "")), backendErr)
	require.ErrorIs(t, handler.listPrivateMemoryErasures(newContext(http.MethodGet, "/control/api/private-memory/erasures", "", "", "")), backendErr)
	require.ErrorIs(t, handler.listPrivateMemoryRetentionRuns(newContext(http.MethodGet, "/control/api/private-memory/retention-runs", "", "", "")), backendErr)
	spaceID := uuid.New()
	require.ErrorIs(t, handler.releasePrivateMemoryLegalHold(newContext(
		http.MethodDelete,
		"/control/api/private-memory/spaces/"+spaceID.String()+"/legal-hold",
		"/control/api/private-memory/spaces/:spaceId/legal-hold",
		"spaceId",
		spaceID.String(),
	)), backendErr)
	operationID := uuid.New()
	require.ErrorIs(t, handler.getPrivateMemoryErasure(newContext(
		http.MethodGet,
		"/control/api/private-memory/erasures/"+operationID.String(),
		"/control/api/private-memory/erasures/:operationId",
		"operationId",
		operationID.String(),
	)), backendErr)

	stub.requestErr = nil
	stub.operation = nil
	var apiErr *httperr.APIError
	err := handler.getPrivateMemoryErasure(newContext(
		http.MethodGet,
		"/control/api/private-memory/erasures/"+operationID.String(),
		"/control/api/private-memory/erasures/:operationId",
		"operationId",
		operationID.String(),
	))
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.NOT_FOUND, apiErr.Code)
	err = handler.getPrivateMemoryErasure(newContext(
		http.MethodGet,
		"/control/api/private-memory/erasures/not-a-uuid",
		"/control/api/private-memory/erasures/:operationId",
		"operationId",
		"not-a-uuid",
	))
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.INVALID_UUID, apiErr.Code)

	stub.hold = nil
	stub.holdChanged = false
	releaseContext := newContext(
		http.MethodDelete,
		"/control/api/private-memory/spaces/"+spaceID.String()+"/legal-hold",
		"/control/api/private-memory/spaces/:spaceId/legal-hold",
		"spaceId",
		spaceID.String(),
	)
	require.NoError(t, handler.releasePrivateMemoryLegalHold(releaseContext))
	require.Contains(t, releaseContext.Response().Writer.(*httptest.ResponseRecorder).Body.String(), `"released":false`)

	stub.requestErr = backendErr
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.POST(
		"/control/api/private-memory/spaces/:spaceId/legal-hold",
		handler.placePrivateMemoryLegalHold,
		httpmw.BindAndValidateStrict[dto.PrivateMemoryLegalHoldRequest](privateMemoryLegalHoldBodyKey),
	)
	e.POST(
		"/control/api/private-memory/spaces/:spaceId/erasures",
		handler.requestControlPrivateMemoryErasure,
		httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey),
	)
	e.POST(
		"/control/api/private-memory/retention-runs",
		handler.runPrivateMemoryRetention,
		httpmw.BindAndValidateStrict[dto.PrivateMemoryErasureRequest](privateMemoryErasureBodyKey),
	)
	for _, request := range []struct {
		target string
		body   string
		key    string
	}{
		{target: "/control/api/private-memory/spaces/" + spaceID.String() + "/legal-hold", body: `{"reason_code":"legal_hold"}`},
		{target: "/control/api/private-memory/spaces/" + spaceID.String() + "/erasures", body: `{"acknowledge_irreversible":true}`, key: "erase-error"},
		{target: "/control/api/private-memory/retention-runs", body: `{"acknowledge_irreversible":true}`, key: "retention-error"},
	} {
		req := httptest.NewRequest(http.MethodPost, request.target, strings.NewReader(request.body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set("Idempotency-Key", request.key)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	}

	credentialID := uuid.New()
	space := toPrivateMemorySpace(domain.PrivateMemorySpaceMetadata{Space: domain.MemorySpace{
		ID: credentialID, TeamID: uuid.New(), Kind: domain.MemorySpaceCredentialPrivate,
		OwnerCredentialID: &credentialID,
	}})
	require.Equal(t, credentialID.String(), space.OwnerCredentialID)
}

func privateMemoryHTTPTestOperation(teamID, credentialID uuid.UUID) *domain.PrivateMemoryErasureOperation {
	now := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
	spaceID := uuid.New()
	spaceKind := domain.MemorySpaceCredentialPrivate
	generation := int64(2)
	return &domain.PrivateMemoryErasureOperation{
		ID: uuid.New(), TeamID: teamID, SpaceID: &spaceID, SpaceKind: &spaceKind,
		TargetCredentialID: &credentialID, Action: domain.PrivateMemoryEraseCredentialPrivate,
		ActorClass: domain.PrivateMemoryActorOwnerCredential, ReasonCode: "owner_request",
		TargetGeneration: &generation, Status: domain.PrivateMemoryErasureQueued,
		DeletedCounts: map[string]int64{}, RequestedAt: now, UpdatedAt: now,
	}
}
