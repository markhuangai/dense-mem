package http

import (
	"context"
	"errors"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

const (
	privateMemoryErasureBodyKey   = "private_memory_erasure_body"
	privateMemoryLegalHoldBodyKey = "private_memory_legal_hold_body"
)

type PrivateMemoryServiceInterface interface {
	RequestSSOProfileErasure(context.Context, uuid.UUID, uuid.UUID, service.PrivateMemoryCommand) (*domain.PrivateMemoryErasureOperation, error)
	RequestCredentialErasure(context.Context, uuid.UUID, uuid.UUID, service.PrivateMemoryCommand) (*domain.PrivateMemoryErasureOperation, error)
	DeleteSSOCredential(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, service.PrivateMemoryCommand) (*domain.PrivateMemoryErasureOperation, error)
	GetOwnerOperation(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, *uuid.UUID) (*domain.PrivateMemoryErasureOperation, error)
	RequestControlErasure(context.Context, uuid.UUID, service.PrivateMemoryCommand) (*domain.PrivateMemoryErasureOperation, error)
	GetOperation(context.Context, uuid.UUID) (*domain.PrivateMemoryErasureOperation, error)
	ListOperations(context.Context, int, int) ([]domain.PrivateMemoryErasureOperation, error)
	ListSpaces(context.Context, int, int) ([]domain.PrivateMemorySpaceMetadata, error)
	PlaceLegalHold(context.Context, uuid.UUID, string) (*domain.PrivateMemoryLegalHold, bool, error)
	ReleaseLegalHold(context.Context, uuid.UUID) (*domain.PrivateMemoryLegalHold, bool, error)
	RunRetention(context.Context, service.PrivateMemoryCommand, domain.PrivateMemoryActorClass) (*domain.PrivateMemoryRetentionRun, error)
	ListRetentionRuns(context.Context, int, int) ([]domain.PrivateMemoryRetentionRun, error)
}

var _ PrivateMemoryServiceInterface = (*service.PrivateMemoryService)(nil)

type privateMemoryOperationResponse struct {
	OperationID        string                            `json:"operation_id"`
	TeamID             string                            `json:"team_id,omitempty"`
	SpaceID            string                            `json:"space_id,omitempty"`
	SpaceKind          domain.MemorySpaceKind            `json:"space_kind,omitempty"`
	TargetCredentialID string                            `json:"target_credential_id,omitempty"`
	Action             domain.PrivateMemoryErasureAction `json:"action"`
	ActorClass         domain.PrivateMemoryActorClass    `json:"actor_class"`
	ReasonCode         string                            `json:"reason_code"`
	TargetGeneration   *int64                            `json:"target_generation,omitempty"`
	RetireSpace        bool                              `json:"retire_space"`
	Status             domain.PrivateMemoryErasureStatus `json:"status"`
	DeletedCounts      map[string]int64                  `json:"deleted_counts"`
	AttemptCount       int                               `json:"attempt_count,omitempty"`
	LastErrorCode      string                            `json:"last_error_code,omitempty"`
	RequestedAt        string                            `json:"requested_at"`
	StartedAt          *string                           `json:"started_at,omitempty"`
	CompletedAt        *string                           `json:"completed_at,omitempty"`
	UpdatedAt          string                            `json:"updated_at"`
}

type privateMemoryLegalHoldResponse struct {
	ID         string  `json:"id"`
	TeamID     string  `json:"team_id"`
	SpaceID    string  `json:"space_id"`
	ReasonCode string  `json:"reason_code"`
	ActorClass string  `json:"actor_class"`
	PlacedAt   string  `json:"placed_at"`
	ReleasedAt *string `json:"released_at,omitempty"`
}

type privateMemorySpaceResponse struct {
	ID                string                           `json:"id"`
	TeamID            string                           `json:"team_id"`
	Kind              domain.MemorySpaceKind           `json:"kind"`
	OwnerProfileID    string                           `json:"owner_profile_id,omitempty"`
	OwnerCredentialID string                           `json:"owner_credential_id,omitempty"`
	Generation        int64                            `json:"generation"`
	LifecycleState    domain.MemorySpaceLifecycleState `json:"lifecycle_state"`
	PrivateContentAt  *string                          `json:"private_content_at,omitempty"`
	SealedAt          *string                          `json:"sealed_at,omitempty"`
	RetiredAt         *string                          `json:"retired_at,omitempty"`
	CreatedAt         string                           `json:"created_at"`
	UpdatedAt         string                           `json:"updated_at"`
	ActiveHold        *privateMemoryLegalHoldResponse  `json:"active_hold,omitempty"`
}

type privateMemoryRetentionRunResponse struct {
	ID            string                         `json:"id"`
	ActorClass    domain.PrivateMemoryActorClass `json:"actor_class"`
	Cutoff        string                         `json:"cutoff"`
	RetentionDays int                            `json:"retention_days"`
	QueuedCount   int                            `json:"queued_count"`
	Status        string                         `json:"status"`
	StartedAt     string                         `json:"started_at"`
	CompletedAt   *string                        `json:"completed_at,omitempty"`
}

type privateMemoryPageResponse struct {
	Data       any `json:"data"`
	Pagination struct {
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	} `json:"pagination"`
}

type controlPrivateMemoryConfigRequest struct {
	Items []controlSSOConfigItemRequest `json:"items"`
}

type controlPrivateMemoryConfigResponse struct {
	UpdateTime string                            `json:"update_time"`
	Items      []controlSSOConfigItemResponse    `json:"items"`
	Effective  domain.PrivateMemoryRuntimeConfig `json:"effective"`
}

func (h *userPortalHandler) eraseSSOPrivateMemory(c echo.Context) error {
	if h.privateMemory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "private-memory service unavailable")
	}
	info, _, err := h.ssoRequestSession(c)
	if err != nil {
		return err
	}
	operation, err := h.privateMemory.RequestSSOProfileErasure(
		c.Request().Context(),
		info.Selected.Team.ID,
		info.Identity.ID,
		privateMemoryCommand(c),
	)
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	return c.JSON(nethttp.StatusAccepted, map[string]any{"data": toPrivateMemoryOperation(operation, false)})
}

func (h *userPortalHandler) eraseCredentialPrivateMemory(c echo.Context) error {
	if h.privateMemory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "private-memory service unavailable")
	}
	principal := httpmw.GetPrincipal(c.Request().Context())
	if principal == nil || principal.AuthMethod == "sso_session" {
		return httperr.New(httperr.FORBIDDEN, "api credential required")
	}
	credentialID := principal.GetCredentialID()
	if credentialID == nil || *credentialID == uuid.Nil {
		return httperr.New(httperr.FORBIDDEN, "api credential required")
	}
	operation, err := h.privateMemory.RequestCredentialErasure(
		c.Request().Context(),
		principal.GetTeamID(),
		*credentialID,
		privateMemoryCommand(c),
	)
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	return c.JSON(nethttp.StatusAccepted, map[string]any{"data": toPrivateMemoryOperation(operation, false)})
}

func (h *userPortalHandler) deleteSSOCredential(c echo.Context) error {
	if h.privateMemory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "private-memory service unavailable")
	}
	info, _, err := h.ssoRequestSession(c)
	if err != nil {
		return err
	}
	if info.Selected.Membership.Role == service.CredentialRoleManager {
		return httperr.New(httperr.FORBIDDEN, "sso managers should use the team credentials section")
	}
	credentialID, err := userPortalCredentialParam(c)
	if err != nil {
		return err
	}
	operation, err := h.privateMemory.DeleteSSOCredential(
		c.Request().Context(),
		info.Selected.Team.ID,
		info.Identity.ID,
		credentialID,
		privateMemoryCommand(c),
	)
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	return c.JSON(nethttp.StatusAccepted, map[string]any{"data": toPrivateMemoryOperation(operation, false)})
}

func (h *userPortalHandler) getOwnerPrivateMemoryErasure(c echo.Context) error {
	if h.privateMemory == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "private-memory service unavailable")
	}
	operationID, err := privateMemoryUUIDParam(c, "operationId", "operation ID")
	if err != nil {
		return err
	}
	principal := httpmw.GetPrincipal(c.Request().Context())
	if principal == nil {
		return httperr.New(httperr.FORBIDDEN, "authentication required")
	}

	teamID := principal.GetTeamID()
	var identityID, credentialID *uuid.UUID
	if principal.AuthMethod == "sso_session" {
		info, _, err := h.ssoRequestSession(c)
		if err != nil {
			return err
		}
		teamID = info.Selected.Team.ID
		identityID = &info.Identity.ID
	} else if current := principal.GetCredentialID(); current != nil && *current != uuid.Nil {
		credentialID = current
	} else {
		return httperr.New(httperr.FORBIDDEN, "authenticated owner required")
	}

	operation, err := h.privateMemory.GetOwnerOperation(c.Request().Context(), teamID, operationID, identityID, credentialID)
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	if operation == nil {
		return httperr.New(httperr.NOT_FOUND, "private-memory erasure not found")
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toPrivateMemoryOperation(operation, false)})
}

func (h *controlPortalHandler) getPrivateMemoryConfig(c echo.Context) error {
	if h.appConfig == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "app config service unavailable")
	}
	settings, err := h.appConfig.GetPrivateMemorySettings(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlPrivateMemoryConfig(settings)})
}

func (h *controlPortalHandler) updatePrivateMemoryConfig(c echo.Context) error {
	if h.appConfig == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "app config service unavailable")
	}
	var body controlPrivateMemoryConfigRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	values := make(map[string]string, len(body.Items))
	for _, item := range body.Items {
		values[item.Key] = item.Value
	}
	settings, err := h.appConfig.UpdatePrivateMemorySettings(c.Request().Context(), values, "control", c.RealIP(), "")
	if err != nil {
		if errors.Is(err, service.ErrInvalidAppConfig) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlPrivateMemoryConfig(settings)})
}

func (h *controlPortalHandler) listPrivateMemorySpaces(c echo.Context) error {
	limit, offset := controlPagination(c)
	spaces, err := h.privateMemory.ListSpaces(c.Request().Context(), limit, offset)
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	items := make([]privateMemorySpaceResponse, 0, len(spaces))
	for _, item := range spaces {
		items = append(items, toPrivateMemorySpace(item))
	}
	return c.JSON(nethttp.StatusOK, newPrivateMemoryPage(items, limit, offset))
}

func (h *controlPortalHandler) placePrivateMemoryLegalHold(c echo.Context) error {
	spaceID, err := privateMemoryUUIDParam(c, "spaceId", "space ID")
	if err != nil {
		return err
	}
	body := httpmw.MustGetValidatedBody[dto.PrivateMemoryLegalHoldRequest](c.Request().Context(), privateMemoryLegalHoldBodyKey)
	hold, created, err := h.privateMemory.PlaceLegalHold(c.Request().Context(), spaceID, body.ReasonCode)
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	status := nethttp.StatusOK
	if created {
		status = nethttp.StatusCreated
	}
	return c.JSON(status, map[string]any{"data": toPrivateMemoryLegalHold(hold)})
}

func (h *controlPortalHandler) releasePrivateMemoryLegalHold(c echo.Context) error {
	spaceID, err := privateMemoryUUIDParam(c, "spaceId", "space ID")
	if err != nil {
		return err
	}
	hold, released, err := h.privateMemory.ReleaseLegalHold(c.Request().Context(), spaceID)
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	var response *privateMemoryLegalHoldResponse
	if hold != nil {
		item := toPrivateMemoryLegalHold(hold)
		response = &item
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]any{"released": released, "hold": response}})
}

func (h *controlPortalHandler) requestControlPrivateMemoryErasure(c echo.Context) error {
	spaceID, err := privateMemoryUUIDParam(c, "spaceId", "space ID")
	if err != nil {
		return err
	}
	operation, err := h.privateMemory.RequestControlErasure(c.Request().Context(), spaceID, privateMemoryCommand(c))
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	return c.JSON(nethttp.StatusAccepted, map[string]any{"data": toPrivateMemoryOperation(operation, true)})
}

func (h *controlPortalHandler) listPrivateMemoryErasures(c echo.Context) error {
	limit, offset := controlPagination(c)
	operations, err := h.privateMemory.ListOperations(c.Request().Context(), limit, offset)
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	items := make([]privateMemoryOperationResponse, 0, len(operations))
	for i := range operations {
		items = append(items, toPrivateMemoryOperation(&operations[i], true))
	}
	return c.JSON(nethttp.StatusOK, newPrivateMemoryPage(items, limit, offset))
}

func (h *controlPortalHandler) getPrivateMemoryErasure(c echo.Context) error {
	operationID, err := privateMemoryUUIDParam(c, "operationId", "operation ID")
	if err != nil {
		return err
	}
	operation, err := h.privateMemory.GetOperation(c.Request().Context(), operationID)
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	if operation == nil {
		return httperr.New(httperr.NOT_FOUND, "private-memory erasure not found")
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toPrivateMemoryOperation(operation, true)})
}

func (h *controlPortalHandler) runPrivateMemoryRetention(c echo.Context) error {
	run, err := h.privateMemory.RunRetention(c.Request().Context(), privateMemoryCommand(c), domain.PrivateMemoryActorControl)
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	return c.JSON(nethttp.StatusAccepted, map[string]any{"data": toPrivateMemoryRetentionRun(run)})
}

func (h *controlPortalHandler) listPrivateMemoryRetentionRuns(c echo.Context) error {
	limit, offset := controlPagination(c)
	runs, err := h.privateMemory.ListRetentionRuns(c.Request().Context(), limit, offset)
	if err != nil {
		return privateMemoryHTTPError(err)
	}
	items := make([]privateMemoryRetentionRunResponse, 0, len(runs))
	for i := range runs {
		items = append(items, toPrivateMemoryRetentionRun(&runs[i]))
	}
	return c.JSON(nethttp.StatusOK, newPrivateMemoryPage(items, limit, offset))
}

func privateMemoryCommand(c echo.Context) service.PrivateMemoryCommand {
	body := httpmw.MustGetValidatedBody[dto.PrivateMemoryErasureRequest](c.Request().Context(), privateMemoryErasureBodyKey)
	return service.PrivateMemoryCommand{
		IdempotencyKey:          strings.TrimSpace(c.Request().Header.Get("Idempotency-Key")),
		AcknowledgeIrreversible: body.AcknowledgeIrreversible != nil && *body.AcknowledgeIrreversible,
	}
}

func privateMemoryUUIDParam(c echo.Context, name, label string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param(name)))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, httperr.New(httperr.INVALID_UUID, "invalid "+label+" format")
	}
	return id, nil
}

func privateMemoryHTTPError(err error) error {
	var apiErr *httperr.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, service.ErrPrivateMemoryAcknowledgementRequired):
		return httperr.New(httperr.VALIDATION_ERROR, "acknowledge_irreversible must be true")
	case errors.Is(err, service.ErrPrivateMemoryIdempotencyKeyRequired):
		return httperr.New(httperr.VALIDATION_ERROR, "Idempotency-Key is required and must be at most 200 characters")
	case errors.Is(err, service.ErrPrivateMemoryInvalidReason):
		return httperr.New(httperr.VALIDATION_ERROR, "reason_code must be a lowercase code of at most 64 characters")
	case errors.Is(err, repository.ErrPrivateMemoryNotFound):
		return httperr.New(httperr.NOT_FOUND, "private-memory target not found")
	case errors.Is(err, repository.ErrPrivateMemoryLegalHold):
		return httperr.New(httperr.CONFLICT, "private memory is under legal hold")
	case errors.Is(err, repository.ErrPrivateMemoryIdempotency):
		return httperr.New(httperr.CONFLICT, "Idempotency-Key conflicts with a different request")
	case errors.Is(err, repository.ErrPrivateMemoryOperationConflict):
		return httperr.New(httperr.CONFLICT, "private-memory erasure is already in progress")
	case errors.Is(err, repository.ErrPrivateMemoryRetentionDisabled):
		return httperr.New(httperr.CONFLICT, "private-memory retention is disabled")
	case errors.Is(err, repository.ErrPrivateMemoryHoldConflict):
		return httperr.New(httperr.CONFLICT, "private-memory legal hold conflicts with the active hold")
	case errors.Is(err, repository.ErrPrivateMemoryManifest):
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "private-memory erasure is unavailable")
	default:
		return err
	}
}

func toPrivateMemoryOperation(operation *domain.PrivateMemoryErasureOperation, control bool) privateMemoryOperationResponse {
	if operation == nil {
		return privateMemoryOperationResponse{DeletedCounts: map[string]int64{}}
	}
	response := privateMemoryOperationResponse{
		OperationID:      operation.ID.String(),
		Action:           operation.Action,
		ActorClass:       operation.ActorClass,
		ReasonCode:       operation.ReasonCode,
		TargetGeneration: operation.TargetGeneration,
		RetireSpace:      operation.RetireSpace,
		Status:           operation.Status,
		DeletedCounts:    clonePrivateMemoryCounts(operation.DeletedCounts),
		RequestedAt:      operation.RequestedAt.UTC().Format(time.RFC3339Nano),
		StartedAt:        privateMemoryTime(operation.StartedAt),
		CompletedAt:      privateMemoryTime(operation.CompletedAt),
		UpdatedAt:        operation.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if operation.SpaceKind != nil {
		response.SpaceKind = *operation.SpaceKind
	}
	if control {
		response.TeamID = operation.TeamID.String()
		if operation.SpaceID != nil {
			response.SpaceID = operation.SpaceID.String()
		}
		if operation.TargetCredentialID != nil {
			response.TargetCredentialID = operation.TargetCredentialID.String()
		}
		response.AttemptCount = operation.AttemptCount
		response.LastErrorCode = operation.LastErrorCode
	}
	return response
}

func toPrivateMemoryLegalHold(hold *domain.PrivateMemoryLegalHold) privateMemoryLegalHoldResponse {
	if hold == nil {
		return privateMemoryLegalHoldResponse{}
	}
	return privateMemoryLegalHoldResponse{
		ID:         hold.ID.String(),
		TeamID:     hold.TeamID.String(),
		SpaceID:    hold.SpaceID.String(),
		ReasonCode: hold.ReasonCode,
		ActorClass: hold.ActorClass,
		PlacedAt:   hold.PlacedAt.UTC().Format(time.RFC3339Nano),
		ReleasedAt: privateMemoryTime(hold.ReleasedAt),
	}
}

func toPrivateMemorySpace(metadata domain.PrivateMemorySpaceMetadata) privateMemorySpaceResponse {
	space := metadata.Space
	response := privateMemorySpaceResponse{
		ID:               space.ID.String(),
		TeamID:           space.TeamID.String(),
		Kind:             space.Kind,
		Generation:       space.Generation,
		LifecycleState:   space.LifecycleState,
		PrivateContentAt: privateMemoryTime(space.PrivateContentAt),
		SealedAt:         privateMemoryTime(space.SealedAt),
		RetiredAt:        privateMemoryTime(space.RetiredAt),
		CreatedAt:        space.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:        space.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if space.OwnerProfileID != nil {
		response.OwnerProfileID = space.OwnerProfileID.String()
	}
	if space.OwnerCredentialID != nil {
		response.OwnerCredentialID = space.OwnerCredentialID.String()
	}
	if metadata.ActiveHold != nil {
		hold := toPrivateMemoryLegalHold(metadata.ActiveHold)
		response.ActiveHold = &hold
	}
	return response
}

func toPrivateMemoryRetentionRun(run *domain.PrivateMemoryRetentionRun) privateMemoryRetentionRunResponse {
	if run == nil {
		return privateMemoryRetentionRunResponse{}
	}
	return privateMemoryRetentionRunResponse{
		ID:            run.ID.String(),
		ActorClass:    run.ActorClass,
		Cutoff:        run.Cutoff.UTC().Format(time.RFC3339Nano),
		RetentionDays: run.RetentionDays,
		QueuedCount:   run.QueuedCount,
		Status:        run.Status,
		StartedAt:     run.StartedAt.UTC().Format(time.RFC3339Nano),
		CompletedAt:   privateMemoryTime(run.CompletedAt),
	}
}

func toControlPrivateMemoryConfig(settings *domain.PrivateMemoryConfigSettings) controlPrivateMemoryConfigResponse {
	if settings == nil {
		return controlPrivateMemoryConfigResponse{Items: []controlSSOConfigItemResponse{}}
	}
	items := make([]controlSSOConfigItemResponse, 0, len(settings.Items))
	for _, item := range settings.Items {
		items = append(items, controlSSOConfigItemResponse{
			Key: item.Key, Value: item.Value, EffectiveValue: item.EffectiveValue,
			UpdatedAt: item.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return controlPrivateMemoryConfigResponse{UpdateTime: settings.UpdateTime, Items: items, Effective: settings.Effective}
}

func newPrivateMemoryPage(data any, limit, offset int) privateMemoryPageResponse {
	response := privateMemoryPageResponse{Data: data}
	response.Pagination.Limit = limit
	response.Pagination.Offset = offset
	return response
}

func privateMemoryTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func clonePrivateMemoryCounts(counts map[string]int64) map[string]int64 {
	cloned := make(map[string]int64, len(counts))
	for table, count := range counts {
		cloned[table] = count
	}
	return cloned
}
