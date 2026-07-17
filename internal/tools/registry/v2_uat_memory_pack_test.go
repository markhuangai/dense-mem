package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

func TestBuildV2UATWiresExecutableMemoryPackTools(t *testing.T) {
	stub := &stubV2SkillPackService{}
	reg, err := BuildV2UAT(Dependencies{V2SkillPack: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}

	find, ok := reg.Get(V2ToolFindMemoryPackCandidates)
	if !ok || find.Invoke == nil {
		t.Fatal("BuildV2UAT did not register executable find_memory_pack_candidates")
	}
	findOut, err := find.Invoke(context.Background(), "ignored-profile", map[string]any{
		"query": "PostgreSQL",
		"limit": float64(7),
	})
	if err != nil {
		t.Fatalf("find_memory_pack_candidates.Invoke: %v", err)
	}
	if findOut["candidates"] == nil || stub.findReq.Query != "PostgreSQL" || stub.findReq.Limit != 7 {
		t.Fatalf("find output = %#v req = %#v", findOut, stub.findReq)
	}

	export, ok := reg.Get(V2ToolExportMemoryPack)
	if !ok || export.Invoke == nil {
		t.Fatal("BuildV2UAT did not register executable export_memory_pack")
	}
	exportOut, err := export.Invoke(context.Background(), "ignored-profile", map[string]any{
		"name":             "PostgreSQL pack",
		"relationship_ids": []any{"relationship-v2"},
		"include_evidence": false,
	})
	if err != nil {
		t.Fatalf("export_memory_pack.Invoke: %v", err)
	}
	if exportOut["content_sha256"] != "hash-v2" || stub.exportReq.Name != "PostgreSQL pack" || len(stub.exportReq.RelationshipIDs) != 1 {
		t.Fatalf("export output = %#v req = %#v", exportOut, stub.exportReq)
	}
	if stub.exportReq.IncludeSupport == nil || *stub.exportReq.IncludeSupport {
		t.Fatalf("export include_support = %#v, want false", stub.exportReq.IncludeSupport)
	}

	inspect, ok := reg.Get(V2ToolInspectMemoryPack)
	if !ok || inspect.Invoke == nil {
		t.Fatal("BuildV2UAT did not register executable inspect_memory_pack")
	}
	inspectOut, err := inspect.Invoke(context.Background(), "ignored-profile", map[string]any{
		"artifact_json": "{}",
		"mode":          "review",
	})
	if err != nil {
		t.Fatalf("inspect_memory_pack.Invoke: %v", err)
	}
	if inspectOut["content_sha256"] != "hash-v2" || stub.inspectReq.ArtifactJSON != "{}" {
		t.Fatalf("inspect output = %#v req = %#v", inspectOut, stub.inspectReq)
	}

	importTool, ok := reg.Get(V2ToolImportMemoryPack)
	if !ok || importTool.Invoke == nil {
		t.Fatal("BuildV2UAT did not register executable import_memory_pack")
	}
	importOut, err := importTool.Invoke(context.Background(), "ignored-profile", map[string]any{
		"artifact_json": "{}",
		"mode":          "review",
	})
	if err != nil {
		t.Fatalf("import_memory_pack.Invoke: %v", err)
	}
	if importOut["import_id"] != "import-v2" || stub.importReq.Mode != "review" {
		t.Fatalf("import output = %#v req = %#v", importOut, stub.importReq)
	}

	rollback, ok := reg.Get(V2ToolRollbackMemoryPackImport)
	if !ok || rollback.Invoke == nil {
		t.Fatal("BuildV2UAT did not register executable rollback_memory_pack_import")
	}
	rollbackOut, err := rollback.Invoke(context.Background(), "ignored-profile", map[string]any{
		"import_id": "import-v2",
		"dry_run":   true,
	})
	if err != nil {
		t.Fatalf("rollback_memory_pack_import.Invoke: %v", err)
	}
	if rollbackOut["safe"] != true || !stub.rollbackReq.DryRun || stub.rollbackReq.Confirm {
		t.Fatalf("rollback output = %#v req = %#v", rollbackOut, stub.rollbackReq)
	}
}

func TestBuildV2UATMemoryPackImportRejectsTenantOverride(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{V2SkillPack: &stubV2SkillPackService{}})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	tool, ok := reg.Get(V2ToolImportMemoryPack)
	if !ok {
		t.Fatal("BuildV2UAT did not register import_memory_pack")
	}
	_, err = tool.Invoke(context.Background(), "ignored-profile", map[string]any{
		"team_id":       "attacker-team",
		"artifact_json": "{}",
		"mode":          "review",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("import_memory_pack.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildV2UATMemoryPackMapsCanonicalImportAndRollbackApply(t *testing.T) {
	stub := &stubV2SkillPackService{}
	reg, err := BuildV2UAT(Dependencies{V2SkillPack: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}

	importTool, ok := reg.Get(V2ToolImportMemoryPack)
	if !ok || importTool.Invoke == nil {
		t.Fatal("BuildV2UAT did not register executable import_memory_pack")
	}
	importOut, err := importTool.Invoke(context.Background(), "ignored-profile", map[string]any{
		"artifact_json": "{}",
		"mode":          "trusted",
		"conflict_decisions": []any{
			map[string]any{"item_id": "item-1", "decision": "skip"},
		},
	})
	if err != nil {
		t.Fatalf("import_memory_pack.Invoke: %v", err)
	}
	if importOut["processing_state"] != "completed" || len(stub.importReq.ConflictDecisions) != 1 ||
		stub.importReq.ConflictDecisions[0].Action != "skip" {
		t.Fatalf("import output = %#v req = %#v", importOut, stub.importReq)
	}

	rollback, ok := reg.Get(V2ToolRollbackMemoryPackImport)
	if !ok || rollback.Invoke == nil {
		t.Fatal("BuildV2UAT did not register executable rollback_memory_pack_import")
	}
	rollbackOut, err := rollback.Invoke(context.Background(), "ignored-profile", map[string]any{
		"import_id":    "import-v2",
		"dry_run":      false,
		"impact_token": "impact-v2",
	})
	if err != nil {
		t.Fatalf("rollback_memory_pack_import.Invoke: %v", err)
	}
	if rollbackOut["applied"] != true || !stub.rollbackReq.Confirm || stub.rollbackReq.DryRun {
		t.Fatalf("rollback output = %#v req = %#v", rollbackOut, stub.rollbackReq)
	}
}

func TestV2MemoryPackBridgeHelpersCoverOptionalBranches(t *testing.T) {
	exportOut := v2MemoryPackExportPublicOutput(&skillpackservice.V2ExportResult{
		SHA256:    "hash-v2",
		ItemCount: 2,
		Omissions: []string{
			"missing relationship",
		},
	})
	if omissions, ok := exportOut["omissions"].([]any); !ok || len(omissions) != 1 {
		t.Fatalf("export omissions = %#v", exportOut["omissions"])
	}

	inspectOut := v2MemoryPackInspectPublicOutput(&skillpackservice.V2InspectResult{
		Format:       skillpackservice.V2MemoryPackFormat,
		ArtifactHash: "hash-v2",
		ItemCount:    1,
		DecisionsRequired: []skillpackservice.V2ConflictPrompt{{
			ItemID:         "item-1",
			Reason:         "entity_conflict",
			AllowedActions: []string{"skip"},
		}},
	}, map[string]any{"mode": "review"})
	if conflicts, ok := inspectOut["conflicts"].([]any); !ok || len(conflicts) != 1 || inspectOut["valid"] != true {
		t.Fatalf("inspect output = %#v", inspectOut)
	}

	importOut := v2MemoryPackImportPublicOutput(&skillpackservice.V2ImportResult{
		ImportID: "import-v2",
		Status:   "pending",
		Items: []skillpackservice.V2ImportItemResult{{
			ItemID: "item-1",
			Status: "skipped",
		}},
	})
	if importOut["processing_state"] != "queued" {
		t.Fatalf("import processing_state = %#v", importOut["processing_state"])
	}
	if omissions, ok := importOut["omissions"].([]any); !ok || len(omissions) != 1 {
		t.Fatalf("import omissions = %#v", importOut["omissions"])
	}

	rollbackOut := v2MemoryPackRollbackPublicOutput(&skillpackservice.V2RollbackResult{
		ImportID:  "import-v2",
		Status:    "blocked",
		DryRun:    true,
		Conflicts: []string{"newer import depends on relationship"},
	})
	if rollbackOut["safe"] != false {
		t.Fatalf("rollback safe = %#v", rollbackOut["safe"])
	}
	if blockers, ok := rollbackOut["blockers"].([]any); !ok || len(blockers) != 1 {
		t.Fatalf("rollback blockers = %#v", rollbackOut["blockers"])
	}
}

type stubV2SkillPackService struct {
	findReq     skillpackservice.V2FindCandidatesRequest
	exportReq   skillpackservice.V2ExportRequest
	inspectReq  skillpackservice.V2InspectRequest
	importReq   skillpackservice.V2ImportRequest
	rollbackReq skillpackservice.V2RollbackRequest
}

func (s *stubV2SkillPackService) FindCandidatesV2(_ context.Context, req skillpackservice.V2FindCandidatesRequest) (*skillpackservice.V2FindCandidatesResult, error) {
	s.findReq = req
	return &skillpackservice.V2FindCandidatesResult{
		Candidates: []skillpackservice.V2MemoryPackCandidate{{
			RelationshipID: "relationship-v2",
			PredicateKey:   "uses",
			Subject:        "Dense-Mem",
			Object:         "PostgreSQL",
		}},
	}, nil
}

func (s *stubV2SkillPackService) ExportV2(_ context.Context, req skillpackservice.V2ExportRequest) (*skillpackservice.V2ExportResult, error) {
	s.exportReq = req
	return &skillpackservice.V2ExportResult{
		CanonicalJSON: "{}",
		SHA256:        "hash-v2",
		ItemCount:     1,
		Filename:      "postgresql-pack.memory-pack.json",
		ContentType:   "application/json",
	}, nil
}

func (s *stubV2SkillPackService) InspectV2(_ context.Context, req skillpackservice.V2InspectRequest) (*skillpackservice.V2InspectResult, error) {
	s.inspectReq = req
	return &skillpackservice.V2InspectResult{
		ArtifactHash: "hash-v2",
		Format:       skillpackservice.V2MemoryPackFormat,
		Name:         "PostgreSQL pack",
		ItemCount:    1,
	}, nil
}

func (s *stubV2SkillPackService) ImportV2(_ context.Context, req skillpackservice.V2ImportRequest) (*skillpackservice.V2ImportResult, error) {
	s.importReq = req
	return &skillpackservice.V2ImportResult{
		ImportID:     "import-v2",
		ArtifactHash: "hash-v2",
		Mode:         req.Mode,
		Status:       domain.SkillPackImportStatusApplied,
		IngestID:     "ingest-v2",
		AppliedCount: 1,
	}, nil
}

func (s *stubV2SkillPackService) RollbackV2(_ context.Context, req skillpackservice.V2RollbackRequest) (*skillpackservice.V2RollbackResult, error) {
	s.rollbackReq = req
	if req.Confirm && !req.DryRun {
		return &skillpackservice.V2RollbackResult{
			ImportID: req.ImportID,
			Status:   domain.SkillPackImportStatusRolledBack,
			DryRun:   false,
		}, nil
	}
	return &skillpackservice.V2RollbackResult{
		ImportID: req.ImportID,
		Status:   "safe",
		DryRun:   req.DryRun,
	}, nil
}

var _ skillpackservice.V2Service = (*stubV2SkillPackService)(nil)
