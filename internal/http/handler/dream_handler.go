package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

const (
	defaultDreamListLimit = 20
	maxDreamListLimit     = 100
)

type DreamHandler struct {
	svc dreamservice.Service
}

type dreamListResponse struct {
	Items      []*domain.Dream `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

func NewDreamHandler(svc dreamservice.Service) *DreamHandler {
	return &DreamHandler{svc: svc}
}

func (h *DreamHandler) Status(c echo.Context) error {
	profileID, ok := middleware.GetResolvedProfileID(c.Request().Context())
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}
	status, err := h.svc.Status(c.Request().Context(), profileID.String())
	if err != nil {
		return err
	}
	return response.SuccessOK(c, status)
}

func (h *DreamHandler) Runs(c echo.Context) error {
	profileID, ok := middleware.GetResolvedProfileID(c.Request().Context())
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}
	limit, err := dreamLimit(c.QueryParam("limit"))
	if err != nil {
		return err
	}
	runs, err := h.svc.ListRuns(c.Request().Context(), profileID.String(), limit)
	if err != nil {
		return err
	}
	if runs == nil {
		runs = []*dreamservice.RunCycleResult{}
	}
	return response.SuccessOK(c, runs)
}

func (h *DreamHandler) List(c echo.Context) error {
	profileID, ok := middleware.GetResolvedProfileID(c.Request().Context())
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}
	opts, err := dreamListOptions(c)
	if err != nil {
		return err
	}
	dreams, nextCursor, err := h.svc.List(c.Request().Context(), profileID.String(), opts)
	if err != nil {
		if errors.Is(err, dreamservice.ErrInvalidDreamCursor) {
			return httperr.New(httperr.VALIDATION_ERROR, "invalid cursor")
		}
		return err
	}
	return response.SuccessOK(c, dreamListResponse{Items: dreams, NextCursor: nextCursor})
}

func (h *DreamHandler) Get(c echo.Context) error {
	profileID, ok := middleware.GetResolvedProfileID(c.Request().Context())
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}
	dreamID := strings.TrimSpace(c.Param("dreamId"))
	if dreamID == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "dream ID is required")
	}
	dream, err := h.svc.Get(c.Request().Context(), profileID.String(), dreamID)
	if err != nil {
		if errors.Is(err, dreamservice.ErrDreamNotFound) {
			return httperr.New(httperr.NOT_FOUND, "dream not found")
		}
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"data": dream})
}

func dreamListOptions(c echo.Context) (dreamservice.ListOptions, error) {
	limit, err := dreamLimit(c.QueryParam("limit"))
	if err != nil {
		return dreamservice.ListOptions{}, err
	}
	status := strings.TrimSpace(c.QueryParam("status"))
	if status != "" && !domain.DreamStatus(status).IsValid() {
		return dreamservice.ListOptions{}, httperr.New(httperr.VALIDATION_ERROR, "status must be one of proposed, reinforced, stale, rejected, promoted")
	}
	sort := strings.TrimSpace(c.QueryParam("sort"))
	switch sort {
	case "", dreamservice.DreamSortUpdatedAt, dreamservice.DreamSortCreatedAt, dreamservice.DreamSortLastEvaluatedAt:
	default:
		return dreamservice.ListOptions{}, httperr.New(httperr.VALIDATION_ERROR, "sort must be updated_at, created_at, or last_evaluated_at")
	}
	direction := strings.TrimSpace(c.QueryParam("direction"))
	switch direction {
	case "", dreamservice.DreamDirectionAsc, dreamservice.DreamDirectionDesc:
	default:
		return dreamservice.ListOptions{}, httperr.New(httperr.VALIDATION_ERROR, "direction must be asc or desc")
	}
	return dreamservice.ListOptions{
		Limit:     limit,
		Status:    status,
		Cursor:    strings.TrimSpace(c.QueryParam("cursor")),
		Sort:      sort,
		Direction: direction,
	}, nil
}

func dreamLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultDreamListLimit, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 1 || parsed > maxDreamListLimit {
		return 0, httperr.New(httperr.VALIDATION_ERROR, "limit must be between 1 and 100")
	}
	return parsed, nil
}
