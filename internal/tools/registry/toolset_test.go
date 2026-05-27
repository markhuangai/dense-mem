package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/tools/graphquery"
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

type stubRecall struct{}

func (stubRecall) Recall(ctx context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	return []recallservice.RecallHit{}, nil
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
	return &domain.Fact{FactID: factID, ProfileID: profileID}, nil
}

type stubFactList struct{}

func (stubFactList) List(ctx context.Context, profileID string, filters factservice.FactListFilters, limit int, cursor string) ([]*domain.Fact, string, error) {
	return []*domain.Fact{{FactID: "f-1", ProfileID: profileID}}, "", nil
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
}

func (s *stubCommunityList) List(ctx context.Context, profileID string, limit int) ([]*domain.Community, error) {
	s.lastProfile = profileID
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
	aProfile, _ := aResult["profile_id"].(string)
	bProfile, _ := bResult["profile_id"].(string)
	if aProfile != "profileA" {
		t.Errorf("get_claim profileA result has profile_id=%q; want profileA", aProfile)
	}
	if bProfile != "profileB" {
		t.Errorf("get_claim profileB result has profile_id=%q; want profileB", bProfile)
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
