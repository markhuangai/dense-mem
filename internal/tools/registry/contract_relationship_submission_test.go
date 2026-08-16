package registry

import (
	"strings"
	"testing"
)

func TestRememberRequiresSpanlessRelationships(t *testing.T) {
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

func TestRememberRejectsFormerPublicGroundingFields(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		field  string
		mutate func(map[string]any)
	}{
		{name: "subject span", field: "span", mutate: func(item map[string]any) {
			item["subject"].(map[string]any)["span"] = map[string]any{"evidence_index": 0, "start": 0, "end": 9}
		}},
		{name: "predicate surface", field: "surface", mutate: func(item map[string]any) {
			item["predicate"].(map[string]any)["surface"] = "uses"
		}},
		{name: "predicate span", field: "span", mutate: func(item map[string]any) {
			item["predicate"].(map[string]any)["span"] = map[string]any{"evidence_index": 0, "start": 10, "end": 14}
		}},
		{name: "object span", field: "span", mutate: func(item map[string]any) {
			item["object"].(map[string]any)["entity"].(map[string]any)["span"] = map[string]any{"evidence_index": 0, "start": 15, "end": 25}
		}},
		{name: "supports", field: "supports", mutate: func(item map[string]any) {
			item["supports"] = []any{map[string]any{"evidence_index": 0, "start": 0, "end": 26}}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validFlatRelationshipSubmission()
			test.mutate(relationship(input))
			err := ValidateContractInput(remember, input, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("error = %v, want rejected field %q", err, test.field)
			}
		})
	}
}

func TestRememberAllowsAssessorToGroundLogicalEndpoints(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	input := validFlatRelationshipSubmission()
	item := relationship(input)
	item["subject"].(map[string]any)["name"] = "It"
	item["predicate"].(map[string]any)["proposed_key"] = "depends_on"
	item["object"].(map[string]any)["entity"].(map[string]any)["name"] = "the primary database"
	if err := ValidateContractInput(remember, input, []string{"write"}); err != nil {
		t.Fatalf("logical endpoint proposal rejected: %v", err)
	}
}

func validFlatRelationshipSubmission() map[string]any {
	return map[string]any{
		"evidence": []any{map[string]any{"content": "Dense-Mem uses PostgreSQL."}},
		"relationships": []any{map[string]any{
			"ref":              "uses-postgresql",
			"subject":          map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
			"predicate":        map[string]any{"proposed_key": "uses"},
			"object":           map[string]any{"entity": map[string]any{"name": "PostgreSQL", "entity_kind": "product"}},
			"polarity":         "+",
			"modality":         "statement",
			"evidence_indices": []any{0},
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
	relationship["evidence_indices"] = []any{evidenceIndex}
}
