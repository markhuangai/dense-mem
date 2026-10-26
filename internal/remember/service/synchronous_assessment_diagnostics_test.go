package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

func TestSynchronousAssessmentDiagnosticsRetainRepairAndFailure(t *testing.T) {
	for _, invalidTurns := range []int{0, 1, 3} {
		t.Run(string(rune('0'+invalidTurns)), func(t *testing.T) {
			fixture := synchronousAssessmentFixture(t)
			fixture.provider.response = func(req assessor.SemanticAssessmentRequest, turn int) assessor.SemanticAssessmentResponse {
				response := validSynchronousAssessmentResponse(req)
				if turn <= invalidTurns {
					key := "not-in-the-allowlist"
					response.RelationshipResults[0].Splits[0].PredicateKey = &key
				}
				return response
			}
			result, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
			var diagnostics map[string]any
			if invalidTurns == 3 {
				require.ErrorIs(t, err, ErrRememberProviderResponseInvalid)
				require.Nil(t, result)
				diagnostics = SynchronousAssessmentDiagnostics(err)
				require.Len(t, SynchronousAssessmentValidationDiagnostics(err)["turns"], 3)
			} else {
				require.NoError(t, err)
				diagnostics = result.Diagnostics
				commit, buildErr := BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{Assessment: result})
				require.NoError(t, buildErr)
				require.Equal(t, diagnostics, commit.AssessorDiagnostics)
			}
			require.NotNil(t, diagnostics["preparation_ms"])
			require.NotNil(t, diagnostics["elapsed_ms"])
			require.NotNil(t, diagnostics["remaining_budget_ms"])
			turns := diagnostics["turns"].([]map[string]any)
			require.Len(t, turns, min(invalidTurns+1, 3))
			for i, turn := range turns {
				require.Equal(t, i+1, turn["attempt"])
				require.NotNil(t, turn["elapsed_ms"])
				require.NotNil(t, turn["validation_ms"])
				if i < invalidTurns {
					require.Contains(t, turn["issues"], map[string]any{"field": "relationship_results[].splits[].predicate_key", "code": "predicate_outside_allowlist"})
				}
			}
		})
	}
}

func TestSynchronousAssessmentDiagnosticsPreserveIncompleteProviderTurn(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	zero := int64(0)
	fixture.provider.err = &modelprovider.RateLimitError{Provider: "test", Message: "provider-secret", RetryAfter: 3}
	fixture.provider.errorTurn.Diagnostics = &modelprovider.CallDiagnostics{QueueMS: &zero, HTTPMS: &zero, FailurePhase: "response_status"}
	result, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrRememberProviderUnavailable)
	require.Zero(t, SynchronousAssessmentProviderTurns(err))
	diagnostics := SynchronousAssessmentDiagnostics(err)
	turns := diagnostics["turns"].([]map[string]any)
	require.Len(t, turns, 1)
	require.Equal(t, "rate_limited", turns[0]["failure_class"])
	require.NotContains(t, turns[0], "validation_ms")
	encoded, marshalErr := json.Marshal(diagnostics)
	require.NoError(t, marshalErr)
	require.Contains(t, string(encoded), `"queue_ms":0`)
	require.NotContains(t, string(encoded), "provider-secret")
}

func TestSynchronousAssessmentDiagnosticsBeforeProviderAndBounds(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.deps.Provider = nil
	_, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.Error(t, err)
	diagnostics := SynchronousAssessmentDiagnostics(err)
	require.Empty(t, diagnostics["turns"])
	require.NotNil(t, diagnostics["preparation_ms"])
	require.Nil(t, SynchronousAssessmentDiagnostics(nil))

	d := &assessmentDiagnostics{started: time.Now()}
	for range 5 {
		d.beginTurn(context.Background())
		d.finishProviderTurn(assessor.SemanticAssessmentTurn{}, nil)
		errors := make([]assessor.SemanticValidationError, 30)
		for i := range errors {
			errors[i] = assessor.SemanticValidationError{Field: "credential-canary", Message: "provider-secret", Code: "unsafe-canary"}
		}
		d.finishValidation(time.Now(), errors)
	}
	encoded, marshalErr := json.Marshal(d.snapshot())
	require.NoError(t, marshalErr)
	require.Len(t, d.turns, 3)
	require.NotContains(t, string(encoded), "canary")
	require.NotContains(t, string(encoded), "provider-secret")
	require.Contains(t, string(encoded), `"code":"other"`)
	require.Contains(t, string(encoded), `"truncated":true`)
}
