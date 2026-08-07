package registry

import (
	"strings"
	"testing"
)

func TestValidateSubmittedRelationshipsUsesExactSupportedSpans(t *testing.T) {
	evidence := []any{map[string]any{"content": "Dense-Mem uses PostgreSQL"}}
	valid := func() map[string]any {
		return map[string]any{
			"ref":      "relationship-1",
			"supports": []any{map[string]any{"evidence_index": float64(0), "start": float64(0), "end": float64(25)}},
			"subject": map[string]any{
				"name": "Dense-Mem",
				"span": map[string]any{"evidence_index": float64(0), "start": float64(0), "end": float64(9)},
			},
			"predicate": map[string]any{
				"surface": "uses",
				"span":    map[string]any{"evidence_index": float64(0), "start": float64(10), "end": float64(14)},
			},
			"object": map[string]any{
				"entity": map[string]any{
					"name": "PostgreSQL",
					"span": map[string]any{"evidence_index": float64(0), "start": float64(15), "end": float64(25)},
				},
			},
		}
	}
	if err := validateSubmittedRelationships([]any{valid()}, evidence, "relationships"); err != nil {
		t.Fatalf("valid relationship rejected: %v", err)
	}
	if err := validateSubmittedRelationships([]any{valid(), valid()}, evidence, "relationships"); err == nil || !strings.Contains(err.Error(), "duplicate ref") {
		t.Fatalf("duplicate relationship ref error = %v", err)
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"blank ref", func(item map[string]any) { item["ref"] = " " }, "must not be blank"},
		{"missing support", func(item map[string]any) { item["supports"] = nil }, "not covered by a support span"},
		{"duplicate support", func(item map[string]any) {
			item["supports"] = []any{map[string]any{"evidence_index": float64(0), "start": float64(0), "end": float64(25)}, map[string]any{"evidence_index": float64(0), "start": float64(0), "end": float64(25)}}
		}, "duplicates a support span"},
		{"subject object", func(item map[string]any) { item["subject"] = "invalid" }, "subject must be an object"},
		{"subject surface", func(item map[string]any) { item["subject"].(map[string]any)["name"] = "wrong" }, "must equal its exact evidence span"},
		{"predicate surface", func(item map[string]any) { item["predicate"].(map[string]any)["surface"] = "wrong" }, "must equal its exact evidence span"},
		{"object choice", func(item map[string]any) {
			item["object"].(map[string]any)["value"] = map[string]any{"type": "string", "value": "x", "surface": "x", "span": map[string]any{"evidence_index": float64(0), "start": float64(0), "end": float64(1)}}
		}, "exactly one entity or value"},
		{"invalid span", func(item map[string]any) {
			item["predicate"].(map[string]any)["span"] = map[string]any{"evidence_index": float64(0), "start": float64(14), "end": float64(10)}
		}, "invalid code-point span"},
		{"invalid correction", func(item map[string]any) { item["correction_target"] = map[string]any{"relationship_id": ""} }, "correction_target.relationship_id"},
		{"invalid conflict", func(item map[string]any) {
			item["conflict_context"] = map[string]any{"conflict_id": "", "expected_version": float64(1)}
		}, "conflict_context.conflict_id"},
		{"invalid window", func(item map[string]any) {
			item["valid_from"] = "2026-01-02T00:00:00Z"
			item["valid_to"] = "2026-01-01T00:00:00Z"
		}, "must not be before valid_from"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := valid()
			tc.mutate(item)
			err := validateSubmittedRelationships([]any{item}, evidence, "relationships")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestContractValidationPrimitives(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value map[string]any
		want  string
	}{
		{"string", map[string]any{"type": "string", "value": "text"}, ""},
		{"date", map[string]any{"type": "date", "value": "2026-01-01"}, ""},
		{"date time", map[string]any{"type": "date_time", "value": "2026-01-01T00:00:00Z"}, ""},
		{"number", map[string]any{"type": "number", "value": float64(1)}, ""},
		{"boolean", map[string]any{"type": "boolean", "value": true}, ""},
		{"string type", map[string]any{"type": "string", "value": true}, "must be a string"},
		{"number type", map[string]any{"type": "number", "value": "1"}, "must be a number"},
		{"boolean type", map[string]any{"type": "boolean", "value": "true"}, "must be a boolean"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTypedValue(tc.value, "value")
			if tc.want == "" && err != nil {
				t.Fatalf("validateTypedValue = %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("validateTypedValue = %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := contractOptionalTime(nil, "time"); err != nil {
		t.Fatalf("nil optional time: %v", err)
	}
	if _, err := contractOptionalTime(" ", "time"); err != nil {
		t.Fatalf("blank optional time: %v", err)
	}
	if parsed, err := contractOptionalTime("2026-01-01T00:00:00+02:00", "time"); err != nil || parsed.Location().String() != "UTC" {
		t.Fatalf("valid optional time = %v, %v", parsed, err)
	}
	for _, raw := range []any{123, "not-a-time"} {
		if _, err := contractOptionalTime(raw, "time"); err == nil {
			t.Fatalf("optional time %v accepted", raw)
		}
	}
	for _, args := range []map[string]any{
		{"relationship_id": "rel", "expected_version": float64(1)},
		{"conflict_id": "conflict", "expected_version": float64(1)},
	} {
		if _, ok := args["relationship_id"]; ok {
			if err := validateCorrectionTarget(args, "target"); err != nil {
				t.Fatalf("valid correction target: %v", err)
			}
		} else if err := validateConflictContext(args, "context"); err != nil {
			t.Fatalf("valid conflict context: %v", err)
		}
	}
	if !contractValueEmpty(nil) || !contractValueEmpty(" ") || !contractValueEmpty([]any{}) || contractValueEmpty("value") {
		t.Fatal("contractValueEmpty did not distinguish empty values")
	}
}
