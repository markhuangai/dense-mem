package registry

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/stretchr/testify/require"
)

func TestRecallContractOutputValidatesSpaceBranchDegradation(t *testing.T) {
	recall, err := requireTool(toolMap(t), ToolRecallMemory)
	if err != nil {
		t.Fatal(err)
	}
	output := recallContractOutput(&memoryservice.RecallResult{
		RecallID: "rec-branch-degraded",
		SearchStates: memoryservice.RecallSearchStates{
			Evidence: string(domain.SearchProjectionCurrent), Relationships: string(domain.SearchProjectionNotRequired),
		},
		Degradations: []memoryservice.RecallDegradationResult{{
			Frontier: "evidence", Optional: true, Code: "space_branch_unavailable", Message: "authorized memory-space branch was unavailable",
		}},
	})
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	var wireOutput map[string]any
	if err := json.Unmarshal(encoded, &wireOutput); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: recall.OutputSchema}, wireOutput); err != nil {
		t.Fatalf("validate output: %v", err)
	}
}

func TestDreamContractOutputKeepsEvidenceReferencesOutOfRelationshipIDs(t *testing.T) {
	dream := &domain.Dream{
		DreamID: "hypothesis-evidence-1", Hypothesis: "Evidence may imply a relationship.",
		Status: domain.DreamStatusProposed, Lane: domain.DreamLaneEvidenceDiscovery,
		SourceRefs:        []domain.DreamSourceRef{{Type: "evidence", ID: "evidence-1"}},
		SourceEvidenceIDs: []string{"evidence-1"},
	}
	output := dreamContractOutput(dream)
	require.Empty(t, output["source_relationship_ids"])
	require.Equal(t, []string{"evidence-1"}, output["source_evidence_ids"])
}

func TestRecallContractOutputValidatesEmptyEquivalentRelationshipIDs(t *testing.T) {
	recall, err := requireTool(toolMap(t), ToolRecallMemory)
	if err != nil {
		t.Fatal(err)
	}
	output := recallContractOutput(&memoryservice.RecallResult{
		RecallID: "rec-empty-equivalents",
		RelatedRelationships: []memoryservice.RelatedRelationshipSummary{{
			RelationshipID:            "relationship-1",
			EquivalentRelationshipIDs: []string{},
			Subject:                   memoryservice.EntityHandle{EntityID: "entity-1", Name: "Dense-Mem"},
			Predicate:                 "uses",
			Object:                    memoryservice.SemanticObject{EntityID: "entity-2", Name: "PostgreSQL"},
			Polarity:                  "+",
			EvidenceIDs:               []string{"evidence-1"},
			SpaceKind:                 string(domain.MemorySpaceTeamShared),
		}},
		SearchStates: memoryservice.RecallSearchStates{
			Evidence: string(domain.SearchProjectionCurrent), Relationships: string(domain.SearchProjectionCurrent),
		},
	})
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	var wireOutput map[string]any
	if err := json.Unmarshal(encoded, &wireOutput); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: recall.OutputSchema}, wireOutput); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	relationships, ok := wireOutput["related_relationships"].([]any)
	if !ok || len(relationships) != 1 {
		t.Fatalf("related_relationships = %#v", wireOutput["related_relationships"])
	}
	relationship, ok := relationships[0].(map[string]any)
	if !ok {
		t.Fatalf("related_relationship = %#v", relationships[0])
	}
	equivalentIDs, ok := relationship["equivalent_relationship_ids"].([]any)
	if !ok || len(equivalentIDs) != 0 {
		t.Fatalf("equivalent_relationship_ids = %#v; want empty array", relationship["equivalent_relationship_ids"])
	}
}

func TestRecallContractOutputValidatesEvidenceConflictBranch(t *testing.T) {
	recall, err := requireTool(toolMap(t), ToolRecallMemory)
	if err != nil {
		t.Fatal(err)
	}
	output := recallContractOutput(&memoryservice.RecallResult{
		RecallID: "rec-evidence-conflict",
		Conflicts: []memoryservice.RecallConflictSummary{{
			ConflictID: "conflict-1", Version: 2, Kind: "evidence_conflict", Status: "resolved",
			PreferredPositionID: "position-1", Positions: []memoryservice.RecallConflictPosition{
				{PositionID: "position-1", Disposition: "preferred", EvidenceID: "evidence-1", OccurrenceID: "occurrence-1", Quote: "first", SpanStart: 0, SpanEnd: 5, Authority: "primary", Submitted: true},
				{PositionID: "position-2", Disposition: "candidate", EvidenceID: "evidence-2", OccurrenceID: "occurrence-2", Quote: "second", SpanStart: 0, SpanEnd: 6, Authority: "secondary", Submitted: false},
			}, PositionsTruncated: false,
		}},
		SearchStates: memoryservice.RecallSearchStates{Evidence: string(domain.SearchProjectionCurrent), Relationships: string(domain.SearchProjectionCurrent)},
	})
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	var wireOutput map[string]any
	if err := json.Unmarshal(encoded, &wireOutput); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInput(Tool{InputSchema: recall.OutputSchema}, wireOutput); err != nil {
		t.Fatalf("validate evidence conflict output: %v", err)
	}
}

func TestFirstNonEmptyReturnsTrimmedNonNilValue(t *testing.T) {
	if got := firstNonEmpty("  ", " "+uuid.Nil.String()+" ", " canonical "); got != "canonical" {
		t.Fatalf("firstNonEmpty = %q; want canonical", got)
	}
}

func TestTraceConflictOutputsIncludePositionsAndResolution(t *testing.T) {
	dueAt := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	effectiveAt := dueAt.Add(time.Hour)
	acceptedAt := dueAt.Add(-time.Hour)
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
			PositionID:     "00000000-0000-0000-0000-000000000201",
			Disposition:    "preferred",
			SupporterCount: 1,
			Supporters: []repository.RelationshipConflictSupporterRecord{{
				ProfileID:          "00000000-0000-0000-0000-000000000401",
				ProfileName:        "Profile A",
				StrongestAuthority: "authoritative",
				EvidenceID:         "00000000-0000-0000-0000-000000000501",
				AcceptedAt:         acceptedAt,
			}},
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
	if position["supporter_count"] != 1 || position["supporters_truncated"] != false {
		t.Fatalf("position provenance output = %#v", position)
	}
	supporters, ok := position["supporters"].([]map[string]any)
	if !ok || len(supporters) != 1 {
		t.Fatalf("supporters = %#v", position["supporters"])
	}
	if supporters[0]["profile_name"] != "Profile A" ||
		supporters[0]["strongest_authority"] != "authoritative" ||
		supporters[0]["accepted_at"] != acceptedAt.Format(time.RFC3339Nano) {
		t.Fatalf("supporter output = %#v", supporters[0])
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
			Supporters:      make([]repository.RelationshipConflictSupporterRecord, 21),
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
		len(first["result_evidence_ids"].([]string)) != 50 ||
		len(first["supporters"].([]map[string]any)) != 20 ||
		first["supporters_truncated"] != true {
		t.Fatalf("bounded position = %#v", first)
	}
}

func TestRecallConflictPositionSchemaValidatesSupporterProvenance(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	output := map[string]any{
		"position_id":          "00000000-0000-0000-0000-000000000201",
		"disposition":          "candidate",
		"supporter_count":      1,
		"supporters_truncated": false,
		"supporters": []any{map[string]any{
			"profile_id":          "00000000-0000-0000-0000-000000000401",
			"profile_name":        "Profile A",
			"strongest_authority": "authoritative",
			"evidence_id":         "00000000-0000-0000-0000-000000000501",
			"accepted_at":         acceptedAt.Format(time.RFC3339),
		}},
		"relationship_ids":    []any{"00000000-0000-0000-0000-000000000301"},
		"owner_profile_ids":   []any{"00000000-0000-0000-0000-000000000401"},
		"result_evidence_ids": []any{"00000000-0000-0000-0000-000000000501"},
	}
	if err := ValidateInput(Tool{InputSchema: recallConflictPositionSchema()}, output); err != nil {
		t.Fatalf("validate conflict supporter output: %v", err)
	}
	output["support_group_count"] = 2
	if err := ValidateInput(Tool{InputSchema: recallConflictPositionSchema()}, output); err == nil {
		t.Fatal("source-group field unexpectedly passed closed output validation")
	}
	delete(output, "support_group_count")
	delete(output, "supporters")
	if err := ValidateInput(Tool{InputSchema: recallConflictPositionSchema()}, output); err == nil {
		t.Fatal("missing supporters unexpectedly passed closed output validation")
	}
}
