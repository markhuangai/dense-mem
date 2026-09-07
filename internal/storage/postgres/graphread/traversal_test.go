package graphread

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
)

func TestNormalizeGraphTypesAndNodeTypes(t *testing.T) {
	if got := NormalizeTypes([]string{"values", "entity", "invalid", "entities"}); len(got) != 2 || got[0] != "entity" || got[1] != "value" {
		t.Fatalf("unexpected normalized types: %#v", got)
	}
	if got := TypeSet(nil); !got["entity"] || !got["value"] {
		t.Fatalf("default type set missing nodes: %#v", got)
	}
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: " entity ", want: "entity"},
		{raw: "VALUES", want: "value"},
		{raw: "unknown", want: ""},
	} {
		if got := NormalizeNodeType(test.raw); got != test.want {
			t.Errorf("NormalizeNodeType(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestScanRowsFiltersTargetTypesAndBuildsReadModels(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	columns := []string{
		"edge_id", "owner_id", "predicate", "support_count", "source_group_count",
		"source_key", "source_id", "source_title", "source_body", "source_status", "source_owner", "source_recorded_at",
		"target_key", "target_id", "target_type", "target_title", "target_body", "target_status", "target_owner", "target_recorded_at",
	}
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows(columns).
		AddRow("edge-value", "owner", "uses", 1, 2, "entity:source", "source", "Source", "kind", "active", "owner", now, "value:target", "target", "value", "Target", "text", "active", "", now).
		AddRow("edge-entity", "owner", "owns", 3, 4, "entity:source", "source", "Source", "kind", "active", "owner", now, "entity:target", "target", "entity", "Target", "kind", "active", "owner", now)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT graph")).WillReturnRows(rows)
	sqlRows, err := db.Query("SELECT graph")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ScanRows(sqlRows, []string{"entity"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Edge.ID != "edge-entity" || got[0].Target.Type != "entity" || got[0].Source.RecordedAt == nil {
		t.Fatalf("unexpected scanned rows: %#v", got)
	}
	if err := sqlRows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestScanRowsSkipsAllRowsWhenEntitiesAreNotAllowed(t *testing.T) {
	if got, err := ScanRows(nil, []string{"value"}); err != nil || got != nil {
		t.Fatalf("ScanRows without entity type = %#v, %v", got, err)
	}
}

func TestTraversePreservesBreadthFirstOrderAndDeduplicatesEdges(t *testing.T) {
	rowsByFrontier := map[string][]Row{
		"entity:root": {
			{Source: graphcontract.Node{Key: "entity:root"}, Target: graphcontract.Node{Key: "entity:a"}, Edge: graphcontract.Edge{ID: "edge-a"}},
			{Source: graphcontract.Node{Key: "entity:root"}, Target: graphcontract.Node{Key: "entity:b"}, Edge: graphcontract.Edge{ID: "edge-b"}},
		},
		"entity:a,entity:b": {
			{Source: graphcontract.Node{Key: "entity:a"}, Target: graphcontract.Node{Key: "entity:c"}, Edge: graphcontract.Edge{ID: "edge-c"}},
			{Source: graphcontract.Node{Key: "entity:b"}, Target: graphcontract.Node{Key: "entity:c"}, Edge: graphcontract.Edge{ID: "edge-c"}},
		},
	}

	rows, err := Traverse(context.Background(), "entity:root", 2, 10, func(_ context.Context, frontier []string, _ int) ([]Row, error) {
		key := ""
		for i, item := range frontier {
			if i > 0 {
				key += ","
			}
			key += item
		}
		return rowsByFrontier[key], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Edge.ID != "edge-a" || rows[1].Edge.ID != "edge-b" || rows[2].Edge.ID != "edge-c" {
		t.Fatalf("unexpected traversal result: %#v", rows)
	}
}

func TestSnapshotKeepsBoundedAnchorAndNodeOrder(t *testing.T) {
	input := graphcontract.Query{Scope: "local", AnchorType: "entity", AnchorID: "root", Depth: 2, Limit: 1}
	snapshot := Snapshot(input, []Row{{
		Source: graphcontract.Node{Key: "entity:root", ID: "root"},
		Target: graphcontract.Node{Key: "entity:child", ID: "child"},
		Edge:   graphcontract.Edge{ID: "edge"},
	}})
	if snapshot.Anchor == nil || snapshot.Anchor.Key != "entity:root" || !snapshot.Truncated {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}
