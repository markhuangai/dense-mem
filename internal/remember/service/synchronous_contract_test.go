package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestTerminalRememberReplayRetainsSupportedContractVersions(t *testing.T) {
	for _, version := range []string{"dense-mem.v2.6.8", "dense-mem.v2.6.7", "dense-mem.v2.6.6", "dense-mem.v2.6.3", "dense-mem.v2.6.2"} {
		result := validTerminalResultForTest()
		result.ContractVersion = version
		require.NoError(t, ValidateTerminalRememberResult(result, 2))
		require.Equal(t, version, result.ContractVersion)
	}
}

func TestTerminalStatusErrorUsesOnlyClosedVocabulary(t *testing.T) {
	for _, raw := range TerminalErrorCodes() {
		status := TerminalStatusError(TerminalErrorCode(raw))
		require.NoError(t, ValidateTerminalStatusError(status), raw)
	}

	unknown := TerminalStatusError("provider-secret-detail")
	require.Equal(t, string(TerminalErrorInternalFailure), unknown.Code)
	require.NotContains(t, unknown.Message, "provider-secret-detail")
	require.NoError(t, ValidateTerminalStatusError(unknown))
}

func TestTerminalStatusErrorWithDetailsBoundsActionableContext(t *testing.T) {
	error := TerminalStatusErrorWithDetails(
		TerminalErrorInputBudgetExceeded,
		" assessor_context_overflow ",
		map[string]any{"component": "assessor.conversation", "observed": 120, "limit": 100, "server_owned": true},
	)
	require.Equal(t, "assessor_context_overflow", error.ReasonCode)
	require.Equal(t, 120, error.Details["observed"])
	require.Equal(t, TerminalNextActionContactOperator, TerminalNextAction(error.NextAction))
	require.Equal(t, serverOwnedInputBudgetRemediation, error.Remediation)
	require.NoError(t, ValidateTerminalStatusError(error))
}

func TestValidateTerminalStatusErrorRejectsInconsistentProjection(t *testing.T) {
	canonical := TerminalStatusErrorWithDetails(
		TerminalErrorProviderUnavailable,
		"provider_unavailable",
		map[string]any{"component": "assessor", "server_owned": true},
	)
	cases := []struct {
		name   string
		mutate func(*SubmissionStatusError)
	}{
		{"unknown code", func(value *SubmissionStatusError) { value.Code = "unknown" }},
		{"unknown action", func(value *SubmissionStatusError) { value.NextAction = "unknown" }},
		{"retryability", func(value *SubmissionStatusError) { value.Retryable = !value.Retryable }},
		{"message", func(value *SubmissionStatusError) { value.Message = "unbounded" }},
		{"remediation", func(value *SubmissionStatusError) { value.Remediation = "unbounded" }},
		{"reason without details", func(value *SubmissionStatusError) { value.Details = nil }},
		{"unbounded details", func(value *SubmissionStatusError) {
			value.Details = map[string]any{"secret": strings.Repeat("x", 2000)}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value := canonical
			testCase.mutate(&value)
			require.Error(t, ValidateTerminalStatusError(value))
		})
	}

	budget := TerminalStatusErrorWithDetails(
		TerminalErrorInputBudgetExceeded,
		"budget",
		map[string]any{"component": "assessor", "observed": 20, "limit": 10, "server_owned": true},
	)
	require.NoError(t, ValidateTerminalStatusError(budget))
	budget.Remediation = "client remediation"
	require.Error(t, ValidateTerminalStatusError(budget))
}

func TestValidateTerminalRememberResultRejectsUnclosedErrorProjection(t *testing.T) {
	result := &TerminalRememberResult{
		ContractVersion: domain.ContractVersion,
		SubmissionID:    "11111111-1111-1111-1111-111111111111",
		SubmissionKind:  "remember",
		CorrelationID:   "correlation",
		Kind:            ResultKindTerminal,
		ProcessingState: string(TerminalProcessingFailed),
		SearchState:     string(TerminalSearchNotRequired),
		Evidence:        []TerminalEvidenceResult{},
		Errors: []SubmissionStatusError{{
			Code:        "provider-secret-detail",
			Message:     "bounded",
			NextAction:  string(TerminalNextActionContactOperator),
			Remediation: "bounded",
		}},
	}
	require.ErrorContains(t, ValidateTerminalRememberResult(result, 0), "terminal error code")
}

func TestValidateTerminalRememberResultBoundsAndDeduplicatesErrors(t *testing.T) {
	t.Run("bounds errors", func(t *testing.T) {
		result := terminalFailureResultForTest(
			string(TerminalProcessingFailed),
			TerminalErrorProviderUnavailable,
			"internal_failure",
		)
		result.Errors = make([]SubmissionStatusError, maxTerminalErrors+1)
		for index := range result.Errors {
			result.Errors[index] = TerminalStatusError(TerminalErrorProviderUnavailable)
		}
		require.ErrorContains(t, ValidateTerminalRememberResult(result, 2), "exceeds limit")
	})

	t.Run("rejects duplicate codes", func(t *testing.T) {
		result := terminalFailureResultForTest(
			string(TerminalProcessingFailed),
			TerminalErrorProviderUnavailable,
			"internal_failure",
		)
		result.Errors = append(result.Errors, TerminalStatusError(TerminalErrorProviderUnavailable))
		require.ErrorContains(t, ValidateTerminalRememberResult(result, 2), "duplicates code")
	})
}

func TestTerminalResultWithErrorForcesTerminalFailureKind(t *testing.T) {
	result := &TerminalRememberResult{ProcessingState: "completed"}
	failure := TerminalResultWithError(result, TerminalErrorEmbeddingResponseInvalid)
	require.NotNil(t, failure)
	require.Equal(t, ResultKindTerminal, failure.Result.Kind)
	require.Equal(t, string(TerminalProcessingFailed), failure.Result.ProcessingState)
	require.Equal(t, string(TerminalSearchNotRequired), failure.Result.SearchState)
	require.Len(t, failure.Result.Errors, 1)
	require.NoError(t, ValidateTerminalStatusError(failure.Result.Errors[0]))
}

func TestTerminalResultWithErrorCreatesNilResultAndNilUnwrapIsSafe(t *testing.T) {
	failure := TerminalResultWithError(nil, TerminalErrorProviderUnavailable)
	require.NotNil(t, failure)
	require.NotNil(t, failure.Result)
	var nilFailure *RememberProcessError
	require.Nil(t, nilFailure.Unwrap())
}

func TestRememberProcessErrorDoesNotExposeOperationalCause(t *testing.T) {
	cause := errors.New("database password and provider payload")
	failure := &RememberProcessError{Err: cause}
	require.Equal(t, "remember: processor failed", failure.Error())
	require.ErrorIs(t, failure, cause)
}

func TestDiagnosticCaptureProjectsAndCopiesCallerResponse(t *testing.T) {
	capture := NewDiagnosticCapture([]byte(`{"name":"remember","arguments":{}}`))
	require.Equal(t, `{"name":"remember","arguments":{}}`, string(capture.RequestBody()))
	requestCopy := capture.RequestBody()
	requestCopy[0] = '['
	require.Equal(t, `{"name":"remember","arguments":{}}`, string(capture.RequestBody()))
	capture.SetResponseProjector(func(result map[string]any, isError bool) ([]byte, error) {
		return json.Marshal(map[string]any{"structuredContent": result, "isError": isError})
	})
	projected, err := capture.ProjectResponse(map[string]any{"code": "provider_unavailable"}, true)
	require.NoError(t, err)
	require.Contains(t, string(projected), `"isError":true`)
	require.Equal(t, `{"isError":true,"structuredContent":{"code":"provider_unavailable"}}`, string(projected))
}

func TestDiagnosticCaptureContextAndUnconfiguredResponse(t *testing.T) {
	capture := NewDiagnosticCapture(nil)
	require.Empty(t, capture.RequestBody())
	require.Nil(t, DiagnosticCaptureFromContext(context.Background()))
	require.Same(t, capture, DiagnosticCaptureFromContext(WithDiagnosticCapture(context.Background(), capture)))
	require.Equal(t, context.Background(), WithDiagnosticCapture(context.Background(), nil))
	projected, err := capture.ProjectResponse(map[string]any{"ignored": true}, false)
	require.NoError(t, err)
	require.Nil(t, projected)
}

func TestDiagnosticCaptureNilReceiverAndNilProjectorAreSafe(t *testing.T) {
	var capture *DiagnosticCapture
	require.Nil(t, capture.RequestBody())
	capture.SetResponseProjector(func(map[string]any, bool) ([]byte, error) { return nil, nil })
	projected, err := capture.ProjectResponse(nil, false)
	require.NoError(t, err)
	require.Nil(t, projected)

	unconfigured := NewDiagnosticCapture([]byte("request"))
	unconfigured.SetResponseProjector(nil)
	projected, err = unconfigured.ProjectResponse(nil, false)
	require.NoError(t, err)
	require.Nil(t, projected)
}

func TestTerminalNextActionsAreClosedAndCopied(t *testing.T) {
	actions := TerminalNextActions()
	require.Equal(t, []string{
		string(TerminalNextActionRetrySameRequest),
		string(TerminalNextActionResubmitRemember),
		string(TerminalNextActionContactOperator),
		string(TerminalNextActionNone),
	}, actions)
	require.True(t, IsTerminalNextAction(TerminalNextActionNone))
	require.False(t, IsTerminalNextAction("unknown"))
	actions[0] = "mutated"
	require.Equal(t, string(TerminalNextActionRetrySameRequest), TerminalNextActions()[0])
}

func validTerminalResultForTest() *TerminalRememberResult {
	return &TerminalRememberResult{ContractVersion: domain.ContractVersion, SubmissionID: "11111111-1111-1111-1111-111111111111", SubmissionKind: "remember", ProcessingState: "completed", SearchState: "current", CorrelationID: "correlation", Kind: ResultKindTerminal, Evidence: []TerminalEvidenceResult{{Disposition: "stored", EvidenceID: "11111111-1111-1111-1111-111111111111", EvidenceIndex: 0, SupersededEvidenceIDs: []string{}, SearchState: "current"}, {Disposition: "stored", EvidenceID: "22222222-2222-2222-2222-222222222222", EvidenceIndex: 1, SupersededEvidenceIDs: []string{}, SearchState: "current"}}, Errors: []SubmissionStatusError{}}
}

func TestEvidenceOnlyUnitTerminalResultRejectsMalformedOutput(t *testing.T) {
	for _, mutate := range []func(*TerminalRememberResult){
		func(r *TerminalRememberResult) { r.Kind = "future" },
		func(r *TerminalRememberResult) { r.ContractVersion = "dense-mem.v2.7.0" },
		func(r *TerminalRememberResult) { r.SubmissionID = "invalid" },
		func(r *TerminalRememberResult) { r.SubmissionKind = "relationship_correction" },
		func(r *TerminalRememberResult) { r.CorrelationID = "" },
		func(r *TerminalRememberResult) { r.ProcessingState = "pending" },
		func(r *TerminalRememberResult) { r.SearchState = "pending" },
		func(r *TerminalRememberResult) { r.Errors = nil },
		func(r *TerminalRememberResult) { r.Evidence = nil },
		func(r *TerminalRememberResult) { r.Evidence = r.Evidence[:1] },
		func(r *TerminalRememberResult) { r.Evidence[0].EvidenceIndex = 1 },
		func(r *TerminalRememberResult) { r.Evidence[0].EvidenceID = "" },
		func(r *TerminalRememberResult) { r.Evidence[0].EvidenceID = "invalid" },
		func(r *TerminalRememberResult) { r.Evidence[0].Disposition = "unknown" },
		func(r *TerminalRememberResult) { r.Evidence[0].Reason = "provider-secret" },
		func(r *TerminalRememberResult) { r.Evidence[0].SupersededEvidenceIDs = nil },
		func(r *TerminalRememberResult) {
			r.Evidence[0].SupersededEvidenceIDs = []string{r.Evidence[0].EvidenceID}
		},
		func(r *TerminalRememberResult) { r.Evidence[0].SupersededEvidenceIDs = []string{"invalid"} },
		func(r *TerminalRememberResult) { r.Evidence[1].EvidenceID = r.Evidence[0].EvidenceID },
		func(r *TerminalRememberResult) { r.Evidence[0].SearchState = "not_required" },
		func(r *TerminalRememberResult) {
			r.Errors = []SubmissionStatusError{TerminalStatusError(TerminalErrorProviderUnavailable)}
		},
		func(r *TerminalRememberResult) { r.ProcessingState = "failed" },
	} {
		result := validTerminalResultForTest()
		mutate(result)
		require.Error(t, ValidateTerminalRememberResult(result, 2))
	}
	require.Error(t, ValidateTerminalRememberResult(nil, 0))
	require.NoError(t, ValidateTerminalRememberResult(validTerminalResultForTest(), 2))
	for _, code := range []TerminalErrorCode{TerminalErrorPolicyRejected, TerminalErrorStaleInput, TerminalErrorProviderUnavailable, TerminalErrorIdempotencyConflict, TerminalErrorRetiredContract} {
		failure := TerminalResultWithError(validTerminalResultForTest(), code)
		require.NoError(t, ValidateTerminalRememberResult(failure.Result, 2))
		require.Equal(t, "failed", failure.Result.ProcessingState)
		for _, item := range failure.Result.Evidence {
			require.Empty(t, item.EvidenceID)
			require.Equal(t, "not_stored", item.Disposition)
			require.Equal(t, "not_required", item.SearchState)
		}
	}
}

func terminalFailureResultForTest(state string, code TerminalErrorCode, reason string) *TerminalRememberResult {
	result := validTerminalResultForTest()
	result.ProcessingState = state
	result.SearchState = string(TerminalSearchNotRequired)
	for index := range result.Evidence {
		result.Evidence[index].Disposition = "not_stored"
		result.Evidence[index].EvidenceID = ""
		result.Evidence[index].SupersededEvidenceIDs = []string{}
		result.Evidence[index].SearchState = string(TerminalSearchNotRequired)
		result.Evidence[index].Reason = reason
	}
	result.Errors = []SubmissionStatusError{TerminalStatusError(code)}
	return result
}

func TestValidateTerminalStatusErrorRejectsMalformedOutput(t *testing.T) {
	valid := TerminalStatusError(TerminalErrorProviderUnavailable)
	tests := []struct {
		name string
		edit func(*SubmissionStatusError)
	}{
		{"unknown action", func(value *SubmissionStatusError) { value.NextAction = "unknown" }},
		{"inconsistent guidance", func(value *SubmissionStatusError) { value.Retryable = false }},
		{"empty message", func(value *SubmissionStatusError) { value.Message = " " }},
		{"long message", func(value *SubmissionStatusError) { value.Message = strings.Repeat("x", 257) }},
		{"non-canonical message", func(value *SubmissionStatusError) { value.Message = "equivalent bounded message" }},
		{"empty remediation", func(value *SubmissionStatusError) { value.Remediation = " " }},
		{"long remediation", func(value *SubmissionStatusError) { value.Remediation = strings.Repeat("x", 513) }},
		{"non-canonical remediation", func(value *SubmissionStatusError) { value.Remediation = "equivalent bounded remediation" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.edit(&value)
			require.Error(t, ValidateTerminalStatusError(value))
		})
	}
}

func TestTerminalResultWithErrorUsesSemanticProcessingStates(t *testing.T) {
	for _, test := range []struct {
		code  TerminalErrorCode
		state TerminalProcessingState
	}{
		{TerminalErrorPolicyRejected, TerminalProcessingFailed},
		{TerminalErrorStaleInput, TerminalProcessingFailed},
		{TerminalErrorProviderUnavailable, TerminalProcessingFailed},
	} {
		result := &TerminalRememberResult{
			ContractVersion: domain.ContractVersion, SubmissionID: "11111111-1111-1111-1111-111111111111",
			SubmissionKind: "remember", CorrelationID: "correlation", SearchState: string(TerminalSearchNotRequired),
		}
		failure := TerminalResultWithError(result, test.code)
		require.Equal(t, ResultKindTerminal, failure.Result.Kind)
		require.Equal(t, string(test.state), failure.Result.ProcessingState)
		require.NoError(t, ValidateTerminalRememberResult(failure.Result, 0), test.code)
	}
}

func TestTerminalResultWithErrorClearsStaleEffects(t *testing.T) {
	failure := TerminalResultWithError(validTerminalResultForTest(), TerminalErrorProviderUnavailable)
	require.Equal(t, string(TerminalSearchNotRequired), failure.Result.SearchState)
	for _, item := range failure.Result.Evidence {
		require.Equal(t, "not_stored", item.Disposition)
		require.Empty(t, item.EvidenceID)
		require.Empty(t, item.SupersededEvidenceIDs)
		require.Equal(t, string(TerminalSearchNotRequired), item.SearchState)
		require.Equal(t, "internal_failure", item.Reason)
	}
	require.NoError(t, ValidateTerminalRememberResult(failure.Result, 2))
}
