package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecallResultJSONOmitsExecutionState(t *testing.T) {
	result := RecallResult{
		RecallID:          "recall-1",
		DiscoveryGuidance: "internal",
		SearchState:       "current",
		Degradation:       &RecallDegradationResult{Code: "internal"},
		Results:           []RecallResultItem{{EvidenceID: "evidence-1"}},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal recall result: %v", err)
	}
	if got := string(encoded); got != `{"recall_id":"recall-1","results":[{"evidence_id":"evidence-1","rank":0}],"conflicts":null,"related_relationships":null,"related_communities":null,"related_hypotheses":null,"search_states":{"evidence":"","relationships":""},"degradations":null,"suggested_actions":null}` {
		t.Fatalf("unexpected recall result JSON: %s", got)
	}
}

func TestRecallDiscoveryPathJSONShapes(t *testing.T) {
	community, err := json.Marshal(RecallDiscoveryPath{
		CommunityID:            "community-1",
		Relationships:          []RecallRelationshipHandle{{RelationshipID: "relationship-1"}},
		CommunityRelationships: []RelatedRelationshipSummary{{RelationshipID: "relationship-2"}},
	})
	if err != nil || !containsAll(string(community), `"community_id":"community-1"`, `"relationships"`, `"relationship_id":"relationship-2"`) {
		t.Fatalf("unexpected community path JSON: %s (%v)", community, err)
	}
	relationship, err := json.Marshal(RecallDiscoveryPath{Relationships: []RecallRelationshipHandle{{RelationshipID: "relationship-1"}}, EvidenceIDs: []string{"evidence-1"}})
	if err != nil || !containsAll(string(relationship), `"relationships"`, `"relationship_id":"relationship-1"`, `"evidence_ids":["evidence-1"]`) {
		t.Fatalf("unexpected relationship path JSON: %s (%v)", relationship, err)
	}
}

func TestRecallConflictSummaryJSONShapes(t *testing.T) {
	for _, kind := range []string{"relationship_conflict", "evidence_conflict"} {
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
