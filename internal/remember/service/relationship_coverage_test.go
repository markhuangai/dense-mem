package service

import (
	"errors"
	"testing"
)

func TestValidateRelationshipCoverage(t *testing.T) {
	for _, test := range []struct {
		name      string
		count     int
		proposals []map[string]any
		path      string
		code      string
	}{
		{name: "omitted", count: 1, path: "/relationships", code: "required"},
		{name: "empty", count: 1, proposals: []map[string]any{}, path: "/relationships", code: "required"},
		{name: "uncited", count: 2, proposals: []map[string]any{{"evidence_indices": []any{float64(0)}}}, path: "/evidence/1", code: "coverage"},
		{name: "malformed", count: 1, proposals: []map[string]any{{"evidence_indices": []any{"0", true, 0.5, -1, 1}}}, path: "/evidence/0", code: "coverage"},
		{name: "complete", count: 2, proposals: []map[string]any{{"evidence_indices": []any{float64(0), float64(1)}}}},
		{name: "separate", count: 2, proposals: []map[string]any{{"evidence_indices": []int{0}}, {"evidence_indices": []int{1}}}},
		{name: "overlapping", count: 2, proposals: []map[string]any{{"evidence_indices": []any{0, 1}}, {"evidence_indices": []any{1}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRelationshipCoverage(test.count, test.proposals)
			if test.path == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var validation *RememberValidationError
			if !errors.As(err, &validation) || len(validation.Issues) != 1 || validation.Issues[0].Path != test.path || validation.Issues[0].Code != test.code {
				t.Fatalf("validation = %#v, error = %v", validation, err)
			}
		})
	}
}
