package graphview

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
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

type fakeV2SemanticGraphStore struct {
	graphInput repository.V2SemanticGraphQuery
	graph      *repository.V2SemanticGraphSnapshot
	graphErr   error

	detailInput repository.V2SemanticGraphNodeDetailInput
	detail      *repository.V2SemanticGraphNode
	detailErr   error
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

func (f *fakeV2SemanticGraphStore) SemanticGraph(_ context.Context, input repository.V2SemanticGraphQuery) (*repository.V2SemanticGraphSnapshot, error) {
	f.graphInput = input
	if f.graphErr != nil {
		return nil, f.graphErr
	}
	return f.graph, nil
}

func (f *fakeV2SemanticGraphStore) SemanticGraphNodeDetail(_ context.Context, input repository.V2SemanticGraphNodeDetailInput) (*repository.V2SemanticGraphNode, error) {
	f.detailInput = input
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	return f.detail, nil
}

func TestGraphOverviewClampsLimitAndUsesScopedQuery(t *testing.T) {
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
	if got.Limit != MaxLimit {
		t.Fatalf("limit = %d; want %d", got.Limit, MaxLimit)
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
		"MATCH (f:Fact {team_id: $profileId})",
		"MATCH (seed)-[r]-(neighbor)",
		"type(r) IN $relationshipTypes",
	} {
		if !strings.Contains(nodeCall.query, required) {
			t.Fatalf("node query missing %q:\n%s", required, nodeCall.query)
		}
	}
	if strings.Contains(nodeCall.query, "LIMIT $limit") || strings.Contains(nodeCall.query, "LIMIT $nodeLimit") {
		t.Fatalf("overview query must not apply node limits:\n%s", nodeCall.query)
	}
	if !strings.Contains(nodeCall.query, "coalesce(c.status, 'candidate') <> 'rejected'") {
		t.Fatalf("overview query must filter rejected claims:\n%s", nodeCall.query)
	}
	if strings.Contains(nodeCall.query, "coalesce(c.status, 'candidate') <> 'superseded'") {
		t.Fatalf("overview query must keep superseded claims as provenance bridge nodes:\n%s", nodeCall.query)
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

func TestGraphOverviewExpandsNeighborsBeforeEdgeRead(t *testing.T) {
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
	if !strings.Contains(nodeCall.query, "MATCH (seed)-[r]-(neighbor)") {
		t.Fatalf("overview node query must expand one-hop neighbors:\n%s", nodeCall.query)
	}
	if nodeCall.params["includeFact"] != true || nodeCall.params["includeClaim"] != true || nodeCall.params["includeFragment"] != true || nodeCall.params["includeDream"] != true {
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
	if len([]rune(node.Title)) != 163 || !strings.HasSuffix(node.Title, "...") {
		t.Fatalf("title = %q; want truncated title", node.Title)
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
	if len([]rune(got.Body)) > maxNodeBodyRunes+3 || !strings.HasSuffix(got.Body, "...") || got.Status != "active" || got.CommunityID != "community-1" || got.Source != "seed" || got.Score != 0.91 {
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
	if !strings.Contains(nodeCall.query, "anchor.claim_id = $anchorID AND coalesce(anchor.status, 'candidate') <> 'rejected'") {
		t.Fatalf("local query must filter rejected claim anchors:\n%s", nodeCall.query)
	}
	if !strings.Contains(nodeCall.query, "$includeClaim AND n:Claim AND coalesce(n.status, 'candidate') <> 'rejected'") {
		t.Fatalf("local query must filter rejected claim nodes:\n%s", nodeCall.query)
	}
	if strings.Contains(nodeCall.query, "coalesce(anchor.status, 'candidate') <> 'superseded") ||
		strings.Contains(nodeCall.query, "coalesce(n.status, 'candidate') <> 'superseded") {
		t.Fatalf("local query must keep superseded claims as provenance bridge nodes:\n%s", nodeCall.query)
	}
	if nodeCall.params["anchorType"] != "claim" || nodeCall.params["anchorID"] != "claim-1" {
		t.Fatalf("anchor params = %#v", nodeCall.params)
	}
	if nodeCall.params["limit"] != int64(MaxLimit) {
		t.Fatalf("limit param = %#v; want max limit", nodeCall.params["limit"])
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

func TestV2SemanticGraphServiceMapsRepositorySnapshot(t *testing.T) {
	recordedAt := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	store := &fakeV2SemanticGraphStore{graph: &repository.V2SemanticGraphSnapshot{
		Scope:     ScopeLocal,
		Query:     "dense",
		Depth:     MaxDepth,
		Limit:     MaxLimit,
		Truncated: true,
		Anchor: &repository.V2SemanticGraphAnchor{
			Type: "entity",
			ID:   "entity-1",
			Key:  "entity:entity-1",
		},
		Nodes: []repository.V2SemanticGraphNode{{
			Key:        "entity:entity-1",
			ID:         "entity-1",
			Type:       "entity",
			Title:      "Dense-Mem",
			Body:       "project",
			Status:     "active",
			RecordedAt: &recordedAt,
		}},
		Edges: []repository.V2SemanticGraphEdge{{
			ID:           "relationship-1",
			Source:       "entity:entity-1",
			Target:       "value:value-1",
			Relationship: "released_on",
			Directed:     true,
		}},
	}}
	svc := NewV2Semantic(store)

	got, err := svc.Graph(context.Background(), "team-1", Query{
		Scope:      ScopeLocal,
		Query:      " Dense ",
		Types:      []string{"entities", "values", "entities", "facts"},
		AnchorType: "entities",
		AnchorID:   "entity-1",
		Depth:      99,
		Limit:      999,
	})
	if err != nil {
		t.Fatalf("Graph returned error: %v", err)
	}

	if store.graphInput.TeamID != "team-1" || store.graphInput.Scope != ScopeLocal || store.graphInput.Query != "dense" {
		t.Fatalf("graph input identity = %#v", store.graphInput)
	}
	if store.graphInput.Depth != MaxDepth || store.graphInput.Limit != MaxLimit {
		t.Fatalf("graph bounds = depth %d limit %d", store.graphInput.Depth, store.graphInput.Limit)
	}
	if got := strings.Join(store.graphInput.Types, ","); got != "entity,value" {
		t.Fatalf("types = %q; want entity,value", got)
	}
	if store.graphInput.AnchorType != "entity" || store.graphInput.AnchorID != "entity-1" {
		t.Fatalf("anchor input = %#v", store.graphInput)
	}
	if got.Anchor == nil || got.Anchor.Key != "entity:entity-1" || len(got.Nodes) != 1 || len(got.Edges) != 1 {
		t.Fatalf("snapshot = %#v", got)
	}
	if got.Nodes[0].RecordedAt == nil || !got.Nodes[0].RecordedAt.Equal(recordedAt) {
		t.Fatalf("recorded_at = %v; want %v", got.Nodes[0].RecordedAt, recordedAt)
	}
}

func TestV2SemanticGraphServiceNodeDetailValidatesAndMapsNotFound(t *testing.T) {
	store := &fakeV2SemanticGraphStore{}
	svc := NewV2Semantic(store)

	_, err := svc.NodeDetail(context.Background(), "team-1", "facts", "node-1")
	if !errors.Is(err, ErrInvalidNodeType) {
		t.Fatalf("err = %v; want ErrInvalidNodeType", err)
	}
	_, err = svc.NodeDetail(context.Background(), "team-1", "entity", "")
	if !errors.Is(err, ErrMissingNode) {
		t.Fatalf("err = %v; want ErrMissingNode", err)
	}
	if store.detailInput.NodeID != "" {
		t.Fatalf("store called for invalid input: %#v", store.detailInput)
	}

	store.detailErr = sql.ErrNoRows
	_, err = svc.NodeDetail(context.Background(), "team-1", "entities", "missing")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("err = %v; want ErrNodeNotFound", err)
	}
	if store.detailInput.TeamID != "team-1" || store.detailInput.NodeType != "entity" || store.detailInput.NodeID != "missing" {
		t.Fatalf("detail input = %#v", store.detailInput)
	}
}

func TestV2SemanticGraphServiceRejectsUnconfiguredStore(t *testing.T) {
	_, err := NewV2Semantic(nil).Graph(context.Background(), "team-1", Query{})
	if err == nil || !strings.Contains(err.Error(), "semantic store is not configured") {
		t.Fatalf("err = %v; want store configuration error", err)
	}

	_, err = (*v2SemanticService)(nil).NodeDetail(context.Background(), "team-1", "entity", "entity-1")
	if err == nil || !strings.Contains(err.Error(), "semantic store is not configured") {
		t.Fatalf("err = %v; want store configuration error", err)
	}
}
