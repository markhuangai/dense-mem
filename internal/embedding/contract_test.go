package embedding

import (
	"slices"
	"testing"

	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
)

func TestEmbeddingSourceKindsIsClosed(t *testing.T) {
	want := []string{"evidence", "search_document", "recall_query"}
	if got := embeddingcontract.EmbeddingSourceKinds(); !slices.Equal(got, want) {
		t.Fatalf("EmbeddingSourceKinds = %#v, want %#v", got, want)
	}
}
