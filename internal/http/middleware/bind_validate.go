package middleware

import (
	"context"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

// BindAndValidate creates a middleware that binds JSON request body into type T,
// validates it with validator v10, and stores *T in context under the given key.
// On failure, returns 422 VALIDATION_ERROR with validation details.
//
// The middleware uses generics to provide type-safe request body access.
// Handlers can retrieve the validated body using GetValidatedBody[T].
func BindAndValidate[T any](ctxKey string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			var body T

			// Bind JSON body
			if err := c.Bind(&body); err != nil {
				// Malformed JSON
				return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
			}

			// Validate with validator v10
			if err := validation.ValidateStruct(&body); err != nil {
				// Validation failed - extract details
				details := extractValidationErrors(err)
				return httperr.NewWithDetails(httperr.VALIDATION_ERROR, "validation failed", details)
			}

			// Store validated body in context
			ctx := context.WithValue(c.Request().Context(), contextKey(ctxKey), &body)
			c.SetRequest(c.Request().WithContext(ctx))

			return next(c)
		}
	}
}

// contextKey is a typed string for context keys.
type contextKey string

// GetValidatedBody retrieves the validated body from context.
// Returns the typed pointer and true if found, or nil and false if not found.
func GetValidatedBody[T any](ctx context.Context, ctxKey string) (*T, bool) {
	if v := ctx.Value(contextKey(ctxKey)); v != nil {
		if typed, ok := v.(*T); ok {
			return typed, true
		}
	}
	return nil, false
}

// MustGetValidatedBody retrieves the validated body from context.
// Panics if not found. Use only when middleware order is guaranteed.
func MustGetValidatedBody[T any](ctx context.Context, ctxKey string) *T {
	body, ok := GetValidatedBody[T](ctx, ctxKey)
	if !ok {
		panic("bind_validate: validated body not found in context")
	}
	return body
}

// extractValidationErrors converts validator errors to APIError details.
func extractValidationErrors(err error) []httperr.ErrorDetail {
	if err == nil {
		return nil
	}

	// Handle validator.ValidationErrors
	type fieldError interface {
		Field() string
		Tag() string
		Error() string
	}

	// Check if it's a validation errors collection
	if validationErrors, ok := err.(interface{ Errors() []error }); ok {
		var details []httperr.ErrorDetail
		for _, e := range validationErrors.Errors() {
			if fe, ok := e.(fieldError); ok {
				details = append(details, httperr.ErrorDetail{
					Field:   fe.Field(),
					Message: formatValidationMessage(fe.Tag(), fe.Error()),
				})
			} else {
				details = append(details, httperr.ErrorDetail{
					Field:   "unknown",
					Message: e.Error(),
				})
			}
		}
		return details
	}

	// Single validation error
	if fe, ok := err.(fieldError); ok {
		return []httperr.ErrorDetail{{
			Field:   fe.Field(),
			Message: formatValidationMessage(fe.Tag(), fe.Error()),
		}}
	}

	// Generic error
	return []httperr.ErrorDetail{{
		Field:   "body",
		Message: err.Error(),
	}}
}

// formatValidationMessage creates a user-friendly validation message.
func formatValidationMessage(tag, errMsg string) string {
	switch tag {
	case "required":
		return "this field is required"
	case "email":
		return "must be a valid email address"
	case "min":
		return "value is below minimum"
	case "max":
		return "value exceeds maximum"
	case "notblank":
		return "must not be blank"
	default:
		return errMsg
	}
}

// BindValidateMiddleware creates a middleware that binds and validates request body.
// This is an alias for BindAndValidate for backward compatibility.
func BindValidateMiddleware[T any](ctxKey string) echo.MiddlewareFunc {
	return BindAndValidate[T](ctxKey)
}

// Context key constants for common validated bodies.
const (
	// CreateProfileBodyKey is the context key for create profile request body.
	CreateProfileBodyKey = "create_profile_body"
	// UpdateProfileBodyKey is the context key for update profile request body.
	UpdateProfileBodyKey = "update_profile_body"
	// CreateAPIKeyBodyKey is the context key for create API key request body.
	CreateAPIKeyBodyKey = "create_apikey_body"
	// UpdateAPIKeyBodyKey is the context key for update API key request body.
	UpdateAPIKeyBodyKey = "update_apikey_body"
)
