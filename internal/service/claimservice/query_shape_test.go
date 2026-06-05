package claimservice

import (
	"strings"
	"testing"
)

func TestCreateClaimDoesNotWriteSupportedByProperty(t *testing.T) {
	if strings.Contains(createClaimCypher, "c.supported_by") {
		t.Fatal("create claim must persist support via SUPPORTED_BY relationships, not c.supported_by")
	}
	if !strings.Contains(createClaimCypher, "MERGE (c)-[r:SUPPORTED_BY") {
		t.Fatal("create claim must write SUPPORTED_BY relationships")
	}
}
