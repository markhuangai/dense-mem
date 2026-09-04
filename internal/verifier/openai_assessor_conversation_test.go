package verifier

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSemanticAssessmentConversationHelpersCoverFamiliesAndBounds(t *testing.T) {
	count, err := semanticAssessmentMessageTokens([]openAIVerifierMessage{{Role: "user", Content: "claim"}}, "cl100k_base")
	require.NoError(t, err)
	require.Positive(t, count)
	_, err = semanticAssessmentMessageTokens([]openAIVerifierMessage{{Role: "user", Content: "claim"}}, "not-a-tokenizer")
	require.Error(t, err)

	errs := make([]SemanticValidationError, SemanticAssessmentMaxCorrectionErrors+2)
	for index := range errs {
		errs[index] = semanticErr("field", strings.Repeat("m", index+1))
	}
	bounded := boundedSemanticAssessmentCorrectionErrors(errs)
	require.Len(t, bounded, SemanticAssessmentMaxCorrectionErrors)
	require.Equal(t, "response", bounded[len(bounded)-1].Field)
	require.Len(t, semanticAssessmentValidationFieldFamilies([]SemanticValidationError{
		semanticErr("request_id", "bad"), semanticErr("input_tokens", "bad"), semanticErr("output_tokens", "bad"), semanticErr("response", "bad"),
		semanticErr("evidence_security_results[0]", "bad"), semanticErr("evidence_equivalence_candidates[0]", "bad"), semanticErr("evidence_conflict_results[0]", "bad"),
		semanticErr("entity_results[0].ref", "bad"), semanticErr("relationship_results[0].evidence[0]", "bad"), semanticErr("unknown", "bad"),
	}), 10)
	require.Equal(t, "entity_results.ref", semanticAssessmentValidationFieldFamily("entity_results[0].ref"))
	require.Equal(t, "relationship_results.temporal", semanticAssessmentValidationFieldFamily("relationship_results[0].valid_from"))
	require.Equal(t, "other", semanticAssessmentValidationFieldFamily("unknown"))
	for _, field := range []string{"entity_results", "entity_results[0].evidence_id", "entity_results[0].kind", "entity_results[0].action", "entity_results[0].unknown", "relationship_results", "relationship_results[0].ref", "relationship_results[0].subject_ref", "relationship_results[0].predicate_key", "relationship_results[0].object_ref", "relationship_results[0].polarity", "relationship_results[0].evidence", "relationship_results[0].unknown"} {
		require.NotEqual(t, "other", semanticAssessmentValidationFieldFamily(field), field)
	}
	require.Equal(t, "field", semanticAssessmentValidationLeaf("prefix.field[0]"))

	require.Equal(t, ProviderFailureClassHTTPClient, openAIHTTPFailureClass(http.StatusBadRequest))
	require.Equal(t, ProviderFailureClassHTTPServer, openAIHTTPFailureClass(http.StatusBadGateway))
	require.Equal(t, ProviderFailureClassHTTPUnexpected, openAIHTTPFailureClass(http.StatusOK))
	require.Equal(t, 12, openAIRetryAfterSeconds("12", time.Now()))
	require.Equal(t, 0, openAIRetryAfterSeconds("0", time.Now()))
	require.Equal(t, 300, openAIRetryAfterSeconds("999", time.Now()))
	require.Equal(t, 0, openAIRetryAfterSeconds("not-a-date", time.Now()))
	future := time.Now().UTC().Add(2 * time.Second)
	require.GreaterOrEqual(t, openAIRetryAfterSeconds(future.Format(http.TimeFormat), time.Now().UTC()), 1)
	require.True(t, openAIRequestTimedOutOrCanceled(context.Background(), context.DeadlineExceeded))
	require.False(t, openAIRequestTimedOutOrCanceled(context.Background(), context.Canceled))
}

func TestDecodeOpenAIVerifierResponseRejectsTransportAndJSONErrors(t *testing.T) {
	_, err := decodeOpenAIVerifierAPIResponse(errorReader{})
	require.ErrorContains(t, err, "read failed")
	_, err = decodeOpenAIVerifierAPIResponse(bytes.NewReader([]byte("not-json")))
	require.Error(t, err)
	_, err = decodeOpenAIVerifierAPIResponse(bytes.NewReader(bytes.Repeat([]byte("x"), openAIVerifierMaxResponseBytes+1)))
	require.ErrorContains(t, err, "transport limit")
	_, err = decodeOpenAIVerifierAPIResponse(bytes.NewReader([]byte(`{"choices":[]}`)))
	require.NoError(t, err)
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestSemanticAssessmentResponseCorrectionStages(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, validationErrs := PrepareSemanticAssessmentRequest(req, limits)
	require.Empty(t, validationErrs)
	valid := string(mustMarshalJSON(t, semanticAssessmentTestResponse()))
	response, errs, stage := semanticAssessmentResponseForCorrection(prepared, openAIStructuredChatResult{
		Content: valid, ReportedUsage: &openAIVerifierUsage{CompletionTokens: int64(limits.MaxOutputTokens + 1)},
	}, limits)
	require.Empty(t, response)
	require.NotEmpty(t, errs)
	require.Equal(t, "response_output_tokens", stage)

	response, errs, stage = semanticAssessmentResponseForCorrection(prepared, openAIStructuredChatResult{Content: "not-json"}, limits)
	require.Empty(t, response)
	require.NotEmpty(t, errs)
	require.Equal(t, "response_json", stage)

	response, errs, stage = semanticAssessmentResponseForCorrection(prepared, openAIStructuredChatResult{Content: valid}, limits)
	require.Empty(t, errs)
	require.Equal(t, "", stage)
	require.Equal(t, semanticAssessmentTestResponse().RequestID, response.RequestID)
}
