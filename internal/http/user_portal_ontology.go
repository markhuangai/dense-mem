package http

import (
	"errors"
	"slices"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	mw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/ontology"
	access "github.com/markhuangai/dense-mem/internal/service/access"
)

const ontologySaveBodyKey = "ontologyDraftSave"
const ontologyDraftBodyKey = "ontologyDraftCommand"
const ontologyPreviewBodyKey = "ontologyDraftPreview"
const ontologyRollbackBodyKey = "ontologyRollback"

type ontologySaveDraftRequest struct {
	OperationKey     string                    `json:"operation_key" validate:"required,max=128"`
	ExpectedVersion  *int64                    `json:"expected_version" validate:"required,min=0"`
	ExpectedRevision *int64                    `json:"expected_revision" validate:"required,min=0"`
	Reason           string                    `json:"reason" validate:"required,max=512"`
	Edits            []ontology.ManagementEdit `json:"edits" validate:"required,min=1,max=64"`
}

type ontologyManagementHandler struct{ service *ontology.ManagementService }

func registerOntologyManagementRoutes(api *echo.Group, service *ontology.ManagementService) {
	h := &ontologyManagementHandler{service: service}
	group := api.Group("/ontology", mw.RequireRole(access.CredentialRoleManager), userPortalServiceAvailable(service != nil, "ontology management unavailable"), func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
			ctx := ontology.WithManagementAuditContext(c.Request().Context(), c.RealIP(), mw.GetCorrelationID(c.Request().Context()))
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	read, write := mw.RequireScopes("read"), mw.RequireScopes("write")
	group.GET("/records", h.records, read, ontologyQuery("kind", "cursor", "limit"))
	group.GET("/records/:id", h.record, read, ontologyQuery("version"))
	group.GET("/sources", h.sources, read, ontologyQuery("kind", "cursor", "q", "limit"))
	group.GET("/status", h.status, read, ontologyQuery())
	group.GET("/drafts", h.drafts, read, ontologyQuery("cursor", "limit"))
	group.GET("/drafts/:id", h.draft, read, ontologyQuery())
	group.POST("/drafts", h.saveDraft, write, ontologyQuery(), mw.BindAndValidateStrict[ontologySaveDraftRequest](ontologySaveBodyKey))
	group.PUT("/drafts/:id", h.saveDraft, write, ontologyQuery(), mw.BindAndValidateStrict[ontologySaveDraftRequest](ontologySaveBodyKey))
	group.POST("/drafts/:id/preview", h.preview, read, ontologyQuery(), mw.BindAndValidateStrict[dto.OntologyPreviewRequest](ontologyPreviewBodyKey))
	group.POST("/drafts/:id/publish", h.publish, write, ontologyQuery(), mw.BindAndValidateStrict[dto.OntologyDraftCommandRequest](ontologyDraftBodyKey))
	group.POST("/drafts/:id/discard", h.discard, write, ontologyQuery(), mw.BindAndValidateStrict[dto.OntologyDraftCommandRequest](ontologyDraftBodyKey))
	group.GET("/publications", h.publications, read, ontologyQuery("after", "limit"))
	group.GET("/publications/:id", h.publication, read, ontologyQuery())
	group.POST("/publications/:id/rollback", h.rollback, write, ontologyQuery(), mw.BindAndValidateStrict[dto.OntologyRollbackRequest](ontologyRollbackBodyKey))
}

func ontologyQuery(allowed ...string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			for name, values := range c.QueryParams() {
				if !slices.Contains(allowed, name) || len(values) != 1 {
					return httperr.New(httperr.VALIDATION_ERROR, "unknown or repeated ontology query parameter")
				}
			}
			return next(c)
		}
	}
}

func ontologyIntQuery(c echo.Context, name string, fallback int64) (int64, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, httperr.New(httperr.VALIDATION_ERROR, "invalid ontology query value")
	}
	return value, nil
}

func ontologyPageLimit(c echo.Context) (int, error) {
	value, err := ontologyIntQuery(c, "limit", 50)
	if err != nil {
		return 0, err
	}
	if value < 1 || value > ontology.MaxPageSize {
		return 0, httperr.New(httperr.VALIDATION_ERROR, "ontology page limit must be 1..200")
	}
	return int(value), nil
}

func managementResponse(c echo.Context, value any, err error) error {
	if err == nil {
		return response.SuccessOK(c, value)
	}
	switch {
	case errors.Is(err, ontology.ErrUnauthorized):
		return httperr.New(httperr.FORBIDDEN, "manager authorization required")
	case errors.Is(err, ontology.ErrNotFound):
		return httperr.New(httperr.NOT_FOUND, "ontology item not found")
	case errors.Is(err, ontology.ErrConflict), errors.Is(err, ontology.ErrSourceStale):
		return httperr.New(httperr.CONFLICT, "ontology state changed; refresh the draft before resubmitting")
	case errors.Is(err, ontology.ErrInvalid), errors.Is(err, ontology.ErrOverride):
		return httperr.New(httperr.VALIDATION_ERROR, "invalid or incompatible ontology correction")
	default:
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "ontology management unavailable")
	}
}

func (h *ontologyManagementHandler) records(c echo.Context) error {
	limit, err := ontologyPageLimit(c)
	if err != nil {
		return err
	}
	value, err := h.service.Records(c.Request().Context(), ontology.Kind(c.QueryParam("kind")), c.QueryParam("cursor"), limit)
	return managementResponse(c, value, err)
}
func (h *ontologyManagementHandler) record(c echo.Context) error {
	version, err := ontologyIntQuery(c, "version", 0)
	if err != nil {
		return err
	}
	value, err := h.service.Record(c.Request().Context(), c.Param("id"), version)
	return managementResponse(c, value, err)
}
func (h *ontologyManagementHandler) sources(c echo.Context) error {
	limit, err := ontologyPageLimit(c)
	if err != nil {
		return err
	}
	kind := ontology.SourceKind(c.QueryParam("kind"))
	if kind == "" {
		kind = ontology.EvidenceSource
	}
	value, err := h.service.Sources(c.Request().Context(), kind, c.QueryParam("cursor"), c.QueryParam("q"), limit)
	return managementResponse(c, value, err)
}
func (h *ontologyManagementHandler) status(c echo.Context) error {
	value, err := h.service.Status(c.Request().Context())
	return managementResponse(c, value, err)
}
func (h *ontologyManagementHandler) drafts(c echo.Context) error {
	limit, err := ontologyPageLimit(c)
	if err != nil {
		return err
	}
	value, err := h.service.Drafts(c.Request().Context(), c.QueryParam("cursor"), limit)
	return managementResponse(c, value, err)
}
func (h *ontologyManagementHandler) draft(c echo.Context) error {
	value, err := h.service.Draft(c.Request().Context(), c.Param("id"))
	return managementResponse(c, value, err)
}
func (h *ontologyManagementHandler) saveDraft(c echo.Context) error {
	body := mw.MustGetValidatedBody[ontologySaveDraftRequest](c.Request().Context(), ontologySaveBodyKey)
	value, err := h.service.SaveDraft(c.Request().Context(), c.Param("id"), ontology.SaveDraftInput{OperationKey: body.OperationKey, ExpectedVersion: *body.ExpectedVersion, ExpectedRevision: *body.ExpectedRevision, Reason: body.Reason, Edits: body.Edits})
	return managementResponse(c, value, err)
}
func (h *ontologyManagementHandler) preview(c echo.Context) error {
	body := mw.MustGetValidatedBody[dto.OntologyPreviewRequest](c.Request().Context(), ontologyPreviewBodyKey)
	value, err := h.service.Preview(c.Request().Context(), c.Param("id"), *body.ExpectedVersion)
	return managementResponse(c, value, err)
}
func (h *ontologyManagementHandler) publish(c echo.Context) error {
	return h.commandDraft(c, "publish")
}
func (h *ontologyManagementHandler) discard(c echo.Context) error {
	return h.commandDraft(c, "discard")
}
func (h *ontologyManagementHandler) commandDraft(c echo.Context, action string) error {
	body := mw.MustGetValidatedBody[dto.OntologyDraftCommandRequest](c.Request().Context(), ontologyDraftBodyKey)
	value, err := h.service.CommandDraft(c.Request().Context(), c.Param("id"), action, ontology.DraftCommand{OperationKey: body.OperationKey, ExpectedVersion: *body.ExpectedVersion, ExpectedRevision: *body.ExpectedRevision, Reason: body.Reason})
	return managementResponse(c, value, err)
}
func (h *ontologyManagementHandler) publications(c echo.Context) error {
	limit, err := ontologyPageLimit(c)
	if err != nil {
		return err
	}
	after, err := ontologyIntQuery(c, "after", 0)
	if err != nil {
		return err
	}
	value, err := h.service.Publications(c.Request().Context(), after, limit)
	return managementResponse(c, value, err)
}
func (h *ontologyManagementHandler) publication(c echo.Context) error {
	value, err := h.service.Publication(c.Request().Context(), c.Param("id"))
	return managementResponse(c, value, err)
}
func (h *ontologyManagementHandler) rollback(c echo.Context) error {
	body := mw.MustGetValidatedBody[dto.OntologyRollbackRequest](c.Request().Context(), ontologyRollbackBodyKey)
	value, err := h.service.Rollback(c.Request().Context(), c.Param("id"), ontology.DraftCommand{OperationKey: body.OperationKey, ExpectedRevision: *body.ExpectedRevision, Reason: body.Reason})
	return managementResponse(c, value, err)
}
