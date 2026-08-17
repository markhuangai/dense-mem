package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// MemorySpaceRepository is the durable catalog boundary for team spaces.
type MemorySpaceRepository interface {
	GetTeamShared(ctx context.Context, teamID uuid.UUID) (*domain.MemorySpace, error)
	GetByIDForTeam(ctx context.Context, teamID, spaceID uuid.UUID) (*domain.MemorySpace, error)
	ListAllowed(ctx context.Context, teamID uuid.UUID, allowed []domain.MemorySpaceAccess) ([]*domain.MemorySpace, error)
	EnsureProfilePrivate(ctx context.Context, teamID, ownerProfileID uuid.UUID) (*domain.MemorySpace, error)
	EnsureCredentialPrivate(ctx context.Context, teamID, credentialID uuid.UUID) (*domain.MemorySpace, error)
}

type MemorySpaceRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ MemorySpaceRepository = (*MemorySpaceRepositoryImpl)(nil)

func NewMemorySpaceRepository(db *gorm.DB, rls postgres.RLSHelper) *MemorySpaceRepositoryImpl {
	return &MemorySpaceRepositoryImpl{db: db, rls: rls}
}

func (r *MemorySpaceRepositoryImpl) GetTeamShared(ctx context.Context, teamID uuid.UUID) (*domain.MemorySpace, error) {
	return r.getOne(ctx, teamID, `SELECT id, team_id, kind, owner_profile_id, owner_credential_id FROM memory_spaces WHERE team_id = $1 AND kind = 'team_shared' LIMIT 1`, teamID)
}

func (r *MemorySpaceRepositoryImpl) GetByIDForTeam(ctx context.Context, teamID, spaceID uuid.UUID) (*domain.MemorySpace, error) {
	return r.getOne(ctx, teamID, `SELECT id, team_id, kind, owner_profile_id, owner_credential_id FROM memory_spaces WHERE team_id = $1 AND id = $2`, teamID, spaceID)
}

func (r *MemorySpaceRepositoryImpl) ListAllowed(ctx context.Context, teamID uuid.UUID, allowed []domain.MemorySpaceAccess) ([]*domain.MemorySpace, error) {
	var out []*domain.MemorySpace
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		rows, err := tx.Raw(`SELECT id, team_id, kind, owner_profile_id, owner_credential_id FROM memory_spaces WHERE team_id = $1 AND (kind = 'team_shared' OR id = ANY(?::uuid[])) ORDER BY kind, id`, teamID, pq.Array(allowedSpaceIDs(allowed))).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			space := &domain.MemorySpace{}
			if err := rows.Scan(&space.ID, &space.TeamID, &space.Kind, &space.OwnerProfileID, &space.OwnerCredentialID); err != nil {
				return err
			}
			out = append(out, space)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list memory spaces: %w", err)
	}
	return out, nil
}

func (r *MemorySpaceRepositoryImpl) EnsureProfilePrivate(ctx context.Context, teamID, ownerProfileID uuid.UUID) (*domain.MemorySpace, error) {
	return r.ensurePrivate(ctx, teamID, "profile_private", ownerProfileID, "owner_profile")
}

func (r *MemorySpaceRepositoryImpl) EnsureCredentialPrivate(ctx context.Context, teamID, credentialID uuid.UUID) (*domain.MemorySpace, error) {
	return r.ensurePrivate(ctx, teamID, "credential_private", credentialID, "owner_credential")
}

func (r *MemorySpaceRepositoryImpl) getOne(ctx context.Context, teamID uuid.UUID, query string, args ...any) (*domain.MemorySpace, error) {
	var space *domain.MemorySpace
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		row := tx.Raw(query, args...).Row()
		candidate := &domain.MemorySpace{}
		if err := row.Scan(&candidate.ID, &candidate.TeamID, &candidate.Kind, &candidate.OwnerProfileID, &candidate.OwnerCredentialID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		space = candidate
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load memory space: %w", err)
	}
	return space, nil
}

func (r *MemorySpaceRepositoryImpl) ensurePrivate(ctx context.Context, teamID uuid.UUID, kind string, ownerID uuid.UUID, ownerColumn string) (*domain.MemorySpace, error) {
	if ownerID == uuid.Nil {
		return nil, fmt.Errorf("memory space owner is required")
	}
	var space *domain.MemorySpace
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		row := tx.Raw(`
			SELECT dense_mem_ensure_private_space($1, $2, $3)
		`, teamID, kind, ownerID).Row()
		candidate := &domain.MemorySpace{}
		if err := row.Scan(&candidate.ID); err != nil {
			return err
		}
		candidate.TeamID = teamID
		candidate.Kind = domain.MemorySpaceKind(kind)
		if kind == "profile_private" {
			candidate.OwnerProfileID = &ownerID
		} else {
			candidate.OwnerCredentialID = &ownerID
		}
		space = candidate
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to ensure %s memory space: %w", kind, err)
	}
	return space, nil
}

func allowedSpaceIDs(spaces []domain.MemorySpaceAccess) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(spaces))
	for _, space := range spaces {
		if space.ID != uuid.Nil {
			ids = append(ids, space.ID)
		}
	}
	return ids
}
