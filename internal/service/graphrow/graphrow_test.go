package graphrow

import (
	"reflect"
	"testing"
)

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
