package graphrow

import (
	"reflect"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestScalarHelpers(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	row := map[string]any{
		"string": "value",
		"int":    int32(7),
		"float":  int64(9),
		"time":   now,
	}

	if got := String(row, "string"); got != "value" {
		t.Fatalf("String = %q; want value", got)
	}
	if got := Int(row, "int"); got != 7 {
		t.Fatalf("Int = %d; want 7", got)
	}
	if got := IntValue(int64(8)); got != 8 {
		t.Fatalf("IntValue = %d; want 8", got)
	}
	if got := IntValue("8"); got != 0 {
		t.Fatalf("IntValue unsupported = %d; want 0", got)
	}
	if got := Float64(row, "float"); got != 9 {
		t.Fatalf("Float64 = %f; want 9", got)
	}
	if got := Float64Value(float32(2.5)); got != 2.5 {
		t.Fatalf("Float64Value = %f; want 2.5", got)
	}
	if got := Float64Value("2.5"); got != 0 {
		t.Fatalf("Float64Value unsupported = %f; want 0", got)
	}
	if got := Time(row, "time"); !got.Equal(now) {
		t.Fatalf("Time = %v; want %v", got, now)
	}
	if got := TimePtr(row, "time"); got == nil || !got.Equal(now) {
		t.Fatalf("TimePtr = %v; want %v", got, now)
	}
	if got := TimePtr(row, "missing"); got != nil {
		t.Fatalf("TimePtr missing = %v; want nil", got)
	}
	if got := FirstNonEmpty("", "first", "second"); got != "first" {
		t.Fatalf("FirstNonEmpty = %q; want first", got)
	}
	if got := FirstNonEmpty("", ""); got != "" {
		t.Fatalf("FirstNonEmpty empty = %q; want empty", got)
	}
}

func TestStringSliceFiltersEmptyValues(t *testing.T) {
	row := map[string]any{
		"decoded": []string{"claim-1", "", "claim-2"},
		"raw":     []any{"claim-1", "", "claim-2", 42},
	}

	for _, key := range []string{"decoded", "raw"} {
		got := StringSlice(row, key)
		want := []string{"claim-1", "claim-2"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("StringSlice(%q) = %#v; want %#v", key, got, want)
		}
	}
}

func TestEvidenceNormalizesRows(t *testing.T) {
	row := map[string]any{
		"evidence": []any{
			map[string]any{"speaker": "missing id"},
			map[string]any{
				"fragment_id":        "frag-1",
				"speaker":            "speaker",
				"span_start":         int64(2),
				"span_end":           int32(5),
				"extract_conf":       float32(0.75),
				"extraction_model":   "extractor",
				"extraction_version": "v1",
				"pipeline_run_id":    "run-1",
				"authority":          "primary",
			},
			"not a row",
		},
	}

	got := Evidence(row, "evidence")
	want := []domain.Evidence{{
		FragmentID:        "frag-1",
		Speaker:           "speaker",
		SpanStart:         2,
		SpanEnd:           5,
		ExtractConf:       0.75,
		ExtractionModel:   "extractor",
		ExtractionVersion: "v1",
		PipelineRunID:     "run-1",
		Authority:         domain.AuthorityPrimary,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Evidence = %#v; want %#v", got, want)
	}
	if got := Evidence(row, "missing"); got != nil {
		t.Fatalf("Evidence missing = %#v; want nil", got)
	}
}
