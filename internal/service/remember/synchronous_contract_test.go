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
		require.ErrorContains(t, ValidateTerminalRememberResult(result, 2, []string{"rel-a", "rel-b"}), "exceeds limit")
	})

	t.Run("rejects duplicate codes", func(t *testing.T) {
		result := terminalFailureResultForTest(
			string(TerminalProcessingFailed),
			TerminalErrorProviderUnavailable,
			"internal_failure",
		)
		result.Errors = append(result.Errors, TerminalStatusError(TerminalErrorProviderUnavailable))
		require.ErrorContains(t, ValidateTerminalRememberResult(result, 2, []string{"rel-a", "rel-b"}), "duplicates code")
	})
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
		string(TerminalNextActionRetryDreamFeedback),
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
			{Disposition: "stored", EvidenceID: "11111111-1111-1111-1111-111111111111", EvidenceIndex: 0, SupersededEvidenceIDs: []string{}, SearchState: string(TerminalSearchCurrent)},
			{Disposition: "not_stored", EvidenceIndex: 1, SupersededEvidenceIDs: []string{}, SearchState: string(TerminalSearchNotRequired), Reason: "not_supported_by_evidence"},
		},
		RelationshipResults: []SubmissionRelationshipResult{
			{RelationshipRef: "rel-a", Disposition: "stored", Splits: []SubmissionRelationshipSplit{{
				SplitIndex: 0, RelationshipID: "11111111-1111-1111-1111-111111111111", RelationshipVersion: 1, Status: "active",
			}}},
			{RelationshipRef: "rel-b", Disposition: "not_stored", Splits: []SubmissionRelationshipSplit{}, Reason: "not_supported_by_evidence"},
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
		{"non-canonical correlation id", func(result *TerminalRememberResult) { result.CorrelationID = " correlation " }},
		{"correlation id exceeds code-point bound", func(result *TerminalRememberResult) { result.CorrelationID = strings.Repeat("界", 129) }},
		{"submission uuid", func(result *TerminalRememberResult) { result.SubmissionID = "not-a-uuid" }},
		{"invalid processing state", func(result *TerminalRememberResult) { result.ProcessingState = "processing" }},
		{"invalid search state", func(result *TerminalRememberResult) { result.SearchState = "queued" }},
		{"missing errors array", func(result *TerminalRememberResult) { result.Errors = nil }},
		{"missing evidence array", func(result *TerminalRememberResult) { result.Evidence = nil }},
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
		{"stored relationship reason", func(result *TerminalRememberResult) {
			result.RelationshipResults[0].Reason = "not_supported_by_evidence"
		}},
		{"stored relationship with malformed split", func(result *TerminalRememberResult) {
			result.RelationshipResults[0].Splits[0].RelationshipID = "not-a-uuid"
		}},
		{"stored relationship with non-contiguous split", func(result *TerminalRememberResult) {
			result.RelationshipResults[0].Splits[0].SplitIndex = 1
		}},
		{"stored relationship split count", func(result *TerminalRememberResult) {
			result.RelationshipResults[0].Splits = make([]SubmissionRelationshipSplit, 51)
			for index := range result.RelationshipResults[0].Splits {
				result.RelationshipResults[0].Splits[index] = SubmissionRelationshipSplit{
					SplitIndex: index, RelationshipID: "11111111-1111-1111-1111-111111111111", RelationshipVersion: 1, Status: "active",
				}
			}
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
		{"non-canonical evidence reason", func(result *TerminalRememberResult) { result.Evidence[1].Reason = " stale_input " }},
		{"non-stored evidence supersession", func(result *TerminalRememberResult) {
			result.Evidence[1].SupersededEvidenceIDs = []string{"22222222-2222-2222-2222-222222222222"}
		}},
		{"missing superseded evidence array", func(result *TerminalRememberResult) {
			result.Evidence[0].SupersededEvidenceIDs = nil
		}},
		{"superseded evidence count", func(result *TerminalRememberResult) {
			result.Evidence[0].SupersededEvidenceIDs = make([]string, 51)
		}},
		{"duplicate stored evidence id", func(result *TerminalRememberResult) {
			result.Evidence[1].Disposition = "stored"
			result.Evidence[1].EvidenceID = result.Evidence[0].EvidenceID
			result.Evidence[1].SearchState = string(TerminalSearchCurrent)
			result.Evidence[1].Reason = ""
		}},
		{"duplicate superseded evidence id across items", func(result *TerminalRememberResult) {
			result.Evidence[1].Disposition = "stored"
			result.Evidence[1].EvidenceID = "22222222-2222-2222-2222-222222222222"
			result.Evidence[1].SearchState = string(TerminalSearchCurrent)
			result.Evidence[1].Reason = ""
			result.Evidence[0].SupersededEvidenceIDs = []string{"33333333-3333-3333-3333-333333333333"}
			result.Evidence[1].SupersededEvidenceIDs = []string{"33333333-3333-3333-3333-333333333333"}
		}},
		{"superseded evidence uuid", func(result *TerminalRememberResult) {
			result.Evidence[0].SupersededEvidenceIDs = []string{"not-a-uuid"}
		}},
		{"self superseded evidence uuid", func(result *TerminalRememberResult) {
			result.Evidence[0].SupersededEvidenceIDs = []string{result.Evidence[0].EvidenceID}
		}},
		{"superseded evidence duplicate", func(result *TerminalRememberResult) {
			result.Evidence[0].SupersededEvidenceIDs = []string{"22222222-2222-2222-2222-222222222222", "22222222-2222-2222-2222-222222222222"}
		}},
		{"evidence search state", func(result *TerminalRememberResult) { result.Evidence[0].SearchState = "queued" }},
		{"stored evidence not-required search state", func(result *TerminalRememberResult) {
			result.Evidence[0].SearchState = string(TerminalSearchNotRequired)
		}},
		{"non-stored evidence current search state", func(result *TerminalRememberResult) {
			result.Evidence[1].SearchState = string(TerminalSearchCurrent)
		}},
		{"relationship count", func(result *TerminalRememberResult) { result.RelationshipResults = result.RelationshipResults[:1] }},
		{"relationship order", func(result *TerminalRememberResult) { result.RelationshipResults[1].RelationshipRef = "rel-c" }},
		{"relationship duplicate", func(result *TerminalRememberResult) { result.RelationshipResults[1].RelationshipRef = "rel-a" }},
		{"relationship disposition", func(result *TerminalRememberResult) { result.RelationshipResults[0].Disposition = "unknown" }},
		{"non-stored relationship splits", func(result *TerminalRememberResult) {
			result.RelationshipResults[1].Splits = []SubmissionRelationshipSplit{{SplitIndex: 0}}
		}},
		{"non-stored relationship missing splits array", func(result *TerminalRememberResult) {
			result.RelationshipResults[1].Splits = nil
		}},
		{"non-stored relationship reason", func(result *TerminalRememberResult) {
			result.RelationshipResults[1].Reason = "provider_secret_detail"
		}},
		{"non-canonical relationship reason", func(result *TerminalRememberResult) {
			result.RelationshipResults[1].Reason = " stale_input "
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
		{"rejected current search state", func(result *TerminalRememberResult) {
			result.ProcessingState = string(TerminalProcessingRejected)
			result.SearchState = string(TerminalSearchCurrent)
			result.Evidence[0].Disposition = "not_stored"
			result.Evidence[0].EvidenceID = ""
			result.Evidence[0].SearchState = string(TerminalSearchNotRequired)
			result.RelationshipResults[0].Disposition = "not_stored"
			result.RelationshipResults[0].Splits = nil
			result.RelationshipResults[0].Reason = "not_supported_by_evidence"
			result.Errors = []SubmissionStatusError{TerminalStatusError(TerminalErrorNoSupportedMemory)}
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

func TestValidateTerminalRememberResultRejectsNilZeroRelationshipResults(t *testing.T) {
	result := validTerminalResultForTest()
	result.RelationshipResults = nil
	require.Error(t, ValidateTerminalRememberResult(result, len(result.Evidence), []string{}))

	result.RelationshipResults = []SubmissionRelationshipResult{}
	require.NoError(t, ValidateTerminalRememberResult(result, len(result.Evidence), []string{}))
}

func TestValidateTerminalRememberResultAcceptsContextualNotStoredReasons(t *testing.T) {
	result := validTerminalResultForTest()
	result.Evidence[1].Reason = "not_supported_by_evidence"
	result.RelationshipResults[1].Reason = "not_supported_by_evidence"
	require.NoError(t, ValidateTerminalRememberResult(result, 2, []string{"rel-a", "rel-b"}))

	for _, test := range []struct {
		name   string
		state  string
		code   TerminalErrorCode
		reason string
	}{
		{"stale input", string(TerminalProcessingRejected), TerminalErrorStaleInput, "stale_input"},
		{"quarantine", string(TerminalProcessingQuarantined), TerminalErrorQuarantined, "security_quarantine"},
		{"failure", string(TerminalProcessingFailed), TerminalErrorProviderUnavailable, "internal_failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := terminalFailureResultForTest(test.state, test.code, test.reason)
			require.NoError(t, ValidateTerminalRememberResult(result, 2, []string{"rel-a", "rel-b"}))
		})
	}
}

func TestValidateTerminalRememberResultRejectsMismatchedNotStoredReasons(t *testing.T) {
	tests := []struct {
		name string
		edit func(*TerminalRememberResult)
	}{
		{"completed evidence reason", func(result *TerminalRememberResult) {
			result.Evidence[1].Reason = "security_quarantine"
		}},
		{"completed relationship reason", func(result *TerminalRememberResult) {
			result.RelationshipResults[1].Reason = "internal_failure"
		}},
		{"rejected evidence reason", func(result *TerminalRememberResult) {
			failure := terminalFailureResultForTest(string(TerminalProcessingRejected), TerminalErrorNoSupportedMemory, "stale_input")
			*result = *failure
		}},
		{"failed relationship reason", func(result *TerminalRememberResult) {
			failure := terminalFailureResultForTest(string(TerminalProcessingFailed), TerminalErrorProviderUnavailable, "stale_input")
			*result = *failure
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validTerminalResultForTest()
			test.edit(result)
			require.Error(t, ValidateTerminalRememberResult(result, 2, []string{"rel-a", "rel-b"}))
		})
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
	for index := range result.RelationshipResults {
		result.RelationshipResults[index].Disposition = "not_stored"
		result.RelationshipResults[index].Splits = []SubmissionRelationshipSplit{}
		result.RelationshipResults[index].Reason = reason
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

func TestDreamFeedbackRetryGuidanceUsesClosedAction(t *testing.T) {
	value := TerminalStatusError(TerminalErrorNoSupportedMemory)
	value.NextAction = string(TerminalNextActionRetryDreamFeedback)
	value.Remediation = DreamFeedbackRetryRemediation("dream-feedback-retry")
	require.NoError(t, ValidateTerminalStatusError(value))

	value.Retryable = false
	require.Error(t, ValidateTerminalStatusError(value))
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
	for _, item := range failure.Result.RelationshipResults {
		require.Equal(t, "not_stored", item.Disposition)
		require.Empty(t, item.Splits)
		require.Equal(t, "internal_failure", item.Reason)
	}
	require.NoError(t, ValidateTerminalRememberResult(failure.Result, 2, []string{"rel-a", "rel-b"}))
}
