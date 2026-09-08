package postgres

import (
	"errors"

	"github.com/google/uuid"
)

// HypothesisEvaluationQuery owns the Hypothesis-specific evaluation read
// model. Generic evaluation pagination and the other evaluation branches stay
// in the repository compatibility evaluator.
func HypothesisEvaluationQuery(input EvaluationListInput, limit, offset int, ids ...string) (string, []any, error) {
	if len(ids) > 1 {
		return "", nil, errors.New("only one id lookup is supported")
	}
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return "", nil, err
	}
	values := []any{input.TeamID}
	idFilter := ""
	if len(ids) == 1 {
		values = append(values, ids[0])
		idFilter = " AND id = ?::uuid"
	}
	statusFilter := ""
	if input.Status != "" {
		values = append(values, input.Status)
		statusFilter = " AND status = ?"
	}
	values = append(values, limit, offset)
	return `
		WITH rows AS (
			SELECT hypothesis_id AS id, COALESCE(created_by_profile_id::text, '') AS created_by_profile_id,
			       status, payload, created_at, updated_at
			FROM hypotheses
			WHERE team_id = ?::uuid
			  AND canonical_hypothesis_id IS NULL
		)
		SELECT jsonb_build_object(
			'type', 'hypothesis',
			'id', id::text,
			'created_by_profile_id', created_by_profile_id,
			'status', status,
			'payload', payload,
			'created_at', created_at,
			'updated_at', updated_at
		)
		FROM rows
		WHERE true` + idFilter + statusFilter + `
		ORDER BY updated_at DESC, id
		LIMIT ? OFFSET ?`, values, nil
}
