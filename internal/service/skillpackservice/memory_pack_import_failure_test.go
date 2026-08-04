package skillpackservice

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMemoryPackImportReturnsBuildAndStatusUpdateFailures(t *testing.T) {
	artifactJSON := testArtifactJSON(t, MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-oversized-evidence",
		Name:      "Oversized evidence pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Relationships: []MemoryPackRelationship{{
			ItemID:             "item-1",
			Subject:            MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Alpha"},
			PredicateKey:       "uses",
			PredicateVersion:   1,
			Object:             MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "Beta"},
			SupportEvidenceIDs: []string{"support-1"},
		}},
		Evidence: []MemoryPackEvidence{{
			EvidenceID: "support-1",
			Content:    strings.Repeat("界", 999),
		}},
	})
	statusErr := errors.New("build status update failed")
	ledger := newLedgerStub()
	ledger.updateErr = statusErr

	_, err := NewMemoryPackService(MemoryPackDependencies{
		Remember: &rememberStub{},
		Ledger:   ledger,
	}).Import(authenticatedMemoryPackContext(uuid.New(), uuid.New(), uuid.New()), ImportRequest{
		ArtifactJSON: artifactJSON,
		Mode:         ModeReview,
	})
	if err == nil || !strings.Contains(err.Error(), "max is 999") || !errors.Is(err, statusErr) {
		t.Fatalf("Import build/status failure error = %v", err)
	}
}
