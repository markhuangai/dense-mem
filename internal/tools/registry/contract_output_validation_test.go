package registry

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/memorypack"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"github.com/stretchr/testify/require"
)

func TestExportMemoryPackContractOutputMatchesClosedSchema(t *testing.T) {
	output := exportMemoryPackContractOutput(&memorypack.ExportResult{
		CanonicalJSON: `{"format":"dense-mem.memory-pack.v3.0"}`,
		SHA256:        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Filename:      "pack.memory-pack.json",
		ItemCount:     1,
		Artifact: memorypack.MemoryPackArtifact{
			Evidence: []memorypack.MemoryPackEvidence{{TraceEvidenceFragment: tracecontract.TraceEvidenceFragment{FragmentID: "evidence-1"}}},
		},
	})
	if err := ValidateInput(Tool{InputSchema: exportMemoryPackOutputSchema()}, output); err != nil {
		t.Fatalf("export output validation: %v", err)
	}
}

func TestEvidenceOnlyCatalogMemoryPackOmissionsRetainEveryReason(t *testing.T) {
	input := []string{"one:evidence_content_excluded_by_request", "one:decisions_max_events", "two:artifact_byte_limit"}
	output := memoryPackOmissionsContractOutput(input)
	require.Equal(t, []map[string]any{
		{"item_id": "one", "reason": "evidence_content_excluded_by_request; decisions_max_events"},
		{"item_id": "two", "reason": "artifact_byte_limit"},
	}, output)
	require.Equal(t, []string{"one:evidence_content_excluded_by_request", "one:decisions_max_events", "two:artifact_byte_limit"}, input)
	require.NoError(t, validateSchemaValue("omissions", output, exportMemoryPackOutputSchema()["properties"].(map[string]any)["omissions"].(map[string]any)))
}
