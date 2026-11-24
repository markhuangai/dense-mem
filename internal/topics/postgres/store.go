package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

type Store struct {
	db                  *gorm.DB
	rls                 storage.RLSHelper
	recallCache         *recallReadCache
	publicationObserver topic.PublicationObserver
}
type scope struct {
	TeamID     string
	SpaceID    string
	Generation int64
}

func NewStore(db *gorm.DB, rls storage.RLSHelper) *Store { return &Store{db: db, rls: rls} }

func (s *Store) WithPublicationDiagnostics(observer topic.PublicationObserver) *Store {
	s.publicationObserver = observer
	return s
}

func (s *Store) withScope(ctx context.Context, teamID string, readOnly bool, fn func(*gorm.DB, scope) error) error {
	if s == nil || s.db == nil || s.rls == nil {
		return errors.New("topics: database and RLS helper are required")
	}
	id, err := uuid.Parse(teamID)
	if err != nil || id == uuid.Nil || id.String() != teamID {
		return fmt.Errorf("%w: team UUID required", topic.ErrInvalid)
	}
	if actor, ok := requestctx.ActorFromContext(ctx); ok && actor.TeamID != id {
		return topic.ErrUnauthorized
	}
	run := s.rls.WithTeamTx
	if readOnly {
		run = s.rls.WithTeamReadOnlyRepeatableTx
	}
	return run(ctx, s.db, teamID, func(tx *gorm.DB) error {
		fence, err := sharedScope(ctx, tx, teamID)
		if err != nil {
			return err
		}
		return fn(tx, fence)
	})
}

func sharedScope(ctx context.Context, tx *gorm.DB, teamID string) (scope, error) {
	fence := scope{TeamID: teamID}
	id, err := uuid.Parse(teamID)
	if err != nil || id == uuid.Nil || id.String() != teamID {
		return fence, topic.ErrInvalid
	}
	if actor, ok := requestctx.ActorFromContext(ctx); ok && actor.TeamID != id {
		return fence, topic.ErrUnauthorized
	}
	if err := tx.WithContext(ctx).Raw(`SELECT space.id::text,space.generation FROM memory_spaces AS space
		JOIN teams AS team ON team.id=space.team_id AND team.status='active' AND team.deleted_at IS NULL
		WHERE space.team_id=?::uuid AND space.kind='team_shared' AND space.lifecycle_state='active'`, teamID).Row().Scan(&fence.SpaceID, &fence.Generation); err != nil {
		return fence, fmt.Errorf("topics: load shared space: %w", err)
	}
	return fence, authorizeSharedScope(ctx, fence)
}

func authorizeSharedScope(ctx context.Context, fence scope) error {
	if actor, ok := requestctx.ActorFromContext(ctx); ok {
		if actor.TeamID.String() != fence.TeamID {
			return topic.ErrUnauthorized
		}
		allowed := slices.ContainsFunc(actor.AllowedSpaces, func(space domain.MemorySpaceAccess) bool {
			return space.ID.String() == fence.SpaceID && space.Kind == domain.MemorySpaceTeamShared && space.Generation == fence.Generation
		})
		if !allowed {
			return topic.ErrUnauthorized
		}
	}
	return nil
}

func managerActor(ctx context.Context, teamID string) (string, error) {
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID.String() != teamID || actor.OwnerID == uuid.Nil || actor.Role != "manager" || !slices.Contains(actor.Grants, "write") {
		return "", topic.ErrUnauthorized
	}
	return actor.OwnerID.String(), nil
}

func requireAutomatic(ctx context.Context) error {
	if _, ok := requestctx.ActorFromContext(ctx); ok {
		return topic.ErrUnauthorized
	}
	return nil
}

var _ topic.Repository = (*Store)(nil)
