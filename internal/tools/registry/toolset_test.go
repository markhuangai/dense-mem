package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
	"github.com/markhuangai/dense-mem/internal/tools/graphquery"
	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
)

type stubCreate struct {
	called      int
	lastProfile string
	lastReq     *dto.CreateFragmentRequest
}

func (s *stubCreate) Create(ctx context.Context, profileID string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
	s.called++
	s.lastProfile = profileID
	s.lastReq = req
	return &fragmentservice.CreateResult{
		Fragment: &domain.Fragment{FragmentID: "f-1", ProfileID: profileID, Content: req.Content},
	}, nil
}

type stubGet struct{}

func (stubGet) GetByID(ctx context.Context, profileID, fragmentID string) (*domain.Fragment, error) {
	if fragmentID == "miss" {
		return nil, fragmentservice.ErrFragmentNotFound
	}
	return &domain.Fragment{FragmentID: fragmentID, ProfileID: profileID, Content: "hello"}, nil
}

type stubList struct{}

func (stubList) List(ctx context.Context, profileID string, opts fragmentservice.ListOptions) ([]domain.Fragment, string, error) {
	return []domain.Fragment{{FragmentID: "f-1", ProfileID: profileID}}, "", nil
}

type stubGraphQuery struct{}

func (stubGraphQuery) Execute(ctx context.Context, profileID string, query string, params map[string]any) (*graphquery.GraphQueryResult, error) {
	return &graphquery.GraphQueryResult{}, nil
}

type stubKeywordSearch struct {
	lastProfile string
	lastReq     *keywordsearch.KeywordSearchRequest
}

func (s *stubKeywordSearch) Search(ctx context.Context, profileID string, req *keywordsearch.KeywordSearchRequest) (*keywordsearch.KeywordSearchResult, error) {
	s.lastProfile = profileID
	s.lastReq = req
	return &keywordsearch.KeywordSearchResult{
		Data: []keywordsearch.SearchHit{{ID: "kw-1", Type: "fragment", Content: "hello", ProfileID: profileID}},
		Meta: keywordsearch.KeywordSearchMeta{LimitApplied: req.Limit},
	}, nil
}

type stubSemanticSearch struct {
	lastProfile string
	lastReq     *semanticsearch.SemanticSearchRequest
}

func (s *stubSemanticSearch) Search(ctx context.Context, profileID string, req *semanticsearch.SemanticSearchRequest) (*semanticsearch.SemanticSearchResult, error) {
	s.lastProfile = profileID
	s.lastReq = req
	return &semanticsearch.SemanticSearchResult{
		Data: []semanticsearch.SearchHit{{ID: "sem-1", Type: "fragment", Content: req.Query, ProfileID: profileID}},
		Meta: semanticsearch.SemanticSearchMeta{LimitApplied: req.Limit},
	}, nil
}

func TestBuildDefault_RegistersV1ToolSurface(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	required := []string{
		"save_memory", "get_memory", "list_recent_memories", "recall_memory",
		"trace_memory", "assemble_context",
		"remember", "import_memories", "reflect_memories", "confirm_memory",
		"dreaming_status", "run_dreaming_cycle", "list_dreams", "get_dream", "resolve_dream_feedback",
		"keyword_search", "semantic_search", "graph_query",
		"find_skill_pack_candidates", "export_skill_pack", "inspect_skill_pack",
		"import_skill_pack", "rollback_skill_pack_import",
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
	if _, ok := reg.Get("submit_recall_feedback"); !ok {
		t.Error("submit_recall_feedback should be registered; runtime config controls public visibility")
	}
}

func TestBuildDefault_SchemaFieldsPopulated(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{})
	tool, _ := reg.Get("save_memory")
	if tool.Description == "" {
		t.Error("save_memory description is empty")
	}
	if tool.InputSchema == nil || tool.OutputSchema == nil {
		t.Error("save_memory schemas must not be nil")
	}
	if len(tool.RequiredScopes) == 0 {
		t.Error("save_memory must declare required scopes")
	}
}

func TestBuildDefault_GraphQueryTimeoutMaximumIsConfigurable(t *testing.T) {
	reg, err := BuildDefault(Dependencies{GraphQuery: stubGraphQuery{}, GraphQueryMaxTimeoutSeconds: 5})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	tool, ok := reg.Get("graph_query")
	if !ok {
		t.Fatal("graph_query tool not registered")
	}

	properties := tool.InputSchema["properties"].(map[string]any)
	timeoutSchema := properties["timeout_seconds"].(map[string]any)
	if got, want := timeoutSchema["maximum"], 5; got != want {
		t.Fatalf("timeout_seconds maximum = %v, want %d", got, want)
	}
	if err := ValidateInput(tool, map[string]any{"query": "RETURN 1", "timeout_seconds": float64(6)}); err == nil {
		t.Fatal("ValidateInput expected timeout maximum error, got nil")
	}
	_, err = tool.Invoke(context.Background(), "profile-1", map[string]any{"query": "RETURN 1", "timeout_seconds": 6})
	if err == nil || !strings.Contains(err.Error(), "less than or equal to 5") {
		t.Fatalf("Invoke error = %v, want configured timeout maximum", err)
	}
}

func TestValidateInputRejectsNestedMemoryClaimViolations(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	tool, ok := reg.Get("remember")
	if !ok {
		t.Fatal("remember tool not registered")
	}

	args := map[string]any{
		"content": "The user likes Go.",
		"claims": []any{map[string]any{
			"subject":         "user",
			"predicate":       "likes",
			"object":          "Go",
			"extract_conf":    float64(99),
			"resolution_conf": float64(0.9),
		}},
	}

	err = ValidateInput(tool, args)
	if err == nil || !strings.Contains(err.Error(), "claims[0].extract_conf") {
		t.Fatalf("ValidateInput error = %v; want nested confidence validation", err)
	}
}

func TestValidateInputRejectsNestedSaveMemoryFields(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	tool, ok := reg.Get("save_memory")
	if !ok {
		t.Fatal("save_memory tool not registered")
	}

	properties := tool.InputSchema["properties"].(map[string]any)
	contentSchema := properties["content"].(map[string]any)
	if got, want := contentSchema["maxLength"], memoryEntryMaxLength; got != want {
		t.Fatalf("save_memory content maxLength = %v; want %d", got, want)
	}
	if err := ValidateInput(tool, map[string]any{
		"content": strings.Repeat("x", memoryEntryMaxLength+1),
	}); err == nil || !strings.Contains(err.Error(), "Split large scenarios") {
		t.Fatalf("ValidateInput long content error = %v; want split guidance", err)
	}

	if err := ValidateInput(tool, map[string]any{
		"content":     "hello",
		"source_type": "webhook",
	}); err == nil || !strings.Contains(err.Error(), "source_type") {
		t.Fatalf("ValidateInput invalid enum error = %v; want source_type validation", err)
	}

	if err := ValidateInput(tool, map[string]any{
		"content": "hello",
		"labels":  []any{strings.Repeat("x", 65)},
	}); err == nil || !strings.Contains(err.Error(), "labels[0]") {
		t.Fatalf("ValidateInput invalid label error = %v; want labels[0] validation", err)
	}
}

func TestBuildDefault_SaveInvokerCallsService(t *testing.T) {
	create := &stubCreate{}
	reg, _ := BuildDefault(Dependencies{
		FragmentCreate: create,
	})
	tool, _ := reg.Get("save_memory")
	out, err := tool.Invoke(context.Background(), "pA", map[string]any{"content": "hello"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if create.called != 1 {
		t.Errorf("service called %d times; want 1", create.called)
	}
	if create.lastProfile != "pA" {
		t.Errorf("service profile = %q; want pA", create.lastProfile)
	}
	if out["status"] != "created" {
		t.Errorf("output status = %v; want created", out["status"])
	}
	if out["id"] != "f-1" {
		t.Errorf("output id = %v; want f-1", out["id"])
	}
}

func TestBuildDefault_InvokerReturnsUnavailableWhenDepsMissing(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{}) // nothing wired
	tool, _ := reg.Get("save_memory")
	_, err := tool.Invoke(context.Background(), "pA", map[string]any{"content": "x"})
	if !errors.Is(err, ErrToolUnavailable) {
		t.Errorf("err = %v; want ErrToolUnavailable", err)
	}
}

func TestBuildDefault_V1InvokersReturnUnavailableWhenDepsMissing(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{})
	cases := []struct {
		name  string
		input map[string]any
	}{
		{name: "get_memory", input: map[string]any{"id": "fragment-1"}},
		{name: "list_recent_memories", input: map[string]any{}},
		{name: "recall_memory", input: map[string]any{"query": "hello"}},
		{name: "trace_memory", input: map[string]any{"type": "fact", "id": "fact-1"}},
		{name: "assemble_context", input: map[string]any{"query": "hello"}},
		{name: "dreaming_status", input: map[string]any{}},
		{name: "run_dreaming_cycle", input: map[string]any{}},
		{name: "list_dreams", input: map[string]any{}},
		{name: "get_dream", input: map[string]any{"dream_id": "dream-1"}},
		{name: "resolve_dream_feedback", input: map[string]any{"dream_id": "dream-1", "decision": "reject"}},
		{name: "keyword_search", input: map[string]any{"keywords": "hello"}},
		{name: "semantic_search", input: map[string]any{"embedding": []any{float64(0.1)}}},
		{name: "graph_query", input: map[string]any{"query": "MATCH (n) RETURN n"}},
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

func TestBuildDefault_V1InvokerInvalidInputBranches(t *testing.T) {
	cases := []struct {
		name string
		deps Dependencies
		in   map[string]any
		want string
	}{
		{
			name: "save_memory",
			deps: Dependencies{FragmentCreate: &stubCreate{}},
			in:   map[string]any{"content": func() {}},
			want: "save_memory: invalid input",
		},
		{
			name: "recall_memory",
			deps: Dependencies{Recall: stubRecall{}},
			in:   map[string]any{"query": func() {}},
			want: "recall_memory: invalid input",
		},
		{
			name: "keyword_search",
			deps: Dependencies{KeywordSearch: &stubKeywordSearch{}},
			in:   map[string]any{"keywords": func() {}},
			want: "keyword_search: invalid input",
		},
		{
			name: "semantic_search",
			deps: Dependencies{SemanticSearch: &stubSemanticSearch{}},
			in:   map[string]any{"embedding": func() {}},
			want: "semantic_search: invalid input",
		},
		{
			name: "run_dreaming_cycle",
			deps: Dependencies{Dreams: &stubDreamService{}},
			in:   map[string]any{"max_outputs": func() {}},
			want: "run_dreaming_cycle: invalid input",
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

func TestBuildDefault_GetInvokerWraps(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{FragmentGet: stubGet{}})
	tool, _ := reg.Get("get_memory")
	out, err := tool.Invoke(context.Background(), "pA", map[string]any{"id": "f-42"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out["id"] != "f-42" {
		t.Errorf("out[id] = %v; want f-42", out["id"])
	}
}

func TestBuildDefault_ListInvokerWraps(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{FragmentList: stubList{}})
	tool, _ := reg.Get("list_recent_memories")
	out, err := tool.Invoke(context.Background(), "pA", map[string]any{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	items, ok := out["items"].([]map[string]any)
	if !ok {
		t.Fatalf("items has type %T; want []map[string]any", out["items"])
	}
	if len(items) != 1 {
		t.Errorf("items length = %d; want 1", len(items))
	}
	if out["has_more"] != false {
		t.Errorf("has_more = %v; want false", out["has_more"])
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

func TestBuildDefault_SearchAndRecallInvokers(t *testing.T) {
	keyword := &stubKeywordSearch{}
	semantic := &stubSemanticSearch{}
	memory := &stubMemory{}
	recall := stubRecallWithHit{}
	dreams := &stubDreamService{}
	reg, _ := BuildDefault(Dependencies{
		KeywordSearch:  keyword,
		SemanticSearch: semantic,
		Recall:         recall,
		Memory:         memory,
		Dreams:         dreams,
		GraphQuery:     stubGraphQuery{},
	})

	keywordTool, _ := reg.Get("keyword_search")
	keywordOut, err := keywordTool.Invoke(context.Background(), "profile-search", map[string]any{
		"keywords": "hello",
		"limit":    float64(7),
		"labels":   []any{"work"},
	})
	if err != nil {
		t.Fatalf("keyword_search Invoke: %v", err)
	}
	if keyword.lastProfile != "profile-search" || keyword.lastReq.Query != "hello" || keyword.lastReq.Limit != 7 {
		t.Fatalf("keyword_search request = profile %q req %+v", keyword.lastProfile, keyword.lastReq)
	}
	if keywordOut["meta"] == nil {
		t.Fatalf("keyword_search output missing meta: %v", keywordOut)
	}

	semanticTool, _ := reg.Get("semantic_search")
	semanticOut, err := semanticTool.Invoke(context.Background(), "profile-search", map[string]any{
		"query":     "hello",
		"embedding": []any{float64(0.1), float64(0.2)},
		"limit":     float64(3),
	})
	if err != nil {
		t.Fatalf("semantic_search Invoke: %v", err)
	}
	if semantic.lastProfile != "profile-search" || semantic.lastReq.Query != "hello" || semantic.lastReq.Limit != 3 {
		t.Fatalf("semantic_search request = profile %q req %+v", semantic.lastProfile, semantic.lastReq)
	}
	if semanticOut["data"] == nil {
		t.Fatalf("semantic_search output missing data: %v", semanticOut)
	}

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

	graphTool, _ := reg.Get("graph_query")
	graphOut, err := graphTool.Invoke(context.Background(), "profile-search", map[string]any{
		"query":           "MATCH (n) RETURN n",
		"parameters":      map[string]any{"x": 1},
		"timeout_seconds": float64(1),
	})
	if err != nil {
		t.Fatalf("graph_query Invoke: %v", err)
	}
	if graphOut == nil {
		t.Fatal("graph_query output is nil")
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
		{name: "find_skill_pack_candidates", input: map[string]any{"query": "skill packs", "limit": float64(5)}, wantField: "candidates"},
		{name: "export_skill_pack", input: map[string]any{"name": "Pack", "manual_items": []any{map[string]any{"subject": "assistant", "predicate": "has_skill", "object": "testing", "source_kind": "manual"}}}, wantField: "sha256", wantValue: strings.Repeat("a", 64)},
		{name: "inspect_skill_pack", input: map[string]any{"artifact_json": `{"schema_version":"dense-mem.skill_pack.v1","name":"Pack","items":[{"subject":"assistant","predicate":"has_skill","object":"testing","source_kind":"manual"}]}`}, wantField: "artifact_hash", wantValue: "hash"},
		{name: "import_skill_pack", input: map[string]any{"mode": "review", "artifact_json": `{"schema_version":"dense-mem.skill_pack.v1","name":"Pack","items":[{"subject":"assistant","predicate":"has_skill","object":"testing","source_kind":"manual"}]}`}, wantField: "import_id", wantValue: "import-1"},
		{name: "rollback_skill_pack_import", input: map[string]any{"import_id": "import-1"}, wantField: "status", wantValue: "rolled_back"},
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
		{name: "find_skill_pack_candidates", in: map[string]any{"query": func() {}}, want: "find_skill_pack_candidates: invalid input"},
		{name: "export_skill_pack", in: map[string]any{"name": func() {}}, want: "export_skill_pack: invalid input"},
		{name: "inspect_skill_pack", in: map[string]any{"artifact_json": func() {}}, want: "inspect_skill_pack: invalid input"},
		{name: "import_skill_pack", in: map[string]any{"mode": func() {}}, want: "import_skill_pack: invalid input"},
		{name: "rollback_skill_pack_import", in: map[string]any{"import_id": func() {}}, want: "rollback_skill_pack_import: invalid input"},
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
		"schema_version": "dense-mem.skill_pack.v1",
		"name":           "Pack",
		"items":          items,
	}

	exportTool, _ := reg.Get("export_skill_pack")
	if err := ValidateInput(exportTool, map[string]any{"name": "Pack", "fact_ids": factIDs}); err != nil {
		t.Fatalf("export_skill_pack ValidateInput: %v", err)
	}
	inspectTool, _ := reg.Get("inspect_skill_pack")
	if err := ValidateInput(inspectTool, map[string]any{"artifact": artifact}); err != nil {
		t.Fatalf("inspect_skill_pack ValidateInput: %v", err)
	}
	importTool, _ := reg.Get("import_skill_pack")
	if err := ValidateInput(importTool, map[string]any{
		"artifact":           artifact,
		"mode":               "review",
		"selected_items":     selectedItems,
		"conflict_decisions": conflictDecisions,
	}); err != nil {
		t.Fatalf("import_skill_pack ValidateInput: %v", err)
	}
}

func TestSkillPackSchemasAcceptSupportGraph(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{SkillPack: &stubSkillPackService{}})
	artifact := map[string]any{
		"schema_version": "dense-mem.skill_pack.v1",
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

	exportTool, _ := reg.Get("export_skill_pack")
	if err := ValidateInput(exportTool, map[string]any{"name": "Pack", "claim_ids": []any{"claim-1"}, "include_support": false}); err != nil {
		t.Fatalf("export_skill_pack support flag ValidateInput: %v", err)
	}
	inspectTool, _ := reg.Get("inspect_skill_pack")
	if err := ValidateInput(inspectTool, map[string]any{"artifact": artifact}); err != nil {
		t.Fatalf("inspect_skill_pack support artifact ValidateInput: %v", err)
	}
	importTool, _ := reg.Get("import_skill_pack")
	if err := ValidateInput(importTool, map[string]any{"artifact": artifact, "mode": "review"}); err != nil {
		t.Fatalf("import_skill_pack support artifact ValidateInput: %v", err)
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
	tool, _ := reg.Get("import_skill_pack")

	out, err := tool.Invoke(context.Background(), "profile-skill", map[string]any{
		"mode":          "review",
		"artifact_json": `{"schema_version":"dense-mem.skill_pack.v1","name":"Pack","items":[{"subject":"assistant","predicate":"has_skill","object":"testing","source_kind":"manual"}]}`,
	})
	if err != nil {
		t.Fatalf("import_skill_pack Invoke: %v", err)
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

func TestBuildDefault_FragmentInvokerEdgeBranches(t *testing.T) {
	t.Run("save duplicate includes duplicate_of", func(t *testing.T) {
		reg, _ := BuildDefault(Dependencies{FragmentCreate: duplicateCreate{}})
		tool, _ := reg.Get("save_memory")

		out, err := tool.Invoke(context.Background(), "profileA", map[string]any{"content": "hello"})

		if err != nil {
			t.Fatalf("save_memory Invoke: %v", err)
		}
		if out["status"] != "duplicate" || out["duplicate_of"] != "fragment-original" {
			t.Fatalf("save_memory duplicate out = %v", out)
		}
	})

	t.Run("get memory requires id", func(t *testing.T) {
		reg, _ := BuildDefault(Dependencies{FragmentGet: stubGet{}})
		tool, _ := reg.Get("get_memory")

		_, err := tool.Invoke(context.Background(), "profileA", map[string]any{})

		if err == nil || !strings.Contains(err.Error(), "id is required") {
			t.Fatalf("get_memory err = %v, want id required", err)
		}
	})

	t.Run("list memory maps options and cursor", func(t *testing.T) {
		list := &stubListCapture{}
		reg, _ := BuildDefault(Dependencies{FragmentList: list})
		tool, _ := reg.Get("list_recent_memories")

		out, err := tool.Invoke(context.Background(), "profileA", map[string]any{
			"limit":       float64(5),
			"cursor":      "next-1",
			"source_type": "manual",
		})

		if err != nil {
			t.Fatalf("list_recent_memories Invoke: %v", err)
		}
		if list.opts.Limit != 5 || list.opts.Cursor != "next-1" || list.opts.SourceType != "manual" {
			t.Fatalf("list options = %+v", list.opts)
		}
		if out["has_more"] != true || out["next_cursor"] != "cursor-2" {
			t.Fatalf("list output = %v", out)
		}
	})
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

func (stubMemoryReflectError) ImportMemories(ctx context.Context, profileID string, req memoryservice.ImportRequest) (*memoryservice.RememberResult, error) {
	return &memoryservice.RememberResult{}, nil
}

func (stubMemoryReflectError) Reflect(ctx context.Context, profileID string, req memoryservice.ReflectRequest) (*memoryservice.ReflectResult, error) {
	return nil, errors.New("reflect failed")
}

func (stubMemoryReflectError) ConfirmMemory(ctx context.Context, profileID string, req memoryservice.ConfirmRequest) (*memoryservice.ConfirmResult, error) {
	return &memoryservice.ConfirmResult{}, nil
}

type duplicateCreate struct{}

func (duplicateCreate) Create(ctx context.Context, profileID string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
	return &fragmentservice.CreateResult{
		Fragment:    &domain.Fragment{FragmentID: "fragment-duplicate", ProfileID: profileID, Content: req.Content},
		Duplicate:   true,
		DuplicateOf: "fragment-original",
	}, nil
}

type stubListCapture struct {
	opts fragmentservice.ListOptions
}

func (s *stubListCapture) List(ctx context.Context, profileID string, opts fragmentservice.ListOptions) ([]domain.Fragment, string, error) {
	s.opts = opts
	return []domain.Fragment{{FragmentID: "fragment-1", ProfileID: profileID}}, "cursor-2", nil
}

type stubMemory struct {
	lastProfile string
}

func (s *stubMemory) Remember(ctx context.Context, profileID string, req memoryservice.RememberRequest) (*memoryservice.RememberResult, error) {
	s.lastProfile = profileID
	return &memoryservice.RememberResult{
		Fragment: memoryservice.FragmentOutcome{ID: "fragment-1", Status: "created"},
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
		Filename:      "pack.skill-pack.json",
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
