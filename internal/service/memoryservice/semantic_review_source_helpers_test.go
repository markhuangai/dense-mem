package memoryservice

import (
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestV2PlacementReviewRelationshipSpecsAcceptsProviderRequiredFields(t *testing.T) {
	validFrom := "2026-07-01T00:00:00Z"
	validTo := "2026-08-01T00:00:00Z"
	specs, validationErrors := v2PlacementReviewRelationshipSpecs(map[string]any{
		"relationships": []any{map[string]any{
			"proposal_id":          "rel:latency",
			"subject":              "dense-mem",
			"predicate":            "has latency",
			"predicate_candidates": []any{" has_latency ", "has_latency"},
			"relationship_kind":    "measurement",
			"object_value":         map[string]any{"type": "duration", "value": 135.5, "unit": "ms"},
			"valid_from":           validFrom,
			"valid_to":             validTo,
			"correction_target":    map[string]any{"relationship_id": "relationship-1", "expected_version": 7},
			"evidence":             []any{map[string]any{"quote": "migration is slow"}},
		}},
	}, repository.V2EvidenceFragment{
		EvidenceIndex: 0,
		Content:       "The production migration is slow today.",
	}, "evidence-1")

	if len(validationErrors) != 0 {
		t.Fatalf("validation errors = %#v", validationErrors)
	}
	if len(specs) != 1 {
		t.Fatalf("specs = %#v", specs)
	}
	spec := specs[0]
	if spec.Ref != "rel:latency" || spec.SubjectRef != "dense-mem" || spec.ObjectValue == nil {
		t.Fatalf("spec = %#v", spec)
	}
	if len(spec.PredicateCandidates) != 1 || spec.PredicateCandidates[0] != "has_latency" {
		t.Fatalf("predicate candidates = %#v", spec.PredicateCandidates)
	}
	if spec.Quote != "migration is slow" || spec.Start != 15 || spec.End != 32 {
		t.Fatalf("span = quote:%q start:%d end:%d", spec.Quote, spec.Start, spec.End)
	}
	if spec.ValidFrom == nil || spec.ValidTo == nil || !spec.ValidFrom.Before(*spec.ValidTo) {
		t.Fatalf("validity window = %#v %#v", spec.ValidFrom, spec.ValidTo)
	}
	if spec.CorrectionTarget == nil || spec.CorrectionTarget.ExpectedVersion != 7 {
		t.Fatalf("correction target = %#v", spec.CorrectionTarget)
	}
}

func TestV2PlacementReviewRelationshipSpecsReturnsBoundedValidationErrors(t *testing.T) {
	content := "Mark works on Dense-Mem."
	specs, validationErrors := v2PlacementReviewRelationshipSpecs(map[string]any{
		"relationship_hints": []map[string]any{
			{
				"subject_ref":       "mark",
				"predicate":         "works_on",
				"object_ref":        "dense-mem",
				"relationship_kind": "state",
			},
			{
				"subject_ref":          "mark",
				"predicate":            "works_on",
				"predicate_candidates": []string{"works_on"},
				"object_ref":           "dense-mem",
			},
			{
				"subject_ref":          "mark",
				"predicate":            "works_on",
				"predicate_candidates": []string{"works_on"},
				"relationship_kind":    "state",
				"object_ref":           "dense-mem",
				"valid_from":           "2026-08-01T00:00:00Z",
				"valid_to":             "2026-07-01T00:00:00Z",
			},
			{
				"subject_ref":          "mark",
				"predicate":            "works_on",
				"predicate_candidates": []string{"works_on"},
				"relationship_kind":    "state",
				"object_ref":           "dense-mem",
				"valid_from":           12,
			},
			{
				"subject_ref":          "mark",
				"predicate":            "works_on",
				"predicate_candidates": []string{"works_on"},
				"relationship_kind":    "state",
				"object_ref":           "dense-mem",
				"conflict_context":     map[string]any{"expected_version": 1},
			},
		},
	}, repository.V2EvidenceFragment{Content: content}, "evidence-1")

	if len(specs) != 0 {
		t.Fatalf("specs = %#v", specs)
	}
	fields := []string{}
	for _, validationError := range validationErrors {
		fields = append(fields, validationError.Field)
	}
	want := []string{
		"relationship_hints[0].predicate_candidates",
		"relationship_hints[1].relationship_kind",
		"relationship_hints[2].valid_to",
		"relationship_hints[3].valid_from",
		"relationship_hints[4].conflict_context",
	}
	if len(fields) != len(want) {
		t.Fatalf("validation fields = %#v", fields)
	}
	for i := range want {
		if fields[i] != want[i] {
			t.Fatalf("validation fields = %#v", fields)
		}
	}
}

func TestV2ReviewSpanHelpersUseRuneOffsets(t *testing.T) {
	start, end, ok := v2ReviewFindSpan("Mark ships 🚀 quickly", "🚀 quickly")
	if !ok || start != 11 || end != 20 {
		t.Fatalf("span = %d %d %v", start, end, ok)
	}
	if quote := v2ReviewSpanQuote("Mark ships 🚀 quickly", start, end); quote != "🚀 quickly" {
		t.Fatalf("quote = %q", quote)
	}
	if _, _, ok := v2ReviewFindSpan("content", "missing"); ok {
		t.Fatal("missing quote matched")
	}
	if quote := v2ReviewSpanQuote("content", -1, 4); quote != "" {
		t.Fatalf("invalid quote = %q", quote)
	}
}

func TestV2ReviewOptionalTimeNormalizesPointers(t *testing.T) {
	local := time.Date(2026, 7, 23, 3, 0, 0, 0, time.FixedZone("offset", -7*60*60))
	parsed, err := v2ReviewOptionalTime(map[string]any{"valid_at": &local}, "valid_at")
	if err != nil {
		t.Fatalf("optional time: %v", err)
	}
	if parsed == nil || parsed.Location() != time.UTC || parsed.Hour() != 10 {
		t.Fatalf("parsed = %#v", parsed)
	}
}
