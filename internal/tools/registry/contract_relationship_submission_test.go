package registry

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRememberAllowsOmittedOrEmptyRelationships(t *testing.T) {
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
	if err := ValidateContractInput(remember, missing, []string{"write"}); err != nil {
		t.Fatalf("omitted relationships rejected: %v", err)
	}

	legacy := cloneMap(input)
	delete(legacy, "relationships")
	legacy["proposal"] = map[string]any{}
	err = ValidateContractInput(remember, legacy, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "proposal") {
		t.Fatalf("legacy proposal error = %v, want unknown field", err)
	}

	empty := cloneMap(input)
	empty["relationships"] = []any{}
	if err := ValidateContractInput(remember, empty, []string{"write"}); err != nil {
		t.Fatalf("empty relationships rejected: %v", err)
	}
}

func TestRememberDoesNotRequireRelationshipCoverageForEveryEvidenceItem(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	input := validFlatRelationshipSubmission()
	input["evidence"] = append(input["evidence"].([]any), map[string]any{"content": "A second source."})
	if err := ValidateContractInput(remember, input, []string{"write"}); err != nil {
		t.Fatalf("incomplete relationship coverage rejected: %v", err)
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

func TestRememberKnownEvidenceIDsAreBoundedUUIDs(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	valid := validFlatRelationshipSubmission()
	relationship(valid)["known_evidence_ids"] = []any{uuid.NewString(), "  " + uuid.NewString() + "  "}
	if err := ValidateContractInput(remember, valid, []string{"write"}); err != nil {
		t.Fatalf("known evidence submission rejected: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{name: "malformed uuid", mutate: func(item map[string]any) {
			item["known_evidence_ids"] = []any{"not-a-uuid"}
		}, want: "must be a UUID"},
		{name: "duplicate uuid", mutate: func(item map[string]any) {
			id := uuid.NewString()
			item["known_evidence_ids"] = []any{id, id}
		}, want: "duplicates evidence ID"},
		{name: "too many ids", mutate: func(item map[string]any) {
			ids := make([]any, 21)
			for index := range ids {
				ids[index] = uuid.NewString()
			}
			item["known_evidence_ids"] = ids
		}, want: "exceeds maximum item count of 20"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := validFlatRelationshipSubmission()
			test.mutate(relationship(input))
			err := ValidateContractInput(remember, input, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDreamFeedbackRejectsKnownEvidenceIDs(t *testing.T) {
	dream, err := requireTool(toolMap(t), ToolResolveDreamFeedback)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"hypothesis_id": "hypothesis-1", "decision": "confirm_true",
		"evidence": []any{map[string]any{"content": "independent evidence"}},
		"relationships": []any{map[string]any{
			"ref": "r-1", "subject": map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
			"predicate": map[string]any{"proposed_key": "uses"},
			"object":    map[string]any{"entity": map[string]any{"name": "PostgreSQL", "entity_kind": "product"}},
			"polarity":  "+", "modality": "statement", "evidence_indices": []any{0},
			"known_evidence_ids": []any{uuid.NewString()},
		}},
	}
	if err := ValidateContractInput(dream, input, []string{"write"}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("dream known evidence validation error = %v", err)
	}
}

func validFlatRelationshipSubmission() map[string]any {
	return map[string]any{
		"idempotency_key": "remember-test-batch",
		"evidence":        []any{map[string]any{"content": "Dense-Mem uses PostgreSQL."}},
		"relationships": []any{map[string]any{
			"ref":              "uses-postgresql",
			"subject":          map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
			"predicate":        map[string]any{"proposed_key": "uses"},
			"object":           map[string]any{"entity": map[string]any{"name": "PostgreSQL", "entity_kind": "product"}},
			"polarity":         "+",
			"evidence_indices": []any{0},
		}},
	}
}

func relationship(input map[string]any) map[string]any {
	return input["relationships"].([]any)[0].(map[string]any)
}

func withRequiredFlatRelationship(input map[string]any) map[string]any {
	output := cloneMap(input)
	if _, exists := output["idempotency_key"]; !exists {
		output["idempotency_key"] = "remember-test-batch"
	}
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
