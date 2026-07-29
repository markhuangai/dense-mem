package validation

import "testing"

func TestNotBlankValidator(t *testing.T) {
	type request struct {
		Name string `validate:"notblank"`
	}

	if err := ValidateStruct(request{Name: "value"}); err != nil {
		t.Fatalf("ValidateStruct returned error for nonblank value: %v", err)
	}
	if err := ValidateStruct(request{Name: "   "}); err == nil {
		t.Fatal("ValidateStruct accepted blank value")
	}
}

func TestMaxBytesValidatorFailsClosedForInvalidParam(t *testing.T) {
	err := ValidateVar([]string{"value"}, "maxbytes=abc")
	if err == nil {
		t.Fatal("ValidateVar accepted invalid maxbytes parameter")
	}
}

func TestMaxBytesValidatorAcceptsValidParam(t *testing.T) {
	err := ValidateVar([]string{"value"}, "maxbytes=32")
	if err != nil {
		t.Fatalf("ValidateVar returned error for valid maxbytes parameter: %v", err)
	}
}
