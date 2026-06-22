package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
)

// --- knowledge pipeline tests ---

// TestBuildDefaultIncludesKnowledgeTools verifies the knowledge pipeline
// tools are registered regardless of whether their dependencies are wired.
func TestBuildDefaultIncludesKnowledgeTools(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	required := []string{
		"post_claim", "get_claim", "list_claims", "verify_claim",
		"promote_claim", "get_fact", "list_facts",
		"retract_fact", "retract_fragment", "detect_community", "get_community_summary", "list_communities",
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
		{"retract_fact", map[string]any{"id": "fact-1"}},
		{"remember", map[string]any{"content": "hello"}},
		{"import_memories", map[string]any{"summary": "hello"}},
		{"reflect_memories", map[string]any{}},
		{"confirm_memory", map[string]any{"claim_id": "claim-1", "decision": "keep_existing"}},
		{"find_memory_pack_candidates", map[string]any{"query": "react testing"}},
		{"export_memory_pack", map[string]any{"name": "React testing"}},
		{"inspect_memory_pack", map[string]any{"artifact_json": `{"schema_version":"dense-mem.memory_pack.v1","name":"x","items":[{"subject":"assistant","predicate":"has_skill","object":"x","source_kind":"manual"}]}`}},
		{"import_memory_pack", map[string]any{"mode": "review", "artifact_json": `{"schema_version":"dense-mem.memory_pack.v1","name":"x","items":[{"subject":"assistant","predicate":"has_skill","object":"x","source_kind":"manual"}]}`}},
		{"rollback_memory_pack_import", map[string]any{"import_id": "import-1"}},
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

func TestBuildDefaultMemoryTools_GranularEntryValidation(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{Memory: &stubMemory{}})

	cases := []struct {
		toolName string
		field    string
	}{
		{toolName: "remember", field: "content"},
		{toolName: "import_memories", field: "summary"},
	}

	for _, tc := range cases {
		t.Run(tc.toolName, func(t *testing.T) {
			tool, ok := reg.Get(tc.toolName)
			if !ok {
				t.Fatalf("%s not registered", tc.toolName)
			}
			properties := tool.InputSchema["properties"].(map[string]any)
			fieldSchema := properties[tc.field].(map[string]any)
			if got, want := fieldSchema["maxLength"], memoryEntryMaxLength; got != want {
				t.Fatalf("%s.%s maxLength = %v; want %d", tc.toolName, tc.field, got, want)
			}

			err := ValidateInput(tool, map[string]any{
				tc.field: strings.Repeat("x", memoryEntryMaxLength+1),
			})
			if err == nil || !strings.Contains(err.Error(), "Split large scenarios") {
				t.Fatalf("ValidateInput long %s error = %v; want split guidance", tc.field, err)
			}
		})
	}
}

func TestBuildDefault_RecallInvokerErrorBranches(t *testing.T) {
	t.Run("empty hit returns mapping error", func(t *testing.T) {
		reg, _ := BuildDefault(Dependencies{Recall: stubRecallNilFragment{}})
		tool, _ := reg.Get("recall_memory")

		_, err := tool.Invoke(context.Background(), "profile-memory", map[string]any{"query": "hello"})

		if err == nil || !strings.Contains(err.Error(), "hit missing payload") {
			t.Fatalf("err = %v; want hit missing payload", err)
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
	factRetract := &stubFactRetract{}
	detect := &stubCommunityDetect{}
	communities := &stubCommunityList{}
	reg, _ := BuildDefault(Dependencies{
		ClaimGet:        stubClaimGet{},
		FactRetract:     factRetract,
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

	// retract_fact — verify profileID routing.
	factTool, _ := reg.Get("retract_fact")
	if _, err := factTool.Invoke(context.Background(), "profileA", map[string]any{"id": "fact-1"}); err != nil {
		t.Fatalf("retract_fact profileA: %v", err)
	}
	if factRetract.lastProfile != "profileA" {
		t.Errorf("retract_fact routed to %q; want profileA", factRetract.lastProfile)
	}
	if _, err := factTool.Invoke(context.Background(), "profileB", map[string]any{"id": "fact-2"}); err != nil {
		t.Fatalf("retract_fact profileB: %v", err)
	}
	if factRetract.lastProfile != "profileB" {
		t.Errorf("retract_fact routed to %q after second call; want profileB", factRetract.lastProfile)
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
	factRetract := &stubFactRetract{}
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
		FactRetract:     factRetract,
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

	retractFactTool, _ := reg.Get("retract_fact")
	retractFactOut, err := retractFactTool.Invoke(context.Background(), "profile-success", map[string]any{"id": "fact-1"})
	if err != nil {
		t.Fatalf("retract_fact Invoke: %v", err)
	}
	if factRetract.lastProfile != "profile-success" {
		t.Fatalf("retract_fact profile = %q; want profile-success", factRetract.lastProfile)
	}
	if retractFactOut["status"] != "retracted" {
		t.Fatalf("retract_fact status = %v; want retracted", retractFactOut["status"])
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
		FactRetract:     &stubFactRetract{},
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
		{name: "retract_fact", input: map[string]any{}, want: "id is required"},
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
