package contract

import (
	"reflect"
	"testing"
)

func TestNormalizeQueryPreservesGraphPolicy(t *testing.T) {
	input := Query{
		TeamID: " team ", Scope: " LOCAL ", Query: " Project ",
		Types:      []string{" values ", "ENTITIES", "entity", "unknown"},
		AnchorType: "VALUES", AnchorID: " value-id ", Depth: 99, Limit: 181,
		MinRelevance: 0.4,
	}
	got := NormalizeQuery(input)
	want := Query{
		TeamID: "team", Scope: ScopeLocal, Query: "project",
		Types:      []string{"value", "entity"},
		AnchorType: "value", AnchorID: "value-id", Depth: MaxDepth, Limit: 181,
		MinRelevance: 0.4,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeQuery() = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(input.Types, []string{" values ", "ENTITIES", "entity", "unknown"}) {
		t.Fatalf("NormalizeQuery mutated caller types: %#v", input.Types)
	}

	defaults := NormalizeQuery(Query{Scope: "unknown", Types: []string{"unknown"}, AnchorType: "entities", AnchorID: " retained ", Depth: -1, Limit: -1})
	if defaults.Scope != ScopeOverview || !reflect.DeepEqual(defaults.Types, []string{"entity", "value"}) ||
		defaults.AnchorType != "entity" || defaults.AnchorID != "retained" || defaults.Depth != DefaultDepth || defaults.Limit != DefaultLimit {
		t.Fatalf("unexpected graph defaults: %#v", defaults)
	}
	large := NormalizeQuery(Query{Scope: ScopeLocal, Depth: 1, Limit: 1_000_000})
	if large.Depth != 1 || large.Limit != 1_000_000 {
		t.Fatalf("positive graph limit or depth changed: %#v", large)
	}
}

func TestNormalizeGraphTypesAndAliases(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{" entity ", "entity"}, {"ENTITIES", "entity"},
		{"value", "value"}, {"VALUES", "value"},
		{"unknown", ""}, {" ", ""},
	} {
		if got := NormalizeNodeType(test.raw); got != test.want {
			t.Errorf("NormalizeNodeType(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
	for _, test := range []struct {
		input []string
		want  []string
	}{
		{nil, []string{"entity", "value"}},
		{[]string{"unknown", " "}, []string{"entity", "value"}},
		{[]string{"values", "entity", "value", "entities"}, []string{"value", "entity"}},
	} {
		if got := NormalizeTypes(test.input); !reflect.DeepEqual(got, test.want) {
			t.Errorf("NormalizeTypes(%#v) = %#v, want %#v", test.input, got, test.want)
		}
	}
}

func TestQueryDoesNotExposeDerivedSpaceScope(t *testing.T) {
	if _, ok := reflect.TypeOf(Query{}).FieldByName("spaceID"); ok {
		t.Fatal("graph query must not expose adapter-derived space scope")
	}
}
