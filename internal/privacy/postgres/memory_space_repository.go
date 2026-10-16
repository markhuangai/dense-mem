package postgres

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
	EnsurePrivate(ctx context.Context, teamID, membershipID uuid.UUID) (*domain.MemorySpace, error)
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
	return r.getOne(ctx, teamID, `SELECT id, team_id, kind, owner_membership_id, generation, lifecycle_state FROM memory_spaces WHERE team_id = $1 AND kind = 'team_shared' LIMIT 1`, teamID)
}

func (r *MemorySpaceRepositoryImpl) GetByIDForTeam(ctx context.Context, teamID, spaceID uuid.UUID) (*domain.MemorySpace, error) {
	return r.getOne(ctx, teamID, `SELECT id, team_id, kind, owner_membership_id, generation, lifecycle_state FROM memory_spaces WHERE team_id = $1 AND id = $2`, teamID, spaceID)
}

func (r *MemorySpaceRepositoryImpl) ListAllowed(ctx context.Context, teamID uuid.UUID, allowed []domain.MemorySpaceAccess) ([]*domain.MemorySpace, error) {
	var out []*domain.MemorySpace
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		rows, err := tx.Raw(`SELECT id, team_id, kind, owner_membership_id, generation, lifecycle_state FROM memory_spaces WHERE team_id = $1 AND (kind = 'team_shared' OR id = ANY($2::uuid[])) ORDER BY kind, id`, teamID, pq.Array(allowedSpaceIDs(allowed))).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			space := &domain.MemorySpace{}
			if err := rows.Scan(&space.ID, &space.TeamID, &space.Kind, &space.OwnerMembershipID, &space.Generation, &space.LifecycleState); err != nil {
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

func (r *MemorySpaceRepositoryImpl) getOne(ctx context.Context, teamID uuid.UUID, query string, args ...any) (*domain.MemorySpace, error) {
	var space *domain.MemorySpace
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		row := tx.Raw(query, args...).Row()
		candidate := &domain.MemorySpace{}
		if err := row.Scan(&candidate.ID, &candidate.TeamID, &candidate.Kind, &candidate.OwnerMembershipID, &candidate.Generation, &candidate.LifecycleState); err != nil {
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

func (r *MemorySpaceRepositoryImpl) EnsurePrivate(ctx context.Context, teamID, membershipID uuid.UUID) (*domain.MemorySpace, error) {
	if membershipID == uuid.Nil {
		return nil, fmt.Errorf("memory space membership is required")
	}
	var space *domain.MemorySpace
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		var id uuid.UUID
		if err := tx.Raw(`SELECT dense_mem_ensure_private_space($1, $2)`, teamID, membershipID).Row().Scan(&id); err != nil {
			return err
		}
		space = &domain.MemorySpace{ID: id, TeamID: teamID, Kind: domain.MemorySpacePrivate, OwnerMembershipID: &membershipID}
		return tx.Raw(`SELECT generation, lifecycle_state FROM memory_spaces WHERE team_id = $1 AND id = $2`, teamID, id).Row().Scan(&space.Generation, &space.LifecycleState)
	})
	return space, err
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
