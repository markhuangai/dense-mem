package registry

import (
	"strings"
	"testing"
)

func TestRememberValidatesSpanlessRelationshipSemanticInvariants(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		input func() map[string]any
		want  string
	}{
		{
			name: "duplicate relationship ref",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationships := input["relationships"].([]any)
				input["relationships"] = append(relationships, relationship(input))
				return input
			},
			want: "relationships[1].ref: duplicate ref",
		},
		{
			name: "blank relationship ref",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["ref"] = " \t"
				return input
			},
			want: "relationships[0].ref",
		},
		{
			name: "trim-equivalent relationship refs",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationships := input["relationships"].([]any)
				duplicate := cloneMap(relationship(input))
				duplicate["ref"] = " uses-postgresql "
				input["relationships"] = append(relationships, duplicate)
				return input
			},
			want: "duplicate ref \"uses-postgresql\"",
		},
		{
			name: "duplicate evidence index",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["evidence_indices"] = []any{0, 0}
				return input
			},
			want: "duplicates evidence index 0",
		},
		{
			name: "evidence index outside submission",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["evidence_indices"] = []any{1}
				return input
			},
			want: "is outside submitted evidence",
		},
		{
			name: "date requires string canonical value",
			input: func() map[string]any {
				input := validFlatValueRelationshipSubmission()
				value := relationship(input)["object"].(map[string]any)["value"].(map[string]any)
				value["type"] = "date"
				value["value"] = true
				return input
			},
			want: "object.value.value must be a string for type date",
		},
		{
			name: "number requires numeric canonical value",
			input: func() map[string]any {
				input := validFlatValueRelationshipSubmission()
				value := relationship(input)["object"].(map[string]any)["value"].(map[string]any)
				value["value"] = "twenty"
				return input
			},
			want: "object.value.value must be a number for type number",
		},
		{
			name: "correction target rejects whitespace identifier",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["correction_target"] = map[string]any{"relationship_id": " ", "expected_version": 1}
				return input
			},
			want: "correction_target.relationship_id is required",
		},
		{
			name: "conflict context rejects whitespace identifier",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["conflict_context"] = map[string]any{"conflict_id": " ", "expected_version": 1}
				return input
			},
			want: "conflict_context.conflict_id is required",
		},
		{
			name: "validity window is ordered",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["valid_from"] = "2026-01-02T00:00:00Z"
				relationship(input)["valid_to"] = "2026-01-01T00:00:00Z"
				return input
			},
			want: "valid_to must not be before valid_from",
		},
		{name: "typed value remains valid without a surface or span", input: validFlatValueRelationshipSubmission},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateContractInput(remember, test.input(), []string{"write"})
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateContractInput: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func validFlatValueRelationshipSubmission() map[string]any {
	return map[string]any{
		"evidence": []any{map[string]any{"content": "Widget costs 20 USD."}},
		"relationships": []any{map[string]any{
			"ref":              "widget-cost",
			"subject":          map[string]any{"name": "Widget", "entity_kind": "product"},
			"predicate":        map[string]any{"proposed_key": "costs"},
			"object":           map[string]any{"value": map[string]any{"type": "number", "value": 20, "display": "20 USD", "unit": "USD"}},
			"polarity":         "+",
			"modality":         "statement",
			"evidence_indices": []any{0},
		}},
	}
}
