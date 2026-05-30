package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/httperr"
)

type bindValidateBody struct {
	Name  string `json:"name" validate:"required"`
	Email string `json:"email" validate:"omitempty,email"`
}

type fakeFieldValidationError struct {
	field string
	tag   string
	msg   string
}

func (e fakeFieldValidationError) Error() string { return e.msg }
func (e fakeFieldValidationError) Field() string { return e.field }
func (e fakeFieldValidationError) Tag() string   { return e.tag }

type fakeValidationErrorCollection struct {
	errs []error
}

func (e fakeValidationErrorCollection) Error() string   { return "validation failed" }
func (e fakeValidationErrorCollection) Errors() []error { return e.errs }

func TestBindAndValidateStoresValidatedBody(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.POST("/body", func(c echo.Context) error {
		body := MustGetValidatedBody[bindValidateBody](c.Request().Context(), "body")
		return c.JSON(http.StatusOK, map[string]any{"name": body.Name, "email": body.Email})
	}, BindAndValidate[bindValidateBody]("body"))

	req := httptest.NewRequest(http.MethodPost, "/body", strings.NewReader(`{"name":"Ada","email":"ada@example.com"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"name":"Ada"`)
	require.Contains(t, rec.Body.String(), `"email":"ada@example.com"`)
}

func TestBindAndValidateRejectsMalformedAndInvalidBodies(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.POST("/body", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	}, BindValidateMiddleware[bindValidateBody]("body"))

	req := httptest.NewRequest(http.MethodPost, "/body", strings.NewReader(`{"name":`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "malformed JSON body")

	req = httptest.NewRequest(http.MethodPost, "/body", strings.NewReader(`{"email":"not-email"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "validation failed")
}

func TestValidatedBodyHelpersAndValidationFormatting(t *testing.T) {
	ctx := context.Background()
	_, ok := GetValidatedBody[bindValidateBody](ctx, "missing")
	require.False(t, ok)
	require.PanicsWithValue(t, "bind_validate: validated body not found in context", func() {
		MustGetValidatedBody[bindValidateBody](ctx, "missing")
	})

	details := extractValidationErrors(fakeValidationErrorCollection{errs: []error{
		fakeFieldValidationError{field: "Name", tag: "required", msg: "required raw"},
		fakeFieldValidationError{field: "Email", tag: "email", msg: "email raw"},
		errors.New("plain error"),
	}})
	require.Len(t, details, 3)
	require.Equal(t, "Name", details[0].Field)
	require.Equal(t, "this field is required", details[0].Message)
	require.Equal(t, "must be a valid email address", details[1].Message)
	require.Equal(t, "unknown", details[2].Field)

	require.Nil(t, extractValidationErrors(nil))
	require.Equal(t, "value is below minimum", formatValidationMessage("min", "raw"))
	require.Equal(t, "value exceeds maximum", formatValidationMessage("max", "raw"))
	require.Equal(t, "must not be blank", formatValidationMessage("notblank", "raw"))
	require.Equal(t, "raw", formatValidationMessage("custom", "raw"))
}
