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

	if len(generated.QRel.BadRefs) == 0 {
		t.Fatalf("QRel.BadRefs is empty, want at least one bad fact ref")
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

func TestBuildRelationalCaseEmitsImportableClaims(t *testing.T) {
	for i, category := range relationalCategories() {
		generated := buildRelationalCase("seed", i+1, i+1, category)
		for _, item := range generated.Corpus {
			if len(item.Claims) != 1 {
				t.Fatalf("%s claim count = %d, want 1", item.SourceDocID, len(item.Claims))
			}
			claim := item.Claims[0]
			if claim.Predicate != "relationship_to" {
				t.Fatalf("%s predicate = %q, want relationship_to", item.SourceDocID, claim.Predicate)
			}
			if claim.Modality != "assertion" {
				t.Fatalf("%s modality = %q, want assertion", item.SourceDocID, claim.Modality)
			}
			if claim.Polarity != "+" && claim.Polarity != "-" {
				t.Fatalf("%s polarity = %q, want + or -", item.SourceDocID, claim.Polarity)
			}
			relation, ok := claim.Classification["eval_relation_predicate"].(string)
			if !ok || relation == "" {
				t.Fatalf("%s missing eval_relation_predicate classification", item.SourceDocID)
			}
			relationObject, ok := claim.Classification["eval_relation_object"].(string)
			if !ok || relationObject == "" {
				t.Fatalf("%s missing eval_relation_object classification", item.SourceDocID)
			}
			if !strings.Contains(claim.Object, relationObject) {
				t.Fatalf("%s object = %q, want it to retain relation object %q", item.SourceDocID, claim.Object, relationObject)
			}
		}
	}
}
