package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

func TestBuildActiveWiresExportMemoryPackOnly(t *testing.T) {
	stub := &exportOnlyMemoryPackStub{}
	reg, err := BuildActive(Dependencies{MemoryPack: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	export, ok := reg.Get(ToolExportMemoryPack)
	if !ok || export.Invoke == nil {
		t.Fatal("BuildActive did not register executable export_memory_pack")
	}
	out, err := export.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"name":             "PostgreSQL pack",
		"relationship_ids": []any{"relationship-canonical"},
		"include_evidence": false,
	})
	if err != nil {
		t.Fatalf("export_memory_pack.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: export.OutputSchema}, out); err != nil {
		t.Fatalf("export_memory_pack output schema: %v", err)
	}
	if stub.req.Name != "PostgreSQL pack" || len(stub.req.RelationshipIDs) != 1 {
		t.Fatalf("export request = %#v", stub.req)
	}
}

func TestBuildActiveDoesNotRegisterMemoryPackImportTools(t *testing.T) {
	reg, err := BuildActive(Dependencies{MemoryPack: &exportOnlyMemoryPackStub{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	for _, name := range []string{"find_memory_pack_candidates", "inspect_memory_pack", "import_memory_pack", "rollback_memory_pack_import"} {
		if _, ok := reg.Get(name); ok {
			t.Fatalf("removed tool %s remains registered", name)
		}
	}
}

type exportOnlyMemoryPackStub struct {
	req skillpackservice.ExportRequest
}

func (s *exportOnlyMemoryPackStub) Export(_ context.Context, req skillpackservice.ExportRequest) (*skillpackservice.ExportResult, error) {
	s.req = req
	return &skillpackservice.ExportResult{
		CanonicalJSON: `{"format":"` + skillpackservice.MemoryPackFormat + `","relationships":[]}`,
		SHA256:        strings.Repeat("a", 64),
		ItemCount:     0,
		Filename:      "postgresql-pack.memory-pack.json",
		ContentType:   "application/json",
	}, nil
}

var _ skillpackservice.MemoryPackService = (*exportOnlyMemoryPackStub)(nil)
