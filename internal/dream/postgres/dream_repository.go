package postgres

import (
	"context"

	"fmt"
	"strings"

	"github.com/google/uuid"

	"gorm.io/gorm"
)

func (r *Store) ListHypotheses(
	ctx context.Context,
	input ListHypothesesInput,
) ([]HypothesisRecord, string, error) {
	input = normalizeListHypothesesInput(input)
	if err := validateListHypothesesInput(input); err != nil {
		return nil, "", err
	}
	offset := evaluationCursorOffset(input.Cursor)
	limit := input.Limit + 1
	records := []HypothesisRecord{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		query := hypothesisSelectSQL(`
			WHERE team_id = ?::uuid
			  AND space_id = dense_mem_team_shared_space(team_id)
			  AND space_generation = dense_mem_team_shared_generation(team_id)
			  AND canonical_hypothesis_id IS NULL
			  AND (? = '' OR status = ?)
			ORDER BY ` + hypothesisListOrder(input.Sort, input.Direction) + `, hypothesis_id
			LIMIT ? OFFSET ?
		`)
		rows, err := tx.WithContext(ctx).Raw(
			query,
			input.TeamID,
			input.Status,
			input.Status,
			limit,
			offset,
		).Rows()
		if err != nil {
			return err
		}
		records, err = scanHypothesisRecords(rows)
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return hydrateDreamHypothesisDerivations(ctx, tx, input.TeamID, records)
	})
	if err != nil {
		return nil, "", fmt.Errorf("dream: list hypotheses: %w", err)
	}
	next := ""
	if len(records) > input.Limit {
		records = records[:input.Limit]
		next = fmt.Sprintf("%d", offset+input.Limit)
	}
	return records, next, nil
}

func (r *Store) GetHypothesis(ctx context.Context, input GetHypothesisInput) (*HypothesisRecord, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.HypothesisID = strings.TrimSpace(input.HypothesisID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.HypothesisID); err != nil {
		return nil, fmt.Errorf("%w: hypothesis_id is required: %w", ErrDreamHypothesisIDInvalid, err)
	}
	var record *HypothesisRecord
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(hypothesisSelectSQL(`
			WHERE team_id = ?::uuid
			  AND space_id = dense_mem_team_shared_space(team_id)
			  AND space_generation = dense_mem_team_shared_generation(team_id)
			  AND canonical_hypothesis_id IS NULL
			  AND hypothesis_id = COALESCE((
			      SELECT canonical_hypothesis_id
			      FROM hypotheses
				WHERE team_id = ?::uuid
				        AND space_id = dense_mem_team_shared_space(team_id)
				        AND space_generation = dense_mem_team_shared_generation(team_id)
			        AND hypothesis_id = ?::uuid
			  ), ?::uuid)
			LIMIT 1
		`), input.TeamID, input.TeamID, input.HypothesisID, input.HypothesisID).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			_ = rows.Close()
			return ErrDreamHypothesisNotFound
		}
		loaded, err := scanHypothesisRecord(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		records := []HypothesisRecord{*loaded}
		if err := hydrateDreamHypothesisDerivations(ctx, tx, input.TeamID, records); err != nil {
			return err
		}
		record = &records[0]
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream: get hypothesis: %w", err)
	}
	return record, nil
}
