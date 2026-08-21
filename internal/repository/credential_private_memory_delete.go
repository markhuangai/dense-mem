package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func queueCredentialPrivateErasureTx(ctx context.Context, tx *gorm.DB, teamID, credentialID uuid.UUID) error {
	return withSystemModeInTx(ctx, tx, teamID.String(), teamID.String(), func(systemTx *gorm.DB) error {
		space, err := privateMemorySpaceForCredentialTx(ctx, systemTx, teamID, credentialID, false)
		if err != nil {
			return err
		}
		_, err = queuePrivateMemorySpaceTx(ctx, systemTx, space, queuePrivateMemoryInput{
			Action:               domain.PrivateMemoryRetireCredential,
			ActorClass:           domain.PrivateMemoryActorControl,
			ReasonCode:           "credential_deleted",
			TargetCredentialID:   &credentialID,
			RetireSpace:          true,
			QueueWhileHeld:       true,
			IdempotencyScopeHash: privateMemoryHash("team-credential-delete", teamID.String(), credentialID.String()),
			RequestHash:          privateMemoryHash("retire-credential", teamID.String(), credentialID.String()),
		})
		return err
	})
}
