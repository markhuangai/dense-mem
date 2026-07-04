package graphview

import (
	"context"
	"errors"
	"strings"
	"testing"

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
