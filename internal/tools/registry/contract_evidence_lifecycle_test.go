package registry

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRememberContractDirectEvidenceSupersessionRules(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)

	targetA := uuid.NewString()
	targetB := uuid.NewString()
	valid := map[string]any{
		"evidence": []any{map[string]any{
			"content":                 "Dense-Mem now uses the replacement evidence.",
			"idempotency_key":         "replacement-a",
			"supersedes_evidence_ids": []any{targetA},
		}},
	}
	valid = withRequiredFlatRelationship(valid)
	require.NoError(t, ValidateContractInput(remember, valid, []string{"write"}))

	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name: "requires evidence idempotency key",
			input: map[string]any{"evidence": []any{map[string]any{
				"content":                 "Replacement without an evidence retry key.",
				"supersedes_evidence_ids": []any{targetA},
			}}},
			want: "idempotency_key is required",
		},
		{
			name: "does not share an idempotency key across replacements",
			input: map[string]any{"evidence": []any{
				map[string]any{"content": "First replacement.", "idempotency_key": "replacement-a", "supersedes_evidence_ids": []any{targetA}},
				map[string]any{"content": "Second replacement.", "idempotency_key": "replacement-a", "supersedes_evidence_ids": []any{targetB}},
			}},
			want: "duplicates evidence[0] direct supersession",
		},
		{
			name: "does not retire the same evidence twice in one intake",
			input: map[string]any{"evidence": []any{
				map[string]any{"content": "First replacement.", "idempotency_key": "replacement-a", "supersedes_evidence_ids": []any{targetA}},
				map[string]any{"content": "Second replacement.", "idempotency_key": "replacement-b", "supersedes_evidence_ids": []any{targetA}},
			}},
			want: "duplicates target from evidence[0]",
		},
		{
			name: "keeps source revisions distinct from direct evidence retirement",
			input: map[string]any{"evidence": []any{map[string]any{
				"content":                  "Source revision replacement.",
				"idempotency_key":          "replacement-a",
				"supersedes_evidence_ids":  []any{targetA},
				"source_key":               "wiki:lifecycle",
				"source_revision":          "rev-2",
				"previous_source_revision": "rev-1",
			}}},
			want: "cannot be combined with previous_source_revision",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateContractInput(remember, withRequiredFlatRelationship(tc.input), []string{"write"})
			require.ErrorContains(t, err, tc.want)
		})
	}
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
