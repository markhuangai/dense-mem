package httperr

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

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
	case GONE:
		return http.StatusGone
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
func ErrorHandler(c *echo.Context, err error) {
	response, _ := echo.UnwrapResponse(c.Response())
	if response != nil && response.Committed {
		return
	}

	correlationID := correlation.FromContext(c.Request().Context())
	var apiErr *APIError
	var statusCode int
	if errors.As(err, &apiErr) {
		statusCode = HTTPStatusCode(apiErr.Code)
	} else {
		statusCode = echo.StatusCode(err)
		if statusCode == 0 {
			statusCode = http.StatusInternalServerError
		}
		message := http.StatusText(statusCode)
		var echoErr *echo.HTTPError
		if errors.As(err, &echoErr) && echoErr.Message != "" {
			message = echoErr.Message
		}
		apiErr = echoHTTPErrorToAPIError(statusCode, message)
		if statusCode == http.StatusNotFound && c.Path() == "" {
			apiErr = WithGuidance(
				New(NOT_FOUND, "The requested route is not available."),
				"invalid_request",
				"correct_and_resubmit",
				"Use a supported route and HTTP method, then submit the request again.",
				false,
				nil,
				correlationID,
			)
		}
	}
	apiErr = withDefaultGuidance(apiErr, statusCode, correlationID)
	_ = c.JSON(statusCode, apiErr.bounded(statusCode))
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

func echoHTTPErrorToAPIError(status int, message string) *APIError {
	switch status {
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
