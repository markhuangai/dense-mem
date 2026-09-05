package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const (
	defaultSemanticGraphLimit = 80
	defaultSemanticGraphDepth = 2
	maxSemanticGraphDepth     = 5
)

func (r *SemanticRepositoryImpl) SemanticGraph(
	ctx context.Context,
	input SemanticGraphQuery,
) (*SemanticGraphSnapshot, error) {
	input = normalizeSemanticGraphQuery(input)
	if err := validateSemanticGraphQuery(input); err != nil {
		return nil, err
	}
	var rows []semanticGraphEdgeRow
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		if input.Scope == "local" {
			rows, err = loadSemanticLocalGraphRows(ctx, tx, input)
		} else {
			rows, err = loadSemanticOverviewGraphRows(ctx, tx, input)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("semantic graph: %w", err)
	}
	return semanticGraphSnapshot(input, rows), nil
}

func (r *SemanticRepositoryImpl) SemanticGraphNodeDetail(
	ctx context.Context,
	input SemanticGraphNodeDetailInput,
) (*SemanticGraphNode, error) {
	input = normalizeSemanticGraphNodeDetailInput(input)
	if err := validateSemanticGraphNodeDetailInput(input); err != nil {
		return nil, err
	}
	var node *SemanticGraphNode
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

type semanticGraphEdgeRow struct {
	source SemanticGraphNode
	target SemanticGraphNode
	edge   SemanticGraphEdge
}

func normalizeSemanticGraphQuery(input SemanticGraphQuery) SemanticGraphQuery {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.spaceID = strings.TrimSpace(input.spaceID)
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

func validateSemanticGraphQuery(input SemanticGraphQuery) error {
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

func normalizeSemanticGraphNodeDetailInput(input SemanticGraphNodeDetailInput) SemanticGraphNodeDetailInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.NodeType = normalizeSemanticGraphNodeType(input.NodeType)
	input.NodeID = strings.TrimSpace(input.NodeID)
	return input
}

func validateSemanticGraphNodeDetailInput(input SemanticGraphNodeDetailInput) error {
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
	input SemanticGraphQuery,
) ([]semanticGraphEdgeRow, error) {
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
	input SemanticGraphQuery,
) ([]semanticGraphEdgeRow, error) {
	anchor := semanticGraphNodeKey(input.AnchorType, input.AnchorID)
	if anchor == "" {
		return nil, sql.ErrNoRows
	}
	frontier := []string{anchor}
	seenNodes := map[string]struct{}{anchor: {}}
	seenEdges := map[string]struct{}{}
	out := []semanticGraphEdgeRow{}
	for depth := 0; depth < input.Depth && len(frontier) > 0 && len(out) < input.Limit; depth++ {
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
			semanticGraphQueryArgs(input, input.Limit-len(out), extraArgs...)...,
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
		next := []string{}
		for _, row := range batch {
			if _, seen := seenEdges[row.edge.ID]; seen {
				continue
			}
			seenEdges[row.edge.ID] = struct{}{}
			out = append(out, row)
			for _, key := range []string{row.source.Key, row.target.Key} {
				if _, seen := seenNodes[key]; seen {
					continue
				}
				seenNodes[key] = struct{}{}
				next = append(next, key)
			}
			if len(out) == input.Limit {
				break
			}
		}
		frontier = next
	}
	return out, nil
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
		    AND ` + activeSemanticSpaceGenerationSQL("entity_names") + `
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
		    AND ` + activeSemanticSpaceGenerationSQL("entity_names") + `
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
		  AND ` + activeSemanticSpaceGenerationSQL("edge_record") + `
		  AND ` + activeSemanticSpaceGenerationSQL("subject") + `
		  AND (
		    e.object_entity_id IS NULL
		    OR ` + activeSemanticSpaceGenerationSQL("object") + `
		  )
		  AND (
		    e.object_entity_id IS NOT NULL
		    OR ` + activeSemanticSpaceGenerationSQL("value") + `
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

func semanticGraphQueryArgs(input SemanticGraphQuery, limit int, extraArgs ...any) []any {
	args := []any{
		input.TeamID,
		input.Query,
		input.Query,
		input.Query,
		input.MinRelevance,
		input.Query,
		input.Query,
		input.MinRelevance,
	}
	args = append(args, extraArgs...)
	args = append(args, input.Query, input.Query, limit)
	return args
}

func scanSemanticGraphRows(rows *sql.Rows, types []string) ([]semanticGraphEdgeRow, error) {
	typeSet := semanticGraphTypeSet(types)
	if !typeSet["entity"] {
		return nil, nil
	}
	out := []semanticGraphEdgeRow{}
	for rows.Next() {
		var (
			edgeID, ownerID, predicate                                              string
			supportCount, sourceGroupCount                                          int
			sourceKey, sourceID, sourceTitle, sourceBody, sourceStatus, sourceOwner string
			targetKey, targetID, targetType, targetTitle, targetBody, targetStatus  string
			targetOwner                                                             string
			sourceRecordedAt, targetRecordedAt                                      time.Time
		)
		if err := rows.Scan(
			&edgeID, &ownerID, &predicate, &supportCount, &sourceGroupCount,
			&sourceKey, &sourceID, &sourceTitle, &sourceBody, &sourceStatus,
			&sourceOwner, &sourceRecordedAt, &targetKey, &targetID, &targetType,
			&targetTitle, &targetBody, &targetStatus, &targetOwner, &targetRecordedAt,
		); err != nil {
			return nil, err
		}
		if !typeSet[targetType] {
			continue
		}
		sourceTime := sourceRecordedAt.UTC()
		targetTime := targetRecordedAt.UTC()
		out = append(out, semanticGraphEdgeRow{
			source: SemanticGraphNode{
				Key:            sourceKey,
				ID:             sourceID,
				Type:           "entity",
				Title:          sourceTitle,
				Body:           sourceBody,
				Status:         sourceStatus,
				OwnerProfileID: sourceOwner,
				RecordedAt:     &sourceTime,
			},
			target: SemanticGraphNode{
				Key:            targetKey,
				ID:             targetID,
				Type:           targetType,
				Title:          targetTitle,
				Body:           targetBody,
				Status:         targetStatus,
				OwnerProfileID: targetOwner,
				RecordedAt:     &targetTime,
			},
			edge: SemanticGraphEdge{
				ID:               edgeID,
				RelationshipID:   edgeID,
				Source:           sourceKey,
				Target:           targetKey,
				Relationship:     predicate,
				Directed:         true,
				OwnerProfileID:   ownerID,
				SupportCount:     supportCount,
				SourceGroupCount: sourceGroupCount,
			},
		})
	}
	return out, rows.Err()
}

func loadSemanticEntityGraphNode(ctx context.Context, tx *gorm.DB, teamID, entityID string) (*SemanticGraphNode, error) {
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
		    AND `+activeSemanticSpaceGenerationSQL("entity_names")+`
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
		  AND `+activeSemanticSpaceGenerationSQL("e")+`
		LIMIT 1
	`, teamID, entityID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	var node SemanticGraphNode
	var recordedAt time.Time
	if err := rows.Scan(&node.Key, &node.ID, &node.Title, &node.Body, &node.Status, &node.OwnerProfileID, &recordedAt); err != nil {
		return nil, err
	}
	node.Type = "entity"
	t := recordedAt.UTC()
	node.RecordedAt = &t
	return &node, rows.Err()
}

func loadSemanticValueGraphNode(ctx context.Context, tx *gorm.DB, teamID, valueID string) (*SemanticGraphNode, error) {
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
		  AND `+activeSemanticSpaceGenerationSQL("value")+`
		LIMIT 1
	`, teamID, valueID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	var node SemanticGraphNode
	var recordedAt time.Time
	if err := rows.Scan(&node.Key, &node.ID, &node.Title, &node.Body, &node.Status, &node.OwnerProfileID, &recordedAt); err != nil {
		return nil, err
	}
	node.Type = "value"
	t := recordedAt.UTC()
	node.RecordedAt = &t
	return &node, rows.Err()
}

func semanticGraphSnapshot(input SemanticGraphQuery, rows []semanticGraphEdgeRow) *SemanticGraphSnapshot {
	nodes := []SemanticGraphNode{}
	edges := []SemanticGraphEdge{}
	seenNodes := map[string]struct{}{}
	seenEdges := map[string]struct{}{}
	for _, row := range rows {
		for _, node := range []SemanticGraphNode{row.source, row.target} {
			if node.Key == "" {
				continue
			}
			if _, seen := seenNodes[node.Key]; seen {
				continue
			}
			seenNodes[node.Key] = struct{}{}
			nodes = append(nodes, node)
		}
		if row.edge.ID == "" {
			continue
		}
		if _, seen := seenEdges[row.edge.ID]; seen {
			continue
		}
		seenEdges[row.edge.ID] = struct{}{}
		edges = append(edges, row.edge)
	}
	snapshot := &SemanticGraphSnapshot{
		Scope:     input.Scope,
		Query:     input.Query,
		Depth:     input.Depth,
		Limit:     input.Limit,
		Truncated: len(edges) >= input.Limit,
		Nodes:     nodes,
		Edges:     edges,
	}
	if input.Scope == "local" {
		snapshot.Anchor = &SemanticGraphAnchor{
			Type: input.AnchorType,
			ID:   input.AnchorID,
			Key:  semanticGraphNodeKey(input.AnchorType, input.AnchorID),
		}
	}
	return snapshot
}

func normalizeSemanticGraphTypes(values []string) []string {
	set := semanticGraphTypeSet(values)
	out := make([]string, 0, len(set))
	for _, value := range []string{"entity", "value"} {
		if set[value] {
			out = append(out, value)
		}
	}
	return out
}

func semanticGraphTypeSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range values {
		if normalized := normalizeSemanticGraphNodeType(raw); normalized != "" {
			out[normalized] = true
		}
	}
	if len(out) == 0 {
		out["entity"] = true
		out["value"] = true
	}
	return out
}

func normalizeSemanticGraphNodeType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "entity", "entities":
		return "entity"
	case "value", "values":
		return "value"
	default:
		return ""
	}
}

func semanticGraphNodeKey(nodeType, id string) string {
	nodeType = normalizeSemanticGraphNodeType(nodeType)
	id = strings.TrimSpace(id)
	if nodeType == "" || id == "" {
		return ""
	}
	return nodeType + ":" + id
}
