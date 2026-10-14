package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

func authorizePrivateErasure(ctx context.Context, tx *gorm.DB, input PrivateMemoryErasureRequest, existing *domain.PrivateMemoryErasureOperation) error {
	actor, present := requestctx.ActorFromContext(ctx)
	if !present {
		return nil
	}
	if actor.TeamID != input.TeamID {
		return domain.ErrMemoryWriteDenied
	}
	if input.CredentialID != uuid.Nil {
		if actor.CredentialID == nil || *actor.CredentialID != input.CredentialID {
			return domain.ErrMemoryWriteDenied
		}
	} else if actor.IdentityID != input.OwnerID || actor.CredentialID != nil {
		return domain.ErrMemoryWriteDenied
	}
	var spaceID uuid.UUID
	if existing != nil {
		if existing.SpaceID == nil || existing.TeamID != actor.TeamID {
			return domain.ErrMemoryWriteDenied
		}
		spaceID = *existing.SpaceID
	} else {
		err := tx.WithContext(ctx).Raw(`SELECT id FROM memory_spaces
			WHERE team_id=? AND owner_membership_id=? AND kind='private'
			  AND NOT migration_archive AND lifecycle_state IN ('active','sealed')`, actor.TeamID, actor.MembershipID).Row().Scan(&spaceID)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrMemoryWriteDenied
		}
		if err != nil {
			return err
		}
	}
	return storage.AuthorizePrivateErasureTx(ctx, tx, actor.TeamID.String(), actor.OwnerID.String(), spaceID.String())
}
