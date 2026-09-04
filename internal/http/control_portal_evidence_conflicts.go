package http

import (
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/evidenceconflict"
)

const evidenceConflictResolutionBodyKey = "evidence_conflict_resolution_body"

type controlEvidenceConflictResolutionRequest struct {
	ExpectedVersion     int     `json:"expected_version" validate:"required,min=1"`
	Decision            string  `json:"decision" validate:"required,oneof=resolve dismiss"`
	Reason              string  `json:"reason" validate:"required,min=1,max=512"`
	PreferredPositionID *string `json:"preferred_position_id"`
}

func (h *controlPortalHandler) listEvidenceConflicts(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if h.evidenceConflicts == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "evidence conflicts unavailable")
	}
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	limit := 0
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, "limit must be between 1 and 100")
		}
	}
	page, err := h.evidenceConflicts.List(c.Request().Context(), teamID.String(), evidenceconflict.ListOptions{
		Status: c.QueryParam("status"), Limit: limit, Cursor: c.QueryParam("cursor"),
	})
	if err != nil {
		return evidenceConflictHTTPError(err)
	}
	return response.SuccessOK(c, page)
}

func (h *controlPortalHandler) getEvidenceConflict(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if h.evidenceConflicts == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "evidence conflicts unavailable")
	}
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	conflictID, err := parseControlUUID(c.Param("conflictId"), "conflict ID")
	if err != nil {
		return err
	}
	eventLimit := 0
	if raw := strings.TrimSpace(c.QueryParam("event_limit")); raw != "" {
		eventLimit, err = strconv.Atoi(raw)
		if err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, "event_limit must be between 1 and 100")
		}
	}
	detail, err := h.evidenceConflicts.Get(c.Request().Context(), teamID.String(), conflictID.String(), eventLimit, c.QueryParam("event_cursor"))
	if err != nil {
		return evidenceConflictHTTPError(err)
	}
	return response.SuccessOK(c, detail)
}

func (h *controlPortalHandler) resolveEvidenceConflict(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if h.evidenceConflicts == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "evidence conflicts unavailable")
	}
	body := httpmw.MustGetValidatedBody[controlEvidenceConflictResolutionRequest](c.Request().Context(), evidenceConflictResolutionBodyKey)
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	conflictID, err := parseControlUUID(c.Param("conflictId"), "conflict ID")
	if err != nil {
		return err
	}
	preferred := ""
	if body.PreferredPositionID != nil {
		preferred = strings.TrimSpace(*body.PreferredPositionID)
		if preferred != "" {
			if _, err := uuid.Parse(preferred); err != nil {
				return httperr.New(httperr.VALIDATION_ERROR, "preferred_position_id must be a valid UUID")
			}
		}
	}
	actorID := controlPortalActorIdentityFromContext(c.Request().Context())
	if actorID == "" {
		actorID = controlPortalActorFromContext(c.Request().Context())
	}
	record, err := h.evidenceConflicts.Resolve(c.Request().Context(), evidenceconflict.ResolutionInput{
		TeamID: teamID.String(), ConflictID: conflictID.String(), ExpectedVersion: body.ExpectedVersion,
		Decision: body.Decision, Reason: body.Reason, PreferredPositionID: preferred,
		ActorKind: "control", ActorID: actorID,
	})
	if err != nil {
		return evidenceConflictHTTPError(err)
	}
	return response.SuccessOK(c, map[string]any{"conflict": record})
}

func evidenceConflictHTTPError(err error) error {
	switch {
	case errors.Is(err, evidenceconflict.ErrInvalidStatus), errors.Is(err, evidenceconflict.ErrInvalidLimit), errors.Is(err, evidenceconflict.ErrInvalidCursor), errors.Is(err, evidenceconflict.ErrInvalid):
		return httperr.New(httperr.VALIDATION_ERROR, "evidence conflict request is invalid")
	case errors.Is(err, evidenceconflict.ErrNotFound):
		return httperr.New(httperr.NOT_FOUND, "evidence conflict not found")
	case errors.Is(err, evidenceconflict.ErrVersionStale), errors.Is(err, evidenceconflict.ErrNotOpen):
		return httperr.New(httperr.CONFLICT, "evidence conflict changed; reload before retrying")
	case errors.Is(err, evidenceconflict.ErrUnavailable):
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "evidence conflicts unavailable")
	default:
		return httperr.New(httperr.INTERNAL_ERROR, "evidence conflicts unavailable")
	}
}
