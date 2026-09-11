package repository

import (
	"context"
	"time"

	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	"gorm.io/gorm"
)

func loadActiveRelationshipConflictRecordsByIDBounded(ctx context.Context, tx *gorm.DB, teamID string, conflictIDs []string, knownAt *time.Time, positionLimit, supporterLimit int) ([]RelationshipConflictCaseRecord, error) {
	return conflictpostgres.LoadActiveRelationshipConflictRecordsByIDBounded(ctx, tx, teamID, conflictIDs, knownAt, positionLimit, supporterLimit)
}
