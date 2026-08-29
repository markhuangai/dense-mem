package registry

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRememberContractDirectEvidenceSupersessionRules(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)

	targetA := uuid.NewString()
	valid := map[string]any{
		"idempotency_key": "replacement-batch",
		"evidence":        validFlatRelationshipSubmission()["evidence"],
	}
	evidence := valid["evidence"].([]any)
	evidence[0].(map[string]any)["supersedes_evidence_ids"] = []any{targetA}
	valid["relationships"] = validFlatRelationshipSubmission()["relationships"]
	require.NoError(t, ValidateContractInput(remember, valid, []string{"write"}))

	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name: "does not retire the same evidence twice in one intake",
			input: map[string]any{"evidence": []any{
				map[string]any{"content": "First replacement.", "supersedes_evidence_ids": []any{targetA}},
				map[string]any{"content": "Second replacement.", "supersedes_evidence_ids": []any{targetA}},
			}},
			want: "duplicates target from evidence[0]",
		},
		{
			name: "detects equivalent UUID spellings as duplicates",
			input: map[string]any{"evidence": []any{
				map[string]any{"content": "First replacement.", "supersedes_evidence_ids": []any{strings.ToUpper(targetA)}},
				map[string]any{"content": "Second replacement.", "supersedes_evidence_ids": []any{targetA}},
			}},
			want: "duplicates target from evidence[0]",
		},
		{
			name: "keeps source revisions distinct from direct evidence retirement",
			input: map[string]any{"evidence": []any{map[string]any{
				"content":                  "Source revision replacement.",
				"supersedes_evidence_ids":  []any{targetA},
				"source_key":               "wiki:lifecycle",
				"source_revision":          "rev-2",
				"previous_source_revision": "rev-1",
			}}},
			want: "cannot be combined with previous_source_revision",
		},
		{
			name: "rejects evidence-level idempotency key",
			input: map[string]any{"evidence": []any{map[string]any{
				"content":         "Evidence key is no longer part of Remember.",
				"idempotency_key": "evidence-key",
			}}},
			want: "idempotency_key is not allowed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateContractInput(remember, withRequiredFlatRelationship(tc.input), []string{"write"})
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestDreamFeedbackContractKeepsItsIndependentSubmissionShape(t *testing.T) {
	tool, err := requireTool(toolMap(t), ToolResolveDreamFeedback)
	require.NoError(t, err)
	properties := schemaProperties(tool.InputSchema)
	evidence := properties["evidence"]
	evidenceItem, ok := evidence["items"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, schemaProperties(evidenceItem), "idempotency_key")

	relationships := properties["relationships"]
	relationshipItem, ok := relationships["items"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, schemaRequiredFields(relationshipItem), "modality")
	relationshipProperties := schemaProperties(relationshipItem)
	require.ElementsMatch(t, []string{"name", "entity_kind"}, schemaRequiredFields(relationshipProperties["subject"]))
	require.Equal(t, []string{"proposed_key"}, schemaRequiredFields(relationshipProperties["predicate"]))
	require.NotContains(t, schemaProperties(relationshipProperties["predicate"]), "known_predicate_key")
	objectEntity := schemaProperties(relationshipProperties["object"])["entity"]
	require.ElementsMatch(t, []string{"name", "entity_kind"}, schemaRequiredFields(objectEntity))

	input := validFlatRelationshipSubmission()
	delete(input, "idempotency_key")
	input["hypothesis_id"] = uuid.NewString()
	input["decision"] = "confirm_true"
	input["evidence"].([]any)[0].(map[string]any)["idempotency_key"] = "dream-evidence-key"
	input["relationships"].([]any)[0].(map[string]any)["modality"] = "statement"
	require.NoError(t, ValidateContractInput(tool, input, []string{"write"}))
}

func TestRetractEvidenceContractRejectsDuplicateEvidenceIDs(t *testing.T) {
	retract, err := requireTool(toolMap(t), ToolRetractEvidence)
	require.NoError(t, err)
	evidenceID := uuid.NewString()
	err = ValidateContractInput(retract, map[string]any{
		"evidence_ids":    []any{evidenceID, evidenceID},
		"reason":          "entered in error",
		"idempotency_key": "retract-duplicate-1",
	}, []string{"write"})
	require.ErrorContains(t, err, "evidence_ids[1]: duplicate value")
}
