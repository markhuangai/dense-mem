package validation

import "testing"

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
