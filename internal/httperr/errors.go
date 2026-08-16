package httperr

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxPublicErrorMessageRunes  = 512
	maxPublicErrorDetails       = 20
	maxPublicErrorFieldRunes    = 128
	maxPublicDetailMessageRunes = 512
)

// ErrorCode represents a typed error code for API errors.
type ErrorCode string

// Error code constants — generic surface codes (kept for backward compatibility)
const (
	AUTH_MISSING                        ErrorCode = "AUTH_MISSING"
	AUTH_INVALID                        ErrorCode = "AUTH_INVALID"
	AUTH_EXPIRED                        ErrorCode = "AUTH_EXPIRED"
	AUTH_REVOKED                        ErrorCode = "AUTH_REVOKED"
	FORBIDDEN                           ErrorCode = "FORBIDDEN"
	NOT_FOUND                           ErrorCode = "NOT_FOUND"
	VALIDATION_ERROR                    ErrorCode = "VALIDATION_ERROR"
	PROFILE_ID_REQUIRED                 ErrorCode = "PROFILE_ID_REQUIRED"
	INVALID_UUID                        ErrorCode = "INVALID_UUID"
	CONFLICT                            ErrorCode = "CONFLICT"
	RATE_LIMITED                        ErrorCode = "RATE_LIMITED"
	SERVICE_UNAVAILABLE                 ErrorCode = "SERVICE_UNAVAILABLE"
	INTERNAL_ERROR                      ErrorCode = "INTERNAL_ERROR"
	EMBEDDING_GENERATION_NOT_CONFIGURED ErrorCode = "EMBEDDING_GENERATION_NOT_CONFIGURED"
)

// Knowledge-pipeline domain error codes — stable external lowercase codes (AC-X6).
// These represent specific failure modes in the claim/fact promotion pipeline and
// are intended to be part of the public API contract for clients.
const (
	// Fragment / lookup errors (404)
	ErrSupportingFragmentMissing ErrorCode = "supporting_fragment_missing"
	ErrClaimNotFound             ErrorCode = "claim_not_found"
	ErrFactNotFound              ErrorCode = "fact_not_found"

	// Verifier back-pressure (429 / 50x)
	ErrVerifierRateLimit         ErrorCode = "verifier_rate_limit"
	ErrVerifierTimeout           ErrorCode = "verifier_timeout"
	ErrVerifierProvider          ErrorCode = "verifier_provider"
	ErrVerifierMalformedResponse ErrorCode = "verifier_malformed_response"

	// Policy / predicate violations (422)
	ErrPredicateNotPoliced    ErrorCode = "predicate_not_policed"
	ErrUnsupportedPolicy      ErrorCode = "unsupported_policy"
	ErrCommunityGraphTooLarge ErrorCode = "community_graph_too_large"

	// State-machine conflicts (409)
	ErrNeedsClaimValidated ErrorCode = "needs_claim_validated"
	ErrGateRejected        ErrorCode = "gate_rejected"
	ErrComparableDisputed  ErrorCode = "comparable_disputed"
	ErrRejectedWeaker      ErrorCode = "rejected_weaker"
)

// ErrorDetail represents a single validation error detail.
type ErrorDetail struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// APIError represents a structured API error with code, message, and optional details.
type APIError struct {
	Code    ErrorCode     `json:"code"`
	Message string        `json:"message"`
	Details []ErrorDetail `json:"details"`
}

func boundedText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes])
}

// boundedMessage keeps public error text finite without splitting a UTF-8
// sequence. It is deliberately applied at the transport boundary so internal
// errors retain their diagnostic context for server-side handling.
func boundedMessage(value string, maxRunes int) string {
	if bounded := boundedText(value, maxRunes); bounded != "" {
		return bounded
	}
	return "unknown error"
}

func (e *APIError) bounded(status int) *APIError {
	if e == nil {
		return New(INTERNAL_ERROR, stablePublicMessage(httpStatusInternalServerError))
	}
	copyErr := &APIError{Code: e.Code, Message: e.Message}
	if status >= 500 {
		copyErr.Message = stablePublicMessage(status)
		return copyErr
	}
	copyErr.Message = boundedMessage(e.Message, maxPublicErrorMessageRunes)
	if len(e.Details) == 0 {
		return copyErr
	}
	limit := len(e.Details)
	if limit > maxPublicErrorDetails {
		limit = maxPublicErrorDetails
	}
	copyErr.Details = make([]ErrorDetail, 0, limit)
	for _, detail := range e.Details[:limit] {
		copyErr.Details = append(copyErr.Details, ErrorDetail{
			Field:   boundedText(detail.Field, maxPublicErrorFieldRunes),
			Message: boundedMessage(detail.Message, maxPublicDetailMessageRunes),
		})
	}
	return copyErr
}

const (
	httpStatusInternalServerError = 500
)

func stablePublicMessage(status int) string {
	switch status {
	case 502:
		return "upstream service error"
	case 503:
		return "service unavailable"
	case 504:
		return "upstream service timeout"
	default:
		return "internal server error"
	}
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// APIErrorProvider is the companion interface for APIError.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type APIErrorProvider interface {
	Error() string
	GetCode() ErrorCode
	GetMessage() string
	GetDetails() []ErrorDetail
}

// Ensure APIError implements APIErrorProvider
var _ APIErrorProvider = (*APIError)(nil)

// GetCode returns the error code.
func (e *APIError) GetCode() ErrorCode {
	return e.Code
}

// GetMessage returns the error message.
func (e *APIError) GetMessage() string {
	return e.Message
}

// GetDetails returns the error details.
func (e *APIError) GetDetails() []ErrorDetail {
	return e.Details
}

// New creates a new APIError with the given code and message.
func New(code ErrorCode, message string) *APIError {
	return &APIError{
		Code:    code,
		Message: message,
		Details: nil,
	}
}

// NewWithDetails creates a new APIError with validation details.
func NewWithDetails(code ErrorCode, message string, details []ErrorDetail) *APIError {
	return &APIError{
		Code:    code,
		Message: message,
		Details: details,
	}
}

// ErrorEnvelope is the JSON envelope for error responses.
type ErrorEnvelope struct {
	Error *APIError `json:"error"`
}

// NewErrorEnvelope creates a new error envelope wrapping an APIError.
func NewErrorEnvelope(err *APIError) ErrorEnvelope {
	return ErrorEnvelope{Error: err}
}
