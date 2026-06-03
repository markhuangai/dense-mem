package claimdedupe

import (
	"strings"
	"testing"
)

func TestDedupeLookupHydratesSupportedByFromRelationships(t *testing.T) {
	for name, query := range map[string]string{
		"idempotency": byIdempotencyKeyQuery,
		"contentHash": byContentHashQuery,
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(query, "coalesce(c.supported_by") {
				t.Fatal("dedupe lookup must not read c.supported_by")
			}
			if !strings.Contains(query, "SUPPORTED_BY") {
				t.Fatal("dedupe lookup must hydrate support from SUPPORTED_BY relationships")
			}
		})
	}
}
