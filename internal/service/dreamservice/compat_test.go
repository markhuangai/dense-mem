package dreamservice

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

func TestCompatibilityFacadeRunsDailyDreamPolicy(t *testing.T) {
	generator := NewHeuristicGenerator("compatibility-test")
	result, err := generator.Generate(context.Background(), "team", GenerateRequest{
		Inputs: []DreamInput{
			{Type: "relationship", ID: "a", Subject: "A", Predicate: "uses", Object: "B"},
			{Type: "relationship", ID: "b", Subject: "B", Predicate: "owns", Object: "C"},
		},
	})
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.NotEmpty(t, result[0].Hypothesis)
}

func TestCompatibilityFacadeRunsHourlyEvidenceDiscovery(t *testing.T) {
	transport := compatibilityStructuredTransport{}
	generator := NewEvidenceProviderGenerator(&transport, "compatibility-evidence", assessor.DefaultSemanticAssessmentLimits())
	targetID := "11111111-1111-4111-8111-111111111111"
	generated, diagnostics, err := generator.GenerateEvidence(context.Background(), "team", EvidenceGenerationRequest{
		Target: dreamcontract.EvidenceTarget{
			EvidenceID: targetID, FragmentID: targetID, Content: "Alice uses Project.",
			Authority: "primary", SourceGroupKey: "ingest:test",
		},
		Contexts:          []dreamcontract.EvidenceContext{{EvidenceID: targetID, FragmentID: targetID, Content: "Alice uses Project.", Authority: "primary", SourceGroupKey: "ingest:test"}},
		Nodes:             []dreamcontract.EvidenceNode{{ID: "entity-a", Display: "Alice", Kind: "entity"}, {ID: "entity-b", Display: "Project", Kind: "entity"}},
		AllowedPredicates: []dreamcontract.DreamTargetPredicate{{PredicateKey: "uses", Version: 1, AllowedSubjectKinds: []string{"entity"}, AllowedObjectKinds: []string{"entity"}}},
		MaxOutputs:        2,
	})
	require.NoError(t, err)
	require.Empty(t, generated)
	require.Equal(t, 1, diagnostics.ProviderTurns)
	require.Greater(t, diagnostics.ProviderInputTokens, 0)
	require.Greater(t, diagnostics.ProviderOutputTokens, 0)
	require.Zero(t, diagnostics.ProviderProposals)
	require.Equal(t, "compatibility-evidence", generator.Model())
	require.Equal(t, "dense_mem_evidence_discovery_response", transport.schemaName)
}

type compatibilityStructuredTransport struct {
	schemaName string
}

func (t *compatibilityStructuredTransport) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	t.schemaName = request.SchemaName
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &payload); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	return modelprovider.StructuredResult{Content: fmt.Sprintf(`{"request_id":%q,"proposals":[]}`, payload.RequestID)}, nil
}
