package main

import (
	"strings"
	"testing"
)

func TestBuildRelationalCasePromotesFactBadRefs(t *testing.T) {
	generated := buildRelationalCase("seed", 1, 1, relationalCategories()[0])
	corpusBySourceDocID := map[string]bool{}
	for _, item := range generated.Corpus {
		corpusBySourceDocID[item.SourceDocID] = item.AutoPromote
	}

	for _, ref := range generated.QRel.BadRefs {
		if ref.Type != "fact" {
			t.Fatalf("bad ref type = %q, want fact", ref.Type)
		}
		sourceDocID, _, ok := strings.Cut(ref.SourceDocID, ":fact:")
		if !ok {
			t.Fatalf("bad fact source_doc_id = %q, want typed fact source id", ref.SourceDocID)
		}
		if !corpusBySourceDocID[sourceDocID] {
			t.Fatalf("bad fact ref %q points to non-promoted corpus row", ref.SourceDocID)
		}
	}
}
