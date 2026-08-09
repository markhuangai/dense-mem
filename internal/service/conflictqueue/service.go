package conflictqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

var (
	ErrInvalidStatus = errors.New("invalid conflict queue status")
	ErrInvalidLimit  = errors.New("invalid conflict queue limit")
	ErrInvalidCursor = errors.New("invalid conflict queue cursor")
)

type ListOptions struct {
	Status string
	Limit  int
	Cursor string
}

type Reader interface {
	List(context.Context, string, ListOptions) (*domain.ConflictQueuePage, error)
}

type Service struct {
	store repository.ConflictQueueRepository
}

var _ Reader = (*Service)(nil)

func New(store repository.ConflictQueueRepository) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context, teamID string, options ListOptions) (*domain.ConflictQueuePage, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("conflict queue service is not configured")
	}
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("conflict queue: invalid team id: %w", err)
	}
	status := strings.ToLower(strings.TrimSpace(options.Status))
	if status != "" && status != "open" && status != "overdue" {
		return nil, ErrInvalidStatus
	}
	limit := options.Limit
	if limit == 0 {
		limit = domain.ConflictQueueDefaultLimit
	}
	if limit < 1 || limit > domain.ConflictQueueMaxLimit {
		return nil, ErrInvalidLimit
	}
	var cursor *domain.ConflictQueueCursor
	if strings.TrimSpace(options.Cursor) != "" {
		decoded, err := domain.DecodeConflictQueueCursor(options.Cursor)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
		}
		if err := decoded.ValidateScope(teamID, status); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
		}
		cursor = decoded
	}
	page, err := s.store.ListConflictQueue(ctx, domain.ConflictQueueQuery{
		TeamID: teamID,
		Status: status,
		Limit:  limit,
		Cursor: cursor,
	})
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, errors.New("conflict queue: repository returned no page")
	}
	if page.Items == nil {
		page.Items = []domain.ConflictQueueItem{}
	}
	return page, nil
}
