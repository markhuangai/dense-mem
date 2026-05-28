package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
)

const (
	defaultClaimListLimit = 20
	// maxClaimListLimit mirrors the DTO validator upper bound (validate:"max=100").
	// The struct validator rejects requests exceeding this value before the limit
	// is ever used — this constant makes the boundary explicit in the handler.
	maxClaimListLimit = 100
)

// ClaimListHandler serves GET /api/v1/claims.
type ClaimListHandler struct {
	svc      claimservice.ListClaimsService
	filtered claimservice.ListClaimsFilteredService
}

// ClaimListHandlerInterface is the companion interface for ClaimListHandler.
type ClaimListHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure ClaimListHandler implements ClaimListHandlerInterface.
var _ ClaimListHandlerInterface = (*ClaimListHandler)(nil)

// NewClaimListHandler constructs a ClaimListHandler.
func NewClaimListHandler(svc claimservice.ListClaimsService) *ClaimListHandler {
	return &ClaimListHandler{svc: svc}
}

// NewClaimListHandlerWithFiltered constructs a ClaimListHandler that uses the
// keyset-paginated service for new opaque cursors while retaining the offset
// compatibility service for legacy numeric cursors.
func NewClaimListHandlerWithFiltered(svc claimservice.ListClaimsService, filtered claimservice.ListClaimsFilteredService) *ClaimListHandler {
	return &ClaimListHandler{svc: svc, filtered: filtered}
}

// Handle lists claims in the caller's profile. New responses use the
// keyset-paginated cursor from ListClaimsFilteredService; legacy numeric cursors
// continue to route through ListClaimsService.
func (h *ClaimListHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	var req dto.ListClaimsRequest
	if err := c.Bind(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "invalid query parameters")
	}
	if err := validation.ValidateStruct(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	// Resolve limit — default to defaultClaimListLimit when the caller omits it.
	limit := req.Limit
	if limit == 0 {
		limit = defaultClaimListLimit
	}

	if h.filtered != nil && !isLegacyClaimOffsetCursor(req.Cursor) {
		result, err := h.filtered.List(ctx, profileID.String(), claimservice.ListClaimOptions{
			Limit:    limit,
			Cursor:   req.Cursor,
			Status:   req.Status,
			Modality: req.Modality,
		})
		if err != nil {
			if errors.Is(err, claimservice.ErrInvalidClaimCursor) {
				return httperr.New(httperr.VALIDATION_ERROR, "invalid cursor")
			}
			return httperr.New(httperr.INTERNAL_ERROR, "failed to list claims")
		}
		claims := make([]domain.Claim, 0, len(result.Items))
		for _, p := range result.Items {
			if p != nil {
				claims = append(claims, *p)
			}
		}
		return c.JSON(http.StatusOK, response.ToListClaimsResponse(claims, result.NextCursor))
	}

	// Decode cursor as an offset integer. An empty cursor means start at 0.
	// An unparseable cursor is rejected as a validation error.
	offset := 0
	if req.Cursor != "" {
		n, err := strconv.Atoi(req.Cursor)
		if err != nil || n < 0 {
			return httperr.New(httperr.VALIDATION_ERROR, "invalid cursor")
		}
		offset = n
	}

	ptrs, total, err := h.svc.List(ctx, profileID.String(), limit, offset)
	if err != nil {
		return httperr.New(httperr.INTERNAL_ERROR, "failed to list claims")
	}

	// Dereference pointer slice to satisfy ToListClaimsResponse signature.
	claims := make([]domain.Claim, 0, len(ptrs))
	for _, p := range ptrs {
		if p != nil {
			claims = append(claims, *p)
		}
	}

	// Produce a next cursor when there are more records beyond this page.
	nextCursor := ""
	if total > offset+len(claims) {
		nextCursor = strconv.Itoa(offset + limit)
	}

	return c.JSON(http.StatusOK, response.ToListClaimsResponse(claims, nextCursor))
}

func isLegacyClaimOffsetCursor(cursor string) bool {
	if cursor == "" {
		return false
	}
	n, err := strconv.Atoi(cursor)
	return err == nil && n >= 0
}
