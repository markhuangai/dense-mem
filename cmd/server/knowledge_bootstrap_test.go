package main

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/service/graphview"
)

func TestUnavailableGraphViewServiceReturnsEmptySnapshot(t *testing.T) {
	svc := unavailableGraphViewService{}

	got, err := svc.Graph(context.Background(), "team-1", graphview.Query{Limit: 500})
	if err != nil {
		t.Fatalf("Graph() error = %v", err)
	}
	if got.Scope != graphview.ScopeOverview || len(got.Nodes) != 0 || len(got.Edges) != 0 {
		t.Fatalf("Graph() = %#v, want empty overview snapshot", got)
	}
	if got.Limit != graphview.MaxLimit {
		t.Fatalf("limit = %d, want %d", got.Limit, graphview.MaxLimit)
	}

	local, err := svc.Graph(context.Background(), "team-1", graphview.Query{
		Scope:      graphview.ScopeLocal,
		AnchorType: "fragment",
		AnchorID:   "fragment-1",
	})
	if err != nil {
		t.Fatalf("Graph(local) error = %v", err)
	}
	if local.Anchor == nil || local.Anchor.Key != "fragment:fragment-1" {
		t.Fatalf("local anchor = %#v", local.Anchor)
	}
}

func TestUnavailableGraphViewServiceValidatesLocalAnchorAndNodeMiss(t *testing.T) {
	svc := unavailableGraphViewService{}

	if _, err := svc.Graph(context.Background(), "team-1", graphview.Query{Scope: graphview.ScopeLocal}); !errors.Is(err, graphview.ErrMissingAnchor) {
		t.Fatalf("Graph(local missing anchor) error = %v", err)
	}
	if _, err := svc.NodeDetail(context.Background(), "team-1", "fragment", "fragment-1"); !errors.Is(err, graphview.ErrNodeNotFound) {
		t.Fatalf("NodeDetail() error = %v", err)
	}
}
