package http

import (
	"errors"
	"slices"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	mw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	access "github.com/markhuangai/dense-mem/internal/service/access"
	topics "github.com/markhuangai/dense-mem/internal/topics"
)

const topicEditsBodyKey = "topicEdits"
const topicSaveBodyKey = "topicDraftSave"
const topicDraftBodyKey = "topicDraftCommand"
const topicPreviewBodyKey = "topicDraftPreview"
const topicRollbackBodyKey = "topicRollback"

type topicSaveDraftRequest struct {
	OperationKey     string                  `json:"operation_key" validate:"required,max=128"`
	ExpectedVersion  *int64                  `json:"expected_version" validate:"required,min=0"`
	ExpectedRevision *int64                  `json:"expected_revision" validate:"required,min=0"`
	Reason           string                  `json:"reason" validate:"required,max=512"`
	Edits            []topics.ManagementEdit `json:"edits" validate:"required,min=1,max=64"`
}

type topicManagementHandler struct{ service *topics.ManagementService }

func registerTopicManagementRoutes(api *echo.Group, service *topics.ManagementService) {
	h := &topicManagementHandler{service: service}
	group := api.Group("/topics", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			err := next(c)
			var publicError *httperr.APIError
			if errors.As(err, &publicError) && publicError.Code == httperr.FORBIDDEN {
				service.ObserveAuthorizationRejection(c.Request().Context(), c.Request().Method+" "+c.Path())
			}
			return err
		}
	}, mw.RequireRole(access.CredentialRoleManager), userPortalServiceAvailable(service != nil, "topic management unavailable"), func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
			ctx := topics.WithManagementAuditContext(c.Request().Context(), c.RealIP(), mw.GetCorrelationID(c.Request().Context()))
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	read, write := mw.RequireScopes("read"), mw.RequireScopes("write")
	group.GET("/records", h.records, read, topicQuery("kind", "cursor", "limit"))
	group.GET("/records/:id", h.record, read, topicQuery("version"))
	group.GET("/sources", h.sources, read, topicQuery("kind", "cursor", "q", "limit"))
	group.GET("/status", h.status, read, topicQuery())
	group.GET("/drafts", h.drafts, read, topicQuery("cursor", "limit"))
	group.GET("/drafts/:id", h.draft, read, topicQuery())
	group.POST("/edits", h.saveEdits, write, topicQuery(), mw.BindAndValidateStrict[topics.SaveEditsInput](topicEditsBodyKey))
	group.POST("/drafts", h.saveDraft, write, topicQuery(), mw.BindAndValidateStrict[topicSaveDraftRequest](topicSaveBodyKey))
	group.PUT("/drafts/:id", h.saveDraft, write, topicQuery(), mw.BindAndValidateStrict[topicSaveDraftRequest](topicSaveBodyKey))
	group.POST("/drafts/:id/preview", h.preview, read, topicQuery(), mw.BindAndValidateStrict[dto.TopicPreviewRequest](topicPreviewBodyKey))
	group.POST("/drafts/:id/publish", h.publish, write, topicQuery(), mw.BindAndValidateStrict[dto.TopicDraftCommandRequest](topicDraftBodyKey))
	group.POST("/drafts/:id/discard", h.discard, write, topicQuery(), mw.BindAndValidateStrict[dto.TopicDraftCommandRequest](topicDraftBodyKey))
	group.GET("/publications", h.publications, read, topicQuery("after", "limit"))
	group.GET("/publications/:id", h.publication, read, topicQuery())
	group.POST("/publications/:id/rollback", h.rollback, write, topicQuery(), mw.BindAndValidateStrict[dto.TopicRollbackRequest](topicRollbackBodyKey))
}

func topicQuery(allowed ...string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			for name, values := range c.QueryParams() {
				if !slices.Contains(allowed, name) || len(values) != 1 {
					return httperr.New(httperr.VALIDATION_ERROR, "unknown or repeated topic query parameter")
				}
			}
			return next(c)
		}
	}
}

func topicIntQuery(c *echo.Context, name string, fallback int64) (int64, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, httperr.New(httperr.VALIDATION_ERROR, "invalid topic query value")
	}
	return value, nil
}

func topicPageLimit(c *echo.Context) (int, error) {
	value, err := topicIntQuery(c, "limit", 50)
	if err != nil {
		return 0, err
	}
	if value < 1 || value > topics.MaxPageSize {
		return 0, httperr.New(httperr.VALIDATION_ERROR, "topic page limit must be between 1 and 200")
	}
	return int(value), nil
}

func managementResponse(c *echo.Context, value any, err error) error {
	if err == nil {
		return response.SuccessOK(c, value)
	}
	switch {
	case errors.Is(err, topics.ErrUnauthorized):
		return httperr.New(httperr.FORBIDDEN, "manager authorization required")
	case errors.Is(err, topics.ErrLegacyMutation):
		return httperr.New(httperr.GONE, "use POST /ui/api/topics/edits to save topic changes")
	case errors.Is(err, topics.ErrNotFound):
		return httperr.New(httperr.NOT_FOUND, "topic management item not found")
	case errors.Is(err, topics.ErrConflict), errors.Is(err, topics.ErrSourceStale):
		return httperr.New(httperr.CONFLICT, "topic state changed. Refresh the record before resubmitting")
	case errors.Is(err, topics.ErrInvalid), errors.Is(err, topics.ErrOverride):
		return httperr.New(httperr.VALIDATION_ERROR, "invalid or incompatible topic edit")
	default:
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "topic management unavailable")
	}
}

func (h *topicManagementHandler) records(c *echo.Context) error {
	limit, err := topicPageLimit(c)
	if err != nil {
		return err
	}
	value, err := h.service.Records(c.Request().Context(), topics.Kind(c.QueryParam("kind")), c.QueryParam("cursor"), limit)
	return managementResponse(c, value, err)
}
func (h *topicManagementHandler) record(c *echo.Context) error {
	version, err := topicIntQuery(c, "version", 0)
	if err != nil {
		return err
	}
	value, err := h.service.Record(c.Request().Context(), c.Param("id"), version)
	return managementResponse(c, value, err)
}
func (h *topicManagementHandler) sources(c *echo.Context) error {
	limit, err := topicPageLimit(c)
	if err != nil {
		return err
	}
	kind := topics.SourceKind(c.QueryParam("kind"))
	if kind == "" {
		kind = topics.EvidenceSource
	}
	value, err := h.service.Sources(c.Request().Context(), kind, c.QueryParam("cursor"), c.QueryParam("q"), limit)
	return managementResponse(c, value, err)
}
func (h *topicManagementHandler) status(c *echo.Context) error {
	value, err := h.service.Status(c.Request().Context())
	return managementResponse(c, value, err)
}
func (h *topicManagementHandler) drafts(c *echo.Context) error {
	limit, err := topicPageLimit(c)
	if err != nil {
		return err
	}
	value, err := h.service.Drafts(c.Request().Context(), c.QueryParam("cursor"), limit)
	return managementResponse(c, value, err)
}
func (h *topicManagementHandler) draft(c *echo.Context) error {
	value, err := h.service.Draft(c.Request().Context(), c.Param("id"))
	return managementResponse(c, value, err)
}
func (h *topicManagementHandler) saveDraft(c *echo.Context) error {
	body := mw.MustGetValidatedBody[topicSaveDraftRequest](c.Request().Context(), topicSaveBodyKey)
	input := topics.SaveDraftInput{OperationKey: body.OperationKey, ExpectedVersion: *body.ExpectedVersion, ExpectedRevision: *body.ExpectedRevision, Reason: body.Reason, Edits: body.Edits}
	value, err := h.service.ReplayDraft(c.Request().Context(), c.Param("id"), "save", body.OperationKey, input)
	return managementResponse(c, value, err)
}
func (h *topicManagementHandler) preview(c *echo.Context) error {
	return managementResponse(c, nil, h.service.RetiredPreview(c.Request().Context()))
}
func (h *topicManagementHandler) publish(c *echo.Context) error {
	return h.commandDraft(c, "publish")
}
func (h *topicManagementHandler) discard(c *echo.Context) error {
	return h.commandDraft(c, "discard")
}
func (h *topicManagementHandler) commandDraft(c *echo.Context, action string) error {
	body := mw.MustGetValidatedBody[dto.TopicDraftCommandRequest](c.Request().Context(), topicDraftBodyKey)
	input := topics.DraftCommand{OperationKey: body.OperationKey, ExpectedVersion: *body.ExpectedVersion, ExpectedRevision: *body.ExpectedRevision, Reason: body.Reason}
	value, err := h.service.ReplayDraft(c.Request().Context(), c.Param("id"), action, body.OperationKey, input)
	return managementResponse(c, value, err)
}
func (h *topicManagementHandler) publications(c *echo.Context) error {
	limit, err := topicPageLimit(c)
	if err != nil {
		return err
	}
	after, err := topicIntQuery(c, "after", 0)
	if err != nil {
		return err
	}
	value, err := h.service.Publications(c.Request().Context(), after, limit)
	return managementResponse(c, value, err)
}
func (h *topicManagementHandler) publication(c *echo.Context) error {
	value, err := h.service.Publication(c.Request().Context(), c.Param("id"))
	return managementResponse(c, value, err)
}
func (h *topicManagementHandler) rollback(c *echo.Context) error {
	body := mw.MustGetValidatedBody[dto.TopicRollbackRequest](c.Request().Context(), topicRollbackBodyKey)
	value, err := h.service.ReplayRollback(c.Request().Context(), c.Param("id"), topics.DraftCommand{OperationKey: body.OperationKey, ExpectedRevision: *body.ExpectedRevision, Reason: body.Reason})
	return managementResponse(c, value, err)
}

func (h *topicManagementHandler) saveEdits(c *echo.Context) error {
	input := mw.MustGetValidatedBody[topics.SaveEditsInput](c.Request().Context(), topicEditsBodyKey)
	value, err := h.service.SaveEdits(c.Request().Context(), *input)
	return managementResponse(c, value, err)
}
