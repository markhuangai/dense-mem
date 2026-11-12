package service

import (
	"errors"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSynchronousAssessmentFailureDetailsClassifiesPreflightStages(t *testing.T) {
	cases := []struct {
		stage     string
		component string
		client    bool
	}{
		{stage: "known_evidence_context", component: "assessor.required_known_evidence"},
		{stage: "catalog_context", component: "assessor.optional_context"},
		{stage: "catalog_context_validation", component: "assessor.optional_context"},
		{stage: "required_context", component: "assessor.required_context"},
		{stage: "assessment_input", component: "assessor.required_input", client: true},
		{stage: "input_tokens", component: "assessor.required_input", client: true},
		{stage: "provider_framing", component: "assessor.provider_framing"},
		{stage: "conversation_input_tokens", component: "assessor.conversation"},
		{stage: "conversation_candidate_context_tokens", component: "assessor.conversation_candidate_context"},
		{stage: "unknown", component: "assessor"},
	}
	for _, test := range cases {
		t.Run(test.stage, func(t *testing.T) {
			err := deterministicSemanticAssessmentPreflightErrorWithMeasurement(test.stage, "bounded failure", assessor.FailureMeasurement{
				Unit: "tokens", Observed: 12, ObservedAtLeast: true, Limit: 10,
			})
			reasonCode, details := SynchronousAssessmentFailureDetails(err)
			require.Equal(t, test.stage, reasonCode)
			require.Equal(t, test.component, details["component"])
			require.Equal(t, test.client, details["client_controlled"] == true)
			require.Equal(t, !test.client, details["server_owned"] == true)
			require.Equal(t, true, details["observed_at_least"])
		})
	}

	empty := &semanticAssessmentPreflightError{err: errors.New("preflight")}
	reasonCode, details := SynchronousAssessmentFailureDetails(empty)
	require.Equal(t, "assessor_preflight_failed", reasonCode)
	require.Equal(t, "assessor", details["component"])
	require.Equal(t, true, details["server_owned"])

	conversation := &assessor.MalformedResponseError{FailureClass: "input_budget", ValidationStage: "conversation_input_tokens"}
	reasonCode, details = SynchronousAssessmentFailureDetails(conversation)
	require.Equal(t, "assessor_conversation_input_exceeded", reasonCode)
	require.Equal(t, "assessor.conversation", details["component"])
	require.NotContains(t, details, "observed")
	_, details = SynchronousAssessmentFailureDetails(&assessor.MalformedResponseError{FailureClass: "provider"})
	require.Nil(t, details)
}

func TestSynchronousAssessmentValidationDiagnosticsAreBoundedAndSafe(t *testing.T) {
	diagnostics := SynchronousAssessmentValidationDiagnostics(&assessor.MalformedResponseError{
		FailureClass:    "malformed_exhausted",
		Attempts:        3,
		ValidationStage: "response_contract",
		ValidationFieldFamilies: []string{
			"evidence_security_results[0].decision",
			"relationship_results[0].provider_secret",
			"unknown-provider-field-with-secret",
		},
	})
	require.Equal(t, "malformed_exhausted", diagnostics["failure_class"])
	turns, ok := diagnostics["turns"].([]any)
	require.True(t, ok)
	require.Len(t, turns, 1)
	turn := turns[0].(map[string]any)
	require.Equal(t, 3, turn["attempt"])
	require.Equal(t, "response_contract", turn["stage"])
	require.NotContains(t, turn["fields"], "provider_secret")
	require.NotContains(t, turn["fields"], "unknown-provider-field-with-secret")
	require.Contains(t, turn["fields"], "evidence_security_results[].decision")
	require.Contains(t, turn["field_families"], "evidence_security_results.semantics")
	require.Equal(t, 3, turn["error_count"])
}

func TestSynchronousAssessmentValidationDiagnosticsAllowBoundaryFields(t *testing.T) {
	fields := []string{
		"evidence_security_results[0].signals[0].start_ref",
		"evidence_security_results[0].signals[0].end_ref",
		"evidence_conflict_results[0].positions[0].start_ref",
		"evidence_conflict_results[0].positions[0].end_ref",
	}
	diagnostics := SynchronousAssessmentValidationDiagnostics(&assessor.MalformedResponseError{
		FailureClass:            "malformed_exhausted",
		Attempts:                1,
		ValidationStage:         "response_contract",
		ValidationFieldFamilies: fields,
	})
	turn := diagnostics["turns"].([]any)[0].(map[string]any)
	require.NotContains(t, turn["fields"], "other")
	for _, field := range fields {
		normalized, ok := normalizeAssessmentValidationIndexes(field)
		require.True(t, ok)
		require.Contains(t, turn["fields"], normalized)
	}
}

func TestSynchronousAssessmentValidationDiagnosticsCapsErrorCount(t *testing.T) {
	diagnostics := SynchronousAssessmentValidationDiagnostics(&submissionAssessmentValidationHistoryError{
		cause: errors.New("validation failed"),
		turns: []submissionAssessmentValidationTurn{{
			Attempt: 1, Stage: "response_contract", Fields: []string{"request_id"}, ErrorCount: 99,
		}},
	})
	turn := diagnostics["turns"].([]any)[0].(map[string]any)
	require.Equal(t, maxAssessorValidationFields, turn["error_count"])
}

func TestSynchronousAssessmentValidationDiagnosticsMarksCappedErrorCountTruncated(t *testing.T) {
	diagnostics := SynchronousAssessmentValidationDiagnostics(&submissionAssessmentValidationHistoryError{
		cause: errors.New("validation failed"),
		turns: []submissionAssessmentValidationTurn{{
			Attempt: 1, Stage: "response_contract", Fields: []string{"request_id"}, ErrorCount: maxAssessorValidationFields + 1,
		}},
	})
	turn := diagnostics["turns"].([]any)[0].(map[string]any)
	require.Equal(t, maxAssessorValidationFields, turn["error_count"])
	require.Equal(t, true, turn["truncated"])
	require.Equal(t, true, diagnostics["truncated"])
}

func TestSynchronousAssessmentValidationDiagnosticsCapsTurnHistory(t *testing.T) {
	turns := make([]submissionAssessmentValidationTurn, SemanticMaxAssessorTurns+1)
	for index := range turns {
		turns[index] = submissionAssessmentValidationTurn{
			Attempt: index + 1, Stage: "response_contract", Fields: []string{"request_id"}, ErrorCount: 1,
		}
	}
	diagnostics := SynchronousAssessmentValidationDiagnostics(&submissionAssessmentValidationHistoryError{
		cause: errors.New("provider failed"), turns: turns,
	})
	projected := diagnostics["turns"].([]any)
	require.Len(t, projected, SemanticMaxAssessorTurns)
	require.Equal(t, true, diagnostics["truncated"])
	require.Equal(t, true, projected[len(projected)-1].(map[string]any)["truncated"])
}

func TestSynchronousAssessmentValidationDiagnosticsPreserveTypedTerminalClass(t *testing.T) {
	turns := []submissionAssessmentValidationTurn{{Attempt: 1, Stage: "response_contract", Fields: []string{"request_id"}, ErrorCount: 1}}
	for _, test := range []struct {
		name  string
		cause error
		want  string
	}{
		{name: "input budget", cause: ErrRememberInputBudgetExceeded, want: "input_budget"},
		{name: "request timeout", cause: ErrRememberRequestTimeout, want: "timeout"},
		{
			name: "malformed exhausted with history",
			cause: errors.Join(ErrRememberProviderResponseInvalid, &assessor.MalformedResponseError{
				FailureClass: "malformed_exhausted", Attempts: SemanticMaxAssessorTurns,
			}),
			want: "malformed_exhausted",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostics := SynchronousAssessmentValidationDiagnostics(&submissionAssessmentValidationHistoryError{
				cause: errors.Join(test.cause, errors.New("typed failure")), turns: turns,
			})
			require.Equal(t, test.want, diagnostics["failure_class"])
		})
	}
}
