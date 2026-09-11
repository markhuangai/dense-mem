package repository

import (
	"context"
	"time"

	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	"gorm.io/gorm"
)

const evidenceConflictRecallCandidateLimit = conflictpostgres.EvidenceConflictRecallCandidateLimit

// loadRecallEvidenceConflictRecords keeps the historical Recall adapter
// signature while delegating cited-evidence hydration to Conflict's native
// PostgreSQL reader.
func loadRecallEvidenceConflictRecords(
	ctx context.Context,
	tx *gorm.DB,
	input RecallEvidenceInput,
	results []RecallEvidenceHit,
) ([]EvidenceConflictCaseRecord, error) {
	return conflictpostgres.LoadEvidenceConflictRecords(ctx, tx, input, results)
}

func loadRecallEvidenceConflictCase(ctx context.Context, tx *gorm.DB, teamID, conflictID string, knownAt *time.Time) (*EvidenceConflictCaseRecord, error) {
	return conflictpostgres.LoadRecallEvidenceConflictCase(ctx, tx, teamID, conflictID, knownAt)
}

func loadEvidenceConflictEventsAt(ctx context.Context, tx *gorm.DB, teamID, conflictID string, knownAt *time.Time) ([]EvidenceConflictEventRecord, error) {
	return conflictpostgres.LoadEvidenceConflictEventsAt(ctx, tx, teamID, conflictID, knownAt)
}
