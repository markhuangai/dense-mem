package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *SemanticRepositoryImpl) SemanticGraph(ctx context.Context, teamID string, query domain.SemanticGraphQuery) (*domain.SemanticGraphSnapshot, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, sql.ErrNoRows
	}
	query.Limit = normalizeSemanticGraphLimit(query.Limit)
	query.Depth = normalizeSemanticGraphDepth(query.Depth)
	query.Scope = strings.TrimSpace(query.Scope)
	if query.Scope == "" {
		query.Scope = "overview"
	}
	query.Query = strings.ToLower(strings.TrimSpace(query.Query))
	types := semanticGraphTypeSet(query.Types)

	var rows []semanticGraphEdgeRow
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		var err error
		if query.Scope == "local" {
			rows, err = loadSemanticLocalGraphRows(ctx, tx, teamID, query, types)
		} else {
			rows, err = loadSemanticOverviewGraphRows(ctx, tx, teamID, query, types)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("semantic graph: %w", err)
	}
	return semanticGraphSnapshot(query, rows), nil
}

func (r *SemanticRepositoryImpl) SemanticGraphNodeDetail(ctx context.Context, teamID string, nodeType string, nodeID string) (*domain.SemanticGraphNode, error) {
	teamID = strings.TrimSpace(teamID)
	nodeType = strings.ToLower(strings.TrimSpace(nodeType))
	nodeID = strings.TrimSpace(nodeID)
	if teamID == "" || nodeType == "" || nodeID == "" {
		return nil, sql.ErrNoRows
	}
	var node *domain.SemanticGraphNode
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		var err error
		switch nodeType {
		case "entity":
			node, err = loadSemanticEntityGraphNode(ctx, tx, teamID, nodeID)
		case "value":
			node, err = loadSemanticValueGraphNode(ctx, tx, teamID, nodeID)
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
	source domain.SemanticGraphNode
	target domain.SemanticGraphNode
	edge   domain.SemanticGraphEdge
}

func loadSemanticOverviewGraphRows(ctx context.Context, tx *gorm.DB, teamID string, query domain.SemanticGraphQuery, types map[string]bool) ([]semanticGraphEdgeRow, error) {
	rows, err := tx.WithContext(ctx).Raw(semanticGraphEdgesSQL(""), teamID, query.Query, query.Query, query.Limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSemanticGraphRows(rows, types)
}

func loadSemanticLocalGraphRows(ctx context.Context, tx *gorm.DB, teamID string, query domain.SemanticGraphQuery, types map[string]bool) ([]semanticGraphEdgeRow, error) {
	anchor := semanticGraphNodeKey(query.AnchorType, query.AnchorID)
	if anchor == "" {
		return nil, sql.ErrNoRows
	}
	frontier := []string{anchor}
	seenNodes := map[string]struct{}{anchor: {}}
	seenEdges := map[string]struct{}{}
	out := []semanticGraphEdgeRow{}
	for depth := 0; depth < query.Depth && len(frontier) > 0 && len(out) < query.Limit; depth++ {
		rows, err := tx.WithContext(ctx).Raw(semanticGraphEdgesSQL(`
		  AND (
		    ('entity:' || subject.entity_id::text) = ANY(?::text[])
		    OR (CASE
		      WHEN object.entity_id IS NOT NULL THEN 'entity:' || object.entity_id::text
		      ELSE 'value:' || value.value_id::text
		    END) = ANY(?::text[])
		  )
		`), teamID, query.Query, query.Query, pq.Array(frontier), pq.Array(frontier), query.Limit-len(out)).Rows()
		if err != nil {
			return nil, err
		}
		batch, err := scanSemanticGraphRows(rows, types)
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
		nextFrontier := []string{}
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
				nextFrontier = append(nextFrontier, key)
			}
			if len(out) == query.Limit {
				break
			}
		}
		frontier = nextFrontier
	}
	return out, nil
}

func semanticGraphEdgesSQL(extraWhere string) string {
	return `
		SELECT r.relationship_id::text,
		       r.predicate,
		       ('entity:' || subject.entity_id::text) AS source_key,
		       subject.entity_id::text AS source_id,
		       subject.canonical_name AS source_title,
		       subject.kind AS source_body,
		       subject.status AS source_status,
		       subject.updated_at AS source_recorded_at,
		       CASE
		         WHEN object.entity_id IS NOT NULL THEN 'entity:' || object.entity_id::text
		         ELSE 'value:' || value.value_id::text
		       END AS target_key,
		       COALESCE(object.entity_id::text, value.value_id::text) AS target_id,
		       CASE WHEN object.entity_id IS NOT NULL THEN 'entity' ELSE 'value' END AS target_type,
		       COALESCE(object.canonical_name, value.display_value) AS target_title,
		       COALESCE(object.kind, value.value_type) AS target_body,
		       COALESCE(object.status, value.status) AS target_status,
		       COALESCE(object.updated_at, value.updated_at) AS target_recorded_at
		FROM semantic_relationship_records r
		JOIN semantic_entities subject
		  ON subject.team_id = r.team_id AND subject.entity_id = r.subject_entity_id
		LEFT JOIN semantic_entities object
		  ON object.team_id = r.team_id AND object.entity_id = r.object_entity_id
		LEFT JOIN semantic_values value
		  ON value.team_id = r.team_id AND value.value_id = r.object_value_id
		WHERE r.team_id = ?
		  AND r.status = 'active'
		  AND r.tier IN ('validated_claim', 'fact')
		  AND subject.status = 'active'
		  AND (
		    (object.entity_id IS NOT NULL AND object.status = 'active')
		    OR (value.value_id IS NOT NULL AND value.status = 'active')
		  )
		  AND (
		    ? = ''
		    OR lower(subject.canonical_name || ' ' || r.predicate || ' ' || COALESCE(object.canonical_name, value.display_value, '')) LIKE '%' || ? || '%'
		  )
	` + extraWhere + `
		ORDER BY r.updated_at DESC, r.relationship_id ASC
		LIMIT ?
	`
}

func scanSemanticGraphRows(rows *sql.Rows, types map[string]bool) ([]semanticGraphEdgeRow, error) {
	out := []semanticGraphEdgeRow{}
	for rows.Next() {
		var (
			edgeID, predicate                                                      string
			sourceKey, sourceID, sourceTitle, sourceBody, sourceStatus             string
			targetKey, targetID, targetType, targetTitle, targetBody, targetStatus string
			sourceRecordedAt, targetRecordedAt                                     time.Time
		)
		if err := rows.Scan(
			&edgeID,
			&predicate,
			&sourceKey,
			&sourceID,
			&sourceTitle,
			&sourceBody,
			&sourceStatus,
			&sourceRecordedAt,
			&targetKey,
			&targetID,
			&targetType,
			&targetTitle,
			&targetBody,
			&targetStatus,
			&targetRecordedAt,
		); err != nil {
			return nil, err
		}
		if !types["entity"] || !types[targetType] {
			continue
		}
		sourceTime := sourceRecordedAt.UTC()
		targetTime := targetRecordedAt.UTC()
		out = append(out, semanticGraphEdgeRow{
			source: domain.SemanticGraphNode{
				Key:        sourceKey,
				ID:         sourceID,
				Type:       "entity",
				Title:      sourceTitle,
				Body:       sourceBody,
				Status:     sourceStatus,
				RecordedAt: &sourceTime,
			},
			target: domain.SemanticGraphNode{
				Key:        targetKey,
				ID:         targetID,
				Type:       targetType,
				Title:      targetTitle,
				Body:       targetBody,
				Status:     targetStatus,
				RecordedAt: &targetTime,
			},
			edge: domain.SemanticGraphEdge{
				ID:           edgeID,
				Source:       sourceKey,
				Target:       targetKey,
				Relationship: predicate,
				Directed:     true,
			},
		})
	}
	return out, rows.Err()
}

func semanticGraphSnapshot(query domain.SemanticGraphQuery, rows []semanticGraphEdgeRow) *domain.SemanticGraphSnapshot {
	nodes := []domain.SemanticGraphNode{}
	edges := []domain.SemanticGraphEdge{}
	seenNodes := map[string]struct{}{}
	seenEdges := map[string]struct{}{}
	addNode := func(node domain.SemanticGraphNode) {
		if node.Key == "" {
			return
		}
		if _, seen := seenNodes[node.Key]; seen {
			return
		}
		seenNodes[node.Key] = struct{}{}
		nodes = append(nodes, node)
	}
	for _, row := range rows {
		addNode(row.source)
		addNode(row.target)
		if _, seen := seenEdges[row.edge.ID]; seen {
			continue
		}
		seenEdges[row.edge.ID] = struct{}{}
		edges = append(edges, row.edge)
	}
	snapshot := &domain.SemanticGraphSnapshot{
		Scope:     query.Scope,
		Query:     query.Query,
		Depth:     query.Depth,
		Limit:     query.Limit,
		Truncated: len(edges) >= query.Limit,
		Nodes:     nodes,
		Edges:     edges,
	}
	if query.Scope == "local" {
		snapshot.Anchor = &domain.SemanticGraphAnchor{
			Type: query.AnchorType,
			ID:   query.AnchorID,
			Key:  semanticGraphNodeKey(query.AnchorType, query.AnchorID),
		}
	}
	return snapshot
}

func loadSemanticEntityGraphNode(ctx context.Context, tx *gorm.DB, teamID, entityID string) (*domain.SemanticGraphNode, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT ('entity:' || entity_id::text), entity_id::text, canonical_name, kind, status, updated_at
		FROM semantic_entities
		WHERE team_id = ? AND entity_id = ?::uuid AND status = 'active'
		LIMIT 1
	`, teamID, entityID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	var node domain.SemanticGraphNode
	var recordedAt time.Time
	if err := rows.Scan(&node.Key, &node.ID, &node.Title, &node.Body, &node.Status, &recordedAt); err != nil {
		return nil, err
	}
	node.Type = "entity"
	t := recordedAt.UTC()
	node.RecordedAt = &t
	return &node, rows.Err()
}

func loadSemanticValueGraphNode(ctx context.Context, tx *gorm.DB, teamID, valueID string) (*domain.SemanticGraphNode, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT ('value:' || value_id::text), value_id::text, display_value, value_type, status, updated_at
		FROM semantic_values
		WHERE team_id = ? AND value_id = ?::uuid AND status = 'active'
		LIMIT 1
	`, teamID, valueID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	var node domain.SemanticGraphNode
	var recordedAt time.Time
	if err := rows.Scan(&node.Key, &node.ID, &node.Title, &node.Body, &node.Status, &recordedAt); err != nil {
		return nil, err
	}
	node.Type = "value"
	t := recordedAt.UTC()
	node.RecordedAt = &t
	return &node, rows.Err()
}

func semanticGraphTypeSet(types []string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range types {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "entity", "entities":
			out["entity"] = true
		case "value", "values":
			out["value"] = true
		}
	}
	if len(out) == 0 {
		out["entity"] = true
		out["value"] = true
	}
	return out
}

func semanticGraphNodeKey(nodeType, id string) string {
	nodeType = strings.ToLower(strings.TrimSpace(nodeType))
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if nodeType != "entity" && nodeType != "value" {
		return ""
	}
	return nodeType + ":" + id
}

func normalizeSemanticGraphLimit(limit int) int {
	if limit <= 0 {
		return 80
	}
	if limit > 180 {
		return 180
	}
	return limit
}

func normalizeSemanticGraphDepth(depth int) int {
	if depth <= 0 {
		return 1
	}
	if depth > 2 {
		return 2
	}
	return depth
}
