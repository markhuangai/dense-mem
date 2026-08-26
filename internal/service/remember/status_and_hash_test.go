package remember

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmissionStatusErrorVocabularyAndGuidance(t *testing.T) {
	assert.Contains(t, SubmissionErrorCodes(), string(SubmissionErrorNoSupportedMemory))
	assert.Contains(t, SubmissionNextActions(), string(SubmissionNextActionRetrySameRequest))

	for _, code := range []SubmissionErrorCode{
		SubmissionErrorProviderUnavailable, SubmissionErrorProviderResponseInvalid,
		SubmissionErrorEmbeddingUnavailable, SubmissionErrorEmbeddingResponseInvalid,
		SubmissionErrorCommitConflict, SubmissionErrorDatabaseFailure,
		SubmissionErrorRequestTimeout, SubmissionErrorRequestCancelled, SubmissionErrorInternalFailure,
	} {
		errorValue := StatusError(code)
		assert.Equal(t, string(code), errorValue.Code)
		assert.True(t, errorValue.Retryable)
		assert.Equal(t, string(SubmissionNextActionRetrySameRequest), errorValue.NextAction)
		assert.NotEmpty(t, errorValue.Remediation)
	}
	for _, code := range []SubmissionErrorCode{SubmissionErrorNoSupportedMemory, SubmissionErrorStaleInput, SubmissionErrorIdempotencyConflict} {
		errorValue := StatusError(code)
		assert.True(t, errorValue.Retryable)
		assert.Equal(t, string(SubmissionNextActionResubmitRemember), errorValue.NextAction)
	}
	assert.False(t, StatusError(SubmissionErrorNoChange).Retryable)
	assert.Equal(t, string(SubmissionNextActionNone), StatusError(SubmissionErrorNoChange).NextAction)
	assert.Equal(t, string(SubmissionNextActionRetryCorrection), StatusError(SubmissionErrorRelationshipChanged).NextAction)
	assert.Equal(t, string(SubmissionNextActionContactOperator), StatusError(SubmissionErrorPolicyRejected).NextAction)
	assert.Equal(t, string(SubmissionErrorInternalFailure), StatusError(SubmissionErrorCode("unknown")).Code)
	assert.Equal(t, "safe override", StatusErrorWithMessage(SubmissionErrorDatabaseFailure, "safe override").Message)
	assert.Equal(t, string(SubmissionErrorNoSupportedMemory), StatusErrorForCode("", "rejected").Code)
	assert.Equal(t, string(SubmissionErrorInternalFailure), StatusErrorForCode("unknown", "failed").Code)
}

func TestFailureCodeMapsAllTerminalPhases(t *testing.T) {
	tests := []struct {
		stage string
		class string
		want  SubmissionErrorCode
	}{
		{stage: "contract_superseded", want: SubmissionErrorInternalFailure},
		{stage: "input_budget", want: SubmissionErrorInputBudgetExceeded},
		{stage: "entity_catalog", want: SubmissionErrorInputBudgetExceeded},
		{stage: "configuration_invalid", want: SubmissionErrorConfigurationInvalid},
		{stage: "database_failure", want: SubmissionErrorDatabaseFailure},
		{stage: "assessment", class: "malformed_exhausted", want: SubmissionErrorProviderResponseInvalid},
		{class: "provider_protocol", want: SubmissionErrorProviderResponseInvalid},
		{class: "timeout", want: SubmissionErrorProviderUnavailable},
		{class: "provider_unavailable", want: SubmissionErrorProviderUnavailable},
		{stage: "other", class: "other", want: SubmissionErrorInternalFailure},
	}
	for _, test := range tests {
		t.Run(test.stage+"/"+test.class, func(t *testing.T) {
			assert.Equal(t, test.want, FailureCode(test.stage, test.class))
		})
	}
}

func TestRememberValidationAndProcessorErrorWrappers(t *testing.T) {
	validation := &RememberValidationError{Issues: []RememberValidationIssue{{Message: "invalid input"}}}
	assert.EqualError(t, validation, "invalid input")
	assert.EqualError(t, (&RememberValidationError{}), "remember validation failed")
	assert.EqualError(t, (*RememberValidationError)(nil), "remember validation failed")

	cause := errors.New("provider failed")
	process := &RememberProcessError{Err: cause}
	assert.Contains(t, process.Error(), "provider failed")
	assert.ErrorIs(t, process, cause)
	assert.EqualError(t, (*RememberProcessError)(nil), "remember: processor failed")
	assert.Nil(t, (*RememberProcessError)(nil).Unwrap())

	assert.EqualError(t, (&RememberValidationError{}), "remember validation failed")
}

func TestCanonicalRememberHashNormalizesContractSets(t *testing.T) {
	evidence := []map[string]any{{"content": "same", "source": " wiki ", "labels": []any{"b", "a"}}}
	entitiesA := []map[string]any{{"ref": "b", "name": "B"}, {"ref": "a", "name": "A"}}
	entitiesB := []map[string]any{{"ref": "a", "name": "A"}, {"ref": "b", "name": "B"}}
	relationshipA := []map[string]any{{"ref": "r", "evidence_indices": []any{2, 1}, "subject": map[string]any{"name": "s"}, "predicate": map[string]any{"proposed_key": " uses "}, "object": map[string]any{"value": map[string]any{"type": "string", "value": "v"}}}}
	relationshipB := []map[string]any{{"ref": "r", "evidence_indices": []any{1, 2}, "subject": map[string]any{"name": "s"}, "predicate": map[string]any{"proposed_key": "uses"}, "object": map[string]any{"value": map[string]any{"type": "string", "value": "v"}}}}
	hashA, err := CanonicalRequestBodyHash(evidence, entitiesA, relationshipA)
	require.NoError(t, err)
	hashB, err := CanonicalRequestBodyHash(evidence, entitiesB, relationshipB)
	require.NoError(t, err)
	assert.Equal(t, hashA, hashB)

	_, err = CanonicalRequestBodyHash(make(chan int), nil, nil)
	assert.Error(t, err)
	_, err = canonicalRememberObjects(make(chan int))
	assert.Error(t, err)
	assert.NotEmpty(t, canonicalRememberValueOrder(make(chan int)))
	assert.Equal(t, []map[string]any{}, mustCanonicalObjects(t, nil))

	encoded, err := json.Marshal([]map[string]any{{"content": "x"}})
	require.NoError(t, err)
	assert.NotEmpty(t, encoded)
}

func mustCanonicalObjects(t *testing.T, value any) []map[string]any {
	t.Helper()
	objects, err := canonicalRememberObjects(value)
	require.NoError(t, err)
	return objects
}
