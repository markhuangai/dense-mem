package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
)

// queueCredentialPrivateErasureTx is retained for legacy repository fixtures;
// the privacy PostgreSQL owner performs the queueing policy and transaction.
func queueCredentialPrivateErasureTx(ctx context.Context, tx *gorm.DB, teamID, credentialID uuid.UUID) error {
	return privacypostgres.QueueCredentialPrivateErasureTx(ctx, tx, teamID, credentialID)
}
