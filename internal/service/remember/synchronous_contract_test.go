package remember

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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

func TestValidateTerminalRememberResultRejectsUnclosedErrorProjection(t *testing.T) {
	result := &TerminalRememberResult{
		ContractVersion: "dense-mem.v2.6",
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
		RelationshipResults: []SubmissionRelationshipResult{},
	}
	require.ErrorContains(t, ValidateTerminalRememberResult(result, 0, nil), "terminal error code")
}

func TestTerminalResultWithErrorForcesTerminalFailureKind(t *testing.T) {
	result := &TerminalRememberResult{Kind: ResultKindLegacyReceipt, ProcessingState: "queued"}
	failure := TerminalResultWithError(result, TerminalErrorEmbeddingResponseInvalid)
	require.NotNil(t, failure)
	require.Equal(t, ResultKindTerminal, failure.Result.Kind)
	require.Equal(t, string(TerminalProcessingFailed), failure.Result.ProcessingState)
	require.Equal(t, string(TerminalSearchNotRequired), failure.Result.SearchState)
	require.Len(t, failure.Result.Errors, 1)
	require.NoError(t, ValidateTerminalStatusError(failure.Result.Errors[0]))
}

func TestRememberProcessErrorDoesNotExposeOperationalCause(t *testing.T) {
	cause := errors.New("database password and provider payload")
	failure := &RememberProcessError{Err: cause}
	require.Equal(t, "remember: processor failed", failure.Error())
	require.ErrorIs(t, failure, cause)
}

func TestTerminalNextActionsAreClosedAndCopied(t *testing.T) {
	actions := TerminalNextActions()
	require.Equal(t, []string{
		string(TerminalNextActionRetrySameRequest),
		string(TerminalNextActionResubmitRemember),
		string(TerminalNextActionRetryCorrection),
		string(TerminalNextActionContactOperator),
		string(TerminalNextActionNone),
	}, actions)
	require.True(t, IsTerminalNextAction(TerminalNextActionNone))
	require.False(t, IsTerminalNextAction("unknown"))
	actions[0] = "mutated"
	require.Equal(t, string(TerminalNextActionRetrySameRequest), TerminalNextActions()[0])
}

func validTerminalResultForTest() *TerminalRememberResult {
	return &TerminalRememberResult{
		ContractVersion: "dense-mem.v2.6", SubmissionID: "11111111-1111-1111-1111-111111111111", SubmissionKind: "remember",
		ProcessingState: string(TerminalProcessingCompleted), SearchState: string(TerminalSearchCurrent),
		CorrelationID: "correlation", Kind: ResultKindTerminal,
		Evidence: []TerminalEvidenceResult{
			{Disposition: "stored", EvidenceID: "11111111-1111-1111-1111-111111111111", EvidenceIndex: 0, SearchState: string(TerminalSearchCurrent)},
			{Disposition: "not_stored", EvidenceIndex: 1, SearchState: string(TerminalSearchNotRequired), Reason: "not_supported_by_evidence"},
		},
		RelationshipResults: []SubmissionRelationshipResult{
			{RelationshipRef: "rel-a", Disposition: "stored", Splits: []SubmissionRelationshipSplit{{
				SplitIndex: 0, RelationshipID: "11111111-1111-1111-1111-111111111111", RelationshipVersion: 1, Status: "active",
			}}},
			{RelationshipRef: "rel-b", Disposition: "not_stored", Reason: "not_supported_by_evidence"},
		},
		Errors: []SubmissionStatusError{},
	}
}

func TestValidateTerminalRememberResultRejectsMalformedOutput(t *testing.T) {
	tests := []struct {
		name string
		edit func(*TerminalRememberResult)
	}{
		{"missing result", nil},
		{"non-terminal kind", func(result *TerminalRememberResult) { result.Kind = ResultKindLegacyReceipt }},
		{"contract version", func(result *TerminalRememberResult) { result.ContractVersion = "dense-mem.v2.future" }},
		{"submission kind", func(result *TerminalRememberResult) { result.SubmissionKind = "relationship_correction" }},
		{"missing identity", func(result *TerminalRememberResult) { result.CorrelationID = "" }},
		{"submission uuid", func(result *TerminalRememberResult) { result.SubmissionID = "not-a-uuid" }},
		{"invalid processing state", func(result *TerminalRememberResult) { result.ProcessingState = "processing" }},
		{"invalid search state", func(result *TerminalRememberResult) { result.SearchState = "queued" }},
		{"missing errors array", func(result *TerminalRememberResult) { result.Errors = nil }},
		{"completed errors", func(result *TerminalRememberResult) {
			result.Errors = []SubmissionStatusError{TerminalStatusError(TerminalErrorInternalFailure)}
		}},
		{"completed without stored result", func(result *TerminalRememberResult) {
			for index := range result.Evidence {
				result.Evidence[index].Disposition = "not_stored"
				result.Evidence[index].EvidenceID = ""
			}
			for index := range result.RelationshipResults {
				result.RelationshipResults[index].Disposition = "not_stored"
			}
		}},
		{"rejected without error", func(result *TerminalRememberResult) {
			result.ProcessingState = string(TerminalProcessingRejected)
		}},
		{"failed without error", func(result *TerminalRememberResult) {
			result.ProcessingState = string(TerminalProcessingFailed)
		}},
		{"rejected with stored result", func(result *TerminalRememberResult) {
			result.ProcessingState = string(TerminalProcessingRejected)
			result.Errors = []SubmissionStatusError{TerminalStatusError(TerminalErrorNoSupportedMemory)}
		}},
		{"stored relationship without split", func(result *TerminalRememberResult) {
			result.RelationshipResults[0].Splits = nil
		}},
		{"stored relationship with malformed split", func(result *TerminalRememberResult) {
			result.RelationshipResults[0].Splits[0].RelationshipID = "not-a-uuid"
		}},
		{"stored relationship with non-contiguous split", func(result *TerminalRememberResult) {
			result.RelationshipResults[0].Splits[0].SplitIndex = 1
		}},
		{"stored relationship with inactive split", func(result *TerminalRememberResult) {
			result.RelationshipResults[0].Splits[0].Status = "superseded"
		}},
		{"evidence count", func(result *TerminalRememberResult) { result.Evidence = result.Evidence[:1] }},
		{"evidence order", func(result *TerminalRememberResult) { result.Evidence[0].EvidenceIndex = 1 }},
		{"evidence disposition", func(result *TerminalRememberResult) { result.Evidence[0].Disposition = "unknown" }},
		{"stored evidence id", func(result *TerminalRememberResult) { result.Evidence[0].EvidenceID = "" }},
		{"stored evidence uuid", func(result *TerminalRememberResult) { result.Evidence[0].EvidenceID = "not-a-uuid" }},
		{"stored evidence reason", func(result *TerminalRememberResult) { result.Evidence[0].Reason = "provider_secret_detail" }},
		{"non-stored evidence id", func(result *TerminalRememberResult) { result.Evidence[1].EvidenceID = "unexpected" }},
		{"non-stored evidence reason", func(result *TerminalRememberResult) { result.Evidence[1].Reason = "provider_secret_detail" }},
		{"superseded evidence uuid", func(result *TerminalRememberResult) {
			result.Evidence[0].SupersededEvidenceIDs = []string{"not-a-uuid"}
		}},
		{"superseded evidence duplicate", func(result *TerminalRememberResult) {
			result.Evidence[0].SupersededEvidenceIDs = []string{"22222222-2222-2222-2222-222222222222", "22222222-2222-2222-2222-222222222222"}
		}},
		{"evidence search state", func(result *TerminalRememberResult) { result.Evidence[0].SearchState = "queued" }},
		{"relationship count", func(result *TerminalRememberResult) { result.RelationshipResults = result.RelationshipResults[:1] }},
		{"relationship order", func(result *TerminalRememberResult) { result.RelationshipResults[1].RelationshipRef = "rel-c" }},
		{"relationship duplicate", func(result *TerminalRememberResult) { result.RelationshipResults[1].RelationshipRef = "rel-a" }},
		{"relationship disposition", func(result *TerminalRememberResult) { result.RelationshipResults[0].Disposition = "unknown" }},
		{"non-stored relationship splits", func(result *TerminalRememberResult) {
			result.RelationshipResults[1].Splits = []SubmissionRelationshipSplit{{SplitIndex: 0}}
		}},
		{"non-stored relationship reason", func(result *TerminalRememberResult) {
			result.RelationshipResults[1].Reason = "provider_secret_detail"
		}},
		{"terminal error state", func(result *TerminalRememberResult) {
			result.ProcessingState = string(TerminalProcessingRejected)
			result.Evidence[0].Disposition = "not_stored"
			result.Evidence[0].EvidenceID = ""
			result.RelationshipResults[0].Disposition = "not_stored"
			result.RelationshipResults[0].Splits = nil
			result.RelationshipResults[0].Reason = "not_supported_by_evidence"
			result.Errors = []SubmissionStatusError{TerminalStatusError(TerminalErrorProviderUnavailable)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.edit == nil {
				require.Error(t, ValidateTerminalRememberResult(nil, 0, nil))
				return
			}
			result := validTerminalResultForTest()
			test.edit(result)
			require.Error(t, ValidateTerminalRememberResult(result, 2, []string{"rel-a", "rel-b"}))
		})
	}
	require.NoError(t, ValidateTerminalRememberResult(validTerminalResultForTest(), 2, []string{"rel-a", "rel-b"}))
}

func TestValidateTerminalRememberResultAcceptsClosedNotStoredRelationshipReasons(t *testing.T) {
	for _, reason := range []string{
		"not_supported_by_evidence", "stale_input", "security_quarantine", "internal_failure",
	} {
		result := validTerminalResultForTest()
		result.Evidence[1].Reason = reason
		result.RelationshipResults[1].Reason = reason
		require.NoError(t, ValidateTerminalRememberResult(result, 2, []string{"rel-a", "rel-b"}), reason)
	}
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
		{TerminalErrorNoSupportedMemory, TerminalProcessingRejected},
		{TerminalErrorStaleInput, TerminalProcessingRejected},
		{TerminalErrorQuarantined, TerminalProcessingQuarantined},
		{TerminalErrorProviderUnavailable, TerminalProcessingFailed},
	} {
		result := &TerminalRememberResult{
			ContractVersion: "dense-mem.v2.6", SubmissionID: "11111111-1111-1111-1111-111111111111",
			SubmissionKind: "remember", CorrelationID: "correlation", SearchState: string(TerminalSearchNotRequired),
		}
		failure := TerminalResultWithError(result, test.code)
		require.Equal(t, ResultKindTerminal, failure.Result.Kind)
		require.Equal(t, string(test.state), failure.Result.ProcessingState)
		require.NoError(t, ValidateTerminalRememberResult(failure.Result, 0, nil), test.code)
	}
}
