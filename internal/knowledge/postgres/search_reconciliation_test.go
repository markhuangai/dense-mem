package postgres

import (
	"context"
	"testing"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

var _ knowledgecontract.SearchProjectionRepository = (*Store)(nil)

func TestSearchProjectionOwnerUsesKnowledgePort(t *testing.T) {
	store := NewStore(nil, nil, knowledgecontract.ConflictRuntimeConfig{})
	if store == nil {
		t.Fatal("NewStore returned nil")
	}
	if _, _, err := store.ReserveSearchReconciliationRun(context.Background(), knowledgecontract.SearchReconciliationRunInput{}); err == nil {
		t.Fatal("invalid reconciliation input should fail before database access")
	}
}

func TestCanonicalSearchDocumentLeavesUnknownSourceUnchanged(t *testing.T) {
	document := knowledgecontract.SearchDocumentForEmbedding{
		SearchDocumentResult: knowledgecontract.SearchDocumentResult{SourceKind: "unknown"},
		DocumentText:         "text",
	}
	expected, known, err := canonicalSearchDocument(context.Background(), nil, document)
	if err != nil {
		t.Fatalf("unknown source returned error: %v", err)
	}
	if known || expected != nil {
		t.Fatalf("unknown source = (%#v, %t), want (nil, false)", expected, known)
	}
}

func TestSearchProjectionValidationRejectsInvalidDocument(t *testing.T) {
	err := validateUpsertSearchDocumentInput(knowledgecontract.UpsertSearchDocumentInput{})
	if err == nil {
		t.Fatal("empty projection input should fail validation")
	}
	if _, err := vectorLiteral(nil); err == nil {
		t.Fatal("empty vector should fail validation")
	}
}
