package registry

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	recallapp "github.com/markhuangai/dense-mem/internal/recall"

	"github.com/stretchr/testify/require"
)

func TestRecallContractOutputValidatesSpaceBranchDegradation(t *testing.T) {
	recall, err := requireTool(toolMap(t), ToolRecallMemory)
	if err != nil {
		t.Fatal(err)
	}
	output := recallContractOutput(&recallapp.RecallResult{
		RecallID: "rec-branch-degraded",
		SearchStates: recallapp.RecallSearchStates{
			Evidence: string(domain.SearchProjectionCurrent),
		},
		Degradations: []recallapp.RecallDegradationResult{{
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

func TestRecallContractEquivalentEvidenceSchemaAndSerialization(t *testing.T) {
	tool, err := requireTool(toolMap(t), ToolRecallMemory)
	require.NoError(t, err)
	result := &recallapp.RecallResult{RecallID: "rec-equivalents", Results: []recallapp.RecallResultItem{{EvidenceID: "E1", Context: "Atlas uses PostgreSQL.", SpaceKind: "team_shared", EquivalentEvidenceIDs: []string{"E2"}}}}
	encoded, err := json.Marshal(recallContractOutput(result))
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(encoded, &wire))
	require.NoError(t, ValidateInput(Tool{InputSchema: tool.OutputSchema}, wire))
	item := wire["results"].([]any)[0].(map[string]any)
	require.Equal(t, []any{"E2"}, item["equivalent_evidence_ids"])
	require.Equal(t, false, item["equivalents_truncated"])
	delete(item, "equivalents_truncated")
	require.Error(t, ValidateInput(Tool{InputSchema: tool.OutputSchema}, wire))
	item["equivalents_truncated"] = true
	item["equivalent_evidence_ids"] = make([]string, 21)
	require.Error(t, ValidateInput(Tool{InputSchema: tool.OutputSchema}, wire))
}

func TestRecallContractOutputValidatesEvidenceConflictBranch(t *testing.T) {
	recall, err := requireTool(toolMap(t), ToolRecallMemory)
	if err != nil {
		t.Fatal(err)
	}
	output := recallContractOutput(&recallapp.RecallResult{
		RecallID: "rec-evidence-conflict",
		Conflicts: []recallapp.RecallConflictSummary{{
			ConflictID: "conflict-1", Version: 2, Kind: "evidence_conflict", Status: "resolved",
			PreferredPositionID: "position-1", Positions: []recallapp.RecallConflictPosition{
				{PositionID: "position-1", Disposition: "preferred", EvidenceID: "evidence-1", OccurrenceID: "occurrence-1", Quote: "first", SpanStart: 0, SpanEnd: 5, Authority: "primary", Submitted: true},
				{PositionID: "position-2", Disposition: "candidate", EvidenceID: "evidence-2", OccurrenceID: "occurrence-2", Quote: "second", SpanStart: 0, SpanEnd: 6, Authority: "secondary", Submitted: false},
			}, PositionsTruncated: false,
		}},
		SearchStates: recallapp.RecallSearchStates{Evidence: string(domain.SearchProjectionCurrent)},
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
