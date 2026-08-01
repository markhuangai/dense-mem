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
			"content":                 "A uses B.",
			"idempotency_key":         "replacement-a",
			"supersedes_evidence_ids": []any{targetA},
		}},
		"proposal": directSupersessionProposal(),
	}
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
			}}, "proposal": directSupersessionProposal()},
			want: "idempotency_key is required",
		},
		{
			name: "does not share an idempotency key across replacements",
			input: map[string]any{"evidence": []any{
				map[string]any{"content": "First replacement.", "idempotency_key": "replacement-a", "supersedes_evidence_ids": []any{targetA}},
				map[string]any{"content": "Second replacement.", "idempotency_key": "replacement-a", "supersedes_evidence_ids": []any{targetB}},
			}, "proposal": directSupersessionProposal()},
			want: "duplicates evidence[0] direct supersession",
		},
		{
			name: "does not retire the same evidence twice in one intake",
			input: map[string]any{"evidence": []any{
				map[string]any{"content": "First replacement.", "idempotency_key": "replacement-a", "supersedes_evidence_ids": []any{targetA}},
				map[string]any{"content": "Second replacement.", "idempotency_key": "replacement-b", "supersedes_evidence_ids": []any{targetA}},
			}, "proposal": directSupersessionProposal()},
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
			}}, "proposal": directSupersessionProposal()},
			want: "cannot be combined with previous_source_revision",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateContractInput(remember, tc.input, []string{"write"})
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func directSupersessionProposal() map[string]any {
	return map[string]any{
		"entities": []any{
			map[string]any{"ref": "a", "name": "A", "evidence": []any{map[string]any{"evidence_index": 0, "start": 0, "end": 1}}},
			map[string]any{"ref": "b", "name": "B", "evidence": []any{map[string]any{"evidence_index": 0, "start": 7, "end": 8}}},
		},
		"relationships": []any{map[string]any{
			"proposal_id": "uses",
			"subject_ref": "a",
			"predicate":   map[string]any{"surface": "uses", "evidence_index": 0, "start": 2, "end": 6},
			"object_ref":  "b",
			"evidence":    []any{map[string]any{"evidence_index": 0, "start": 0, "end": 9}},
		}},
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
