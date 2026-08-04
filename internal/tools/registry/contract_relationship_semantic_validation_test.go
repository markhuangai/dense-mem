package registry

import (
	"strings"
	"testing"
)

func TestRememberValidatesFlatRelationshipSemanticInvariants(t *testing.T) {
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
			want: "relationships[0].ref: must not be blank",
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
			want: "relationships[1].ref: duplicate ref \"uses-postgresql\"",
		},
		{
			name: "duplicate support span",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				supports := relationship(input)["supports"].([]any)
				relationship(input)["supports"] = append(supports, map[string]any{
					"evidence_index": 0,
					"start":          0,
					"end":            26,
				})
				return input
			},
			want: "relationships[0].supports[1] duplicates a support span",
		},
		{
			name: "predicate not covered by support",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["supports"] = []any{map[string]any{
					"evidence_index": 0,
					"start":          0,
					"end":            9,
				}}
				return input
			},
			want: "relationships[0].predicate.span is not covered by a support span",
		},
		{
			name: "object entity not covered by support",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["supports"] = []any{map[string]any{
					"evidence_index": 0,
					"start":          0,
					"end":            14,
				}}
				return input
			},
			want: "relationships[0].object.entity.span is not covered by a support span",
		},
		{
			name: "object entity surface differs from its span",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				entity := relationship(input)["object"].(map[string]any)["entity"].(map[string]any)
				entity["name"] = "Postgres"
				return input
			},
			want: "relationships[0].object.entity.name must equal its exact evidence span",
		},
		{
			name: "support references unavailable evidence",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["supports"] = []any{map[string]any{
					"evidence_index": 1,
					"start":          0,
					"end":            26,
				}}
				return input
			},
			want: "relationships[0].supports[0].evidence_index is outside submitted evidence",
		},
		{
			name: "support has empty span",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["supports"] = []any{map[string]any{
					"evidence_index": 0,
					"start":          0,
					"end":            0,
				}}
				return input
			},
			want: "relationships[0].supports[0] has invalid code-point span",
		},
		{
			name: "object value not covered by support",
			input: func() map[string]any {
				input := validFlatValueRelationshipSubmission()
				relationship(input)["supports"] = []any{map[string]any{
					"evidence_index": 0,
					"start":          0,
					"end":            12,
				}}
				return input
			},
			want: "relationships[0].object.value.span is not covered by a support span",
		},
		{
			name: "object value references unavailable evidence",
			input: func() map[string]any {
				input := validFlatValueRelationshipSubmission()
				value := relationship(input)["object"].(map[string]any)["value"].(map[string]any)
				value["span"].(map[string]any)["evidence_index"] = 1
				return input
			},
			want: "relationships[0].object.value.span.evidence_index is outside submitted evidence",
		},
		{
			name: "object value display not in support",
			input: func() map[string]any {
				input := validFlatValueRelationshipSubmission()
				value := relationship(input)["object"].(map[string]any)["value"].(map[string]any)
				value["display"] = "unrecorded value"
				return input
			},
			want: "relationships[0].object.value.display must occur within a support span",
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
			want: "relationships[0].object.value.value must be a string for type date",
		},
		{
			name: "number requires numeric canonical value",
			input: func() map[string]any {
				input := validFlatValueRelationshipSubmission()
				value := relationship(input)["object"].(map[string]any)["value"].(map[string]any)
				value["value"] = "twenty"
				return input
			},
			want: "relationships[0].object.value.value must be a number for type number",
		},
		{
			name: "correction target rejects whitespace identifier",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["correction_target"] = map[string]any{
					"relationship_id":  " ",
					"expected_version": 1,
				}
				return input
			},
			want: "relationships[0].correction_target.relationship_id is required",
		},
		{
			name: "conflict context rejects whitespace identifier",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["conflict_context"] = map[string]any{
					"conflict_id":      " ",
					"expected_version": 1,
				}
				return input
			},
			want: "relationships[0].conflict_context.conflict_id is required",
		},
		{
			name:  "anchored typed value display and unit",
			input: validFlatValueRelationshipSubmission,
		},
		{
			name: "complete correction and conflict context",
			input: func() map[string]any {
				input := validFlatRelationshipSubmission()
				relationship(input)["correction_target"] = map[string]any{
					"relationship_id":  "relationship-1",
					"expected_version": 1,
				}
				relationship(input)["conflict_context"] = map[string]any{
					"conflict_id":      "conflict-1",
					"expected_version": 1,
				}
				return input
			},
		},
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
			"ref": "widget-cost",
			"subject": map[string]any{
				"name": "Widget", "entity_kind": "product",
				"span": map[string]any{"evidence_index": 0, "start": 0, "end": 6},
			},
			"predicate": map[string]any{
				"proposed_key": "costs", "surface": "costs",
				"span": map[string]any{"evidence_index": 0, "start": 7, "end": 12},
			},
			"object": map[string]any{"value": map[string]any{
				"type": "number", "value": 20, "surface": "20", "display": "20 USD", "unit": "USD",
				"span": map[string]any{"evidence_index": 0, "start": 13, "end": 15},
			}},
			"polarity": "+", "modality": "statement",
			"supports": []any{map[string]any{"evidence_index": 0, "start": 0, "end": 20}},
		}},
	}
}
