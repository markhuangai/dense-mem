package httperr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/correlation"
)

func TestErrorEnvelopeShape(t *testing.T) {
	// Create an APIError
	apiErr := New(VALIDATION_ERROR, "test error message")
	envelope := NewErrorEnvelope(apiErr)

	// Marshal to JSON
	jsonBytes, err := json.Marshal(envelope)
	require.NoError(t, err)

	// Unmarshal to verify structure
	var result map[string]interface{}
	err = json.Unmarshal(jsonBytes, &result)
	require.NoError(t, err)

	// Verify top-level "error" key exists
	errorObj, ok := result["error"]
	require.True(t, ok, "expected 'error' key at top level")

	// Verify error object has required fields
	errorMap, ok := errorObj.(map[string]interface{})
	require.True(t, ok, "expected error to be an object")

	// Check code field
	code, ok := errorMap["code"]
	require.True(t, ok, "expected 'code' field")
	assert.Equal(t, "VALIDATION_ERROR", code)

	// Check message field
	message, ok := errorMap["message"]
	require.True(t, ok, "expected 'message' field")
	assert.Equal(t, "test error message", message)

	// Check details field exists (can be null)
	_, ok = errorMap["details"]
	require.True(t, ok, "expected 'details' field")
}

func TestErrorCodes(t *testing.T) {
	tests := []struct {
		code     ErrorCode
		expected string
	}{
		{AUTH_MISSING, "AUTH_MISSING"},
		{AUTH_INVALID, "AUTH_INVALID"},
		{AUTH_EXPIRED, "AUTH_EXPIRED"},
		{AUTH_REVOKED, "AUTH_REVOKED"},
		{FORBIDDEN, "FORBIDDEN"},
		{NOT_FOUND, "NOT_FOUND"},
		{VALIDATION_ERROR, "VALIDATION_ERROR"},
		{PROFILE_ID_REQUIRED, "PROFILE_ID_REQUIRED"},
		{INVALID_UUID, "INVALID_UUID"},
		{CONFLICT, "CONFLICT"},
		{RATE_LIMITED, "RATE_LIMITED"},
		{SERVICE_UNAVAILABLE, "SERVICE_UNAVAILABLE"},
		{INTERNAL_ERROR, "INTERNAL_ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			// Verify the code serializes correctly
			apiErr := New(tt.code, "test message")
			assert.Equal(t, tt.expected, string(apiErr.Code))

			// Verify Error() method
			assert.Contains(t, apiErr.Error(), tt.expected)
		})
	}
}

func TestErrorWithDetails(t *testing.T) {
	details := []ErrorDetail{
		{Field: "name", Message: "name is required"},
		{Field: "email", Message: "email is invalid"},
	}

	apiErr := NewWithDetails(VALIDATION_ERROR, "validation failed", details)

	// Verify details are set
	require.Len(t, apiErr.Details, 2)
	assert.Equal(t, "name", apiErr.Details[0].Field)
	assert.Equal(t, "name is required", apiErr.Details[0].Message)
	assert.Equal(t, "email", apiErr.Details[1].Field)
	assert.Equal(t, "email is invalid", apiErr.Details[1].Message)

	// Verify JSON serialization
	envelope := NewErrorEnvelope(apiErr)
	jsonBytes, err := json.Marshal(envelope)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(jsonBytes, &result)
	require.NoError(t, err)

	errorMap := result["error"].(map[string]interface{})
	detailsArr := errorMap["details"].([]interface{})
	require.Len(t, detailsArr, 2)

	detail0 := detailsArr[0].(map[string]interface{})
	assert.Equal(t, "name", detail0["field"])
	assert.Equal(t, "name is required", detail0["message"])
}

func TestHTTPStatusCode(t *testing.T) {
	tests := []struct {
		code     ErrorCode
		expected int
	}{
		{AUTH_MISSING, http.StatusUnauthorized},
		{AUTH_INVALID, http.StatusUnauthorized},
		{AUTH_EXPIRED, http.StatusUnauthorized},
		{AUTH_REVOKED, http.StatusUnauthorized},
		{FORBIDDEN, http.StatusForbidden},
		{NOT_FOUND, http.StatusNotFound},
		{VALIDATION_ERROR, http.StatusUnprocessableEntity}, // 422 for validation errors
		{PROFILE_ID_REQUIRED, http.StatusBadRequest},
		{INVALID_UUID, http.StatusBadRequest},
		{CONFLICT, http.StatusConflict},
		{RATE_LIMITED, http.StatusTooManyRequests},
		{SERVICE_UNAVAILABLE, http.StatusServiceUnavailable},
		{INTERNAL_ERROR, http.StatusInternalServerError},
		{ErrorCode("UNKNOWN"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			assert.Equal(t, tt.expected, HTTPStatusCode(tt.code))
		})
	}
}

func TestErrorHandler(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = ErrorHandler

	t.Run("handles APIError", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		apiErr := New(NOT_FOUND, "resource not found")
		ErrorHandler(apiErr, c)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), `"code":"NOT_FOUND"`)
		assert.Contains(t, rec.Body.String(), `"message":"resource not found"`)
	})

	t.Run("classifies an unmatched route as a correctable request", func(t *testing.T) {
		router := echo.New()
		router.HTTPErrorHandler = ErrorHandler
		router.GET("/known", func(c echo.Context) error {
			return c.NoContent(http.StatusNoContent)
		})
		req := httptest.NewRequest(http.MethodGet, "/missing", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		var body APIError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, NOT_FOUND, body.Code)
		assert.Equal(t, "The requested route is not available.", body.Message)
		assert.Equal(t, "invalid_request", body.ReasonCode)
		assert.Equal(t, "correct_and_resubmit", body.NextAction)
		assert.Equal(t, "Use a supported route and HTTP method, then submit the request again.", body.Remediation)
		assert.False(t, body.Retryable)
	})

	t.Run("preserves capability conflict guidance", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/teams", nil)
		req = req.WithContext(correlation.WithID(req.Context(), "http-conflict-correlation"))
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		ErrorHandler(WithGuidance(
			New(CONFLICT, "team name already exists"),
			"team_name_conflict", "correct_and_resubmit", "Choose a different team name and submit again.", false, nil, "",
		), c)

		var body APIError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "team_name_conflict", body.ReasonCode)
		assert.Equal(t, "correct_and_resubmit", body.NextAction)
		assert.Equal(t, "Choose a different team name and submit again.", body.Remediation)
		assert.Equal(t, "http-conflict-correlation", body.CorrelationID)
		assert.False(t, body.Retryable)
	})

	t.Run("handles generic error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		ErrorHandler(assert.AnError, c)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Contains(t, rec.Body.String(), `"code":"INTERNAL_ERROR"`)
		assert.Contains(t, rec.Body.String(), `"message":"internal server error"`)
		assert.NotContains(t, rec.Body.String(), assert.AnError.Error())
	})

	t.Run("bounds public details and fixes server messages", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		long := strings.Repeat("x", maxPublicDetailMessageRunes+100)
		details := make([]ErrorDetail, maxPublicErrorDetails+5)
		for i := range details {
			details[i] = ErrorDetail{Field: strings.Repeat("f", maxPublicErrorFieldRunes+10), Message: long}
		}
		ErrorHandler(NewWithDetails(VALIDATION_ERROR, long, details), c)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		var body struct {
			Code    ErrorCode     `json:"code"`
			Message string        `json:"message"`
			Details []ErrorDetail `json:"details"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, VALIDATION_ERROR, body.Code)
		assert.LessOrEqual(t, utf8.RuneCountInString(body.Message), maxPublicErrorMessageRunes)
		assert.Len(t, body.Details, maxPublicErrorDetails)
		assert.LessOrEqual(t, utf8.RuneCountInString(body.Details[0].Field), maxPublicErrorFieldRunes)
		assert.LessOrEqual(t, utf8.RuneCountInString(body.Details[0].Message), maxPublicDetailMessageRunes)

		rec = httptest.NewRecorder()
		c = e.NewContext(req, rec)
		ErrorHandler(echo.NewHTTPError(http.StatusBadGateway, long), c)
		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Contains(t, rec.Body.String(), `"message":"upstream service error"`)
		assert.NotContains(t, rec.Body.String(), long)

		rec = httptest.NewRecorder()
		c = e.NewContext(req, rec)
		ErrorHandler(NewWithDetails(VALIDATION_ERROR, "validation failed", []ErrorDetail{{Message: "value is invalid"}}), c)
		var emptyFieldBody struct {
			Details []ErrorDetail `json:"details"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &emptyFieldBody))
		require.Len(t, emptyFieldBody.Details, 1)
		assert.Empty(t, emptyFieldBody.Details[0].Field)

		rec = httptest.NewRecorder()
		c = e.NewContext(req, rec)
		ErrorHandler(echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body exceeds limit"), c)
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		var oversizedBody APIError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &oversizedBody))
		assert.Equal(t, VALIDATION_ERROR, oversizedBody.Code)
		assert.Equal(t, "correct_and_resubmit", oversizedBody.NextAction)
		assert.False(t, oversizedBody.Retryable)

		for _, status := range []int{http.StatusMethodNotAllowed, http.StatusUnsupportedMediaType} {
			rec = httptest.NewRecorder()
			c = e.NewContext(req, rec)
			ErrorHandler(echo.NewHTTPError(status, http.StatusText(status)), c)
			var clientError APIError
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &clientError))
			assert.Equal(t, status, rec.Code)
			assert.Equal(t, VALIDATION_ERROR, clientError.Code)
			assert.Equal(t, "correct_and_resubmit", clientError.NextAction)
			assert.False(t, clientError.Retryable)
		}
	})

	t.Run("does not write committed response", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Response().Committed = true

		ErrorHandler(New(NOT_FOUND, "resource not found"), c)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Body.String())
	})
}

func TestEchoHTTPErrorToAPIErrorBranches(t *testing.T) {
	cases := []struct {
		status int
		code   ErrorCode
	}{
		{http.StatusBadRequest, VALIDATION_ERROR},
		{http.StatusRequestEntityTooLarge, VALIDATION_ERROR},
		{http.StatusMethodNotAllowed, VALIDATION_ERROR},
		{http.StatusUnsupportedMediaType, VALIDATION_ERROR},
		{http.StatusUnauthorized, AUTH_INVALID},
		{http.StatusForbidden, FORBIDDEN},
		{http.StatusNotFound, NOT_FOUND},
		{http.StatusConflict, CONFLICT},
		{http.StatusTooManyRequests, RATE_LIMITED},
		{http.StatusServiceUnavailable, SERVICE_UNAVAILABLE},
		{http.StatusInternalServerError, INTERNAL_ERROR},
		{http.StatusTeapot, INTERNAL_ERROR},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			got := echoHTTPErrorToAPIError(echo.NewHTTPError(tc.status, errors.New("wrapped")))
			assert.Equal(t, tc.code, got.Code)
			assert.Contains(t, got.Message, "wrapped")
		})
	}
}

// TestKnowledgeErrorCodes verifies the stable external domain error codes introduced
// for the knowledge pipeline (AC-X6).  Every code must:
//   - Serialise to its exact lowercase string (stable public API contract)
//   - Map to the correct HTTP status via HTTPStatusCode
func TestKnowledgeErrorCodes(t *testing.T) {
	tests := []struct {
		code           ErrorCode
		expectedString string
		expectedHTTP   int
	}{
		// Fragment / lookup errors
		{ErrSupportingFragmentMissing, "supporting_fragment_missing", http.StatusNotFound},
		{ErrClaimNotFound, "claim_not_found", http.StatusNotFound},
		{ErrFactNotFound, "fact_not_found", http.StatusNotFound},

		// Verifier back-pressure
		{ErrVerifierRateLimit, "verifier_rate_limit", http.StatusTooManyRequests},
		{ErrVerifierTimeout, "verifier_timeout", http.StatusGatewayTimeout},
		{ErrVerifierProvider, "verifier_provider", http.StatusServiceUnavailable},
		{ErrVerifierMalformedResponse, "verifier_malformed_response", http.StatusBadGateway},
		{ErrEmbeddingUnavailable, "embedding_unavailable", http.StatusServiceUnavailable},
		{ErrEmbeddingResponseInvalid, "embedding_response_invalid", http.StatusBadGateway},
		{ErrEmbeddingTimeout, "embedding_timeout", http.StatusGatewayTimeout},

		// Policy / predicate violations
		{ErrPredicateNotPoliced, "predicate_not_policed", http.StatusUnprocessableEntity},
		{ErrUnsupportedPolicy, "unsupported_policy", http.StatusUnprocessableEntity},
		{ErrCommunityGraphTooLarge, "community_graph_too_large", http.StatusUnprocessableEntity},

		// State-machine conflicts
		{ErrNeedsClaimValidated, "needs_claim_validated", http.StatusConflict},
		{ErrGateRejected, "gate_rejected", http.StatusConflict},
		{ErrComparableDisputed, "comparable_disputed", http.StatusConflict},
		{ErrRejectedWeaker, "rejected_weaker", http.StatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.expectedString, func(t *testing.T) {
			// Verify the constant value equals the expected string (stable contract)
			require.Equal(t, tt.expectedString, string(tt.code),
				"error code string must be stable lowercase domain token")

			// Verify HTTPStatusCode mapping
			require.Equal(t, tt.expectedHTTP, HTTPStatusCode(tt.code),
				"HTTP status mapping must match plan spec")

			// Verify the code round-trips through APIError / JSON
			apiErr := New(tt.code, "test")
			data, err := json.Marshal(NewErrorEnvelope(apiErr))
			require.NoError(t, err)
			require.Contains(t, string(data), tt.expectedString,
				"code must appear verbatim in JSON envelope")
		})
	}
}

func TestAPIErrorProvider(t *testing.T) {
	// Verify APIError implements APIErrorProvider
	var _ APIErrorProvider = (*APIError)(nil)

	apiErr := New(FORBIDDEN, "access denied")

	// Test interface methods
	assert.Equal(t, FORBIDDEN, apiErr.GetCode())
	assert.Equal(t, "access denied", apiErr.GetMessage())
	assert.Nil(t, apiErr.GetDetails())

	// Test with details
	details := []ErrorDetail{{Field: "id", Message: "invalid"}}
	apiErrWithDetails := NewWithDetails(VALIDATION_ERROR, "bad input", details)
	assert.Equal(t, details, apiErrWithDetails.GetDetails())
}

func TestAPIErrorGuidanceIsBoundedAndPreservedByHTTPProjection(t *testing.T) {
	retryAfter := 30
	apiErr := WithGuidance(nil, strings.Repeat("r", maxPublicErrorFieldRunes+20), "retry_same_request", strings.Repeat("m", maxPublicDetailMessageRunes+20), true, &retryAfter, strings.Repeat("c", maxPublicErrorFieldRunes+20))
	require.Equal(t, INTERNAL_ERROR, apiErr.Code)
	require.True(t, apiErr.Retryable)
	require.Equal(t, retryAfter, *apiErr.RetryAfterSeconds)
	require.LessOrEqual(t, utf8.RuneCountInString(apiErr.ReasonCode), maxPublicErrorFieldRunes)
	require.LessOrEqual(t, utf8.RuneCountInString(apiErr.Remediation), maxPublicDetailMessageRunes)
	require.LessOrEqual(t, utf8.RuneCountInString(apiErr.CorrelationID), maxPublicErrorFieldRunes)

	invalidRetry := -1
	invalidErr := WithGuidance(New(VALIDATION_ERROR, "invalid retry"), "reason", "action", "remediation", false, &invalidRetry, "correlation")
	require.Nil(t, invalidErr.RetryAfterSeconds)

	projected := invalidErr.bounded(http.StatusBadRequest)
	require.Equal(t, "reason", projected.ReasonCode)
	require.Equal(t, "action", projected.NextAction)
	require.Equal(t, "remediation", projected.Remediation)
	require.Equal(t, "correlation", projected.CorrelationID)
	emptyRemediation := WithGuidance(New(VALIDATION_ERROR, "invalid"), "reason", "action", "", false, nil, "correlation")
	require.Empty(t, emptyRemediation.Remediation)
	require.Empty(t, emptyRemediation.bounded(http.StatusBadRequest).Remediation)
	encoded, err := json.Marshal(emptyRemediation)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"retryable":false`)
}
