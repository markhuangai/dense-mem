package evalharness

import "testing"

func TestDirectImportKeepsSourceDocID(t *testing.T) {
	if !directImportKeepsSourceDocID(nil, "doc-alpha") {
		t.Fatal("nil keep map should keep all source docs")
	}
	if !directImportKeepsSourceDocID(map[string]struct{}{}, "doc-alpha") {
		t.Fatal("empty keep map should keep all source docs")
	}

	keep := map[string]struct{}{"doc-alpha": {}}
	if !directImportKeepsSourceDocID(keep, "doc-alpha") {
		t.Fatal("listed source doc should be kept")
	}
	if directImportKeepsSourceDocID(keep, "doc-beta") {
		t.Fatal("unlisted source doc should not be kept")
	}
}

func TestDirectImportIdempotencyKey(t *testing.T) {
	if got := directImportIdempotencyKey("doc-alpha"); got != "eval:doc-alpha" {
		t.Fatalf("directImportIdempotencyKey = %q", got)
	}
}
