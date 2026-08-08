package registry

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestFirstNonEmptyReturnsTrimmedNonNilValue(t *testing.T) {
	if got := firstNonEmpty("  ", " "+uuid.Nil.String()+" ", " canonical "); got != "canonical" {
		t.Fatalf("firstNonEmpty = %q; want canonical", got)
	}
}

func TestTraceConflictOutputsIncludePositionsAndResolution(t *testing.T) {
	dueAt := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	effectiveAt := dueAt.Add(time.Hour)
	out := traceConflictOutputs([]repository.RelationshipConflictCaseRecord{{
		ConflictID:          "00000000-0000-0000-0000-000000000101",
		Version:             2,
		Kind:                "cross_profile_current_state",
		Status:              "resolved",
		Question:            "Which value is current?",
		ReviewDueAt:         dueAt,
		EffectiveAt:         &effectiveAt,
		EffectiveTimeBasis:  "valid_from",
		PreferredPositionID: "00000000-0000-0000-0000-000000000201",
		Positions: []repository.RelationshipConflictPositionRecord{{
			PositionID:      "00000000-0000-0000-0000-000000000201",
			Disposition:     "preferred",
			RelationshipIDs: []string{"00000000-0000-0000-0000-000000000301"},
			OwnerProfileIDs: []string{"00000000-0000-0000-0000-000000000401"},
			EvidenceIDs:     []string{"00000000-0000-0000-0000-000000000501"},
		}},
	}})

	if len(out) != 1 {
		t.Fatalf("outputs = %#v", out)
	}
	item := out[0]
	if item["conflict_id"] != "00000000-0000-0000-0000-000000000101" ||
		item["status"] != "resolved" ||
		item["preferred_position_id"] != "00000000-0000-0000-0000-000000000201" ||
		item["positions_truncated"] != false {
		t.Fatalf("conflict output = %#v", item)
	}
	positions, ok := item["positions"].([]map[string]any)
	if !ok || len(positions) != 1 {
		t.Fatalf("positions = %#v", item["positions"])
	}
	position := positions[0]
	if position["position_id"] != "00000000-0000-0000-0000-000000000201" ||
		position["disposition"] != "preferred" {
		t.Fatalf("position output = %#v", position)
	}
	if got := position["relationship_ids"].([]string); len(got) != 1 || got[0] != "00000000-0000-0000-0000-000000000301" {
		t.Fatalf("relationship_ids = %#v", position["relationship_ids"])
	}
	if got := position["owner_profile_ids"].([]string); len(got) != 1 || got[0] != "00000000-0000-0000-0000-000000000401" {
		t.Fatalf("owner_profile_ids = %#v", position["owner_profile_ids"])
	}
	if got := position["result_evidence_ids"].([]string); len(got) != 1 || got[0] != "00000000-0000-0000-0000-000000000501" {
		t.Fatalf("result_evidence_ids = %#v", position["result_evidence_ids"])
	}
}

func TestTraceConflictOutputsEnforcePositionBounds(t *testing.T) {
	positions := make([]repository.RelationshipConflictPositionRecord, 0, 11)
	for i := 0; i < 11; i++ {
		positions = append(positions, repository.RelationshipConflictPositionRecord{
			PositionID:      "00000000-0000-0000-0000-000000000201",
			Disposition:     "candidate",
			RelationshipIDs: make([]string, 21),
			OwnerProfileIDs: make([]string, 21),
			EvidenceIDs:     make([]string, 51),
		})
	}
	out := traceConflictOutputs([]repository.RelationshipConflictCaseRecord{{
		ConflictID:  "00000000-0000-0000-0000-000000000101",
		Version:     1,
		Kind:        "cross_profile_current_state",
		Status:      "open",
		Question:    "Which value is current?",
		ReviewDueAt: time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC),
		Positions:   positions,
	}})

	if out[0]["positions_truncated"] != true {
		t.Fatalf("positions_truncated = %#v", out[0]["positions_truncated"])
	}
	bounded := out[0]["positions"].([]map[string]any)
	if len(bounded) != 10 {
		t.Fatalf("positions len = %d, want 10", len(bounded))
	}
	first := bounded[0]
	if len(first["relationship_ids"].([]string)) != 20 ||
		len(first["owner_profile_ids"].([]string)) != 20 ||
		len(first["result_evidence_ids"].([]string)) != 50 {
		t.Fatalf("bounded position = %#v", first)
	}
}
