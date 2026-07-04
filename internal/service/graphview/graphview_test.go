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
	if len(reader.calls) != 2 {
		t.Fatalf("read calls = %d; want nodes and edges reads", len(reader.calls))
	}

	nodeCall := reader.calls[0]
	if nodeCall.profileID != "team-1" {
		t.Fatalf("ScopedRead profile = %q; want team-1", nodeCall.profileID)
	}
	for _, required := range []string{"$profileId", "MATCH (f:Fact {team_id: $profileId})", "LIMIT $limit"} {
		if !strings.Contains(nodeCall.query, required) {
			t.Fatalf("node query missing %q:\n%s", required, nodeCall.query)
		}
	}
	if strings.Count(nodeCall.query, "LIMIT $limit") < 4 {
		t.Fatalf("node query should apply the limit per graph node type:\n%s", nodeCall.query)
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

func TestGraphOverviewReturnsNormalizedEdgesAndDedupedNodes(t *testing.T) {
	recordedAt := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	reader := &fakeGraphReader{rowsByCall: [][]map[string]any{
		{
			{
				"type":         "fact",
				"id":           "fact-1",
				"key":          "fact:fact-1",
				"title":        strings.Repeat("a", 200),
				"body":         strings.Repeat("b", 500),
				"status":       "active",
				"community_id": "community-1",
				"source":       "seed",
				"score":        0.91,
				"recorded_at":  recordedAt,
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
	if len([]rune(node.Body)) != maxNodeBodyRunes+3 || node.Status != "active" || node.CommunityID != "community-1" || node.Source != "seed" || node.Score != 0.91 {
		t.Fatalf("node = %#v; want normalized metadata", node)
	}
	if node.RecordedAt == nil || !node.RecordedAt.Equal(recordedAt) {
		t.Fatalf("recorded_at = %v; want %v", node.RecordedAt, recordedAt)
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
