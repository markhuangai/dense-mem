package domain

import "testing"

func TestNormalizeReadIDListRetainsInvalidNonblankValues(t *testing.T) {
	got := NormalizeReadIDList([]string{" first ", "", "invalid", "first", " invalid "})
	want := []string{"first", "invalid"}
	if len(got) != len(want) {
		t.Fatalf("NormalizeReadIDList() = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("NormalizeReadIDList() = %#v, want %#v", got, want)
		}
	}
}
