package skillpackservice

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestMemoryPackReconcilesTerminalSubmissionBeforeReturningOrRollingBack(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	ctx := authenticatedMemoryPackContext(teamID, ownerID, uuid.New())
	artifact := MemoryPackArtifact{
		Format:    MemoryPackFormat,
		PackID:    "pack-reconcile",
		Name:      "Reconcile pack",
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Relationships: []MemoryPackRelationship{{
			ItemID:           "item-1",
			Subject:          MemoryPackEndpoint{Ref: "subject", Kind: "entity", DisplayName: "Dense-Mem"},
			PredicateKey:     "uses",
			PredicateVersion: 1,
			Object:           MemoryPackEndpoint{Ref: "object", Kind: "entity", DisplayName: "PostgreSQL"},
		}},
	}
	artifactJSON := testArtifactJSON(t, artifact)
	loaded, err := NewMemoryPackService(MemoryPackDependencies{}).(*memoryPackService).loadArtifact(ctx, artifactJSON, "", "")
	if err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	importID := MemoryPackImportID(teamID.String(), ownerID.String(), loaded.hash, ModeReview)
	ledger := newLedgerStub()
	ledger.imports[ledgerKey(teamID.String(), importID)] = domain.SkillPackImport{
		ImportID:       importID,
		TeamID:         teamID.String(),
		OwnerProfileID: ownerID.String(),
		ArtifactHash:   loaded.hash,
		Mode:           ModeReview,
		Status:         domain.SkillPackImportStatusSubmitted,
		SubmissionID:   "submission-completed",
		ItemCount:      1,
		Summary: map[string]any{
			"items": []ImportItemResult{{ItemID: "item-1", Status: "submitted"}},
		},
	}
	remember := &rememberStub{status: &memoryservice.SubmissionStatusResult{
		SubmissionID:    "submission-completed",
		ProcessingState: string(domain.SubmissionCompleted),
	}}
	svc := NewMemoryPackService(MemoryPackDependencies{Remember: remember, Ledger: ledger})

	result, err := svc.Import(ctx, ImportRequest{ArtifactJSON: artifactJSON, Mode: ModeReview})
	if err != nil {
		t.Fatalf("Import retry: %v", err)
	}
	if result.Status != domain.SkillPackImportStatusApplied || result.AppliedCount != 1 || len(remember.statusReqs) != 1 {
		t.Fatalf("reconciled import = %#v status requests=%#v", result, remember.statusReqs)
	}
	stored := ledger.imports[ledgerKey(teamID.String(), importID)]
	if stored.Status != domain.SkillPackImportStatusApplied || stored.Summary["submission_processing_state"] != string(domain.SubmissionCompleted) {
		t.Fatalf("stored reconciled import = %#v", stored)
	}

	failedImportID := "import-terminal-failed"
	ledger.imports[ledgerKey(teamID.String(), failedImportID)] = domain.SkillPackImport{
		ImportID:       failedImportID,
		TeamID:         teamID.String(),
		OwnerProfileID: ownerID.String(),
		Status:         domain.SkillPackImportStatusSubmitted,
		SubmissionID:   "submission-failed",
	}
	ledger.changes[ledgerKey(teamID.String(), failedImportID)] = []domain.SkillPackImportChange{{
		ImportID: failedImportID, TeamID: teamID.String(), EntityType: "submission", EntityID: "submission-failed", Action: domain.SkillPackChangeActionLinked,
	}}
	remember.status = &memoryservice.SubmissionStatusResult{SubmissionID: "submission-failed", ProcessingState: string(domain.SubmissionFailed)}
	rollback, err := svc.Rollback(ctx, RollbackRequest{ImportID: failedImportID, DryRun: true})
	if err != nil {
		t.Fatalf("Rollback terminal failed import: %v", err)
	}
	if rollback.Status != "safe" {
		t.Fatalf("rollback = %#v", rollback)
	}
	if stored := ledger.imports[ledgerKey(teamID.String(), failedImportID)]; stored.Status != domain.SkillPackImportStatusFailed {
		t.Fatalf("failed submission import was not reconciled: %#v", stored)
	}
}
