package repository

import (
	"context"
	"time"

	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	"gorm.io/gorm"
)

const evidenceConflictRecallCandidateLimit = recallpostgres.EvidenceConflictRecallCandidateLimit

// loadRecallEvidenceConflictRecords preserves the in-package fixture helper
// while the live reader is owned by Recall's PostgreSQL adapter.
func loadRecallEvidenceConflictRecords(
	ctx context.Context,
	tx *gorm.DB,
	input RecallEvidenceInput,
	results []RecallEvidenceHit,
) ([]EvidenceConflictCaseRecord, error) {
	return recallpostgres.LoadRecallEvidenceConflictRecords(ctx, tx, input, results)
}

// loadRecallEvidenceConflictCase preserves the in-package fixture helper
// while historical hydration is owned by Recall's PostgreSQL adapter.
func loadRecallEvidenceConflictCase(
	ctx context.Context,
	tx *gorm.DB,
	teamID, conflictID string,
	knownAt *time.Time,
) (*EvidenceConflictCaseRecord, error) {
	return recallpostgres.LoadRecallEvidenceConflictCase(ctx, tx, teamID, conflictID, knownAt)
}

// loadEvidenceConflictEventsAt preserves the in-package fixture helper while
// event hydration is owned by Recall's PostgreSQL adapter.
func loadEvidenceConflictEventsAt(
	ctx context.Context,
	tx *gorm.DB,
	teamID, conflictID string,
	knownAt *time.Time,
) ([]EvidenceConflictEventRecord, error) {
	return recallpostgres.LoadRecallEvidenceConflictEventsAt(ctx, tx, teamID, conflictID, knownAt)
}
