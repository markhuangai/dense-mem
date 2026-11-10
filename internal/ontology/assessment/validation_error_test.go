package assessment

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestOrganizationDecodeValidationDiagnostics(t *testing.T) {
	_, response := assessmentFixture()
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	for name, raw := range map[string]string{
		"syntax": `{`, "wrong_type": `{"request_id":[],"definitions":[],"items":[],"equivalence":[],"hierarchy_updates":[]}`, "trailing": string(encoded) + " {}", "null": "null",
		"unknown":   `{"request_id":"request","definitions":[],"items":[],"equivalence":[],"hierarchy_updates":[],"synthetic-provider-secret raw-source-canary":true}`,
		"duplicate": `{"synthetic-provider-secret":1,"synthetic-provider-secret":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(raw)
			diagnostic := requireValidation(t, err, "decode", "strict_json", "response")
			body, marshalErr := json.Marshal(diagnostic)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(body), "synthetic-provider-secret")
			require.NotContains(t, string(body), "raw-source-canary")
		})
	}
	for _, tc := range []struct{ raw, field, family, message string }{
		{`{}`, "request_id", "request_id", "is required"},
		{`{"request_id":"request","items":[],"equivalence":[],"hierarchy_updates":[]}`, "definitions", "definitions", "is required"},
		{strings.Replace(string(encoded), `"parent_ref":"",`, "", 1), "definitions[0].parent_ref", "definitions.parent_ref", "is required"},
		{strings.Replace(string(encoded), `"reason":""`, `"reason":null`, 1), "items[0].reason", "items.reason", "must not be null"},
	} {
		_, err := Decode(tc.raw)
		var rejected *assessor.SemanticAssessmentResponseValidationError
		require.ErrorAs(t, err, &rejected)
		require.Contains(t, rejected.Errors, assessor.SemanticValidationError{Field: tc.field, Message: tc.message, Code: "missing_field"})
		requireValidation(t, err, "decode", "missing_field", tc.family)
	}
}

func TestOrganizationSemanticValidationDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		code, field string
		change      func(*Request, *Response)
	}{
		{"request_id_mismatch", "request_id", func(_ *Request, r *Response) { r.RequestID = "other" }},
		{"missing_array", "definitions", func(_ *Request, r *Response) { r.Definitions = nil }},
		{"missing_array", "items", func(_ *Request, r *Response) { r.Items = nil }},
		{"missing_array", "equivalence", func(_ *Request, r *Response) { r.Equivalence = nil }},
		{"definition_limit", "definitions", func(_ *Request, r *Response) { r.Definitions = make([]Definition, 21) }},
		{"item_count_mismatch", "items", func(_ *Request, r *Response) { r.Items = r.Items[:1] }},
		{"definition_ref_invalid", "definitions", func(_ *Request, r *Response) { r.Definitions[0].Ref = "" }},
		{"definition_ref_duplicate", "definitions", func(_ *Request, r *Response) { r.Definitions = append(r.Definitions, r.Definitions[0]) }},
		{"definition_ref_supplied", "definitions", func(q *Request, r *Response) { q.Definitions = append(q.Definitions, r.Definitions[0]) }},
		{"definition_parent_ref", "definitions", func(_ *Request, r *Response) { r.Definitions[0].ParentRef = "outside" }},
		{"definition_record", "definitions", func(_ *Request, r *Response) { r.Definitions[0].Label = "" }},
		{"definition_catalog", "definitions", func(_ *Request, r *Response) { r.Definitions[0].ParentRef = r.Definitions[0].Ref }},
		{"definition_unused", "definitions", func(_ *Request, r *Response) {
			r.Definitions = append(r.Definitions, Definition{Ref: "unused", Kind: ontology.Topic, Key: "unused", Label: "Unused", Aliases: []string{}})
		}},
		{"item_ref_unknown", "items", func(_ *Request, r *Response) { r.Items[0].Ref = "outside" }},
		{"item_ref_duplicate", "items", func(_ *Request, r *Response) { r.Items[1].Ref = r.Items[0].Ref }},
		{"item_reason_limit", "items", func(_ *Request, r *Response) { r.Items[0].Reason = strings.Repeat("x", 129) }},
		{"item_ambiguous_classification", "items", func(_ *Request, r *Response) { r.Items[0].Status = "ambiguous" }},
		{"item_definition_ref", "items", func(_ *Request, r *Response) { r.Items[0].DefinitionRef = "synthetic-provider-secret" }},
		{"item_locked_classification", "items", func(q *Request, _ *Response) { q.Items[0].LockedDefinitionRef = "locked" }},
		{"item_source_kind", "items", func(q *Request, _ *Response) { q.Items[0].Kind = ontology.PredicateSource }},
		{"item_status", "items", func(_ *Request, r *Response) { r.Items[0].Status = "synthetic-provider-secret" }},
		{"equivalence_ref", "equivalence", func(_ *Request, r *Response) { r.Equivalence[0].Ref = "outside" }},
		{"equivalence_relation", "equivalence", func(_ *Request, r *Response) { r.Equivalence[0].Relation = "similar" }},
		{"equivalence_required_relation", "equivalence", func(q *Request, _ *Response) { q.Pairs[0].RequiredRelation = "distinct" }},
		{"equivalence_coverage", "equivalence", func(_ *Request, r *Response) { r.Equivalence = []Equivalence{} }},
		{"equivalence_coverage", "equivalence", func(_ *Request, r *Response) { r.Equivalence = append(r.Equivalence, r.Equivalence[0]) }},
	} {
		t.Run(tc.code+"/"+tc.field, func(t *testing.T) {
			request, response := assessmentFixture()
			tc.change(&request, &response)
			err := Validate(request, response)
			diagnostic := requireValidation(t, err, "validate", tc.code, tc.field, response)
			if tc.code == "item_count_mismatch" {
				require.Equal(t, 2, *diagnostic.ExpectedCount)
				require.Equal(t, 1, *diagnostic.ActualCount)
			}
			if tc.code == "definition_record" || tc.code == "definition_catalog" {
				require.ErrorIs(t, err, ontology.ErrInvalid)
			}
			body, marshalErr := json.Marshal(diagnostic)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(body), "synthetic-provider-secret")
		})
	}
}

func TestOrganizationValidationDiagnosticsCompleteRepairAndLimitPrecedence(t *testing.T) {
	request, response := assessmentFixture()
	valid, err := json.Marshal(response)
	require.NoError(t, err)
	limits := assessor.DefaultSemanticAssessmentLimits()
	for _, overflow := range []bool{false, true} {
		calls := 0
		provider := NewProvider(fixtureTransport(func(_ context.Context, outbound modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
			calls++
			require.Equal(t, SystemPrompt, outbound.Messages[0].Content)
			require.Equal(t, ResponseSchema(request.RequestID), outbound.Schema)
			if calls > 1 {
				var correction correctionFeedback
				require.NoError(t, json.Unmarshal([]byte(outbound.Messages[len(outbound.Messages)-1].Content), &correction))
				expected := "request_id: is required"
				if overflow {
					expected = "output exceeds token budget"
				}
				require.Contains(t, correctionMessages(correction), expected)
				require.Equal(t, "{}", outbound.Messages[len(outbound.Messages)-2].Content)
			}
			if calls == 3 && !overflow {
				return modelprovider.StructuredResult{Content: string(valid), PromptTokens: 17, CompletionTokens: 9, TotalTokens: 26}, nil
			}
			result := modelprovider.StructuredResult{Content: "{}", PromptTokens: 17, CompletionTokens: 9, TotalTokens: 26}
			if overflow {
				result.CompletionTokens = limits.MaxOutputTokens + 1
			}
			return result, nil
		}), "model", limits, 300000)
		identity := provider.Identity()
		result, attempts, err := provider.Assess(context.Background(), request)
		require.Len(t, attempts, 3)
		require.Equal(t, 3, calls)
		require.Equal(t, identity, provider.Identity())
		if overflow {
			require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
			require.Empty(t, result.Items)
		} else {
			require.NoError(t, err)
			require.Equal(t, response, result)
			require.Nil(t, attempts[2].Validation)
		}
		for i, attempt := range attempts {
			require.True(t, attempt.ReportedUsageAvailable)
			require.Equal(t, 17, attempt.ReportedInputTokens)
			if i == 2 && !overflow {
				continue
			}
			require.Equal(t, "response_invalid", attempt.FailureCode)
			require.Equal(t, "validation", attempt.Diagnostics.FailurePhase)
			require.NotNil(t, attempt.Validation)
			require.True(t, attempt.Validation.Valid())
			if overflow {
				require.Equal(t, "limits", attempt.Validation.Stage)
				require.Equal(t, "output_limit", attempt.Validation.Code)
				require.Equal(t, limits.MaxOutputTokens, *attempt.Validation.ExpectedCount)
				require.Equal(t, limits.MaxOutputTokens+1, *attempt.Validation.ActualCount)
			} else {
				require.Equal(t, "decode", attempt.Validation.Stage)
				require.Equal(t, "missing_field", attempt.Validation.Code)
			}
		}
	}
}

type correctionFeedback struct {
	ValidationErrors []assessor.SemanticValidationError `json:"validation_errors"`
	Instruction      string                             `json:"instruction"`
}

func correctionMessages(feedback correctionFeedback) string {
	return (&assessor.SemanticAssessmentResponseValidationError{Errors: feedback.ValidationErrors}).Error()
}

func requireCorrectionErrors(t *testing.T, err error, actual []assessor.SemanticValidationError) {
	t.Helper()
	var rejected *assessor.SemanticAssessmentResponseValidationError
	require.ErrorAs(t, err, &rejected)
	expected, marshalErr := json.Marshal(assessor.BoundSemanticAssessmentCorrectionErrors(rejected.Errors))
	require.NoError(t, marshalErr)
	encoded, marshalErr := json.Marshal(actual)
	require.NoError(t, marshalErr)
	require.JSONEq(t, string(expected), string(encoded))
}

func requireValidation(t *testing.T, err error, stage, code, family string, responses ...Response) ontology.AssessmentValidation {
	t.Helper()
	var rejected *assessor.SemanticAssessmentResponseValidationError
	require.ErrorAs(t, err, &rejected)
	request, response := assessmentFixture()
	if len(responses) > 0 {
		response = responses[0]
	}
	diagnostic := validationDiagnostic(stage, err, request, response)
	require.NotNil(t, diagnostic)
	require.True(t, diagnostic.Valid(), "%+v", diagnostic)
	require.Equal(t, stage, diagnostic.Stage)
	require.Equal(t, code, diagnostic.Code)
	require.Equal(t, family, diagnostic.FieldFamily)
	return *diagnostic
}

func TestOrganizationDecodeReportsIndependentIndexedFields(t *testing.T) {
	_, response := assessmentFixture()
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	raw := strings.Replace(string(encoded), `"parent_ref":"",`, "", 1)
	raw = strings.Replace(raw, `"ref":"a"`, `"ref":"a","unexpected":true`, 1)
	raw = strings.Replace(raw, `"ref":"b","status":"classified"`, `"ref":"b","status":null`, 1)
	result, err := Decode(raw)
	require.Empty(t, result)
	var rejected *assessor.SemanticAssessmentResponseValidationError
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, []assessor.SemanticValidationError{
		{Field: "definitions[0].parent_ref", Message: "is required", Code: "missing_field"},
		{Field: "items[0].unexpected", Message: "is unknown", Code: "strict_json"},
		{Field: "items[1].status", Message: "must not be null", Code: "missing_field"},
	}, rejected.Errors)

	raw = strings.Replace(string(encoded), `"request_id":"request"`, `"request_id":[]`, 1)
	raw = strings.Replace(raw, `"ref":"b","status":"classified"`, `"ref":"b","status":false`, 1)
	result, err = Decode(raw)
	require.Empty(t, result)
	require.ErrorAs(t, err, &rejected)
	require.Len(t, rejected.Errors, 2)
	require.Equal(t, "request_id", rejected.Errors[0].Field)
	require.Equal(t, "items[1].status", rejected.Errors[1].Field)
	require.Contains(t, rejected.Errors[1].Message, `"ref":"b"`)
	require.Contains(t, rejected.Errors[1].Message, "bool")
}

func TestOrganizationDetailedSharedFeedbackRepairsCompleteResponses(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "exhausted", true: "repaired"}[repair], func(t *testing.T) {
			request, invalid := assessmentFixture()
			locked := invalid.Definitions[0]
			locked.Ref, locked.Key, locked.Label = "locked", "locked-topic", "Locked topic"
			request.Definitions = []Definition{locked}
			request.Items[0].LockedDefinitionRef = locked.Ref
			request.Items[1].Kind = ontology.PredicateSource
			request.Pairs[0].RequiredRelation = "distinct"
			invalid.Items[1].Reason = strings.Repeat("x", 129)
			invalidErr := Validate(request, invalid)
			var rejected *assessor.SemanticAssessmentResponseValidationError
			require.ErrorAs(t, invalidErr, &rejected)
			require.Len(t, rejected.Errors, 4)
			require.Contains(t, invalidErr.Error(), `items[0].definition_ref: item "a" classification changes deterministic reuse: must equal locked_definition_ref "locked"; received "new-topic"`)
			require.Contains(t, invalidErr.Error(), `items[1].definition_ref: item "b" has incompatible source kind "predicate": expected predicate_concept; definition_ref "new-topic" selects kind "topic"`)
			require.Contains(t, invalidErr.Error(), `pair "pair" contradicts deterministic context or manager rule: must equal required_relation "distinct"; received "equivalent"`)
			_, valid := assessmentFixture()
			valid.Definitions[0].Kind = ontology.PredicateConcept
			valid.Items[0].DefinitionRef = locked.Ref
			valid.Equivalence[0].Relation = "distinct"
			require.NoError(t, Validate(request, valid))
			original, err := json.Marshal(request)
			require.NoError(t, err)
			calls := 0
			provider := NewProvider(fixtureTransport(func(_ context.Context, outbound modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				calls++
				require.Len(t, outbound.Messages, 2*calls)
				require.Equal(t, string(original), outbound.Messages[1].Content)
				require.Equal(t, ResponseSchema(request.RequestID), outbound.Schema)
				if calls > 1 {
					var feedback correctionFeedback
					require.NoError(t, json.Unmarshal([]byte(outbound.Messages[len(outbound.Messages)-1].Content), &feedback))
					requireCorrectionErrors(t, invalidErr, feedback.ValidationErrors)
					for _, issue := range feedback.ValidationErrors {
						require.Empty(t, issue.Code)
					}
					prior, err := json.Marshal(invalid)
					require.NoError(t, err)
					require.Equal(t, string(prior), outbound.Messages[len(outbound.Messages)-2].Content)
				}
				response := invalid
				if repair && calls == 3 {
					response = valid
				}
				body, err := json.Marshal(response)
				require.NoError(t, err)
				return modelprovider.StructuredResult{Content: string(body)}, nil
			}), "model", assessor.DefaultSemanticAssessmentLimits(), 300000)
			result, attempts, err := provider.Assess(context.Background(), request)
			require.Equal(t, 3, calls)
			require.Len(t, attempts, 3)
			if repair {
				require.NoError(t, err)
				require.Equal(t, valid, result)
			} else {
				require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
				require.Empty(t, result)
			}
		})
	}
}
