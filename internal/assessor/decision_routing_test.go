package assessor

import (
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
		`{"request_id":"request","evidence_conflict_results":[],"entity_results":[],"relationship_results":[],"unexpected":true}`,
		`{"request_id":"request","evidence_conflict_results":[],"entity_results":[]}`,
		`{"request_id":"request","request_id":"other","evidence_conflict_results":[],"entity_results":[],"relationship_results":[]}`,
	} {
		_, err := DecodeSemanticAssessmentGeneration([]byte(invalid), request, DefaultSemanticAssessmentLimits())
		require.Error(t, err)
	}
}
