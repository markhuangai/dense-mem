// Package postgres owns graph view SQL and transaction-bound reads.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres/graphread"
)

type Store struct {
	db  *gorm.DB
	rls storagepostgres.RLSHelper
}

func NewStore(db *gorm.DB, rls storagepostgres.RLSHelper) *Store {
	return &Store{db: db, rls: rls}
}

func (r *Store) withTeamTx(ctx context.Context, teamID string, fn func(*gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("graph: database is required")
	}
	if r.rls == nil {
		return errors.New("graph: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}

var _ graphcontract.Store = (*Store)(nil)

const (
	defaultSemanticGraphLimit = 80
	defaultSemanticGraphDepth = 2
	maxSemanticGraphDepth     = 5
)

func NormalizeQuery(input graphcontract.Query) graphcontract.Query {
	return normalizeSemanticGraphQuery(input)
}

func NormalizeNodeDetailInput(input graphcontract.NodeDetailInput) graphcontract.NodeDetailInput {
	return normalizeSemanticGraphNodeDetailInput(input)
}

func ValidateQuery(input graphcontract.Query) error {
	return validateSemanticGraphQuery(input)
}

func ValidateNodeDetailInput(input graphcontract.NodeDetailInput) error {
	return validateSemanticGraphNodeDetailInput(input)
}

func LoadLocalRows(ctx context.Context, tx *gorm.DB, input graphcontract.Query, spaceID string) ([]graphread.Row, error) {
	input = normalizeSemanticGraphQuery(input)
	if err := validateSemanticGraphQuery(input); err != nil {
		return nil, err
	}
	return loadSemanticLocalGraphRows(ctx, tx, graphExecutionQuery{
		Query:   input,
		spaceID: strings.TrimSpace(spaceID),
	})
}

func (r *Store) SemanticGraph(
	ctx context.Context,
	input graphcontract.Query,
) (*graphcontract.Snapshot, error) {
	input = normalizeSemanticGraphQuery(input)
	if err := validateSemanticGraphQuery(input); err != nil {
		return nil, err
	}
	var rows []graphEdgeRow
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		execution := graphExecutionQuery{Query: input}
		if input.Scope == "local" {
			rows, err = loadSemanticLocalGraphRows(ctx, tx, execution)
		} else {
			rows, err = loadSemanticOverviewGraphRows(ctx, tx, execution)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("semantic graph: %w", err)
	}
	return graphSnapshot(input, rows), nil
}

func (r *Store) SemanticGraphNodeDetail(
	ctx context.Context,
	input graphcontract.NodeDetailInput,
) (*graphcontract.Node, error) {
	input = normalizeSemanticGraphNodeDetailInput(input)
	if err := validateSemanticGraphNodeDetailInput(input); err != nil {
		return nil, err
	}
	var node *graphcontract.Node
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		switch input.NodeType {
		case "entity":
			node, err = loadSemanticEntityGraphNode(ctx, tx, input.TeamID, input.NodeID)
		case "value":
			node, err = loadSemanticValueGraphNode(ctx, tx, input.TeamID, input.NodeID)
		default:
			return sql.ErrNoRows
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("semantic graph node: %w", err)
	}
	return node, nil
}

type graphEdgeRow = graphread.Row

// graphExecutionQuery carries adapter-derived scope separately from
// the caller-owned graph contract.
type graphExecutionQuery struct {
	graphcontract.Query
	spaceID string
}

func normalizeSemanticGraphQuery(input graphcontract.Query) graphcontract.Query {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	if input.Scope == "" || input.Scope != "local" {
		input.Scope = "overview"
	}
	input.Query = strings.ToLower(strings.TrimSpace(input.Query))
	input.AnchorType = normalizeSemanticGraphNodeType(input.AnchorType)
	input.AnchorID = strings.TrimSpace(input.AnchorID)
	input.Types = normalizeSemanticGraphTypes(input.Types)
	input.Depth = clampInt(input.Depth, defaultSemanticGraphDepth, maxSemanticGraphDepth)
	input.Limit = defaultPositiveInt(input.Limit, defaultSemanticGraphLimit)
	input.MinRelevance = normalizeRelevance(input.MinRelevance)
	return input
}

func validateSemanticGraphQuery(input graphcontract.Query) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.Scope == "local" {
		if input.AnchorType == "" {
			return errors.New("anchor_type is required for local graph")
		}
		if _, err := uuid.Parse(input.AnchorID); err != nil {
			return fmt.Errorf("anchor_id is required for local graph: %w", err)
		}
	}
	return nil
}

func normalizeSemanticGraphNodeDetailInput(input graphcontract.NodeDetailInput) graphcontract.NodeDetailInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.NodeType = normalizeSemanticGraphNodeType(input.NodeType)
	input.NodeID = strings.TrimSpace(input.NodeID)
	return input
}

func validateSemanticGraphNodeDetailInput(input graphcontract.NodeDetailInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.NodeType == "" {
		return errors.New("node_type must be entity or value")
	}
	if _, err := uuid.Parse(input.NodeID); err != nil {
		return fmt.Errorf("node_id is required: %w", err)
	}
	return nil
}

func loadSemanticOverviewGraphRows(
	ctx context.Context,
	tx *gorm.DB,
	input graphExecutionQuery,
) ([]graphEdgeRow, error) {
	extraWhere := ""
	var extraArgs []any
	if input.spaceID != "" {
		extraWhere = " AND edge_record.space_id = ?::uuid"
		extraArgs = append(extraArgs, input.spaceID)
	}
	rows, err := tx.WithContext(ctx).Raw(
		semanticGraphEdgesSQL(extraWhere),
		semanticGraphQueryArgs(input, input.Limit, extraArgs...)...,
	).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSemanticGraphRows(rows, input.Types)
}

func loadSemanticLocalGraphRows(
	ctx context.Context,
	tx *gorm.DB,
	input graphExecutionQuery,
) ([]graphEdgeRow, error) {
	anchor := semanticGraphNodeKey(input.AnchorType, input.AnchorID)
	if anchor == "" {
		return nil, sql.ErrNoRows
	}
	return graphread.Traverse(ctx, anchor, input.Depth, input.Limit, func(ctx context.Context, frontier []string, limit int) ([]graphread.Row, error) {
		extraWhere := `
		  AND (
		    ('entity:' || e.subject_entity_id::text) = ANY(?::text[])
		    OR (CASE
		      WHEN e.object_entity_id IS NOT NULL THEN 'entity:' || e.object_entity_id::text
		      ELSE 'value:' || e.object_value_id::text
		    END) = ANY(?::text[])
		  )
		`
		extraArgs := []any{pq.Array(frontier), pq.Array(frontier)}
		if input.spaceID != "" {
			extraWhere += " AND edge_record.space_id = ?::uuid"
			extraArgs = append(extraArgs, input.spaceID)
		}
		rows, err := tx.WithContext(ctx).Raw(
			semanticGraphEdgesSQL(extraWhere),
			semanticGraphQueryArgs(input, limit, extraArgs...)...,
		).Rows()
		if err != nil {
			return nil, err
		}
		batch, err := scanSemanticGraphRows(rows, input.Types)
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
		return batch, nil
	})
}

func semanticGraphEdgesSQL(extraWhere string) string {
	searchText := semanticGraphSearchTextSQL()
	return `
			SELECT e.relationship_id::text,
			       e.owner_profile_id::text,
			       e.predicate_key,
			       e.support_count,
		       e.source_group_count,
		       ('entity:' || e.subject_entity_id::text) AS source_key,
		       e.subject_entity_id::text AS source_id,
		       COALESCE(subject_name.display_name, e.subject_entity_id::text) AS source_title,
		       subject.entity_kind AS source_body,
		       subject.status AS source_status,
		       e.owner_profile_id::text AS source_owner_profile_id,
		       subject.updated_at AS source_recorded_at,
		       CASE
		         WHEN e.object_entity_id IS NOT NULL THEN 'entity:' || e.object_entity_id::text
		         ELSE 'value:' || e.object_value_id::text
		       END AS target_key,
		       COALESCE(e.object_entity_id::text, e.object_value_id::text) AS target_id,
		       CASE WHEN e.object_entity_id IS NOT NULL THEN 'entity' ELSE 'value' END AS target_type,
		       COALESCE(object_name.display_name, NULLIF(value.display, ''), value.canonical_value, e.object_entity_id::text, e.object_value_id::text) AS target_title,
		       COALESCE(object.entity_kind, value.value_type, '') AS target_body,
		       COALESCE(object.status, 'active') AS target_status,
		       e.owner_profile_id::text AS target_owner_profile_id,
		       COALESCE(object.updated_at, value.created_at, subject.updated_at) AS target_recorded_at
		FROM semantic_edges e
		JOIN relationship_records edge_record
		  ON edge_record.team_id = e.team_id
		 AND edge_record.relationship_id = e.relationship_id
		JOIN entity_records subject
		  ON subject.team_id = e.team_id
		 AND subject.entity_id = e.subject_entity_id
		 AND subject.space_id = edge_record.space_id
		LEFT JOIN LATERAL (
		  SELECT display_name
		  FROM entity_names
		  WHERE team_id = e.team_id
		    AND entity_id = e.subject_entity_id
		    AND space_id = edge_record.space_id
		    AND ` + storagepostgres.ActiveSemanticSpaceGenerationSQL("entity_names") + `
		    AND name_kind = 'canonical'
		    AND valid_to IS NULL
		  ORDER BY created_at DESC, entity_name_id DESC
		  LIMIT 1
		) subject_name ON true
		LEFT JOIN entity_records object
		  ON object.team_id = e.team_id
		 AND object.entity_id = e.object_entity_id
		 AND object.space_id = edge_record.space_id
		LEFT JOIN LATERAL (
		  SELECT display_name
		  FROM entity_names
		  WHERE team_id = e.team_id
		    AND entity_id = e.object_entity_id
		    AND space_id = edge_record.space_id
		    AND ` + storagepostgres.ActiveSemanticSpaceGenerationSQL("entity_names") + `
		    AND name_kind = 'canonical'
		    AND valid_to IS NULL
		  ORDER BY created_at DESC, entity_name_id DESC
		  LIMIT 1
		) object_name ON true
		LEFT JOIN value_records value
		  ON value.team_id = e.team_id
		 AND value.value_id = e.object_value_id
		 AND value.space_id = edge_record.space_id
		WHERE e.team_id = ?::uuid
		  AND (
		    edge_record.space_id = dense_mem_team_shared_space(edge_record.team_id)
		    OR dense_mem_space_allowed(edge_record.space_id)
		  )
		  AND ` + storagepostgres.ActiveSemanticSpaceGenerationSQL("edge_record") + `
		  AND ` + storagepostgres.ActiveSemanticSpaceGenerationSQL("subject") + `
		  AND (
		    e.object_entity_id IS NULL
		    OR ` + storagepostgres.ActiveSemanticSpaceGenerationSQL("object") + `
		  )
		  AND (
		    e.object_entity_id IS NOT NULL
		    OR ` + storagepostgres.ActiveSemanticSpaceGenerationSQL("value") + `
		  )
		  AND subject.status = 'active'
		  AND (
		    e.object_entity_id IS NULL
		    OR object.status = 'active'
		  )
		  AND (
		    ? = ''
		    OR ` + searchText + ` LIKE '%' || ? || '%'
		    OR to_tsvector('simple', ` + searchText + `) @@ plainto_tsquery('simple', ?)
		  )
		  AND (
		    ? <= 0
		    OR ? = ''
		    OR ts_rank_cd(to_tsvector('simple', ` + searchText + `), plainto_tsquery('simple', ?), 32) >= ?
		  )
	` + extraWhere + `
		ORDER BY
		  CASE
		    WHEN ? = '' THEN 0
		    ELSE ts_rank_cd(to_tsvector('simple', ` + searchText + `), plainto_tsquery('simple', ?), 32)
		  END DESC,
		  e.relationship_id ASC
		LIMIT ?
	`
}

func semanticGraphSearchTextSQL() string {
	return `lower(COALESCE(subject_name.display_name, '') || ' ' || e.predicate_key || ' ' ||
		             COALESCE(object_name.display_name, value.display, value.canonical_value, ''))`
}

func semanticGraphQueryArgs(input graphExecutionQuery, limit int, extraArgs ...any) []any {
	queryText := input.Query.Query
	args := []any{
		input.TeamID,
		queryText,
		queryText,
		queryText,
		input.MinRelevance,
		queryText,
		queryText,
		input.MinRelevance,
	}
	args = append(args, extraArgs...)
	args = append(args, queryText, queryText, limit)
	return args
}

func scanSemanticGraphRows(rows *sql.Rows, types []string) ([]graphEdgeRow, error) {
	return graphread.ScanRows(rows, types)
}

func loadSemanticEntityGraphNode(ctx context.Context, tx *gorm.DB, teamID, entityID string) (node *graphcontract.Node, err error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT ('entity:' || e.entity_id::text), e.entity_id::text,
		       COALESCE(name.display_name, e.entity_id::text), e.entity_kind,
		       e.status, COALESCE(name.owner_profile_id::text, ''), e.updated_at
		FROM entity_records e
		LEFT JOIN LATERAL (
		  SELECT display_name, owner_profile_id
		  FROM entity_names
		  WHERE team_id = e.team_id
		    AND entity_id = e.entity_id
		    AND space_id = e.space_id
		    AND `+storagepostgres.ActiveSemanticSpaceGenerationSQL("entity_names")+`
		    AND name_kind = 'canonical'
		    AND valid_to IS NULL
		  ORDER BY created_at DESC, entity_name_id DESC
		  LIMIT 1
		) name ON true
		WHERE e.team_id = ?::uuid
		  AND e.entity_id = ?::uuid
		  AND e.status = 'active'
		  AND (
		    e.space_id = dense_mem_team_shared_space(e.team_id)
		    OR dense_mem_space_allowed(e.space_id)
		  )
		  AND `+storagepostgres.ActiveSemanticSpaceGenerationSQL("e")+`
		LIMIT 1
	`, teamID, entityID).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			node = nil
			err = closeErr
		}
	}()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	var loaded graphcontract.Node
	var recordedAt time.Time
	if err := rows.Scan(&loaded.Key, &loaded.ID, &loaded.Title, &loaded.Body, &loaded.Status, &loaded.OwnerProfileID, &recordedAt); err != nil {
		return nil, err
	}
	loaded.Type = "entity"
	t := recordedAt.UTC()
	loaded.RecordedAt = &t
	return &loaded, rows.Err()
}

func loadSemanticValueGraphNode(ctx context.Context, tx *gorm.DB, teamID, valueID string) (node *graphcontract.Node, err error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT ('value:' || value_id::text), value_id::text,
		       COALESCE(NULLIF(display, ''), canonical_value), value_type,
		       'active', ''::text, created_at
		FROM value_records AS value
		WHERE value.team_id = ?::uuid
		  AND value.value_id = ?::uuid
		  AND (
		    value.space_id = dense_mem_team_shared_space(value.team_id)
		    OR dense_mem_space_allowed(value.space_id)
		  )
		  AND `+storagepostgres.ActiveSemanticSpaceGenerationSQL("value")+`
		LIMIT 1
	`, teamID, valueID).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			node = nil
			err = closeErr
		}
	}()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	var loaded graphcontract.Node
	var recordedAt time.Time
	if err := rows.Scan(&loaded.Key, &loaded.ID, &loaded.Title, &loaded.Body, &loaded.Status, &loaded.OwnerProfileID, &recordedAt); err != nil {
		return nil, err
	}
	loaded.Type = "value"
	t := recordedAt.UTC()
	loaded.RecordedAt = &t
	return &loaded, rows.Err()
}

func graphSnapshot(input graphcontract.Query, rows []graphEdgeRow) *graphcontract.Snapshot {
	return graphread.Snapshot(input, rows)
}

func normalizeSemanticGraphTypes(values []string) []string {
	return graphread.NormalizeTypes(values)
}

func semanticGraphTypeSet(values []string) map[string]bool {
	return graphread.TypeSet(values)
}

func normalizeSemanticGraphNodeType(raw string) string {
	return graphread.NormalizeNodeType(raw)
}

func clampInt(value, defaultValue, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func defaultPositiveInt(value, defaultValue int) int {
	if value <= 0 {
		return defaultValue
	}
	return value
}

func normalizeRelevance(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func semanticGraphNodeKey(nodeType, id string) string {
	nodeType = normalizeSemanticGraphNodeType(nodeType)
	id = strings.TrimSpace(id)
	if nodeType == "" || id == "" {
		return ""
	}
	return nodeType + ":" + id
}
