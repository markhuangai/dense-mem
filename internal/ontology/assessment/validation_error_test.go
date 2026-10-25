package assessment

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestOrganizationDecodeValidationDiagnostics(t *testing.T) {
	_, response := assessmentFixture()
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	for name, raw := range map[string]string{
		"syntax": `{`, "wrong_type": `{"request_id":[]}`, "trailing": string(encoded) + " {}",
		"unknown":   `{"synthetic-provider-secret raw-source-canary":true}`,
		"duplicate": `{"synthetic-provider-secret":1,"synthetic-provider-secret":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(raw)
			var original Response
			originalErr := jsonstrict.Decode(strings.NewReader(raw), &original, ontology.MaxPublicationBytes)
			require.EqualError(t, err, originalErr.Error())
			diagnostic := requireValidation(t, err, "decode", "strict_json", "response")
			body, marshalErr := json.Marshal(diagnostic)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(body), "synthetic-provider-secret")
			require.NotContains(t, string(body), "raw-source-canary")
		})
	}
	for _, tc := range []struct{ raw, field, message string }{
		{`null`, "request_id", "missing request_id"},
		{`{"request_id":"request","items":[],"equivalence":[]}`, "definitions", "missing definitions"},
		{strings.Replace(string(encoded), `"parent_ref":"",`, "", 1), "definitions.parent_ref", "missing definitions.parent_ref"},
		{strings.Replace(string(encoded), `"reason":""`, `"reason":null`, 1), "items.reason", "missing items.reason"},
	} {
		_, err := Decode(tc.raw)
		require.EqualError(t, err, tc.message)
		requireValidation(t, err, "decode", "missing_field", tc.field)
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
			diagnostic := requireValidation(t, err, "validate", tc.code, tc.field)
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
			require.Equal(t, ResponseSchema(), outbound.Schema)
			if calls > 1 {
				var correction map[string]string
				require.NoError(t, json.Unmarshal([]byte(outbound.Messages[len(outbound.Messages)-1].Content), &correction))
				expected := "missing request_id"
				if overflow {
					expected = "output exceeds token budget"
				}
				require.Equal(t, expected, correction["validation_errors"])
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

func requireValidation(t *testing.T, err error, stage, code, family string) ontology.AssessmentValidation {
	t.Helper()
	var rejected *validationError
	require.ErrorAs(t, err, &rejected)
	require.True(t, rejected.diagnostic.Valid())
	require.Equal(t, stage, rejected.diagnostic.Stage)
	require.Equal(t, code, rejected.diagnostic.Code)
	require.Equal(t, family, rejected.diagnostic.FieldFamily)
	return rejected.diagnostic
}
