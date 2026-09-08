package repository

import (
	"context"

	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

func lockRelationshipConflictSnapshotScope(ctx context.Context, tx *gorm.DB, teamID, semanticScopeKey string) error {
	return knowledgepostgres.LockRelationshipConflictSnapshotScopeTx(ctx, knowledgepostgres.LegacyTransaction(tx), teamID, semanticScopeKey)
}

func relationshipConflictScopeKey(record *RelationshipRecord, spaceID, spaceKind string) string {
	return knowledgepostgres.RelationshipConflictScopeKey(toKnowledgeRelationshipRecord(record), spaceID, spaceKind)
}
