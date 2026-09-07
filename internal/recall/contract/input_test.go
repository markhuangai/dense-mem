package contract

import (
	"reflect"
	"testing"
)

func TestRequestContainsOnlyCallerOwnedFields(t *testing.T) {
	for _, name := range []string{"recallContract", "recallEmbedding", "recallEmbeddingReady"} {
		if _, ok := reflect.TypeOf(Request{}).FieldByName(name); ok {
			t.Fatalf("recall request exposes execution field %q", name)
		}
	}
}
