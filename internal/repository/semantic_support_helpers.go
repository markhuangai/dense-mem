package repository

import (
	"context"

	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

// errRelationshipVersionMismatch is retained for the legacy sqlmock test and
// is the same sentinel returned by the PostgreSQL write owner.
var errRelationshipVersionMismatch = knowledgepostgres.ErrRelationshipVersionMismatch

func requireRelationshipVersion(
	ctx context.Context,
	tx *gorm.DB,
	teamID, relationshipID, ownerProfileID string,
	version int,
) error {
	return knowledgepostgres.ValidateRelationshipVersion(ctx, knowledgepostgres.LegacyTransaction(tx), teamID, relationshipID, ownerProfileID, version)
}
