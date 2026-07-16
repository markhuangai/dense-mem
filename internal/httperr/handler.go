package httperr

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// HTTPStatusCode maps ErrorCode to HTTP status codes.
func HTTPStatusCode(code ErrorCode) int {
	switch code {
	case AUTH_MISSING, AUTH_INVALID, AUTH_EXPIRED, AUTH_REVOKED:
		return http.StatusUnauthorized
	case FORBIDDEN:
		return http.StatusForbidden
	case NOT_FOUND:
		return http.StatusNotFound
	case VALIDATION_ERROR:
		return http.StatusUnprocessableEntity
	case PROFILE_ID_REQUIRED, INVALID_UUID:
		return http.StatusBadRequest
	case CONFLICT, PROFILE_HAS_ACTIVE_KEYS:
		return http.StatusConflict
	case RATE_LIMITED:
		return http.StatusTooManyRequests
	case SERVICE_UNAVAILABLE:
		return http.StatusServiceUnavailable
	case INTERNAL_ERROR:
		return http.StatusInternalServerError

	// Knowledge-pipeline domain codes (AC-X6)
	case ErrSupportingFragmentMissing, ErrClaimNotFound, ErrFactNotFound:
		return http.StatusNotFound
	case ErrVerifierRateLimit:
		return http.StatusTooManyRequests
	case ErrVerifierTimeout:
		return http.StatusGatewayTimeout
	case ErrVerifierProvider:
		return http.StatusServiceUnavailable
	case ErrVerifierMalformedResponse:
		return http.StatusBadGateway
	case ErrPredicateNotPoliced, ErrUnsupportedPolicy, ErrCommunityGraphTooLarge:
		return http.StatusUnprocessableEntity
	case ErrNeedsClaimValidated, ErrGateRejected, ErrComparableDisputed, ErrRejectedWeaker:
		return http.StatusConflict

	default:
		return http.StatusInternalServerError
	}
}

// ErrorHandler is the central Echo error handler that formats all errors
// into the standard error envelope.
func ErrorHandler(err error, c echo.Context) {
	// Determine the APIError and status code
	var apiErr *APIError
	var statusCode int

	if he, ok := err.(*echo.HTTPError); ok {
		// Handle Echo's HTTPError
		statusCode = he.Code
		apiErr = echoHTTPErrorToAPIError(he)
	} else if ae, ok := err.(*APIError); ok {
		// Handle our typed APIError
		apiErr = ae
		statusCode = HTTPStatusCode(ae.Code)
	} else {
		// Handle generic errors
		apiErr = New(INTERNAL_ERROR, err.Error())
		statusCode = http.StatusInternalServerError
	}

	// Don't overwrite the response if already committed
	if c.Response().Committed {
		return
	}

	// Send the APIError directly as a flat JSON object {code, message, details}.
	// AC-X6: The stable external contract exposes code at the top level so
	// callers can match on body.code without needing to unwrap a nested key.
	c.JSON(statusCode, apiErr)
}

// echoHTTPErrorToAPIError converts an Echo HTTPError to our APIError.
// Uses safe formatting to avoid panics on non-string Message types.
func echoHTTPErrorToAPIError(he *echo.HTTPError) *APIError {
	// Safely format message - handle both string and non-string types
	message := "unknown error"
	if he.Message != nil {
		if msg, ok := he.Message.(string); ok {
			message = msg
		} else {
			message = he.Error()
		}
	}

	switch he.Code {
	case http.StatusBadRequest:
		return New(VALIDATION_ERROR, message)
	case http.StatusUnauthorized:
		return New(AUTH_INVALID, message)
	case http.StatusForbidden:
		return New(FORBIDDEN, message)
	case http.StatusNotFound:
		return New(NOT_FOUND, message)
	case http.StatusConflict:
		return New(CONFLICT, message)
	case http.StatusTooManyRequests:
		return New(RATE_LIMITED, message)
	case http.StatusServiceUnavailable:
		return New(SERVICE_UNAVAILABLE, message)
	case http.StatusInternalServerError:
		return New(INTERNAL_ERROR, message)
	default:
		return New(INTERNAL_ERROR, message)
	}
}
