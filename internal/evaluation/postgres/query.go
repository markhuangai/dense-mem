package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	evaluationcontract "github.com/markhuangai/dense-mem/internal/evaluation/contract"
	"gorm.io/gorm"
)

type EvaluationRepository = evaluationcontract.EvaluationRepository
type EvaluationListInput = evaluationcontract.EvaluationListInput
type EvaluationGetInput = evaluationcontract.EvaluationGetInput
type EvaluationPage = evaluationcontract.EvaluationPage

func normalizeEvaluationListInput(input EvaluationListInput) EvaluationListInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Type = normalizeEvaluationType(input.Type)
	input.Cursor = strings.TrimSpace(input.Cursor)
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	if input.Limit <= 0 {
		input.Limit = 100
	}
	if input.Limit > 500 {
		input.Limit = 500
	}
	return input
}

func normalizeEvaluationGetInput(input EvaluationGetInput) EvaluationGetInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Type = normalizeEvaluationType(input.Type)
	input.ID = strings.TrimSpace(input.ID)
	return input
}

func normalizeEvaluationType(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "fragment":
		return "evidence"
	default:
		return normalized
	}
}

func validateEvaluationListInput(input EvaluationListInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if !evaluationTypeSupported(input.Type) {
		return fmt.Errorf("unsupported type %q", input.Type)
	}
	return nil
}

func validateEvaluationGetInput(input EvaluationGetInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if !evaluationTypeSupported(input.Type) {
		return fmt.Errorf("unsupported type %q", input.Type)
	}
	if _, err := uuid.Parse(input.ID); err != nil {
		return fmt.Errorf("id is required: %w", err)
	}
	return nil
}

func evaluationTypeSupported(value string) bool {
	switch value {
	case "evidence":
		return true
	default:
		return false
	}
}

func evaluationCursorOffset(cursor string) int {
	if cursor == "" {
		return 0
	}
	offset, err := strconv.Atoi(cursor)
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}

func queryEvaluationItems(
	ctx context.Context,
	tx *gorm.DB,
	input EvaluationListInput,
	limit int,
	offset int,
	ids ...string,
) ([]map[string]any, error) {
	query, args, err := evaluationQuery(input, limit, offset, ids...)
	if err != nil {
		return nil, err
	}
	rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var item map[string]any
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("decode evaluation item: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func evaluationQuery(input EvaluationListInput, limit int, offset int, ids ...string) (string, []any, error) {
	if len(ids) > 1 {
		return "", nil, errors.New("only one id lookup is supported")
	}
	args := evaluationQueryArgs{values: []any{input.TeamID}}
	idFilter := args.idFilter(ids)
	statusFilter := args.statusFilter(input.Status)
	args.limit(limit, offset)
	switch input.Type {
	case "evidence":
		return `
			WITH rows AS (
				SELECT fragment.fragment_id AS id,
				       CASE WHEN quarantine.quarantine_id IS NULL THEN 'active' ELSE 'quarantined' END AS status,
				       fragment.owner_profile_id::text AS owner_profile_id,
				       fragment.source_id::text AS source_id,
				       fragment.source_revision_id::text AS source_revision_id,
				       fragment.source_type,
				       fragment.authority,
				       fragment.labels,
				       fragment.metadata,
				       fragment.content,
				       fragment.created_at
				FROM evidence_fragments fragment
				LEFT JOIN evidence_quarantines quarantine
				  ON quarantine.team_id = fragment.team_id
				 AND quarantine.fragment_id = fragment.fragment_id
				 AND quarantine.status = 'active'
				WHERE fragment.team_id = ?::uuid
			)
			SELECT jsonb_build_object(
				'type', 'evidence',
				'id', id::text,
				'status', status,
				'owner_profile_id', owner_profile_id,
				'source_id', COALESCE(source_id, ''),
				'source_revision_id', COALESCE(source_revision_id, ''),
				'source_type', source_type,
				'authority', authority,
				'labels', labels,
				'metadata', metadata,
				'content', content,
				'created_at', created_at
			)
			FROM rows
			WHERE true` + idFilter + statusFilter + `
			ORDER BY created_at DESC, id
			LIMIT ? OFFSET ?`, args.values, nil
	default:
		return "", nil, fmt.Errorf("unsupported type %q", input.Type)
	}
}

type evaluationQueryArgs struct {
	values []any
}

func (a *evaluationQueryArgs) idFilter(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	a.values = append(a.values, ids[0])
	return " AND id = ?::uuid"
}

func (a *evaluationQueryArgs) statusFilter(status string) string {
	if status == "" {
		return ""
	}
	a.values = append(a.values, status)
	return " AND status = ?"
}

func (a *evaluationQueryArgs) limit(limit int, offset int) {
	a.values = append(a.values, limit, offset)
}
