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
		"remember", "import_memories", "reflect_memories", "confirm_memory",
		"keyword-search", "semantic-search", "graph-query",
	}
	for _, name := range required {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
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
	tool, ok := reg.Get("graph-query")
	if !ok {
		t.Fatal("graph-query tool not registered")
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
		{name: "keyword-search", input: map[string]any{"keywords": "hello"}},
		{name: "semantic-search", input: map[string]any{"embedding": []any{float64(0.1)}}},
		{name: "graph-query", input: map[string]any{"query": "MATCH (n) RETURN n"}},
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
			name: "keyword-search",
			deps: Dependencies{KeywordSearch: &stubKeywordSearch{}},
			in:   map[string]any{"keywords": func() {}},
			want: "keyword-search: invalid input",
		},
		{
			name: "semantic-search",
			deps: Dependencies{SemanticSearch: &stubSemanticSearch{}},
			in:   map[string]any{"embedding": func() {}},
			want: "semantic-search: invalid input",
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
	reg, _ := BuildDefault(Dependencies{
		KeywordSearch:  keyword,
		SemanticSearch: semantic,
		Recall:         recall,
		Memory:         memory,
		GraphQuery:     stubGraphQuery{},
	})

	keywordTool, _ := reg.Get("keyword-search")
	keywordOut, err := keywordTool.Invoke(context.Background(), "profile-search", map[string]any{
		"keywords": "hello",
		"limit":    float64(7),
		"labels":   []any{"work"},
	})
	if err != nil {
		t.Fatalf("keyword-search Invoke: %v", err)
	}
	if keyword.lastProfile != "profile-search" || keyword.lastReq.Query != "hello" || keyword.lastReq.Limit != 7 {
		t.Fatalf("keyword-search request = profile %q req %+v", keyword.lastProfile, keyword.lastReq)
	}
	if keywordOut["meta"] == nil {
		t.Fatalf("keyword-search output missing meta: %v", keywordOut)
	}

	semanticTool, _ := reg.Get("semantic-search")
	semanticOut, err := semanticTool.Invoke(context.Background(), "profile-search", map[string]any{
		"query":     "hello",
		"embedding": []any{float64(0.1), float64(0.2)},
		"limit":     float64(3),
	})
	if err != nil {
		t.Fatalf("semantic-search Invoke: %v", err)
	}
	if semantic.lastProfile != "profile-search" || semantic.lastReq.Query != "hello" || semantic.lastReq.Limit != 3 {
		t.Fatalf("semantic-search request = profile %q req %+v", semantic.lastProfile, semantic.lastReq)
	}
	if semanticOut["data"] == nil {
		t.Fatalf("semantic-search output missing data: %v", semanticOut)
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
	if memory.lastProfile != "profile-search" {
		t.Fatalf("recall reflection profile = %q, want profile-search", memory.lastProfile)
	}

	graphTool, _ := reg.Get("graph-query")
	graphOut, err := graphTool.Invoke(context.Background(), "profile-search", map[string]any{
		"query":           "MATCH (n) RETURN n",
		"parameters":      map[string]any{"x": 1},
		"timeout_seconds": float64(1),
	})
	if err != nil {
		t.Fatalf("graph-query Invoke: %v", err)
	}
	if graphOut == nil {
		t.Fatal("graph-query output is nil")
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

func TestToolsetHelperBranches(t *testing.T) {
	if got, ok := intInput(int64(7)); !ok || got != 7 {
		t.Fatalf("intInput int64 = %d, %v; want 7, true", got, ok)
	}
	if got, ok := intInput(float64(8)); !ok || got != 8 {
		t.Fatalf("intInput float64 = %d, %v; want 8, true", got, ok)
	}
	if _, ok := intInput(float64(8.5)); ok {
		t.Fatal("intInput non-integral float ok = true, want false")
	}
	if _, ok := intInput("8"); ok {
		t.Fatal("intInput string ok = true, want false")
	}
	if err := remapInput(map[string]any{"bad": func() {}}, &struct{}{}); err == nil {
		t.Fatal("remapInput with unmarshalable value: want error")
	}
	if _, err := structToMap(map[string]any{"bad": func() {}}); err == nil {
		t.Fatal("structToMap with unmarshalable value: want error")
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

// --- knowledge pipeline tests ---

// TestBuildDefaultIncludesKnowledgeTools verifies all 9 knowledge pipeline
// tools are registered regardless of whether their dependencies are wired.
func TestBuildDefaultIncludesKnowledgeTools(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	required := []string{
		"post_claim", "get_claim", "list_claims", "verify_claim",
		"promote_claim", "get_fact", "list_facts",
		"retract_fragment", "detect_community", "get_community_summary", "list_communities",
	}
	for _, name := range required {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}

func TestBuildDefaultKnowledgeTools_ReturnUnavailableWhenDepsMissing(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{})
	cases := []struct {
		name  string
		input map[string]any
	}{
		{"post_claim", map[string]any{"supported_by": []any{"fragment-1"}}},
		{"get_claim", map[string]any{"id": "claim-1"}},
		{"list_claims", map[string]any{}},
		{"verify_claim", map[string]any{"id": "claim-1"}},
		{"promote_claim", map[string]any{"claim_id": "claim-1"}},
		{"get_fact", map[string]any{"id": "fact-1"}},
		{"list_facts", map[string]any{}},
		{"remember", map[string]any{"content": "hello"}},
		{"import_memories", map[string]any{"summary": "hello"}},
		{"reflect_memories", map[string]any{}},
		{"confirm_memory", map[string]any{"claim_id": "claim-1", "decision": "keep_existing"}},
		{"retract_fragment", map[string]any{"id": "fragment-1"}},
		{"detect_community", map[string]any{}},
		{"get_community_summary", map[string]any{"community_id": "community-1"}},
		{"list_communities", map[string]any{}},
	}
	for _, tc := range cases {
		tool, _ := reg.Get(tc.name)
		if _, err := tool.Invoke(context.Background(), "profileA", tc.input); !errors.Is(err, ErrToolUnavailable) {
			t.Errorf("%s err = %v; want ErrToolUnavailable", tc.name, err)
		}
	}
}

func TestBuildDefaultMemoryTools_InvokeAndScope(t *testing.T) {
	mem := &stubMemory{}
	reg, _ := BuildDefault(Dependencies{Memory: mem})

	cases := []struct {
		name  string
		input map[string]any
		scope string
	}{
		{"remember", map[string]any{"content": "hello"}, "write"},
		{"import_memories", map[string]any{"summary": "old chats"}, "write"},
		{"reflect_memories", map[string]any{}, "read"},
		{"confirm_memory", map[string]any{"claim_id": "claim-1", "decision": "keep_existing"}, "write"},
	}
	for _, tc := range cases {
		tool, ok := reg.Get(tc.name)
		if !ok {
			t.Fatalf("%s not registered", tc.name)
		}
		if len(tool.RequiredScopes) != 1 || tool.RequiredScopes[0] != tc.scope {
			t.Fatalf("%s scopes = %v; want [%s]", tc.name, tool.RequiredScopes, tc.scope)
		}
		if _, err := tool.Invoke(context.Background(), "profile-memory", tc.input); err != nil {
			t.Fatalf("%s Invoke: %v", tc.name, err)
		}
		if mem.lastProfile != "profile-memory" {
			t.Fatalf("%s routed to %q; want profile-memory", tc.name, mem.lastProfile)
		}
	}
}

func TestBuildDefaultMemoryTools_InvalidInputBranches(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{Memory: &stubMemory{}})

	for _, tc := range []struct {
		name string
		in   map[string]any
		want string
	}{
		{name: "remember", in: map[string]any{"content": func() {}}, want: "remember: invalid input"},
		{name: "import_memories", in: map[string]any{"summary": func() {}}, want: "import_memories: invalid input"},
		{name: "reflect_memories", in: map[string]any{"limit": func() {}}, want: "reflect_memories: invalid input"},
		{name: "confirm_memory", in: map[string]any{"claim_id": func() {}}, want: "confirm_memory: invalid input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool, _ := reg.Get(tc.name)
			_, err := tool.Invoke(context.Background(), "profile-memory", tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s err = %v; want %q", tc.name, err, tc.want)
			}
		})
	}

	confirm, _ := reg.Get("confirm_memory")
	if _, err := confirm.Invoke(context.Background(), "profile-memory", map[string]any{"decision": "keep_existing"}); err == nil || !strings.Contains(err.Error(), "claim_id is required") {
		t.Fatalf("confirm missing claim_id err = %v", err)
	}
	if _, err := confirm.Invoke(context.Background(), "profile-memory", map[string]any{"claim_id": "claim-1"}); err == nil || !strings.Contains(err.Error(), "decision is required") {
		t.Fatalf("confirm missing decision err = %v", err)
	}
}

func TestBuildDefault_RecallInvokerErrorBranches(t *testing.T) {
	t.Run("nil fragment hit returns mapping error", func(t *testing.T) {
		reg, _ := BuildDefault(Dependencies{Recall: stubRecallNilFragment{}})
		tool, _ := reg.Get("recall_memory")

		_, err := tool.Invoke(context.Background(), "profile-memory", map[string]any{"query": "hello"})

		if err == nil || !strings.Contains(err.Error(), "hit missing fragment") {
			t.Fatalf("err = %v; want hit missing fragment", err)
		}
	})

	t.Run("memory reflection error propagates", func(t *testing.T) {
		reg, _ := BuildDefault(Dependencies{Recall: stubRecall{}, Memory: stubMemoryReflectError{}})
		tool, _ := reg.Get("recall_memory")

		_, err := tool.Invoke(context.Background(), "profile-memory", map[string]any{"query": "hello"})

		if err == nil || !strings.Contains(err.Error(), "reflect failed") {
			t.Fatalf("err = %v; want reflect failed", err)
		}
	})
}

// TestBuildDefaultKnowledgeTools_CrossProfileIsolation verifies that each
// knowledge tool's invoker passes the profileID argument through to the
// service — no cross-profile data leakage is possible at the tool layer.
func TestBuildDefaultKnowledgeTools_CrossProfileIsolation(t *testing.T) {
	retract := &stubFragmentRetract{}
	detect := &stubCommunityDetect{}
	communities := &stubCommunityList{}
	reg, _ := BuildDefault(Dependencies{
		ClaimGet:        stubClaimGet{},
		FragmentRetract: retract,
		CommunityDetect: detect,
		CommunityList:   communities,
	})

	// retract_fragment — verify profileID routing.
	tool, _ := reg.Get("retract_fragment")
	if _, err := tool.Invoke(context.Background(), "profileA", map[string]any{"id": "frag-1"}); err != nil {
		t.Fatalf("retract_fragment profileA: %v", err)
	}
	if retract.lastProfile != "profileA" {
		t.Errorf("retract_fragment routed to %q; want profileA", retract.lastProfile)
	}
	if _, err := tool.Invoke(context.Background(), "profileB", map[string]any{"id": "frag-2"}); err != nil {
		t.Fatalf("retract_fragment profileB: %v", err)
	}
	if retract.lastProfile != "profileB" {
		t.Errorf("retract_fragment routed to %q after second call; want profileB", retract.lastProfile)
	}

	// get_claim — verify that each profile receives only its own scoped data.
	claimTool, _ := reg.Get("get_claim")
	aResult, err := claimTool.Invoke(context.Background(), "profileA", map[string]any{"id": "c-shared-id"})
	if err != nil {
		t.Fatalf("get_claim profileA: %v", err)
	}
	bResult, err := claimTool.Invoke(context.Background(), "profileB", map[string]any{"id": "c-shared-id"})
	if err != nil {
		t.Fatalf("get_claim profileB: %v", err)
	}
	aProfile, _ := aResult["team_id"].(string)
	bProfile, _ := bResult["team_id"].(string)
	if aProfile != "profileA" {
		t.Errorf("get_claim profileA result has team_id=%q; want profileA", aProfile)
	}
	if bProfile != "profileB" {
		t.Errorf("get_claim profileB result has team_id=%q; want profileB", bProfile)
	}
	// The cross-profile isolation invariant: B's result must not contain A's data.
	if bProfile == "profileA" {
		t.Error("cross-profile isolation failure: profileB received profileA-scoped data")
	}

	communityTool, _ := reg.Get("list_communities")
	if _, err := communityTool.Invoke(context.Background(), "profileA", map[string]any{}); err != nil {
		t.Fatalf("list_communities profileA: %v", err)
	}
	if communities.lastProfile != "profileA" {
		t.Errorf("list_communities routed to %q; want profileA", communities.lastProfile)
	}

	detectTool, _ := reg.Get("detect_community")
	if _, err := detectTool.Invoke(context.Background(), "profileB", map[string]any{
		"gamma":      1.4,
		"tolerance":  0.0003,
		"max_levels": 4,
	}); err != nil {
		t.Fatalf("detect_community profileB: %v", err)
	}
	if detect.lastProfile != "profileB" {
		t.Errorf("detect_community routed to %q; want profileB", detect.lastProfile)
	}
	if detect.lastOptions != (communityservice.DetectOptions{
		Gamma:     1.4,
		Tolerance: 0.0003,
		MaxLevels: 4,
	}) {
		t.Errorf("detect_community options = %+v; want gamma/tolerance/max_levels passthrough", detect.lastOptions)
	}
	if _, err := detectTool.Invoke(context.Background(), "profileB", map[string]any{"gamma": -1.0}); err == nil {
		t.Fatal("detect_community with invalid gamma: want validation error")
	}
}

func TestBuildDefaultKnowledgeTools_InvokeSuccessPaths(t *testing.T) {
	retract := &stubFragmentRetract{}
	detect := &stubCommunityDetect{}
	communities := &stubCommunityList{}
	reg, _ := BuildDefault(Dependencies{
		ClaimCreate:     &stubClaimCreate{},
		ClaimGet:        stubClaimGet{},
		ClaimList:       stubClaimList{},
		ClaimVerify:     stubClaimVerify{},
		FactPromote:     stubFactPromote{},
		FactGet:         stubFactGet{},
		FactList:        stubFactList{},
		FragmentRetract: retract,
		CommunityDetect: detect,
		CommunityGet:    stubCommunityGet{},
		CommunityList:   communities,
	})

	cases := []struct {
		name      string
		input     map[string]any
		wantField string
		wantValue any
	}{
		{
			name: "post_claim",
			input: map[string]any{
				"subject":      "Alice",
				"predicate":    "knows",
				"object":       "Bob",
				"supported_by": []any{"fragment-1"},
			},
			wantField: "claim_id",
			wantValue: "c-1",
		},
		{name: "get_claim", input: map[string]any{"id": "claim-1"}, wantField: "claim_id", wantValue: "claim-1"},
		{name: "verify_claim", input: map[string]any{"id": "claim-1"}, wantField: "status", wantValue: string(domain.StatusValidated)},
		{name: "promote_claim", input: map[string]any{"claim_id": "claim-1"}, wantField: "promoted_from_claim_id", wantValue: "claim-1"},
		{name: "get_fact", input: map[string]any{"id": "fact-1", "include_evidence": true}, wantField: "fact_id", wantValue: "fact-1"},
		{name: "get_community_summary", input: map[string]any{"community_id": "community-1"}, wantField: "community_id", wantValue: "community-1"},
	}
	for _, tc := range cases {
		tool, _ := reg.Get(tc.name)
		out, err := tool.Invoke(context.Background(), "profile-success", tc.input)
		if err != nil {
			t.Fatalf("%s Invoke: %v", tc.name, err)
		}
		if out[tc.wantField] != tc.wantValue {
			t.Fatalf("%s %s = %v; want %v", tc.name, tc.wantField, out[tc.wantField], tc.wantValue)
		}
		if out["team_id"] != "profile-success" {
			t.Fatalf("%s team_id = %v; want profile-success", tc.name, out["team_id"])
		}
	}

	listClaims, _ := reg.Get("list_claims")
	claimsOut, err := listClaims.Invoke(context.Background(), "profile-success", map[string]any{"limit": float64(1)})
	if err != nil {
		t.Fatalf("list_claims Invoke: %v", err)
	}
	if claimsOut["total"] != 1 {
		t.Fatalf("list_claims total = %v; want 1", claimsOut["total"])
	}
	if claimsOut["has_more"] != false {
		t.Fatalf("list_claims has_more = %v; want false", claimsOut["has_more"])
	}

	listFacts, _ := reg.Get("list_facts")
	factsOut, err := listFacts.Invoke(context.Background(), "profile-success", map[string]any{
		"limit":            float64(1),
		"subject":          "Alice",
		"status":           string(domain.FactStatusActive),
		"include_evidence": false,
	})
	if err != nil {
		t.Fatalf("list_facts Invoke: %v", err)
	}
	if factsOut["has_more"] != false {
		t.Fatalf("list_facts has_more = %v; want false", factsOut["has_more"])
	}
	factItems := factsOut["items"].([]map[string]any)
	if len(factItems) != 1 || factItems[0]["fact_id"] != "f-1" {
		t.Fatalf("list_facts items = %v; want one fact f-1", factsOut["items"])
	}
	if _, ok := factItems[0]["evidence"]; ok {
		t.Fatalf("list_facts evidence key present despite include_evidence=false: %v", factItems[0]["evidence"])
	}

	retractTool, _ := reg.Get("retract_fragment")
	if _, err := retractTool.Invoke(context.Background(), "profile-success", map[string]any{"id": "fragment-1"}); err != nil {
		t.Fatalf("retract_fragment Invoke: %v", err)
	}
	if retract.lastProfile != "profile-success" {
		t.Fatalf("retract_fragment profile = %q; want profile-success", retract.lastProfile)
	}

	detectTool, _ := reg.Get("detect_community")
	detectOut, err := detectTool.Invoke(context.Background(), "profile-success", map[string]any{"gamma": 1.2})
	if err != nil {
		t.Fatalf("detect_community Invoke: %v", err)
	}
	if detectOut["community_count"] != 1 {
		t.Fatalf("detect_community community_count = %v; want 1", detectOut["community_count"])
	}
	if detect.lastOptions.Gamma != 1.2 {
		t.Fatalf("detect_community gamma = %v; want 1.2", detect.lastOptions.Gamma)
	}

	listCommunities, _ := reg.Get("list_communities")
	communitiesOut, err := listCommunities.Invoke(context.Background(), "profile-success", map[string]any{"limit": float64(1)})
	if err != nil {
		t.Fatalf("list_communities Invoke: %v", err)
	}
	if communitiesOut["total"] != 1 {
		t.Fatalf("list_communities total = %v; want 1", communitiesOut["total"])
	}
	if communities.lastProfile != "profile-success" {
		t.Fatalf("list_communities profile = %q; want profile-success", communities.lastProfile)
	}
}

func TestBuildDefaultKnowledgeTools_AdditionalBranches(t *testing.T) {
	t.Run("post_claim duplicate output", func(t *testing.T) {
		reg, _ := BuildDefault(Dependencies{ClaimCreate: stubClaimCreateDuplicate{}})
		tool, _ := reg.Get("post_claim")

		out, err := tool.Invoke(context.Background(), "profile-branch", map[string]any{
			"subject":      "Alice",
			"predicate":    "knows",
			"object":       "Bob",
			"supported_by": []any{"fragment-1"},
		})

		if err != nil {
			t.Fatalf("post_claim Invoke: %v", err)
		}
		if out["duplicate"] != true || out["duplicate_of"] != "c-original" {
			t.Fatalf("post_claim duplicate output = %v", out)
		}
	})

	t.Run("list_facts keeps evidence and exposes next cursor", func(t *testing.T) {
		reg, _ := BuildDefault(Dependencies{FactList: stubFactListWithCursor{}})
		tool, _ := reg.Get("list_facts")

		out, err := tool.Invoke(context.Background(), "profile-branch", map[string]any{
			"include_evidence": true,
		})

		if err != nil {
			t.Fatalf("list_facts Invoke: %v", err)
		}
		if out["has_more"] != true || out["next_cursor"] != "next-cursor" {
			t.Fatalf("list_facts pagination = %v", out)
		}
		items := out["items"].([]map[string]any)
		if _, ok := items[0]["evidence"]; !ok {
			t.Fatalf("list_facts evidence missing: %v", items[0])
		}
	})

	t.Run("detect_community list error propagates", func(t *testing.T) {
		reg, _ := BuildDefault(Dependencies{
			CommunityDetect: &stubCommunityDetect{},
			CommunityList:   &stubCommunityList{err: errors.New("community list failed")},
		})
		tool, _ := reg.Get("detect_community")

		_, err := tool.Invoke(context.Background(), "profile-branch", map[string]any{})

		if err == nil || !strings.Contains(err.Error(), "community list failed") {
			t.Fatalf("detect_community err = %v; want community list failed", err)
		}
	})

	for _, tc := range []struct {
		name string
		deps Dependencies
		in   map[string]any
		want string
	}{
		{name: "post_claim", deps: Dependencies{ClaimCreate: &stubClaimCreate{}}, in: map[string]any{"supported_by": func() {}}, want: "post_claim: invalid input"},
		{name: "get_fact", deps: Dependencies{FactGet: stubFactGet{}}, in: map[string]any{"id": func() {}}, want: "get_fact: invalid input"},
		{name: "list_facts", deps: Dependencies{FactList: stubFactList{}}, in: map[string]any{"limit": func() {}}, want: "list_facts: invalid input"},
		{name: "detect_community", deps: Dependencies{CommunityDetect: &stubCommunityDetect{}, CommunityList: &stubCommunityList{}}, in: map[string]any{"gamma": func() {}}, want: "detect_community: invalid input"},
	} {
		t.Run(tc.name+" invalid input", func(t *testing.T) {
			reg, _ := BuildDefault(tc.deps)
			tool, _ := reg.Get(tc.name)
			_, err := tool.Invoke(context.Background(), "profile-branch", tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s err = %v; want %q", tc.name, err, tc.want)
			}
		})
	}
}

func TestBuildDefaultKnowledgeTools_RequiredIDsAndTemporalFiltering(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{
		ClaimGet:        stubClaimGet{},
		ClaimVerify:     stubClaimVerify{},
		FactPromote:     stubFactPromote{},
		FactGet:         stubFactGet{},
		FragmentRetract: &stubFragmentRetract{},
		CommunityGet:    stubCommunityGet{},
	})

	requiredIDCases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{name: "get_claim", input: map[string]any{}, want: "id is required"},
		{name: "verify_claim", input: map[string]any{}, want: "id is required"},
		{name: "promote_claim", input: map[string]any{}, want: "claim_id is required"},
		{name: "get_fact", input: map[string]any{}, want: "id is required"},
		{name: "retract_fragment", input: map[string]any{}, want: "id is required"},
		{name: "get_community_summary", input: map[string]any{}, want: "community_id is required"},
	}
	for _, tc := range requiredIDCases {
		tool, _ := reg.Get(tc.name)
		if _, err := tool.Invoke(context.Background(), "profileA", tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s err = %v; want %q", tc.name, err, tc.want)
		}
	}

	getFact, _ := reg.Get("get_fact")
	tooEarly := time.Date(2026, 5, 30, 11, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if _, err := getFact.Invoke(context.Background(), "profileA", map[string]any{
		"id":       "fact-1",
		"known_at": tooEarly,
	}); !errors.Is(err, factservice.ErrFactNotFound) {
		t.Fatalf("get_fact known_at before recorded_at err = %v; want ErrFactNotFound", err)
	}

	out, err := getFact.Invoke(context.Background(), "profileA", map[string]any{
		"id":               "fact-1",
		"include_evidence": false,
	})
	if err != nil {
		t.Fatalf("get_fact without evidence: %v", err)
	}
	if _, ok := out["evidence"]; ok {
		t.Fatalf("get_fact evidence key present despite include_evidence=false: %v", out["evidence"])
	}
}

func TestFactMatchesTemporalWindow(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	before := now.Add(-time.Hour)
	after := now.Add(time.Hour)

	if factMatchesTemporalWindow(nil, &now, &now) {
		t.Fatal("nil fact matched temporal window")
	}

	fact := &domain.Fact{RecordedAt: now, ValidFrom: &before, ValidTo: &after}
	if !factMatchesTemporalWindow(fact, &now, &after) {
		t.Fatal("fact should match valid and known window")
	}
	if factMatchesTemporalWindow(&domain.Fact{RecordedAt: now, ValidFrom: &after}, &now, nil) {
		t.Fatal("fact with valid_from after valid_at should not match")
	}
	if factMatchesTemporalWindow(&domain.Fact{RecordedAt: now, ValidTo: &now}, &now, nil) {
		t.Fatal("fact with valid_to equal valid_at should not match")
	}
	if factMatchesTemporalWindow(&domain.Fact{RecordedAt: after}, nil, &now) {
		t.Fatal("fact recorded after known_at should not match")
	}
	if factMatchesTemporalWindow(&domain.Fact{RecordedAt: before, RecordedTo: &now}, nil, &now) {
		t.Fatal("fact recorded_to equal known_at should not match")
	}
}
