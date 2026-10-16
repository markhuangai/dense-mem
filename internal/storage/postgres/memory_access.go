package postgres

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

// AuthorizeMemoryMutationTx fences an authenticated mutation against current
// grants and the space generation; callers acquire this fence before data locks.
func AuthorizeMemoryMutationTx(ctx context.Context, tx *gorm.DB, teamID, ownerID, spaceID string, generation int64) error {
	return authorizeMemoryMutationTx(ctx, tx, teamID, ownerID, spaceID, generation, false)
}

// Erasure replay and fresh recovery intents require live authority even when the target is sealed.
func AuthorizePrivateErasureTx(ctx context.Context, tx *gorm.DB, teamID, ownerID, spaceID string) error {
	return authorizeMemoryMutationTx(ctx, tx, teamID, ownerID, spaceID, 0, true)
}

func authorizeMemoryMutationTx(ctx context.Context, tx *gorm.DB, teamID, ownerID, spaceID string, generation int64, privateErasure bool) error {
	actor, authenticated := requestctx.ActorFromContext(ctx)
	if !authenticated {
		return nil
	}
	id, err := uuid.Parse(spaceID)
	if err != nil || actor.TeamID.String() != teamID || actor.OwnerID.String() != ownerID ||
		actor.MembershipID == uuid.Nil || actor.IdentityID == uuid.Nil {
		return domain.ErrMemoryWriteDenied
	}
	if !privateErasure {
		if err := domain.AuthorizeMemoryMutation(id, actor.Grants, actor.AllowedSpaces); err != nil {
			return err
		}
	}
	grants := append([]string(nil), actor.Grants...)
	if actor.CredentialID != nil {
		var scopes pq.StringArray
		err := tx.WithContext(ctx).Raw(`
			SELECT scopes FROM credentials
			WHERE id = ? AND team_id = ? AND actor_identity_id = ?
			  AND kind = 'api_key' AND status = 'active' AND revoked_at IS NULL
			  AND (expires_at IS NULL OR expires_at > clock_timestamp()) FOR SHARE
		`, *actor.CredentialID, actor.TeamID, actor.IdentityID).Row().Scan(&scopes)
		if err != nil {
			return memoryAuthorizationError(err)
		}
		grants = intersectMemoryGrants(grants, scopes)
		var issuer *uuid.UUID
		if err := tx.Raw(`SELECT owner_identity_id FROM credentials WHERE id=? AND team_id=?`, *actor.CredentialID, actor.TeamID).Row().Scan(&issuer); err != nil {
			return memoryAuthorizationError(err)
		}
		if issuer != nil {
			var issuerGrants pq.StringArray
			if err := tx.Raw(`SELECT dense_mem_sso_memory_grants(?,?)`, actor.TeamID, *issuer).Row().Scan(&issuerGrants); err != nil {
				return memoryAuthorizationError(err)
			}
			grants = intersectMemoryGrants(grants, issuerGrants)
		}
	}
	var isSSO bool
	if err := tx.Raw(`SELECT sso_provider_id IS NOT NULL FROM team_memberships WHERE id=? AND team_id=?`, actor.MembershipID, actor.TeamID).Row().Scan(&isSSO); err != nil {
		return memoryAuthorizationError(err)
	}
	if isSSO {
		var entitled pq.StringArray
		if err := tx.Raw(`SELECT dense_mem_sso_memory_grants(?,?)`, actor.TeamID, actor.IdentityID).Row().Scan(&entitled); err != nil {
			return memoryAuthorizationError(err)
		}
		grants = intersectMemoryGrants(grants, entitled)
	}
	var maximum pq.StringArray
	err = tx.WithContext(ctx).Raw(`
		SELECT maximum_grants FROM team_memberships
		WHERE id = ? AND team_id = ? AND actor_identity_id = ? AND status = 'active'
		  AND (sso_provider_id IS NULL OR sso_entitlement_status = 'active') FOR SHARE
	`, actor.MembershipID, actor.TeamID, actor.IdentityID).Row().Scan(&maximum)
	if err != nil {
		return memoryAuthorizationError(err)
	}
	var identityID uuid.UUID
	if err := tx.WithContext(ctx).Raw(`
		SELECT id FROM actor_identities WHERE id = ? AND active FOR SHARE
	`, actor.IdentityID).Row().Scan(&identityID); err != nil {
		return memoryAuthorizationError(err)
	}
	var current pq.StringArray
	if err := tx.WithContext(ctx).Raw(`
		SELECT COALESCE(array_agg(grant_name ORDER BY grant_name), ARRAY[]::text[])
        FROM (SELECT grant_name FROM membership_grants WHERE membership_id = ? FOR SHARE) live_grants
	`, actor.MembershipID).Row().Scan(&current); err != nil {
		return err
	}
	grants = intersectMemoryGrants(intersectMemoryGrants(grants, maximum), current)
	lock := ""
	if privateErasure {
		lock = "FOR UPDATE"
	} else {
		var locked bool
		if err := tx.Raw(`SELECT dense_mem_lock_memory_space(?,?)`, actor.TeamID, id).Row().Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return domain.ErrMemoryWriteDenied
		}
	}
	var space domain.MemorySpaceAccess
	err = tx.WithContext(ctx).Raw(`
		SELECT id, kind, generation FROM memory_spaces
		WHERE team_id = ? AND id = ? AND (lifecycle_state = 'active' OR (? AND kind='private'))
		  AND (kind = 'team_shared' OR (kind = 'private' AND owner_membership_id = ?))
		  AND EXISTS (SELECT 1 FROM ownership_aliases
		      WHERE team_id = ? AND legacy_owner_id = ? AND canonical_identity_id = ?)
	`+lock, actor.TeamID, id, privateErasure, actor.MembershipID, actor.TeamID, actor.OwnerID, actor.IdentityID).
		Row().Scan(&space.ID, &space.Kind, &space.Generation)
	if err != nil {
		return memoryAuthorizationError(err)
	}
	if privateErasure {
		if space.Kind != domain.MemorySpacePrivate {
			return domain.ErrMemoryWriteDenied
		}
		return domain.AuthorizeMemoryMutation(id, grants, []domain.MemorySpaceAccess{space})
	}
	for _, allowed := range actor.AllowedSpaces {
		if allowed.ID == id && (allowed.Kind != space.Kind || allowed.Generation != space.Generation) {
			return domain.ErrMemoryWriteDenied
		}
	}
	if generation != 0 && generation != space.Generation {
		return domain.ErrMemoryWriteDenied
	}
	return domain.AuthorizeMemoryMutation(id, grants, []domain.MemorySpaceAccess{space})
}

func intersectMemoryGrants(grants, limit []string) []string {
	return slices.DeleteFunc(grants, func(grant string) bool { return !slices.Contains(limit, grant) })
}

func memoryAuthorizationError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrMemoryWriteDenied
	}
	return err
}

func CheckPrivateMemoryRetryTx(ctx context.Context, tx *gorm.DB, teamID, ownerID, kind, key string) error {
	var erased bool
	if err := tx.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM private_memory_erased_operations WHERE team_id=?::uuid AND owner_profile_id=?::uuid AND operation_kind=? AND key_hash=encode(sha256(convert_to(?,'UTF8')),'hex'))`, teamID, ownerID, kind, key).Row().Scan(&erased); err != nil {
		return err
	}
	if erased {
		return domain.ErrMemoryOperationErased
	}
	return nil
}
