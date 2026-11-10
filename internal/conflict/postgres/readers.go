package postgres

import (
	"context"
	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	"github.com/markhuangai/dense-mem/internal/storage/postgres/conflictread"
	"gorm.io/gorm"
)

func LoadEvidenceConflictPositions(ctx context.Context, tx *gorm.DB, teamID, conflictID string) ([]conflictcontract.EvidenceConflictPositionRecord, error) {
	return conflictread.LoadEvidenceConflictPositions(ctx, tx, teamID, conflictID)
}

func LoadEvidenceConflictEvents(ctx context.Context, tx *gorm.DB, input conflictcontract.EvidenceConflictGetInput) ([]conflictcontract.EvidenceConflictEventRecord, *conflictcontract.EvidenceConflictEventCursor, error) {
	return loadEvidenceConflictEvents(ctx, tx, input, "")
}
