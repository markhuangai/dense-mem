package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type EvaluationRepository interface {
	ListEvaluationRefs(ctx context.Context, input EvaluationListInput) (*EvaluationPage, error)
	GetEvaluationItem(ctx context.Context, input EvaluationGetInput) (map[string]any, error)
}

type EvaluationListInput struct {
	TeamID string
	Type   string
	Limit  int
	Cursor string
	Status string
}

type EvaluationGetInput struct {
	TeamID string
	Type   string
	ID     string
}

type EvaluationPage struct {
	Items      []map[string]any `json:"items"`
	NextCursor string           `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

var _ EvaluationRepository = (*SemanticRepositoryImpl)(nil)

func (r *SemanticRepositoryImpl) ListEvaluationRefs(ctx context.Context, input EvaluationListInput) (*EvaluationPage, error) {
	input = normalizeEvaluationListInput(input)
	if err := validateEvaluationListInput(input); err != nil {
		return nil, err
	}
	offset := evaluationCursorOffset(input.Cursor)
	limit := input.Limit + 1
	var items []map[string]any
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		items, err = queryEvaluationItems(ctx, tx, input, limit, offset)
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

func (r *SemanticRepositoryImpl) GetEvaluationItem(ctx context.Context, input EvaluationGetInput) (map[string]any, error) {
	input = normalizeEvaluationGetInput(input)
	if err := validateEvaluationGetInput(input); err != nil {
		return nil, err
	}
	var item map[string]any
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		items, err := queryEvaluationItems(ctx, tx, EvaluationListInput{
			TeamID: input.TeamID,
			Type:   input.Type,
			Limit:  1,
			Status: "",
		}, 1, 0, input.ID)
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
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "fragment":
		return "evidence"
	case "dream":
		return "hypothesis"
	default:
		return strings.TrimSpace(value)
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
	case "evidence", "relationship", "entity", "value", "hypothesis":
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
	case "relationship":
		return `
			WITH rows AS (
				SELECT relationship_id AS id, owner_profile_id::text, semantic_group_key,
				       subject_entity_id::text, predicate_key, predicate_version,
				       COALESCE(object_entity_id::text, '') AS object_entity_id,
				       COALESCE(object_value_id::text, '') AS object_value_id,
				       relationship_kind, current_cardinality, status, polarity,
				       COALESCE(scope_key, '') AS scope_key, valid_from, valid_to,
				       support_count, source_group_count, version, metadata, created_at, updated_at
				FROM relationship_records
				WHERE team_id = ?::uuid
			)
			SELECT jsonb_build_object(
				'type', 'relationship',
				'id', id::text,
				'owner_profile_id', owner_profile_id,
				'semantic_group_key', semantic_group_key,
				'subject_entity_id', subject_entity_id,
				'predicate_key', predicate_key,
				'predicate_version', predicate_version,
				'object_entity_id', object_entity_id,
				'object_value_id', object_value_id,
				'relationship_kind', relationship_kind,
				'current_cardinality', current_cardinality,
				'status', status,
				'polarity', polarity,
				'scope_key', scope_key,
				'valid_from', valid_from,
				'valid_to', valid_to,
				'support_count', support_count,
				'source_group_count', source_group_count,
				'version', version,
				'metadata', metadata,
				'created_at', created_at,
				'updated_at', updated_at
			)
			FROM rows
			WHERE true` + idFilter + statusFilter + `
			ORDER BY updated_at DESC, id
			LIMIT ? OFFSET ?`, args.values, nil
	case "entity":
		return `
			WITH rows AS (
				SELECT entity.entity_id AS id, entity.entity_kind, entity.status,
				       entity.version, entity.identity_context, entity.metadata,
				       entity.created_at, entity.updated_at,
				       COALESCE(name.display_name, '') AS canonical_name
				FROM entity_records entity
				LEFT JOIN LATERAL (
					SELECT display_name
					FROM entity_names
					WHERE team_id = entity.team_id
					  AND entity_id = entity.entity_id
					  AND name_kind = 'canonical'
					  AND valid_to IS NULL
					ORDER BY created_at DESC, entity_name_id DESC
					LIMIT 1
				) name ON true
				WHERE entity.team_id = ?::uuid
			)
			SELECT jsonb_build_object(
				'type', 'entity',
				'id', id::text,
				'entity_kind', entity_kind,
				'canonical_name', canonical_name,
				'status', status,
				'version', version,
				'identity_context', identity_context,
				'metadata', metadata,
				'created_at', created_at,
				'updated_at', updated_at
			)
			FROM rows
			WHERE true` + idFilter + statusFilter + `
			ORDER BY updated_at DESC, id
			LIMIT ? OFFSET ?`, args.values, nil
	case "value":
		return `
			WITH rows AS (
				SELECT value_id AS id, 'active' AS status, value_type, canonical_value, COALESCE(unit, '') AS unit,
				       display, normalization_version, metadata, created_at
				FROM value_records
				WHERE team_id = ?::uuid
			)
			SELECT jsonb_build_object(
				'type', 'value',
				'id', id::text,
				'status', status,
				'value_type', value_type,
				'canonical_value', canonical_value,
				'unit', unit,
				'display', display,
				'normalization_version', normalization_version,
				'metadata', metadata,
				'created_at', created_at
			)
			FROM rows
			WHERE true` + idFilter + statusFilter + `
			ORDER BY created_at DESC, id
			LIMIT ? OFFSET ?`, args.values, nil
	case "hypothesis":
		return `
			WITH rows AS (
				SELECT hypothesis_id AS id, owner_profile_id::text, status, payload, created_at, updated_at
				FROM hypotheses
				WHERE team_id = ?::uuid
			)
			SELECT jsonb_build_object(
				'type', 'hypothesis',
				'id', id::text,
				'owner_profile_id', owner_profile_id,
				'status', status,
				'payload', payload,
				'created_at', created_at,
				'updated_at', updated_at
			)
			FROM rows
			WHERE true` + idFilter + statusFilter + `
			ORDER BY updated_at DESC, id
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
