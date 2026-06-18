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

func TestEmbeddingDimValidator(t *testing.T) {
	defer SetEmbeddingDimensions(0)

	SetEmbeddingDimensions(0)
	if err := ValidateVar([]float64{1, 2, 3}, "embedding_dim"); err != nil {
		t.Fatalf("ValidateVar returned error with unset embedding dimension: %v", err)
	}

	SetEmbeddingDimensions(3)
	if err := ValidateVar([]float64{1, 2, 3}, "embedding_dim"); err != nil {
		t.Fatalf("ValidateVar returned error for matching embedding dimension: %v", err)
	}
	if err := ValidateVar([]float64{1, 2}, "embedding_dim"); err == nil {
		t.Fatal("ValidateVar accepted mismatched embedding dimension")
	}
	if err := ValidateVar("not a slice", "embedding_dim"); err != nil {
		t.Fatalf("ValidateVar returned error for non-slice value: %v", err)
	}
}
