package registry

import (
	"strings"
	"testing"
)

func TestRememberRequiresFlatSpanGroundedRelationships(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}

	input := validFlatRelationshipSubmission()
	if err := ValidateContractInput(remember, input, []string{"write"}); err != nil {
		t.Fatalf("ValidateContractInput valid submission: %v", err)
	}

	missing := cloneMap(input)
	delete(missing, "relationships")
	err = ValidateContractInput(remember, missing, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "relationships is required") {
		t.Fatalf("missing relationships error = %v", err)
	}

	legacy := cloneMap(input)
	delete(legacy, "relationships")
	legacy["proposal"] = map[string]any{}
	err = ValidateContractInput(remember, legacy, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "relationships is required") {
		t.Fatalf("legacy proposal error = %v", err)
	}
}

func TestRememberRequiresRelationshipCoverageForEverySubmittedEvidenceItem(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	input := validFlatRelationshipSubmission()
	input["evidence"] = append(input["evidence"].([]any), map[string]any{"content": "A second source."})
	err = ValidateContractInput(remember, input, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "missing evidence indexes: [1]") {
		t.Fatalf("coverage error = %v", err)
	}
}

func TestRememberRejectsInexactOrUnsupportedFlatRelationshipFields(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "subject surface differs from span",
			mutate: func(input map[string]any) {
				relationship(input)["subject"].(map[string]any)["name"] = "Dense Mem"
			},
			want: "subject.name must equal its exact evidence span",
		},
		{
			name: "predicate surface differs from span",
			mutate: func(input map[string]any) {
				relationship(input)["predicate"].(map[string]any)["surface"] = "uses PostgreSQL"
			},
			want: "predicate.surface must equal its exact evidence span",
		},
		{
			name: "component outside support",
			mutate: func(input map[string]any) {
				relationship(input)["supports"] = []any{map[string]any{"evidence_index": 0, "start": 10, "end": 14}}
			},
			want: "subject.span is not covered by a support span",
		},
		{
			name: "object has both forms",
			mutate: func(input map[string]any) {
				relationship(input)["object"].(map[string]any)["value"] = map[string]any{
					"type": "string", "value": "PostgreSQL", "surface": "PostgreSQL",
					"span": map[string]any{"evidence_index": 0, "start": 15, "end": 25},
				}
			},
			want: "object requires exactly one entity or value",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validFlatRelationshipSubmission()
			test.mutate(input)
			err := ValidateContractInput(remember, input, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func validFlatRelationshipSubmission() map[string]any {
	return map[string]any{
		"evidence": []any{map[string]any{"content": "Dense-Mem uses PostgreSQL."}},
		"relationships": []any{map[string]any{
			"ref": "uses-postgresql",
			"subject": map[string]any{
				"name": "Dense-Mem", "entity_kind": "project",
				"span": map[string]any{"evidence_index": 0, "start": 0, "end": 9},
			},
			"predicate": map[string]any{
				"proposed_key": "uses", "surface": "uses",
				"span": map[string]any{"evidence_index": 0, "start": 10, "end": 14},
			},
			"object": map[string]any{"entity": map[string]any{
				"name": "PostgreSQL", "entity_kind": "product",
				"span": map[string]any{"evidence_index": 0, "start": 15, "end": 25},
			}},
			"polarity": "+", "modality": "statement",
			"supports": []any{map[string]any{"evidence_index": 0, "start": 0, "end": 26}},
		}},
	}
}

func relationship(input map[string]any) map[string]any {
	return input["relationships"].([]any)[0].(map[string]any)
}

func withRequiredFlatRelationship(input map[string]any) map[string]any {
	output := cloneMap(input)
	evidence, _ := output["evidence"].([]any)
	relationshipInput := validFlatRelationshipSubmission()
	relationshipValue := relationship(relationshipInput)
	evidenceIndex := len(evidence)
	setRelationshipEvidenceIndex(relationshipValue, evidenceIndex)
	output["evidence"] = append(evidence, relationshipInput["evidence"].([]any)...)
	output["relationships"] = []any{relationshipValue}
	return output
}

func setRelationshipEvidenceIndex(relationship map[string]any, evidenceIndex int) {
	setSpanEvidenceIndex(relationship["subject"].(map[string]any)["span"].(map[string]any), evidenceIndex)
	setSpanEvidenceIndex(relationship["predicate"].(map[string]any)["span"].(map[string]any), evidenceIndex)
	object := relationship["object"].(map[string]any)
	setSpanEvidenceIndex(object["entity"].(map[string]any)["span"].(map[string]any), evidenceIndex)
	for _, support := range relationship["supports"].([]any) {
		setSpanEvidenceIndex(support.(map[string]any), evidenceIndex)
	}
}

func setSpanEvidenceIndex(span map[string]any, evidenceIndex int) {
	span["evidence_index"] = evidenceIndex
}
