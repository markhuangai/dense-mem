package registry

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestValidateInputSchemaBranches(t *testing.T) {
	tool := Tool{
		Name: "complex",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []any{"name", "count"},
			"additionalProperties": false,
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "minLength": 2, "maxLength": 5},
				"count":    map[string]any{"type": "integer", "minimum": 1, "maximum": 3},
				"ratio":    map[string]any{"type": "number", "minimum": 0.25, "maximum": 0.75},
				"enabled":  map[string]any{"type": "boolean"},
				"nullable": map[string]any{"type": []any{"string", "null"}, "maxLength": 5},
				"at": map[string]any{
					"type": []any{"string", "null"}, "format": "date-time", "x-enforce-format": true,
				},
				"bounded": map[string]any{
					"type": "object", "additionalProperties": true,
					"maxProperties": 2, "x-max-bytes": 64, "x-max-depth": 2,
				},
				"mode": map[string]any{"type": "string", "enum": []any{"fast", "safe"}},
				"nested": map[string]any{
					"type":                 "object",
					"required":             []string{"id"},
					"additionalProperties": false,
					"properties": map[string]any{
						"id": map[string]any{"type": "string"},
					},
				},
				"items": map[string]any{
					"type":     "array",
					"minItems": 1,
					"maxItems": 2,
					"items":    map[string]any{"type": "number", "enum": []any{json.Number("1"), float64(2)}},
				},
			},
		},
	}

	valid := map[string]any{
		"name":     "mark",
		"count":    json.Number("2"),
		"ratio":    float32(0.5),
		"enabled":  true,
		"nullable": nil,
		"at":       "2026-07-17T00:00:00Z",
		"bounded":  map[string]any{"key": "ok"},
		"mode":     "fast",
		"nested":   map[string]string{"id": "n1"},
		"items":    []any{json.Number("1"), float64(2)},
	}
	if err := ValidateInput(tool, valid); err != nil {
		t.Fatalf("ValidateInput valid input: %v", err)
	}

	nonASCIIValid := map[string]any{
		"name":    strings.Repeat("界", 5),
		"count":   json.Number("2"),
		"ratio":   float32(0.5),
		"enabled": true,
		"mode":    "fast",
		"nested":  map[string]string{"id": "n1"},
		"items":   []any{json.Number("1"), float64(2)},
	}
	if err := ValidateInput(tool, nonASCIIValid); err != nil {
		t.Fatalf("ValidateInput non-ASCII exact max input: %v", err)
	}

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing required", map[string]any{"name": "ok"}, "count is required"},
		{"unknown field", map[string]any{"name": "ok", "count": 1, "extra": true}, "unknown field: extra"},
		{"string type", map[string]any{"name": 3, "count": 1}, "name must be a string"},
		{"string min length", map[string]any{"name": "a", "count": 1}, "at least 2"},
		{"string min length non-ASCII", map[string]any{"name": "界", "count": 1}, "at least 2"},
		{"string max length", map[string]any{"name": "abcdef", "count": 1}, "maximum length"},
		{"string max length non-ASCII", map[string]any{"name": strings.Repeat("界", 6), "count": 1}, "maximum length"},
		{"integer type", map[string]any{"name": "ok", "count": 1.2}, "count must be an integer"},
		{"integer finite", map[string]any{"name": "ok", "count": math.Inf(1)}, "count must be an integer"},
		{"integer min", map[string]any{"name": "ok", "count": 0}, "greater than or equal to 1"},
		{"integer max", map[string]any{"name": "ok", "count": 4}, "less than or equal to 3"},
		{"number type", map[string]any{"name": "ok", "count": 1, "ratio": "bad"}, "ratio must be a number"},
		{"number finite", map[string]any{"name": "ok", "count": 1, "ratio": math.NaN()}, "ratio must be a number"},
		{"number min", map[string]any{"name": "ok", "count": 1, "ratio": 0.1}, "greater than or equal to 0.25"},
		{"number max", map[string]any{"name": "ok", "count": 1, "ratio": 0.9}, "less than or equal to 0.75"},
		{"boolean type", map[string]any{"name": "ok", "count": 1, "enabled": "yes"}, "enabled must be a boolean"},
		{"union type", map[string]any{"name": "ok", "count": 1, "nullable": true}, "nullable must be one of"},
		{"date-time format", map[string]any{"name": "ok", "count": 1, "at": "17 July"}, "valid RFC 3339"},
		{"object type", map[string]any{"name": "ok", "count": 1, "nested": "bad"}, "nested must be an object"},
		{"object required", map[string]any{"name": "ok", "count": 1, "nested": map[string]any{}}, "nested.id is required"},
		{"object additional", map[string]any{"name": "ok", "count": 1, "nested": map[string]any{"id": "n1", "extra": true}}, "nested.extra is not allowed"},
		{"object max properties", map[string]any{"name": "ok", "count": 1, "bounded": map[string]any{"a": 1, "b": 2, "c": 3}}, "maximum property count"},
		{"object max bytes", map[string]any{"name": "ok", "count": 1, "bounded": map[string]any{"a": strings.Repeat("x", 80)}}, "maximum encoded size"},
		{"object max depth", map[string]any{"name": "ok", "count": 1, "bounded": map[string]any{"a": map[string]any{"b": map[string]any{"c": true}}}}, "maximum nesting depth"},
		{"array type", map[string]any{"name": "ok", "count": 1, "items": "bad"}, "items must be an array"},
		{"array min", map[string]any{"name": "ok", "count": 1, "items": []any{}}, "at least 1"},
		{"array max", map[string]any{"name": "ok", "count": 1, "items": []any{1, 2, 3}}, "maximum item count"},
		{"array item", map[string]any{"name": "ok", "count": 1, "items": []any{3}}, "items[0] must be one of"},
		{"enum string", map[string]any{"name": "ok", "count": 1, "mode": "turbo"}, "mode must be one of"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateInput(tool, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateInput error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateInputOneOfStillAppliesEnclosingObjectConstraints(t *testing.T) {
	tool := Tool{InputSchema: map[string]any{
		"type":                 "object",
		"required":             []any{"common"},
		"additionalProperties": false,
		"properties": map[string]any{
			"common": map[string]any{"type": "string"},
			"kind":   map[string]any{"type": "string"},
		},
		"oneOf": []any{
			map[string]any{
				"type":                 "object",
				"required":             []any{"kind"},
				"properties":           map[string]any{"kind": map[string]any{"type": "string"}},
				"additionalProperties": true,
			},
		},
		"x-enforce-one-of": true,
	}}
	if err := ValidateInput(tool, map[string]any{"kind": "a", "common": "ok"}); err != nil {
		t.Fatalf("valid oneOf input rejected: %v", err)
	}
	if err := ValidateInput(tool, map[string]any{"kind": "a"}); err == nil || !strings.Contains(err.Error(), "common is required") {
		t.Fatalf("missing enclosing required field error = %v", err)
	}
	if err := ValidateInput(tool, map[string]any{"kind": "a", "common": "ok", "extra": true}); err == nil || !strings.Contains(err.Error(), "unknown field: extra") {
		t.Fatalf("enclosing additionalProperties error = %v", err)
	}
}

func TestValidateInputDateTimeEnforcementIsOptIn(t *testing.T) {
	dateTime := map[string]any{"type": "string", "format": "date-time"}
	tool := Tool{InputSchema: map[string]any{
		"type":       "object",
		"properties": map[string]any{"at": dateTime},
	}}
	args := map[string]any{"at": "17 July"}
	if err := ValidateInput(tool, args); err != nil {
		t.Fatalf("annotation-only format changed existing validation: %v", err)
	}
	dateTime["x-enforce-format"] = true
	if err := ValidateInput(tool, args); err == nil {
		t.Fatal("enforced date-time accepted invalid RFC 3339 input")
	}
}

func TestValidateInputUUIDEnforcement(t *testing.T) {
	uuidSchema := map[string]any{"type": "string", "format": "uuid", "x-enforce-format": true}
	tool := Tool{InputSchema: map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": uuidSchema},
	}}
	if err := ValidateInput(tool, map[string]any{"id": "00000000-0000-0000-0000-000000000001"}); err != nil {
		t.Fatalf("valid UUID rejected: %v", err)
	}
	if err := ValidateInput(tool, map[string]any{"id": "not-a-uuid"}); err == nil || !strings.Contains(err.Error(), "valid UUID") {
		t.Fatalf("invalid UUID error = %v, want valid UUID error", err)
	}
}

func TestValidationHelperBranches(t *testing.T) {
	if err := ValidateInput(Tool{}, map[string]any{"anything": true}); err != nil {
		t.Fatalf("ValidateInput empty schema = %v, want nil", err)
	}

	if _, ok := schemaNumber("not-number"); ok {
		t.Fatal("schemaNumber string ok = true, want false")
	}
	if fields, ok := objectFields(map[int]any{1: "bad"}); ok || fields != nil {
		t.Fatalf("objectFields non-string-key map = %#v, %v; want nil, false", fields, ok)
	}

	if err := validateSchemaEnum("field", 1, map[string]any{"enum": []string{"one"}}); err != nil {
		t.Fatalf("validateSchemaEnum ignores non-string for []string enum: %v", err)
	}
	if err := validateSchemaEnum("field", "anything", map[string]any{"enum": "bad"}); err != nil {
		t.Fatalf("validateSchemaEnum ignores malformed enum: %v", err)
	}
	if got := schemaEnumDescription("bad"); got != "the allowed values" {
		t.Fatalf("schemaEnumDescription malformed = %q", got)
	}
	if required := schemaRequiredFields(map[string]any{"required": "bad"}); required != nil {
		t.Fatalf("schemaRequiredFields malformed = %#v, want nil", required)
	}
}
