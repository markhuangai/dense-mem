package httperr

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/correlation"
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
	case TEAM_REQUIRED:
		return http.StatusBadRequest
	case CONFLICT:
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
	case ErrVerifierTimeout, ErrEmbeddingTimeout:
		return http.StatusGatewayTimeout
	case ErrVerifierProvider, ErrEmbeddingUnavailable:
		return http.StatusServiceUnavailable
	case ErrVerifierMalformedResponse, ErrEmbeddingResponseInvalid:
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
		apiErr = New(INTERNAL_ERROR, stablePublicMessage(http.StatusInternalServerError))
		statusCode = http.StatusInternalServerError
	}
	apiErr = withDefaultGuidance(apiErr, statusCode, correlation.FromContext(c.Request().Context()))
	apiErr = apiErr.bounded(statusCode)

	// Don't overwrite the response if already committed
	if c.Response().Committed {
		return
	}

	// Send the APIError directly as a flat JSON object {code, message, details}.
	// AC-X6: The stable external contract exposes code at the top level so
	// callers can match on body.code without needing to unwrap a nested key.
	c.JSON(statusCode, apiErr)
}

func withDefaultGuidance(err *APIError, status int, correlationID string) *APIError {
	if err == nil {
		err = New(INTERNAL_ERROR, stablePublicMessage(status))
	}
	if err.NextAction != "" {
		if err.CorrelationID == "" {
			err.CorrelationID = correlationID
		}
		return err
	}
	reason := strings.ToLower(string(err.Code))
	action := "contact_operator"
	remediation := "Contact an operator with the correlation ID and request details."
	retryable := false
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		reason, action, remediation = "authorization_required", "obtain_authorization", "Obtain the required authorization or scope, then retry the request."
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusMethodNotAllowed, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		reason, action, remediation = "invalid_request", "correct_and_resubmit", "Correct the identified request fields and submit the request again."
	case http.StatusNotFound:
		reason, action, remediation = "reference_not_found", "refresh_state", "Refresh authorized state and retry with a current reference."
	case http.StatusConflict:
		reason, action, remediation = "state_conflict", "refresh_state", "Refresh authoritative state and retry with current values."
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		reason, action, remediation, retryable = "temporary_service_failure", "retry_same_request", "Retry the same request after the service recovers.", true
	}
	return WithGuidance(err, reason, action, remediation, retryable, nil, correlationID)
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
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusMethodNotAllowed, http.StatusUnsupportedMediaType:
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
