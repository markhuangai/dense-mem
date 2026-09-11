package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// EvaluationHypothesisQuery is the Dream-owned SQL seam used by the
// evaluation reader. Evaluation can move without copying Dream's query or
// making the registry depend on a concrete adapter.
type EvaluationHypothesisQuery func(EvaluationListInput, int, int, ...string) (string, []any, error)

// EvaluationReader exposes the team-scoped evaluation read model while the
// legacy SemanticRepositoryImpl remains a compatibility facade.
type EvaluationReader struct {
	db              *gorm.DB
	rls             storagepostgres.RLSHelper
	hypothesisQuery EvaluationHypothesisQuery
}

var _ EvaluationRepository = (*EvaluationReader)(nil)

func NewEvaluationReader(db *gorm.DB, rls storagepostgres.RLSHelper, hypothesisQuery EvaluationHypothesisQuery) *EvaluationReader {
	return &EvaluationReader{db: db, rls: rls, hypothesisQuery: hypothesisQuery}
}

func (r *EvaluationReader) ListEvaluationRefs(ctx context.Context, input EvaluationListInput) (*EvaluationPage, error) {
	input = normalizeEvaluationListInput(input)
	if err := validateEvaluationListInput(input); err != nil {
		return nil, err
	}
	offset := evaluationCursorOffset(input.Cursor)
	limit := input.Limit + 1
	var items []map[string]any
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		items, err = queryEvaluationItemsWithHypothesis(ctx, tx, input, limit, offset, r.hypothesisQuery)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("evaluation: list %s: %w", input.Type, err)
	}
	hasMore := len(items) > input.Limit
	if hasMore {
		items = items[:input.Limit]
	}
	nextCursor := ""
	if hasMore {
		nextCursor = strconv.Itoa(offset + input.Limit)
	}
	return &EvaluationPage{Items: items, NextCursor: nextCursor, HasMore: hasMore}, nil
}

func (r *EvaluationReader) GetEvaluationItem(ctx context.Context, input EvaluationGetInput) (map[string]any, error) {
	input = normalizeEvaluationGetInput(input)
	if err := validateEvaluationGetInput(input); err != nil {
		return nil, err
	}
	var item map[string]any
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		items, err := queryEvaluationItemsWithHypothesis(ctx, tx, EvaluationListInput{
			TeamID: input.TeamID,
			Type:   input.Type,
			Limit:  1,
			Status: "",
		}, 1, 0, r.hypothesisQuery, input.ID)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return sql.ErrNoRows
		}
		item = items[0]
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("evaluation: get %s: %w", input.Type, err)
	}
	return item, nil
}

func (r *EvaluationReader) withTeamTx(ctx context.Context, teamID string, fn func(*gorm.DB) error) error {
	if err := r.validateDependencies(); err != nil {
		return err
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}

func (r *EvaluationReader) validateDependencies() error {
	if r == nil || r.db == nil {
		return errors.New("semantic: database is required")
	}
	if r.rls == nil {
		return errors.New("semantic: rls helper is required")
	}
	if r.hypothesisQuery == nil {
		return errors.New("evaluation: hypothesis query is required")
	}
	return nil
}
