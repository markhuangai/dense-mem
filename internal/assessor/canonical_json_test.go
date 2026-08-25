package assessor

import "testing"

func TestCanonicalJSON(t *testing.T) {
	canonical, err := CanonicalJSON([]byte(` { "b": 2, "a": [true, null] } `))
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	if got, want := string(canonical), `{"a":[true,null],"b":2}`; got != want {
		t.Fatalf("CanonicalJSON() = %s, want %s", got, want)
	}

	for _, raw := range []string{`{`, `{} {}`, `{"value":1,"value":2}`, `{"items":[{"value":1,"value":2}]}`} {
		if _, err := CanonicalJSON([]byte(raw)); err == nil {
			t.Fatalf("CanonicalJSON(%q) error = nil", raw)
		}
	}
}
