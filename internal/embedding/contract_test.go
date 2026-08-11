package embedding

import (
	"slices"
	"testing"
)

func TestEmbeddingSourceKindsIsClosed(t *testing.T) {
	want := []string{"evidence", "search_document", "recall_query"}
	if got := EmbeddingSourceKinds(); !slices.Equal(got, want) {
		t.Fatalf("EmbeddingSourceKinds = %#v, want %#v", got, want)
	}
}
