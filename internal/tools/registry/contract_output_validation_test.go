package registry

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/memorypack"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
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
