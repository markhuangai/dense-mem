package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
	"github.com/markhuangai/dense-mem/internal/tools/graphquery"
)

type stubList struct{}

func (stubList) List(ctx context.Context, profileID string, opts fragmentservice.ListOptions) ([]domain.Fragment, string, error) {
	return []domain.Fragment{{FragmentID: "f-1", ProfileID: profileID}}, "", nil
}

type stubGraphQuery struct{}

func (stubGraphQuery) Execute(ctx context.Context, profileID string, query string, params map[string]any) (*graphquery.GraphQueryResult, error) {
	return &graphquery.GraphQueryResult{}, nil
}

func TestBuildDefault_RegistersV2ToolSurface(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	required := []string{
		"recall_memory",
		"trace_memory", "assemble_context",
		"remember", "get_memory_placement", "dispute_memory_placement",
		"import_memories", "reflect_memories", "confirm_memory",
		"list_dreams", "get_dream", "resolve_dream_feedback",
		"find_memory_pack_candidates", "export_memory_pack", "inspect_memory_pack",
		"import_memory_pack", "rollback_memory_pack_import",
	}
	for _, name := range required {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
	for _, name := range []string{"keyword-search", "semantic-search", "graph-query"} {
		if _, ok := reg.Get(name); ok {
			t.Errorf("legacy hyphenated tool %q must not be registered", name)
		}
	}
	for _, removed := range []string{"list_recent_memories", "keyword_search", "semantic_search", "graph_query"} {
		if _, ok := reg.Get(removed); ok {
			t.Errorf("removed tool %q must not be registered", removed)
		}
	}
	listed := map[string]struct{}{}
	for _, tool := range reg.List() {
		listed[tool.Name] = struct{}{}
	}
	for _, legacy := range []string{
		"find_skill_pack_candidates",
		"export_skill_pack",
		"inspect_skill_pack",
		"import_skill_pack",
		"rollback_skill_pack_import",
	} {
		tool, ok := reg.Get(legacy)
		if !ok {
			t.Errorf("legacy tool alias %q should resolve", legacy)
			continue
		}
		if strings.Contains(tool.Name, "skill_pack") {
			t.Errorf("legacy alias %q resolved to non-canonical tool %q", legacy, tool.Name)
		}
		if _, ok := listed[legacy]; ok {
			t.Errorf("legacy alias %q must not be listed", legacy)
		}
	}
	if _, ok := reg.Get("submit_recall_session_feedback"); !ok {
		t.Error("submit_recall_session_feedback should be registered; runtime config controls public visibility")
	}
}

func TestBuildDefault_SchemaFieldsPopulated(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{})
	tool, _ := reg.Get("remember")
	if tool.Description == "" {
		t.Error("remember description is empty")
	}
	if tool.InputSchema == nil || tool.OutputSchema == nil {
		t.Error("remember schemas must not be nil")
	}
	if len(tool.RequiredScopes) == 0 {
		t.Error("remember must declare required scopes")
	}
}

func TestBuildDefault_DoesNotRegisterGraphQueryEvenWhenDependencyIsWired(t *testing.T) {
	reg, err := BuildDefault(Dependencies{GraphQuery: stubGraphQuery{}})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	if _, ok := reg.Get("graph_query"); ok {
		t.Fatal("graph_query must not be registered in the v2 client surface")
	}
}

func TestValidateInputRejectsClientSuppliedRememberClaims(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	tool, ok := reg.Get("remember")
	if !ok {
		t.Fatal("remember tool not registered")
	}

	args := map[string]any{
		"evidence": []any{map[string]any{"content": "The user likes Go."}},
		"claims":   []any{},
	}

	err = ValidateInput(tool, args)
	if err == nil || !strings.Contains(err.Error(), "unknown field: claims") {
		t.Fatalf("ValidateInput error = %v; want claims rejected", err)
	}
}

func TestBuildDefault_InvokersReturnUnavailableWhenDepsMissing(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{})
	cases := []struct {
		name  string
		input map[string]any
	}{
		{name: "recall_memory", input: map[string]any{"query": "hello"}},
		{name: "trace_memory", input: map[string]any{"type": "fact", "id": "fact-1"}},
		{name: "assemble_context", input: map[string]any{"query": "hello"}},
		{name: "list_dreams", input: map[string]any{}},
		{name: "get_dream", input: map[string]any{"dream_id": "dream-1"}},
		{name: "resolve_dream_feedback", input: map[string]any{"dream_id": "dream-1", "decision": "reject"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, _ := reg.Get(tc.name)
			_, err := tool.Invoke(context.Background(), "pA", tc.input)
			if !errors.Is(err, ErrToolUnavailable) {
				t.Fatalf("err = %v; want ErrToolUnavailable", err)
			}
		})
	}
}

func TestBuildDefault_InvokerInvalidInputBranches(t *testing.T) {
	cases := []struct {
		name string
		deps Dependencies
		in   map[string]any
		want string
	}{
		{
			name: "recall_memory",
			deps: Dependencies{Recall: stubRecall{}},
			in:   map[string]any{"query": func() {}},
			want: "recall_memory: invalid input",
		},
		{
			name: "resolve_dream_feedback",
			deps: Dependencies{Dreams: &stubDreamService{}},
			in:   map[string]any{"dream_id": func() {}, "decision": "reject"},
			want: "resolve_dream_feedback: invalid input",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, _ := BuildDefault(tc.deps)
			tool, _ := reg.Get(tc.name)
			_, err := tool.Invoke(context.Background(), "pA", tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestBuildDefault_RecallInvokerCallsServiceWhenWired(t *testing.T) {
	rec := stubRecall{}
	reg, _ := BuildDefault(Dependencies{Recall: rec})
	tool, _ := reg.Get("recall_memory")
	if _, err := tool.Invoke(context.Background(), "pA", map[string]any{"query": "hello"}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
}

func TestBuildDefault_RecallInvokerMapsReflectionAndDreams(t *testing.T) {
	memory := &stubMemory{}
	recall := stubRecallWithHit{}
	dreams := &stubDreamService{}
	reg, _ := BuildDefault(Dependencies{
		Recall: recall,
		Memory: memory,
		Dreams: dreams,
	})

	recallTool, _ := reg.Get("recall_memory")
	recallOut, err := recallTool.Invoke(context.Background(), "profile-search", map[string]any{
		"query":            "hello",
		"limit":            float64(2),
		"include_evidence": true,
	})
	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	results := recallOut["results"].([]map[string]any)
	if len(results) != 1 || results[0]["id"] != "fragment-hit" {
		t.Fatalf("recall results = %v, want fragment-hit", results)
	}
	if results[0]["tier"] != recallservice.TierFragment {
		t.Fatalf("fragment tier = %v, want %s", results[0]["tier"], recallservice.TierFragment)
	}
	fragment, ok := results[0]["fragment"].(map[string]any)
	if !ok || fragment["id"] != "fragment-hit" {
		t.Fatalf("fragment payload = %v, want nested fragment-hit", results[0]["fragment"])
	}
	if memory.lastProfile != "profile-search" {
		t.Fatalf("recall reflection profile = %q, want profile-search", memory.lastProfile)
	}
	if dreams.recallQuery != "hello" {
		t.Fatalf("dream recall query = %q, want hello", dreams.recallQuery)
	}
}

func TestBuildDefaultSkillPackTools_InvokeSuccessAndInvalidInput(t *testing.T) {
	skillPack := &stubSkillPackService{}
	reg, _ := BuildDefault(Dependencies{SkillPack: skillPack})

	cases := []struct {
		name      string
		input     map[string]any
		wantField string
		wantValue any
	}{
		{name: "find_memory_pack_candidates", input: map[string]any{"query": "memory packs", "limit": float64(5)}, wantField: "candidates"},
		{name: "export_memory_pack", input: map[string]any{"name": "Pack", "manual_items": []any{map[string]any{"subject": "assistant", "predicate": "has_skill", "object": "testing", "source_kind": "manual"}}}, wantField: "sha256", wantValue: strings.Repeat("a", 64)},
		{name: "inspect_memory_pack", input: map[string]any{"artifact_json": `{"schema_version":"dense-mem.memory_pack.v1","name":"Pack","items":[{"subject":"assistant","predicate":"has_skill","object":"testing","source_kind":"manual"}]}`}, wantField: "artifact_hash", wantValue: "hash"},
		{name: "import_memory_pack", input: map[string]any{"mode": "review", "artifact_json": `{"schema_version":"dense-mem.memory_pack.v1","name":"Pack","items":[{"subject":"assistant","predicate":"has_skill","object":"testing","source_kind":"manual"}]}`}, wantField: "import_id", wantValue: "import-1"},
		{name: "rollback_memory_pack_import", input: map[string]any{"import_id": "import-1"}, wantField: "status", wantValue: "rolled_back"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, _ := reg.Get(tc.name)
			out, err := tool.Invoke(context.Background(), "profile-skill", tc.input)
			if err != nil {
				t.Fatalf("%s Invoke: %v", tc.name, err)
			}
			if skillPack.lastProfile != "profile-skill" {
				t.Fatalf("%s profile = %q, want profile-skill", tc.name, skillPack.lastProfile)
			}
			if tc.wantValue != nil && out[tc.wantField] != tc.wantValue {
				t.Fatalf("%s %s = %v; want %v", tc.name, tc.wantField, out[tc.wantField], tc.wantValue)
			}
			if tc.wantValue == nil && out[tc.wantField] == nil {
				t.Fatalf("%s missing field %s in %v", tc.name, tc.wantField, out)
			}
		})
	}

	for _, tc := range []struct {
		name string
		in   map[string]any
		want string
	}{
		{name: "find_memory_pack_candidates", in: map[string]any{"query": func() {}}, want: "find_memory_pack_candidates: invalid input"},
		{name: "export_memory_pack", in: map[string]any{"name": func() {}}, want: "export_memory_pack: invalid input"},
		{name: "inspect_memory_pack", in: map[string]any{"artifact_json": func() {}}, want: "inspect_memory_pack: invalid input"},
		{name: "import_memory_pack", in: map[string]any{"mode": func() {}}, want: "import_memory_pack: invalid input"},
		{name: "rollback_memory_pack_import", in: map[string]any{"import_id": func() {}}, want: "rollback_memory_pack_import: invalid input"},
	} {
		t.Run(tc.name+" invalid input", func(t *testing.T) {
			tool, _ := reg.Get(tc.name)
			_, err := tool.Invoke(context.Background(), "profile-skill", tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s err = %v; want %q", tc.name, err, tc.want)
			}
		})
	}
}

func TestSkillPackSchemasDoNotCapItemsAtOneHundred(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{SkillPack: &stubSkillPackService{}})

	factIDs := make([]any, 101)
	selectedItems := make([]any, 101)
	conflictDecisions := make([]any, 101)
	items := make([]any, 101)
	for i := 0; i < 101; i++ {
		factIDs[i] = "fact-1"
		selectedItems[i] = i
		conflictDecisions[i] = map[string]any{"index": i, "action": "skip"}
		items[i] = map[string]any{
			"subject":     "assistant",
			"predicate":   "has_skill",
			"object":      "testing",
			"source_kind": "manual",
		}
	}
	artifact := map[string]any{
		"schema_version": "dense-mem.memory_pack.v1",
		"name":           "Pack",
		"items":          items,
	}

	exportTool, _ := reg.Get("export_memory_pack")
	if err := ValidateInput(exportTool, map[string]any{"name": "Pack", "fact_ids": factIDs}); err != nil {
		t.Fatalf("export_memory_pack ValidateInput: %v", err)
	}
	inspectTool, _ := reg.Get("inspect_memory_pack")
	if err := ValidateInput(inspectTool, map[string]any{"artifact": artifact}); err != nil {
		t.Fatalf("inspect_memory_pack ValidateInput: %v", err)
	}
	importTool, _ := reg.Get("import_memory_pack")
	if err := ValidateInput(importTool, map[string]any{
		"artifact":           artifact,
		"mode":               "review",
		"selected_items":     selectedItems,
		"conflict_decisions": conflictDecisions,
	}); err != nil {
		t.Fatalf("import_memory_pack ValidateInput: %v", err)
	}
}

func TestSkillPackSchemasAcceptSupportGraph(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{SkillPack: &stubSkillPackService{}})
	artifact := map[string]any{
		"schema_version": "dense-mem.memory_pack.v1",
		"name":           "Supported pack",
		"items": []any{map[string]any{
			"subject":              "assistant",
			"predicate":            "has_skill",
			"object":               "testing",
			"source_kind":          "source_validated_claim",
			"source_id":            "claim-1",
			"support_claim_ids":    []any{"claim-1"},
			"support_fragment_ids": []any{"fragment-1"},
		}},
		"support": map[string]any{
			"claims": []any{map[string]any{
				"claim_id":     "claim-1",
				"subject":      "assistant",
				"predicate":    "has_skill",
				"object":       "testing",
				"supported_by": []any{"fragment-1"},
			}},
			"fragments": []any{map[string]any{
				"fragment_id":    "fragment-1",
				"content":        "testing evidence",
				"source_type":    "conversation",
				"authority":      "primary",
				"source_quality": 0.9,
			}},
		},
	}

	exportTool, _ := reg.Get("export_memory_pack")
	if err := ValidateInput(exportTool, map[string]any{"name": "Pack", "claim_ids": []any{"claim-1"}, "include_support": false}); err != nil {
		t.Fatalf("export_memory_pack support flag ValidateInput: %v", err)
	}
	inspectTool, _ := reg.Get("inspect_memory_pack")
	if err := ValidateInput(inspectTool, map[string]any{"artifact": artifact}); err != nil {
		t.Fatalf("inspect_memory_pack support artifact ValidateInput: %v", err)
	}
	importTool, _ := reg.Get("import_memory_pack")
	if err := ValidateInput(importTool, map[string]any{"artifact": artifact, "mode": "review"}); err != nil {
		t.Fatalf("import_memory_pack support artifact ValidateInput: %v", err)
	}
}

func TestImportSkillPackReturnsRecoverableResultOnPartialError(t *testing.T) {
	skillPack := &stubSkillPackService{
		importResult: &skillpackservice.ImportResult{
			ImportID:     "import-rollback",
			ArtifactHash: "hash",
			Mode:         skillpackservice.ModeReview,
			Status:       "status_update_failed",
		},
		importErr: errors.New("update failed"),
	}
	reg, _ := BuildDefault(Dependencies{SkillPack: skillPack})
	tool, _ := reg.Get("import_memory_pack")

	out, err := tool.Invoke(context.Background(), "profile-skill", map[string]any{
		"mode":          "review",
		"artifact_json": `{"schema_version":"dense-mem.memory_pack.v1","name":"Pack","items":[{"subject":"assistant","predicate":"has_skill","object":"testing","source_kind":"manual"}]}`,
	})
	if err != nil {
		t.Fatalf("import_memory_pack Invoke: %v", err)
	}
	if out["import_id"] != "import-rollback" {
		t.Fatalf("import_id = %v; want import-rollback", out["import_id"])
	}
	if out["status"] != "status_update_failed" {
		t.Fatalf("status = %v; want status_update_failed", out["status"])
	}
	if out["error"] != "update failed" {
		t.Fatalf("error = %v; want update failed", out["error"])
	}
}

type stubRecall struct{}

func (stubRecall) Recall(ctx context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	return []recallservice.RecallHit{}, nil
}

type stubRecallWithHit struct{}

func (stubRecallWithHit) Recall(ctx context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	return []recallservice.RecallHit{{
		Fragment:     &domain.Fragment{FragmentID: "fragment-hit", ProfileID: profileID, Content: req.Query},
		Tier:         recallservice.TierFragment,
		Score:        0.75,
		SemanticRank: 1,
		KeywordRank:  2,
		FinalScore:   0.75,
	}}, nil
}

type stubRecallNilFragment struct{}

func (stubRecallNilFragment) Recall(ctx context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	return []recallservice.RecallHit{{Fragment: nil}}, nil
}

type stubMemoryReflectError struct{}

func (stubMemoryReflectError) Remember(ctx context.Context, profileID string, req memoryservice.RememberRequest) (*memoryservice.RememberResult, error) {
	return &memoryservice.RememberResult{}, nil
}

func (stubMemoryReflectError) GetMemoryPlacement(ctx context.Context, profileID string, req memoryservice.PlacementStatusRequest) (*memoryservice.PlacementStatusResult, error) {
	return &memoryservice.PlacementStatusResult{}, nil
}

func (stubMemoryReflectError) DisputeMemoryPlacement(ctx context.Context, profileID string, req memoryservice.DisputeRequest) (*memoryservice.DisputeResult, error) {
	return &memoryservice.DisputeResult{}, nil
}

func (stubMemoryReflectError) ImportMemories(ctx context.Context, profileID string, req memoryservice.ImportRequest) (*memoryservice.RememberResult, error) {
	return &memoryservice.RememberResult{}, nil
}

func (stubMemoryReflectError) Reflect(ctx context.Context, profileID string, req memoryservice.ReflectRequest) (*memoryservice.ReflectResult, error) {
	return nil, errors.New("reflect failed")
}

func (stubMemoryReflectError) ConfirmMemory(ctx context.Context, profileID string, req memoryservice.ConfirmRequest) (*memoryservice.ConfirmResult, error) {
	return &memoryservice.ConfirmResult{}, nil
}

type stubListCapture struct {
	opts fragmentservice.ListOptions
}

func (s *stubListCapture) List(ctx context.Context, profileID string, opts fragmentservice.ListOptions) ([]domain.Fragment, string, error) {
	s.opts = opts
	return []domain.Fragment{{FragmentID: "fragment-1", ProfileID: profileID}}, "cursor-2", nil
}

type stubMemory struct {
	lastProfile  string
	lastRemember memoryservice.RememberRequest
}

func (s *stubMemory) Remember(ctx context.Context, profileID string, req memoryservice.RememberRequest) (*memoryservice.RememberResult, error) {
	s.lastProfile = profileID
	s.lastRemember = req
	return &memoryservice.RememberResult{
		IngestID: "ingest-1",
		Status:   "queued",
	}, nil
}

func (s *stubMemory) GetMemoryPlacement(ctx context.Context, profileID string, req memoryservice.PlacementStatusRequest) (*memoryservice.PlacementStatusResult, error) {
	s.lastProfile = profileID
	return &memoryservice.PlacementStatusResult{
		Run: domain.MemoryPlacementRun{IngestID: req.IngestID, Status: domain.MemoryPlacementCompleted},
	}, nil
}

func (s *stubMemory) DisputeMemoryPlacement(ctx context.Context, profileID string, req memoryservice.DisputeRequest) (*memoryservice.DisputeResult, error) {
	s.lastProfile = profileID
	return &memoryservice.DisputeResult{
		Session: domain.MemoryDisputeSession{DisputeID: "dispute-1", Status: domain.MemoryDisputeOpen},
	}, nil
}

func (s *stubMemory) ImportMemories(ctx context.Context, profileID string, req memoryservice.ImportRequest) (*memoryservice.RememberResult, error) {
	s.lastProfile = profileID
	return &memoryservice.RememberResult{
		Fragment: memoryservice.FragmentOutcome{ID: "fragment-import", Status: "created"},
	}, nil
}

func (s *stubMemory) Reflect(ctx context.Context, profileID string, req memoryservice.ReflectRequest) (*memoryservice.ReflectResult, error) {
	s.lastProfile = profileID
	return &memoryservice.ReflectResult{}, nil
}

func (s *stubMemory) ConfirmMemory(ctx context.Context, profileID string, req memoryservice.ConfirmRequest) (*memoryservice.ConfirmResult, error) {
	s.lastProfile = profileID
	return &memoryservice.ConfirmResult{ClaimID: req.ClaimID, Decision: req.Decision, Status: "accepted"}, nil
}

// --- knowledge pipeline stubs ---

type stubClaimCreate struct {
	lastProfile string
}

func (s *stubClaimCreate) Create(ctx context.Context, profileID string, claim *domain.Claim) (*claimservice.CreateResult, error) {
	s.lastProfile = profileID
	return &claimservice.CreateResult{
		Claim: &domain.Claim{ClaimID: "c-1", ProfileID: profileID},
	}, nil
}

type stubClaimCreateDuplicate struct{}

func (stubClaimCreateDuplicate) Create(ctx context.Context, profileID string, claim *domain.Claim) (*claimservice.CreateResult, error) {
	return &claimservice.CreateResult{
		Claim:       &domain.Claim{ClaimID: "c-duplicate", ProfileID: profileID},
		Duplicate:   true,
		DuplicateOf: "c-original",
	}, nil
}

type stubClaimGet struct{}

func (stubClaimGet) Get(ctx context.Context, profileID, claimID string) (*domain.Claim, error) {
	return &domain.Claim{ClaimID: claimID, ProfileID: profileID}, nil
}

type stubClaimList struct{}

func (stubClaimList) List(ctx context.Context, profileID string, limit, offset int) ([]*domain.Claim, int, error) {
	return []*domain.Claim{{ClaimID: "c-1", ProfileID: profileID}}, 1, nil
}

type stubClaimVerify struct{}

func (stubClaimVerify) Verify(ctx context.Context, profileID, claimID string) (*domain.Claim, error) {
	return &domain.Claim{ClaimID: claimID, ProfileID: profileID, Status: domain.StatusValidated}, nil
}

type stubFactPromote struct{}

func (stubFactPromote) Promote(ctx context.Context, profileID, claimID string) (*domain.Fact, error) {
	return &domain.Fact{FactID: "f-1", ProfileID: profileID, PromotedFromClaimID: claimID}, nil
}

type stubFactGet struct{}

func (stubFactGet) Get(ctx context.Context, profileID, factID string) (*domain.Fact, error) {
	return &domain.Fact{
		FactID:     factID,
		ProfileID:  profileID,
		RecordedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		Evidence:   []domain.Evidence{{FragmentID: "fragment-1", ExtractConf: 0.9}},
	}, nil
}

type stubFactList struct{}

func (stubFactList) List(ctx context.Context, profileID string, filters factservice.FactListFilters, limit int, cursor string) ([]*domain.Fact, string, error) {
	return []*domain.Fact{{
		FactID:     "f-1",
		ProfileID:  profileID,
		Status:     domain.FactStatusActive,
		RecordedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		Evidence:   []domain.Evidence{{FragmentID: "fragment-1", ExtractConf: 0.9}},
	}}, "", nil
}

type stubFactListWithCursor struct{}

func (stubFactListWithCursor) List(ctx context.Context, profileID string, filters factservice.FactListFilters, limit int, cursor string) ([]*domain.Fact, string, error) {
	return []*domain.Fact{{
		FactID:     "f-cursor",
		ProfileID:  profileID,
		Status:     domain.FactStatusActive,
		RecordedAt: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		Evidence:   []domain.Evidence{{FragmentID: "fragment-evidence", ExtractConf: 0.9}},
	}}, "next-cursor", nil
}

type stubFragmentRetract struct {
	lastProfile string
}

func (s *stubFragmentRetract) Retract(ctx context.Context, profileID, fragmentID string) error {
	s.lastProfile = profileID
	return nil
}

type stubFactRetract struct {
	lastProfile string
}

func (s *stubFactRetract) Retract(ctx context.Context, profileID, factID string) error {
	s.lastProfile = profileID
	return nil
}

type stubCommunityDetect struct {
	lastProfile string
	lastOptions communityservice.DetectOptions
}

func (s *stubCommunityDetect) Detect(ctx context.Context, profileID string, opts communityservice.DetectOptions) error {
	s.lastProfile = profileID
	s.lastOptions = opts
	return nil
}

type stubCommunityGet struct{}

func (stubCommunityGet) Get(ctx context.Context, profileID string, communityID string) (*domain.Community, error) {
	return &domain.Community{CommunityID: communityID, ProfileID: profileID, MemberCount: 2}, nil
}

type stubCommunityList struct {
	lastProfile string
	err         error
}

func (s *stubCommunityList) List(ctx context.Context, profileID string, limit int) ([]*domain.Community, error) {
	s.lastProfile = profileID
	if s.err != nil {
		return nil, s.err
	}
	return []*domain.Community{{CommunityID: "community-1", ProfileID: profileID, MemberCount: 2}}, nil
}

type stubSkillPackService struct {
	lastProfile  string
	importResult *skillpackservice.ImportResult
	importErr    error
}

func (s *stubSkillPackService) FindCandidates(ctx context.Context, profileID string, req skillpackservice.FindCandidatesRequest) (*skillpackservice.FindCandidatesResult, error) {
	s.lastProfile = profileID
	return &skillpackservice.FindCandidatesResult{Candidates: []skillpackservice.Candidate{{
		ID:   "candidate-1",
		Type: "fact",
		Item: skillpackservice.SkillPackItem{
			Subject:    "assistant",
			Predicate:  "has_skill",
			Object:     req.Query,
			SourceKind: skillpackservice.SourceKindFact,
		},
	}}}, nil
}

func (s *stubSkillPackService) Export(ctx context.Context, profileID string, req skillpackservice.ExportRequest) (*skillpackservice.ExportResult, error) {
	s.lastProfile = profileID
	return &skillpackservice.ExportResult{
		Artifact: skillpackservice.SkillPack{
			SchemaVersion: skillpackservice.SchemaVersion,
			Name:          req.Name,
			Items: []skillpackservice.SkillPackItem{{
				Subject:    "assistant",
				Predicate:  "has_skill",
				Object:     "testing",
				SourceKind: skillpackservice.SourceKindManual,
			}},
		},
		CanonicalJSON: "{}",
		SHA256:        strings.Repeat("a", 64),
		ItemCount:     1,
		Filename:      "pack.memory-pack.json",
		ContentType:   "application/json",
	}, nil
}

func (s *stubSkillPackService) Inspect(ctx context.Context, profileID string, req skillpackservice.InspectRequest) (*skillpackservice.InspectResult, error) {
	s.lastProfile = profileID
	return &skillpackservice.InspectResult{
		ArtifactHash: "hash",
		Name:         "Pack",
		ItemCount:    1,
		Items:        []skillpackservice.InspectItem{{Index: 0, Status: "new"}},
	}, nil
}

func (s *stubSkillPackService) Import(ctx context.Context, profileID string, req skillpackservice.ImportRequest) (*skillpackservice.ImportResult, error) {
	s.lastProfile = profileID
	if s.importResult != nil || s.importErr != nil {
		return s.importResult, s.importErr
	}
	return &skillpackservice.ImportResult{
		ImportID:     "import-1",
		ArtifactHash: "hash",
		Mode:         req.Mode,
		Status:       domain.SkillPackImportStatusApplied,
		AppliedCount: 1,
	}, nil
}

func (s *stubSkillPackService) Rollback(ctx context.Context, profileID string, req skillpackservice.RollbackRequest) (*skillpackservice.RollbackResult, error) {
	s.lastProfile = profileID
	return &skillpackservice.RollbackResult{
		ImportID:      req.ImportID,
		Status:        domain.SkillPackImportStatusRolledBack,
		RevertedCount: 2,
	}, nil
}
