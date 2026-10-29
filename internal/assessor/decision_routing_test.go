package assessor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerationOmitsServerCompletedTasksAndValidatesAssembly(t *testing.T) {
	request := SemanticAssessmentRequest{RequestID: "request", DecisionRouting: &SemanticAssessmentRouting{Security: []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "evidence", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}}, Equivalence: []SemanticAssessmentEvidenceEquivalenceResult{{EvidenceID: "evidence", Action: "new"}}}}
	schema := SemanticAssessmentGenerationSchema(request, DefaultSemanticAssessmentLimits())
	properties := schema["properties"].(map[string]any)
	require.NotContains(t, properties, "evidence_security_results")
	require.NotContains(t, properties, "evidence_equivalence_results")
	require.Contains(t, properties, "evidence_conflict_results")
	raw := []byte(`{"request_id":"request","evidence_conflict_results":[],"entity_results":[],"relationship_results":[]}`)
	response, err := DecodeSemanticAssessmentGeneration(raw, request, DefaultSemanticAssessmentLimits())
	require.NoError(t, err)
	require.Equal(t, request.DecisionRouting.Security, response.EvidenceSecurityResults)
	require.Equal(t, request.DecisionRouting.Equivalence, response.EvidenceEquivalenceResults)
	for _, invalid := range []string{
		`{"request_id":"request","evidence_security_results":[],"evidence_conflict_results":[],"entity_results":[],"relationship_results":[]}`,
		`{"request_id":"request","evidence_equivalence_results":[],"evidence_conflict_results":[],"entity_results":[],"relationship_results":[]}`,
		`{"request_id":"request","evidence_conflict_results":[],"entity_results":[],"relationship_results":[],"unexpected":true}`,
		`{"request_id":"request","evidence_conflict_results":[],"entity_results":[]}`,
		`{"request_id":"request","request_id":"other","evidence_conflict_results":[],"entity_results":[],"relationship_results":[]}`,
	} {
		_, err := DecodeSemanticAssessmentGeneration([]byte(invalid), request, DefaultSemanticAssessmentLimits())
		require.Error(t, err)
	}
	for _, field := range []string{"evidence_security_results", "evidence_equivalence_results"} {
		_, err := DecodeSemanticAssessmentGeneration([]byte(`{"`+field+`":null}`), request, DefaultSemanticAssessmentLimits())
		require.EqualError(t, err, "generation repeated server-completed field "+field)
	}
}

func TestGenerationPreservesRawValidationErrorsAndRouting(t *testing.T) {
	for _, route := range []struct {
		name                  string
		security, equivalence bool
	}{{"unrouted", false, false}, {"security", true, false}, {"equivalence", false, true}, {"both", true, true}} {
		t.Run(route.name, func(t *testing.T) {
			request := SemanticAssessmentRequest{RequestID: "request"}
			if route.security || route.equivalence {
				request.DecisionRouting = &SemanticAssessmentRouting{}
				if route.security {
					request.DecisionRouting.Security = []SemanticAssessmentEvidenceSecurityResult{}
				}
				if route.equivalence {
					request.DecisionRouting.Equivalence = []SemanticAssessmentEvidenceEquivalenceResult{}
				}
			}
			before, err := json.Marshal(request.DecisionRouting)
			require.NoError(t, err)
			for _, test := range []struct{ name, relationship, field, message string }{
				{"missing", `{"ref":"r","reason":null,"splits":[]}`, "relationship_results[0].disposition", "is required"},
				{"null", `{"ref":"r","disposition":null,"reason":null,"splits":[]}`, "relationship_results[0].disposition", "must not be null"},
				{"unknown", `{"ref":"r","disposition":"not_supported","reason":null,"splits":[],"unexpected":true}`, "relationship_results[0].unexpected", "is unknown"},
				{"duplicate", `{"ref":"r","disposition":"stored","disposition":"not_supported","reason":null,"splits":[]}`, "", "duplicate JSON field"},
				{"wrong type", `{"ref":"r","disposition":42,"reason":null,"splits":[]}`, "", "cannot unmarshal number"},
			} {
				t.Run(test.name, func(t *testing.T) {
					fields := map[string]json.RawMessage{
						"request_id": []byte(`"request"`), "entity_results": []byte(`[]`), "evidence_conflict_results": []byte(`[]`),
						"relationship_results": []byte("[" + test.relationship + "]"),
					}
					if !route.security {
						fields["evidence_security_results"] = []byte(`[]`)
					}
					if !route.equivalence {
						fields["evidence_equivalence_results"] = []byte(`[]`)
					}
					raw, err := json.Marshal(fields)
					require.NoError(t, err)
					response, err := DecodeSemanticAssessmentGeneration(raw, request, DefaultSemanticAssessmentLimits())
					require.ErrorContains(t, err, test.message)
					require.Equal(t, SemanticAssessmentResponse{}, response)
					if test.field != "" {
						var validation *SemanticAssessmentResponseValidationError
						require.ErrorAs(t, err, &validation)
						require.Equal(t, []SemanticValidationError{{Field: test.field, Message: test.message}}, validation.Errors)
						require.Equal(t, test.field+": "+test.message, err.Error())
					}
					if test.name == "wrong type" {
						var typed *json.UnmarshalTypeError
						require.ErrorAs(t, err, &typed)
						require.True(t, strings.HasSuffix(typed.Field, "disposition"))
					}
					after, err := json.Marshal(request.DecisionRouting)
					require.NoError(t, err)
					require.Equal(t, before, after)
				})
			}
		})
	}
}
