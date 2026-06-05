package factservice

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestFactOwnerIDFallbacks(t *testing.T) {
	if got := factOwnerID(nil); got != "" {
		t.Fatalf("factOwnerID(nil) = %q; want empty", got)
	}

	fact := &domain.Fact{
		CreatedByProfileID:  "created-owner",
		PromotedByProfileID: "promoted-owner",
	}
	if got := factOwnerID(fact); got != "created-owner" {
		t.Fatalf("factOwnerID created fallback = %q; want created-owner", got)
	}

	fact.OwnerProfileID = "explicit-owner"
	if got := factOwnerID(fact); got != "explicit-owner" {
		t.Fatalf("factOwnerID explicit owner = %q; want explicit-owner", got)
	}
}
