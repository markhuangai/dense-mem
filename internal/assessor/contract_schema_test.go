package assessor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderProposalSchemaIsClosedAndCanonical(t *testing.T) {
	schema := ProviderProposalSchema()
	assertClosedProviderObjects(t, schema, "proposal")
	props := schemaPropertiesForTest(t, schema)
	for _, field := range []string{"predicate_options", "entity_proposals", "relationship_proposals"} {
		if _, ok := props[field]; !ok {
			t.Fatalf("provider proposal schema missing %s", field)
		}
	}
	if _, ok := props["evidence"]; ok {
		t.Fatal("provider proposal schema should not require immutable evidence echo")
	}
	assertOpenAIStrictSchemaSubset(t, schema, "provider proposal")
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
