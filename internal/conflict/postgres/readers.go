package postgres

import (
	"context"
	"time"

	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"gorm.io/gorm"
)

// RelationshipConflictReader is the transaction-scoped read callback used by
// Recall and Trace. The caller owns the transaction and its visibility fence.
type RelationshipConflictReader func(context.Context, *gorm.DB, string, *time.Time, []recallcontract.RecallEvidenceHit) ([]tracecontract.RelationshipConflictCaseRecord, error)

// LoadRelationshipConflictRecordsInSpace loads bounded conflict projections on
// the supplied transaction without creating a second transaction boundary.
func LoadRelationshipConflictRecordsInSpace(ctx context.Context, tx *gorm.DB, teamID string, relationshipIDs []string, knownAt *time.Time, spaceID string) ([]tracecontract.RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsInSpace(ctx, tx, teamID, relationshipIDs, knownAt, spaceID)
}

// LoadRelationshipConflictRecordsByID loads bounded projections for callers
// that already selected conflict IDs under their own visibility fence.
func LoadRelationshipConflictRecordsByID(ctx context.Context, tx *gorm.DB, teamID string, conflictIDs []string, knownAt *time.Time) ([]tracecontract.RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsByID(ctx, tx, teamID, conflictIDs, knownAt)
}

func LoadActiveRelationshipConflictRecordsByIDBounded(ctx context.Context, tx *gorm.DB, teamID string, conflictIDs []string, knownAt *time.Time, positionLimit, supporterLimit int) ([]tracecontract.RelationshipConflictCaseRecord, error) {
	return loadActiveRelationshipConflictRecordsByIDBounded(ctx, tx, teamID, conflictIDs, knownAt, positionLimit, supporterLimit)
}

func LoadEvidenceConflictPositions(ctx context.Context, tx *gorm.DB, teamID, conflictID string) ([]conflictcontract.EvidenceConflictPositionRecord, error) {
	return loadEvidenceConflictPositions(ctx, tx, teamID, conflictID)
}

func LoadEvidenceConflictEvents(ctx context.Context, tx *gorm.DB, input conflictcontract.EvidenceConflictGetInput) ([]conflictcontract.EvidenceConflictEventRecord, *conflictcontract.EvidenceConflictEventCursor, error) {
	return loadEvidenceConflictEvents(ctx, tx, input, "")
}
