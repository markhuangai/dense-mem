package postgrescompat

import (
	"database/sql/driver"
	"testing"

	"github.com/google/uuid"
)

func TestArrayValueAndScan(t *testing.T) {
	value := Array([]string{"plain", `quoted\value`, `quote"value`, ""})
	encoded, err := value.Value()
	if err != nil {
		t.Fatal(err)
	}
	if encoded != `{plain,"quoted\\value","quote\"value",""}` {
		t.Fatalf("encoded array = %v", encoded)
	}

	var decoded []string
	if err := Array(&decoded).Scan(encoded); err != nil {
		t.Fatal(err)
	}
	want := []string{"plain", `quoted\value`, `quote"value`, ""}
	if len(decoded) != len(want) {
		t.Fatalf("decoded length = %d, want %d", len(decoded), len(want))
	}
	for i := range want {
		if decoded[i] != want[i] {
			t.Errorf("decoded[%d] = %q, want %q", i, decoded[i], want[i])
		}
	}
}

func TestStringArrayNilAndEmpty(t *testing.T) {
	var nilArray StringArray
	value, err := nilArray.Value()
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		t.Fatalf("nil array value = %v, want nil", value)
	}

	empty := StringArray{}
	value, err = empty.Value()
	if err != nil {
		t.Fatal(err)
	}
	if value != "{}" {
		t.Fatalf("empty array value = %v, want {}", value)
	}

	var decoded StringArray
	if err := decoded.Scan(nil); err != nil {
		t.Fatal(err)
	}
	if decoded != nil {
		t.Fatalf("decoded nil array = %#v, want nil", decoded)
	}
}

func TestArrayValueSupportsIntegerSlices(t *testing.T) {
	value, err := Array([]int{1, 2, 3}).Value()
	if err != nil {
		t.Fatal(err)
	}
	if value != "{1,2,3}" {
		t.Fatalf("integer array value = %v, want {1,2,3}", value)
	}
}

func TestArrayValueSupportsUUIDSlices(t *testing.T) {
	values := []uuid.UUID{uuid.MustParse("00000000-0000-0000-0000-000000000001")}
	value, err := Array(values).Value()
	if err != nil {
		t.Fatal(err)
	}
	if value != "{00000000-0000-0000-0000-000000000001}" {
		t.Fatalf("UUID array value = %v", value)
	}
}

func TestQuoteHelpers(t *testing.T) {
	if got := QuoteIdentifier("a\"b"); got != `"a""b"` {
		t.Fatalf("QuoteIdentifier() = %q", got)
	}
	if got := QuoteLiteral(`a'b\\c`); got != ` E'a''b\\\\c'` {
		t.Fatalf("QuoteLiteral() = %q", got)
	}
}

func TestArrayImplementsDatabaseInterfaces(t *testing.T) {
	var _ driver.Valuer = Array([]string{})
	var _ interface{ Scan(any) error } = Array(&[]string{})
}
