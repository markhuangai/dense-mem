package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecallResultJSONOmitsExecutionState(t *testing.T) {
	result := RecallResult{
		RecallID:    "recall-1",
		SearchState: "current",
		Degradation: &RecallDegradationResult{Code: "internal"},
		Results:     []RecallResultItem{{EvidenceID: "evidence-1", EquivalentEvidenceIDs: []string{}}},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal recall result: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"search_state", "degradation", "related_relationships", "related_communities", "discovery_paths"} {
		if _, found := wire[retired]; found {
			t.Fatalf("retired/internal field %s exposed", retired)
		}
	}
	if _, found := wire["related_topics"]; !found {
		t.Fatal("topic hints missing")
	}
}

func TestRecallConflictSummaryJSONShapes(t *testing.T) {
	for _, kind := range []string{"evidence_conflict"} {
		encoded, err := json.Marshal(RecallConflictSummary{
			ConflictID: "conflict-1",
			Kind:       kind,
			Positions:  []RecallConflictPosition{{PositionID: "position-1", Disposition: "preferred", EvidenceID: "evidence-1", OccurrenceID: "occurrence-1"}},
		})
		if err != nil || !containsAll(string(encoded), `"conflict_id":"conflict-1"`, `"kind":"`+kind+`"`, `"positions"`) {
			t.Fatalf("unexpected %s JSON: %s (%v)", kind, encoded, err)
		}
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
