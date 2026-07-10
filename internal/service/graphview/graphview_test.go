package graphview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type graphReadCall struct {
	profileID string
	query     string
	params    map[string]any
}

type fakeGraphReader struct {
	calls      []graphReadCall
	rowsByCall [][]map[string]any
	errByCall  []error
}

func (f *fakeGraphReader) ScopedRead(_ context.Context, profileID string, query string, params map[string]any) (neo4jdriver.ResultSummary, []map[string]any, error) {
	callParams := map[string]any{}
	for key, value := range params {
		callParams[key] = value
	}
	f.calls = append(f.calls, graphReadCall{profileID: profileID, query: query, params: callParams})
	idx := len(f.calls) - 1
	if idx < len(f.errByCall) && f.errByCall[idx] != nil {
		return nil, nil, f.errByCall[idx]
	}
	if idx < len(f.rowsByCall) {
		return nil, f.rowsByCall[idx], nil
	}
	return nil, nil, nil
}

func TestGraphOverviewReturnsCompleteScopedPayloadRegardlessOfLimit(t *testing.T) {
	reader := &fakeGraphReader{rowsByCall: [][]map[string]any{{
		{
			"type":  "fact",
			"id":    "fact-1",
			"key":   "fact:fact-1",
			"title": "Dense-Mem stores graph memory",
		},
	}}}
	svc := New(reader)

	got, err := svc.Graph(context.Background(), "team-1", Query{
		Query: " Graph ",
		Limit: 999,
		Types: []string{"facts", "dreams", "invalid"},
	})
	if err != nil {
		t.Fatalf("Graph returned error: %v", err)
	}

	if got.Scope != ScopeOverview {
		t.Fatalf("scope = %q; want overview", got.Scope)
	}
	if got.Limit != 0 {
		t.Fatalf("limit = %d; want 0 for uncapped overview", got.Limit)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Key != "fact:fact-1" {
		t.Fatalf("nodes = %#v; want fact node", got.Nodes)
	}
	if got.Truncated {
		t.Fatal("overview graph must not mark all-topology response truncated")
	}
	if len(reader.calls) != 2 {
		t.Fatalf("read calls = %d; want nodes and edges reads", len(reader.calls))
	}

	nodeCall := reader.calls[0]
	if nodeCall.profileID != "team-1" {
		t.Fatalf("ScopedRead profile = %q; want team-1", nodeCall.profileID)
	}
	for _, required := range []string{
		"$profileId",
		"MATCH (n:Entity {team_id: $profileId})",
		"MATCH (n:Value {team_id: $profileId})",
		"MATCH (n:Fact {team_id: $profileId})",
		"coalesce(n.content, '') AS body",
	} {
		if !strings.Contains(nodeCall.query, required) {
			t.Fatalf("node query missing %q:\n%s", required, nodeCall.query)
		}
	}
	if strings.Contains(nodeCall.query, "LIMIT $limit") || strings.Contains(nodeCall.query, "LIMIT $nodeLimit") {
		t.Fatalf("overview query must not apply node limits:\n%s", nodeCall.query)
	}
	if strings.Contains(nodeCall.query, "<> 'rejected'") || strings.Contains(nodeCall.query, "<> 'superseded'") {
		t.Fatalf("overview query must include every lifecycle state:\n%s", nodeCall.query)
	}
	if nodeCall.params["limit"] != int64(MaxLimit) {
		t.Fatalf("limit param = %#v; want %d", nodeCall.params["limit"], MaxLimit)
	}
	if _, exists := nodeCall.params["nodeLimit"]; exists {
		t.Fatalf("nodeLimit param must not be sent for unbounded overview: %#v", nodeCall.params)
	}
	if nodeCall.params["query"] != "graph" {
		t.Fatalf("query param = %#v; want graph", nodeCall.params["query"])
	}
	if nodeCall.params["includeFact"] != true || nodeCall.params["includeDream"] != true {
		t.Fatalf("include fact/dream params = %#v", nodeCall.params)
	}
	if nodeCall.params["includeClaim"] != false || nodeCall.params["includeFragment"] != false {
		t.Fatalf("unexpected default type params after explicit filter: %#v", nodeCall.params)
	}
	if nodeCall.params["includeSuperseded"] != false {
		t.Fatalf("includeSuperseded = %#v; want false", nodeCall.params["includeSuperseded"])
	}
}

func TestGraphOverviewReadsEdgesForEveryReturnedNode(t *testing.T) {
	recordedAt := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	reader := &fakeGraphReader{rowsByCall: [][]map[string]any{
		{
			{
				"type":        "claim",
				"id":          "claim-1",
				"key":         "claim:claim-1",
				"title":       "claim title",
				"status":      "superseded",
				"recorded_at": recordedAt,
			},
			{
				"type":        "fact",
				"id":          "fact-1",
				"key":         "fact:fact-1",
				"title":       "fact title",
				"status":      "active",
				"recorded_at": recordedAt.Add(-time.Minute),
			},
			{
				"type":        "fragment",
				"id":          "fragment-1",
				"key":         "fragment:fragment-1",
				"title":       "fragment title",
				"status":      "active",
				"recorded_at": recordedAt.Add(-2 * time.Minute),
			},
		},
		{
			{
				"id":           "edge-promotes",
				"source":       "claim:claim-1",
				"target":       "fact:fact-1",
				"relationship": "PROMOTES_TO",
			},
			{
				"id":           "edge-supports",
				"source":       "claim:claim-1",
				"target":       "fragment:fragment-1",
				"relationship": "SUPPORTED_BY",
			},
		},
	}}
	svc := New(reader)

	got, err := svc.Graph(context.Background(), "team-1", Query{})
	if err != nil {
		t.Fatalf("Graph returned error: %v", err)
	}

	if len(got.Nodes) != 3 {
		t.Fatalf("nodes = %#v; want expanded claim/fact/fragment neighborhood", got.Nodes)
	}
	if len(got.Edges) != 2 {
		t.Fatalf("edges = %#v; want claim connected to fact and fragment", got.Edges)
	}
	claimDegree := 0
	for _, edge := range got.Edges {
		if edge.Source == "claim:claim-1" || edge.Target == "claim:claim-1" {
			claimDegree++
		}
	}
	if claimDegree != 2 {
		t.Fatalf("claim degree = %d; want 2 edges", claimDegree)
	}

	nodeCall := reader.calls[0]
	if strings.Contains(nodeCall.query, "MATCH (seed)-[r]-(neighbor)") {
		t.Fatalf("overview node query should read the full team graph directly:\n%s", nodeCall.query)
	}
	if nodeCall.params["includeFact"] != true || nodeCall.params["includeClaim"] != true || nodeCall.params["includeFragment"] != true || nodeCall.params["includeDream"] != true || nodeCall.params["includeEntity"] != true || nodeCall.params["includeValue"] != true || nodeCall.params["includeCommunity"] != true {
		t.Fatalf("default overview types should include all graph node types: %#v", nodeCall.params)
	}
	if strings.Contains(nodeCall.query, "LIMIT $limit") || strings.Contains(nodeCall.query, "LIMIT $nodeLimit") {
		t.Fatalf("overview node query must not cap returned nodes:\n%s", nodeCall.query)
	}
	if _, exists := nodeCall.params["nodeLimit"]; exists {
		t.Fatalf("nodeLimit param must not be sent: %#v", nodeCall.params)
	}

	edgeCall := reader.calls[1]
	keys, ok := edgeCall.params["nodeKeys"].([]string)
	if !ok {
		t.Fatalf("nodeKeys = %#v; want []string", edgeCall.params["nodeKeys"])
	}
	for _, want := range []string{"claim:claim-1", "fact:fact-1", "fragment:fragment-1"} {
		if !containsString(keys, want) {
			t.Fatalf("nodeKeys = %#v; missing %q", keys, want)
		}
	}
}

func TestGraphOverviewReturnsNormalizedEdgesAndDedupedNodes(t *testing.T) {
	reader := &fakeGraphReader{rowsByCall: [][]map[string]any{
		{
			{
				"type":  "fact",
				"id":    "fact-1",
				"key":   "fact:fact-1",
				"title": strings.Repeat("a", 200),
			},
			{
				"type":  "fact",
				"id":    "fact-1",
				"key":   "fact:fact-1",
				"title": "duplicate",
			},
			{
				"type": "claim",
				"id":   "claim-1",
			},
		},
		{
			{
				"id":           "edge-1",
				"source":       "fact:fact-1",
				"target":       "claim:claim-1",
				"relationship": "SUPPORTED_BY",
			},
			{
				"id":           "edge-1",
				"source":       "fact:fact-1",
				"target":       "claim:claim-1",
				"relationship": "SUPPORTED_BY",
			},
			{
				"source":       "claim:claim-1",
				"target":       "fact:fact-1",
				"relationship": "PROMOTES_TO",
			},
			{
				"source": "fact:fact-1",
			},
		},
	}}
	svc := New(reader)

	got, err := svc.Graph(context.Background(), "team-1", Query{Limit: 2})
	if err != nil {
		t.Fatalf("Graph returned error: %v", err)
	}

	if len(got.Nodes) != 1 {
		t.Fatalf("nodes = %#v; want one valid deduped node", got.Nodes)
	}
	node := got.Nodes[0]
	if len([]rune(node.Title)) != 200 {
		t.Fatalf("title length = %d; want full title", len([]rune(node.Title)))
	}
	if node.Body != "" || node.Status != "" || node.CommunityID != "" || node.Source != "" || node.Score != 0 || node.RecordedAt != nil {
		t.Fatalf("overview node = %#v; want topology summary only", node)
	}

	if len(got.Edges) != 2 {
		t.Fatalf("edges = %#v; want two normalized edges", got.Edges)
	}
	if got.Edges[1].ID != "claim:claim-1|PROMOTES_TO|fact:fact-1" || !got.Edges[1].Directed {
		t.Fatalf("fallback edge = %#v", got.Edges[1])
	}
	edgeCall := reader.calls[1]
	if _, exists := edgeCall.params["edgeLimit"]; exists || strings.Contains(edgeCall.query, "LIMIT $edgeLimit") {
		t.Fatalf("edge query should not apply a separate edge cap: params=%#v\n%s", edgeCall.params, edgeCall.query)
	}
	if keys, ok := edgeCall.params["nodeKeys"].([]string); !ok || len(keys) != 1 || keys[0] != "fact:fact-1" {
		t.Fatalf("nodeKeys = %#v; want returned node keys", edgeCall.params["nodeKeys"])
	}
}

func TestGraphNodeDetailReturnsFullNode(t *testing.T) {
	recordedAt := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	reader := &fakeGraphReader{rowsByCall: [][]map[string]any{{
		{
			"type":         "fact",
			"id":           "fact-1",
			"key":          "fact:fact-1",
			"title":        "fact title",
			"body":         strings.Repeat("body ", 120),
			"status":       "active",
			"community_id": "community-1",
			"source":       "seed",
			"score":        0.91,
			"recorded_at":  recordedAt,
		},
	}}}
	svc := New(reader)

	got, err := svc.NodeDetail(context.Background(), "team-1", "facts", "fact-1")
	if err != nil {
		t.Fatalf("NodeDetail returned error: %v", err)
	}

	if got.Key != "fact:fact-1" || got.Type != "fact" || got.Title != "fact title" {
		t.Fatalf("detail identity = %#v", got)
	}
	if got.Body != strings.TrimSpace(strings.Repeat("body ", 120)) || got.Status != "active" || got.CommunityID != "community-1" || got.Source != "seed" || got.Score != 0.91 {
		t.Fatalf("detail = %#v; want full normalized metadata", got)
	}
	if got.RecordedAt == nil || !got.RecordedAt.Equal(recordedAt) {
		t.Fatalf("recorded_at = %v; want %v", got.RecordedAt, recordedAt)
	}
	call := reader.calls[0]
	if call.profileID != "team-1" || call.params["nodeType"] != "fact" || call.params["nodeID"] != "fact-1" {
		t.Fatalf("detail call = %#v", call)
	}
	if !strings.Contains(call.query, "$nodeType = 'fact'") || !strings.Contains(call.query, "$nodeType = 'fragment'") {
		t.Fatalf("detail query missing type branches:\n%s", call.query)
	}
}

func TestGraphNodeDetailValidatesInputAndMisses(t *testing.T) {
	reader := &fakeGraphReader{}
	svc := New(reader)

	_, err := svc.NodeDetail(context.Background(), "team-1", "bad", "node-1")
	if !errors.Is(err, ErrInvalidNodeType) {
		t.Fatalf("err = %v; want invalid type", err)
	}
	_, err = svc.NodeDetail(context.Background(), "team-1", "fact", "")
	if !errors.Is(err, ErrMissingNode) {
		t.Fatalf("err = %v; want missing node", err)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("ScopedRead calls = %d; want 0", len(reader.calls))
	}

	reader = &fakeGraphReader{rowsByCall: [][]map[string]any{{}}}
	svc = New(reader)
	_, err = svc.NodeDetail(context.Background(), "team-1", "fact", "missing")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("err = %v; want ErrNodeNotFound", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestGraphLocalClampsDepthAndRejectsDynamicTraversalInput(t *testing.T) {
	reader := &fakeGraphReader{rowsByCall: [][]map[string]any{{
		{
			"type":  "claim",
			"id":    "claim-1",
			"key":   "claim:claim-1",
			"title": "claim title",
		},
	}}}
	svc := New(reader)

	got, err := svc.Graph(context.Background(), "team-1", Query{
		Scope:      ScopeLocal,
		AnchorType: "claim",
		AnchorID:   "claim-1",
		Depth:      99,
		Limit:      250,
	})
	if err != nil {
		t.Fatalf("Graph returned error: %v", err)
	}

	if got.Depth != MaxDepth {
		t.Fatalf("depth = %d; want %d", got.Depth, MaxDepth)
	}
	if got.Anchor == nil || got.Anchor.Key != "claim:claim-1" {
		t.Fatalf("anchor = %#v; want claim anchor", got.Anchor)
	}
	if len(reader.calls) != 2 {
		t.Fatalf("read calls = %d; want nodes and edges reads", len(reader.calls))
	}

	nodeCall := reader.calls[0]
	if strings.Contains(nodeCall.query, "*1..99") {
		t.Fatalf("local query used unclamped depth:\n%s", nodeCall.query)
	}
	if !strings.Contains(nodeCall.query, "*1..2") {
		t.Fatalf("local query missing clamped depth:\n%s", nodeCall.query)
	}
	if !strings.Contains(nodeCall.query, "anchor.claim_id = $anchorID") {
		t.Fatalf("local query must support claim anchors:\n%s", nodeCall.query)
	}
	if !strings.Contains(nodeCall.query, "$includeClaim AND n:Claim") {
		t.Fatalf("local query must support claim nodes:\n%s", nodeCall.query)
	}
	for _, lifecycleFilter := range []string{"<> 'rejected'", "<> 'retracted'", "IN ['active', 'needs_revalidation']", "IN ['proposed', 'reinforced']"} {
		if strings.Contains(nodeCall.query, lifecycleFilter) {
			t.Fatalf("local query must include every lifecycle state; found %q:\n%s", lifecycleFilter, nodeCall.query)
		}
	}
	if nodeCall.params["anchorType"] != "claim" || nodeCall.params["anchorID"] != "claim-1" {
		t.Fatalf("anchor params = %#v", nodeCall.params)
	}
	if nodeCall.params["limit"] != int64(250) {
		t.Fatalf("limit param = %#v; want requested local limit", nodeCall.params["limit"])
	}
	if _, exists := nodeCall.params["relationshipTypes"]; exists {
		t.Fatalf("local traversal must not send a fixed relationship allowlist: %#v", nodeCall.params)
	}
	if !strings.Contains(nodeCall.query, "all(rel IN relationships(p) WHERE rel.team_id = $profileId)") {
		t.Fatalf("local traversal must scope every dynamic relationship to the team:\n%s", nodeCall.query)
	}
	if strings.Contains(nodeCall.query, "type(rel) IN $relationshipTypes") {
		t.Fatalf("local traversal must discover open-ended relationship types:\n%s", nodeCall.query)
	}
}

func TestGraphRedactsSecretLikeTextFromEveryDisplayField(t *testing.T) {
	nodes := nodesFromRows([]map[string]any{{
		"type": "fragment", "id": "fragment-1", "key": "fragment:fragment-1",
		"title": "api_key=top-secret", "body": "Bearer sk-abcdefghijklmnop",
		"source": "token=source-secret", "aliases": []any{"sk-abcdefghijklmnop"},
	}})
	edges := edgesFromRows([]map[string]any{{
		"id": "edge-1", "source": "fragment:fragment-1", "target": "entity:one",
		"relationship": "MENTIONS", "knowledge": "password=graph-secret",
	}})

	if len(nodes) != 1 || len(edges) != 1 {
		t.Fatalf("nodes=%#v edges=%#v", nodes, edges)
	}
	encoded := strings.Join([]string{nodes[0].Title, nodes[0].Body, nodes[0].Source, strings.Join(nodes[0].Aliases, " "), edges[0].Knowledge}, " ")
	for _, secret := range []string{"top-secret", "abcdefghijklmnop", "source-secret", "graph-secret"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("graph display fields leaked %q: %s", secret, encoded)
		}
	}
	if !strings.Contains(encoded, "[REDACTED]") {
		t.Fatalf("graph display fields were not visibly redacted: %s", encoded)
	}
}

func TestGraphLocalSupportsEntityAnchorsAndFullDynamicEdgeDetail(t *testing.T) {
	recordedAt := time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC)
	reader := &fakeGraphReader{rowsByCall: [][]map[string]any{
		{
			{
				"type":              "entity",
				"id":                "entity-mark",
				"key":               "entity:entity-mark",
				"title":             "Mark",
				"body":              "M. Huang",
				"status":            "resolved",
				"recorded_at":       recordedAt,
				"aliases":           []any{"M. Huang"},
				"entity_type":       "person",
				"resolution_status": "resolved",
				"resolution_conf":   0.98,
			},
			{
				"type":        "entity",
				"id":          "entity-dense-mem",
				"key":         "entity:entity-dense-mem",
				"title":       "Dense-Mem",
				"entity_type": "project",
			},
		},
		{
			{
				"id":                 "assertion-1",
				"source":             "entity:entity-mark",
				"target":             "entity:entity-dense-mem",
				"relationship":       "CONTRIBUTED_TO",
				"assertion_id":       "assertion-1",
				"predicate":          "contributed_to",
				"tier":               "fact",
				"status":             "active",
				"policy_family":      "event",
				"knowledge":          "Mark CONTRIBUTED_TO Dense-Mem",
				"support_count":      2,
				"source_group_count": 2,
				"evidence_ids":       []any{"fragment-1", "fragment-2"},
				"recorded_at":        recordedAt,
			},
		},
	}}
	svc := New(reader)

	got, err := svc.Graph(context.Background(), "team-1", Query{
		Scope:      ScopeLocal,
		AnchorType: "entity",
		AnchorID:   "entity-mark",
		Depth:      2,
	})
	if err != nil {
		t.Fatalf("Graph returned error: %v", err)
	}
	if got.Anchor == nil || got.Anchor.Key != "entity:entity-mark" {
		t.Fatalf("anchor = %#v; want entity anchor", got.Anchor)
	}
	if len(got.Nodes) != 2 || got.Nodes[0].Aliases[0] != "M. Huang" || got.Nodes[0].EntityType != "person" || got.Nodes[0].ResolutionConf != 0.98 {
		t.Fatalf("nodes = %#v; want complete entity metadata", got.Nodes)
	}
	if len(got.Edges) != 1 || got.Edges[0].Relationship != "CONTRIBUTED_TO" || got.Edges[0].Tier != "fact" || got.Edges[0].SourceGroupCount != 2 {
		t.Fatalf("edges = %#v; want complete dynamic assertion edge", got.Edges)
	}

	nodeCall := reader.calls[0]
	for _, required := range []string{
		"$anchorType = 'entity' AND anchor:Entity",
		"$anchorType = 'value' AND anchor:Value",
		"$anchorType = 'community' AND anchor:Community",
		"$includeEntity AND n:Entity",
		"$includeValue AND n:Value",
		"$includeCommunity AND n:Community",
		"END AS body",
		"END AS resolution_conf",
	} {
		if !strings.Contains(nodeCall.query, required) {
			t.Fatalf("local query missing %q:\n%s", required, nodeCall.query)
		}
	}
	edgeCall := reader.calls[1]
	if _, exists := edgeCall.params["relationshipTypes"]; exists {
		t.Fatalf("edge read must not depend on a fixed relationship allowlist: %#v", edgeCall.params)
	}
}

func TestGraphLocalRejectsUnsupportedAnchorTypeBeforeRead(t *testing.T) {
	reader := &fakeGraphReader{}
	svc := New(reader)

	_, err := svc.Graph(context.Background(), "team-1", Query{
		Scope:      ScopeLocal,
		AnchorType: "fact) DETACH DELETE n",
		AnchorID:   "fact-1",
	})
	if !errors.Is(err, ErrInvalidAnchorType) {
		t.Fatalf("err = %v; want ErrInvalidAnchorType", err)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("ScopedRead calls = %d; want 0", len(reader.calls))
	}
}

func TestGraphLocalRequiresAnchorBeforeRead(t *testing.T) {
	reader := &fakeGraphReader{}
	svc := New(reader)

	_, err := svc.Graph(context.Background(), "team-1", Query{Scope: ScopeLocal, AnchorType: "fact"})
	if !errors.Is(err, ErrMissingAnchor) {
		t.Fatalf("err = %v; want ErrMissingAnchor", err)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("ScopedRead calls = %d; want 0", len(reader.calls))
	}
}

func TestGraphReaderErrorsAreWrapped(t *testing.T) {
	reader := &fakeGraphReader{errByCall: []error{errors.New("neo4j unavailable")}}
	svc := New(reader)

	_, err := svc.Graph(context.Background(), "team-1", Query{})
	if err == nil || !strings.Contains(err.Error(), "graph view nodes") {
		t.Fatalf("err = %v; want wrapped node read error", err)
	}

	reader = &fakeGraphReader{
		rowsByCall: [][]map[string]any{{
			{"type": "fact", "id": "fact-1", "key": "fact:fact-1", "title": "fact"},
		}},
		errByCall: []error{nil, errors.New("edge read failed")},
	}
	svc = New(reader)
	_, err = svc.Graph(context.Background(), "team-1", Query{})
	if err == nil || !strings.Contains(err.Error(), "graph view edges") {
		t.Fatalf("err = %v; want wrapped edge read error", err)
	}
}

func TestGraphRejectsUnconfiguredReader(t *testing.T) {
	_, err := (*service)(nil).Graph(context.Background(), "team-1", Query{})
	if err == nil || !strings.Contains(err.Error(), "reader is not configured") {
		t.Fatalf("err = %v; want reader configuration error", err)
	}

	_, err = New(nil).Graph(context.Background(), "team-1", Query{})
	if err == nil || !strings.Contains(err.Error(), "reader is not configured") {
		t.Fatalf("err = %v; want reader configuration error", err)
	}
}

func TestGraphReturnsNoEdgeReadForEmptyNodes(t *testing.T) {
	reader := &fakeGraphReader{}
	svc := New(reader)

	got, err := svc.Graph(context.Background(), "team-1", Query{})
	if err != nil {
		t.Fatalf("Graph returned error: %v", err)
	}
	if len(got.Nodes) != 0 || len(got.Edges) != 0 {
		t.Fatalf("snapshot = %#v; want empty graph", got)
	}
	if len(reader.calls) != 1 {
		t.Fatalf("ScopedRead calls = %d; want only node read", len(reader.calls))
	}
}
