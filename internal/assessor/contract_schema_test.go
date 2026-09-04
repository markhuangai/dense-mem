package assessor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSemanticAssessmentSchemaIsClosedAndCanonical(t *testing.T) {
	schema := SemanticAssessmentResponseSchema()
	assertClosedProviderObjects(t, schema, "assessment response")
	props := schemaPropertiesForTest(t, schema)
	for _, field := range []string{"evidence_security_results", "evidence_equivalence_results", "evidence_conflict_results", "entity_results", "relationship_results"} {
		if _, ok := props[field]; !ok {
			t.Fatalf("assessment schema missing %s", field)
		}
	}
	assertOpenAIStrictSchemaSubset(t, schema, "assessment response")
}

func TestExportedAssessmentAdaptersPreserveDefaultsAndShapeErrors(t *testing.T) {
	defaults := DefaultSemanticAssessmentLimits()
	require.Equal(t, defaults, NormalizeSemanticAssessmentLimits(SemanticAssessmentLimits{}))
	require.NotEmpty(t, ValidateSemanticAssessmentResponseRaw([]byte(`{}`)))

	providerErr := &ProviderError{Provider: "test", FailureClass: ProviderFailureClassHTTPServer, StatusCode: 503}
	details := ProviderFailureDetails(providerErr)
	require.Equal(t, ProviderFailureClassHTTPServer, details.Class)
}

func TestAssessorSchemaAndWhitespaceHelpers(t *testing.T) {
	require.Equal(t, map[string]any{"type": "number", "minimum": float64(0), "maximum": float64(1)}, numberSchema(0, 1))
	require.True(t, semanticWhitespaceEquivalent("A\nworks", " A works "))
	require.False(t, semanticWhitespaceEquivalent("A works", "A fails"))
	require.Equal(t, "field: message", SemanticValidationError{Field: "field", Message: "message"}.Error())
	require.Equal(t, "message", SemanticValidationError{Message: "message"}.Error())
	require.True(t, semanticSecuritySignalSpanMatchesKind("hidden_control_markup", "<!-- control -->"))
	require.False(t, semanticSecuritySignalSpanMatchesKind("hidden_control_markup", "ordinary text"))
	require.True(t, semanticSecuritySignalSpanMatchesKind("other", "ordinary text"))
	exact, err := semanticExactSpanQuote("A B", 0, 3, " A   B ")
	require.NoError(t, err)
	require.Equal(t, "A B", exact)
	_, err = semanticExactSpanQuote("A B", 0, 3, "not the evidence")
	require.Error(t, err)
}
