package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

func TestBuildActiveWiresExecutableMemoryPackTools(t *testing.T) {
	stub := &stubMemoryPackService{}
	reg, err := BuildActive(Dependencies{MemoryPack: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}

	find, ok := reg.Get(ToolFindMemoryPackCandidates)
	if !ok || find.Invoke == nil {
		t.Fatal("BuildActive did not register executable find_memory_pack_candidates")
	}
	findOut, err := find.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"query": "PostgreSQL",
		"limit": float64(7),
	})
	if err != nil {
		t.Fatalf("find_memory_pack_candidates.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: find.OutputSchema}, findOut); err != nil {
		t.Fatalf("find_memory_pack_candidates output schema: %v", err)
	}
	if findOut["candidates"] == nil || stub.findReq.Query != "PostgreSQL" || stub.findReq.Limit != 7 {
		t.Fatalf("find output = %#v req = %#v", findOut, stub.findReq)
	}

	export, ok := reg.Get(ToolExportMemoryPack)
	if !ok || export.Invoke == nil {
		t.Fatal("BuildActive did not register executable export_memory_pack")
	}
	exportOut, err := export.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"name":             "PostgreSQL pack",
		"relationship_ids": []any{"relationship-canonical"},
		"include_evidence": false,
	})
	if err != nil {
		t.Fatalf("export_memory_pack.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: export.OutputSchema}, exportOut); err != nil {
		t.Fatalf("export_memory_pack output schema: %v", err)
	}
	if exportOut["content_sha256"] != testMemoryPackHash || stub.exportReq.Name != "PostgreSQL pack" || len(stub.exportReq.RelationshipIDs) != 1 {
		t.Fatalf("export output = %#v req = %#v", exportOut, stub.exportReq)
	}
	if stub.exportReq.IncludeSupport == nil || *stub.exportReq.IncludeSupport {
		t.Fatalf("export include_support = %#v, want false", stub.exportReq.IncludeSupport)
	}

	inspect, ok := reg.Get(ToolInspectMemoryPack)
	if !ok || inspect.Invoke == nil {
		t.Fatal("BuildActive did not register executable inspect_memory_pack")
	}
	inspectOut, err := inspect.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"artifact_json": "{}",
		"mode":          "review",
	})
	if err != nil {
		t.Fatalf("inspect_memory_pack.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: inspect.OutputSchema}, inspectOut); err != nil {
		t.Fatalf("inspect_memory_pack output schema: %v", err)
	}
	if inspectOut["content_sha256"] != testMemoryPackHash || stub.inspectReq.ArtifactJSON != "{}" || stub.inspectReq.Mode != "review" {
		t.Fatalf("inspect output = %#v req = %#v", inspectOut, stub.inspectReq)
	}

	importTool, ok := reg.Get(ToolImportMemoryPack)
	if !ok || importTool.Invoke == nil {
		t.Fatal("BuildActive did not register executable import_memory_pack")
	}
	importOut, err := importTool.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"artifact_json": "{}",
		"mode":          "review",
	})
	if err != nil {
		t.Fatalf("import_memory_pack.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: importTool.OutputSchema}, importOut); err != nil {
		t.Fatalf("import_memory_pack output schema: %v", err)
	}
	if importOut["import_id"] != "import-canonical" || stub.importReq.Mode != "review" {
		t.Fatalf("import output = %#v req = %#v", importOut, stub.importReq)
	}

	rollback, ok := reg.Get(ToolRollbackMemoryPackImport)
	if !ok || rollback.Invoke == nil {
		t.Fatal("BuildActive did not register executable rollback_memory_pack_import")
	}
	rollbackOut, err := rollback.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"import_id": "import-canonical",
		"dry_run":   true,
	})
	if err != nil {
		t.Fatalf("rollback_memory_pack_import.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: rollback.OutputSchema}, rollbackOut); err != nil {
		t.Fatalf("rollback_memory_pack_import output schema: %v", err)
	}
	if rollbackOut["safe"] != true || !stub.rollbackReq.DryRun || stub.rollbackReq.Confirm {
		t.Fatalf("rollback output = %#v req = %#v", rollbackOut, stub.rollbackReq)
	}
}

func TestBuildActiveMemoryPackImportRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{MemoryPack: &stubMemoryPackService{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	tool, ok := reg.Get(ToolImportMemoryPack)
	if !ok {
		t.Fatal("BuildActive did not register import_memory_pack")
	}
	_, err = tool.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"team_id":       "attacker-team",
		"artifact_json": "{}",
		"mode":          "review",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("import_memory_pack.Invoke err = %v, want tenant override rejection", err)
	}
}

type stubMemoryPackService struct {
	findReq     skillpackservice.FindCandidatesRequest
	exportReq   skillpackservice.ExportRequest
	inspectReq  skillpackservice.InspectRequest
	importReq   skillpackservice.ImportRequest
	rollbackReq skillpackservice.RollbackRequest
}

const testMemoryPackHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (s *stubMemoryPackService) FindCandidates(_ context.Context, req skillpackservice.FindCandidatesRequest) (*skillpackservice.FindCandidatesResult, error) {
	s.findReq = req
	return &skillpackservice.FindCandidatesResult{
		Candidates: []skillpackservice.MemoryPackCandidate{{
			RelationshipID:  "relationship-canonical",
			PredicateKey:    "uses",
			SubjectEntityID: "entity-subject",
			Subject:         "Dense-Mem",
			ObjectEntityID:  "entity-object",
			Object:          "PostgreSQL",
		}},
	}, nil
}

func (s *stubMemoryPackService) Export(_ context.Context, req skillpackservice.ExportRequest) (*skillpackservice.ExportResult, error) {
	s.exportReq = req
	return &skillpackservice.ExportResult{
		CanonicalJSON: `{"format":"dense-mem.memory-pack.v2"}`,
		SHA256:        testMemoryPackHash,
		ItemCount:     1,
		Filename:      "postgresql-pack.memory-pack.json",
		ContentType:   "application/json",
	}, nil
}

func (s *stubMemoryPackService) Inspect(_ context.Context, req skillpackservice.InspectRequest) (*skillpackservice.InspectResult, error) {
	s.inspectReq = req
	return &skillpackservice.InspectResult{
		ArtifactHash: testMemoryPackHash,
		Format:       skillpackservice.MemoryPackFormat,
		Name:         "PostgreSQL pack",
		ItemCount:    1,
	}, nil
}

func (s *stubMemoryPackService) Import(_ context.Context, req skillpackservice.ImportRequest) (*skillpackservice.ImportResult, error) {
	s.importReq = req
	return &skillpackservice.ImportResult{
		ImportID:     "import-canonical",
		ArtifactHash: "hash-canonical",
		Mode:         req.Mode,
		Status:       domain.SkillPackImportStatusApplied,
		AppliedCount: 1,
	}, nil
}

func (s *stubMemoryPackService) Rollback(_ context.Context, req skillpackservice.RollbackRequest) (*skillpackservice.RollbackResult, error) {
	s.rollbackReq = req
	return &skillpackservice.RollbackResult{
		ImportID: req.ImportID,
		Status:   "safe",
		DryRun:   req.DryRun,
	}, nil
}

var _ skillpackservice.MemoryPackService = (*stubMemoryPackService)(nil)
