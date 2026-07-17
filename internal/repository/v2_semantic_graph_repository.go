package repository

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

func loadV2TraceGraphContext(
	ctx context.Context,
	tx *gorm.DB,
	relationship *V2RelationshipTraceRecord,
	input V2TraceRelationshipInput,
) ([]V2SemanticGraphNode, []V2SemanticGraphEdge, error) {
	if relationship == nil || input.MaxEdges <= 0 || input.MaxDepth <= 0 {
		return nil, nil, nil
	}
	rows, err := loadV2SemanticLocalGraphRows(ctx, tx, V2SemanticGraphQuery{
		TeamID:     input.TeamID,
		Scope:      "local",
		Query:      strings.ToLower(input.Topic),
		Types:      []string{"entity", "value"},
		AnchorType: "entity",
		AnchorID:   relationship.SubjectEntityID,
		Depth:      input.MaxDepth,
		Limit:      input.MaxEdges,
	})
	if err != nil {
		return nil, nil, err
	}
	snapshot := v2SemanticGraphSnapshot(V2SemanticGraphQuery{
		TeamID: input.TeamID,
		Scope:  "local",
		Depth:  input.MaxDepth,
		Limit:  input.MaxEdges,
	}, rows)
	return snapshot.Nodes, snapshot.Edges, nil
}

func loadV2SemanticOverviewGraphRows(
	ctx context.Context,
	tx *gorm.DB,
	input V2SemanticGraphQuery,
) ([]v2SemanticGraphEdgeRow, error) {
	rows, err := tx.WithContext(ctx).Raw(v2SemanticGraphEdgesSQL(""), input.TeamID, input.Query, input.Query, input.Limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanV2SemanticGraphRows(rows, input.Types)
}

func loadV2SemanticLocalGraphRows(
	ctx context.Context,
	tx *gorm.DB,
	input V2SemanticGraphQuery,
) ([]v2SemanticGraphEdgeRow, error) {
	anchor := v2SemanticGraphNodeKey(input.AnchorType, input.AnchorID)
	if anchor == "" {
		return nil, sql.ErrNoRows
	}
	frontier := []string{anchor}
	seenNodes := map[string]struct{}{anchor: {}}
	seenEdges := map[string]struct{}{}
	out := []v2SemanticGraphEdgeRow{}
	for depth := 0; depth < input.Depth && len(frontier) > 0 && len(out) < input.Limit; depth++ {
		rows, err := tx.WithContext(ctx).Raw(v2SemanticGraphEdgesSQL(`
		  AND (
		    ('entity:' || e.subject_entity_id::text) = ANY(?::text[])
		    OR (CASE
		      WHEN e.object_entity_id IS NOT NULL THEN 'entity:' || e.object_entity_id::text
		      ELSE 'value:' || e.object_value_id::text
		    END) = ANY(?::text[])
		  )
		`), input.TeamID, input.Query, input.Query, pq.Array(frontier), pq.Array(frontier), input.Limit-len(out)).Rows()
		if err != nil {
			return nil, err
		}
		batch, err := scanV2SemanticGraphRows(rows, input.Types)
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

func v2SemanticGraphEdgesSQL(extraWhere string) string {
	return `
		SELECT e.relationship_id::text,
		       e.owner_profile_id::text,
		       e.predicate_key,
		       e.tier,
		       e.support_count,
		       e.source_group_count,
		       ('entity:' || e.subject_entity_id::text) AS source_key,
		       e.subject_entity_id::text AS source_id,
		       COALESCE(subject_name.display_name, e.subject_entity_id::text) AS source_title,
		       subject.entity_kind AS source_body,
		       subject.status AS source_status,
		       subject.owner_profile_id::text AS source_owner_profile_id,
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
		       COALESCE(object.owner_profile_id::text, value.owner_profile_id::text, e.owner_profile_id::text) AS target_owner_profile_id,
		       COALESCE(object.updated_at, value.created_at, subject.updated_at) AS target_recorded_at
		FROM semantic_edges e
		JOIN entity_records subject
		  ON subject.team_id = e.team_id AND subject.entity_id = e.subject_entity_id
		LEFT JOIN LATERAL (
		  SELECT display_name
		  FROM entity_names
		  WHERE team_id = e.team_id
		    AND entity_id = e.subject_entity_id
		    AND name_kind = 'canonical'
		    AND valid_to IS NULL
		  ORDER BY created_at DESC, entity_name_id DESC
		  LIMIT 1
		) subject_name ON true
		LEFT JOIN entity_records object
		  ON object.team_id = e.team_id AND object.entity_id = e.object_entity_id
		LEFT JOIN LATERAL (
		  SELECT display_name
		  FROM entity_names
		  WHERE team_id = e.team_id
		    AND entity_id = e.object_entity_id
		    AND name_kind = 'canonical'
		    AND valid_to IS NULL
		  ORDER BY created_at DESC, entity_name_id DESC
		  LIMIT 1
		) object_name ON true
		LEFT JOIN value_records value
		  ON value.team_id = e.team_id AND value.value_id = e.object_value_id
		WHERE e.team_id = ?::uuid
		  AND subject.status = 'active'
		  AND (
		    e.object_entity_id IS NULL
		    OR object.status = 'active'
		  )
		  AND (
		    ? = ''
		    OR lower(COALESCE(subject_name.display_name, '') || ' ' || e.predicate_key || ' ' ||
		             COALESCE(object_name.display_name, value.display, value.canonical_value, '')) LIKE '%' || ? || '%'
		  )
	` + extraWhere + `
		ORDER BY e.relationship_id ASC
		LIMIT ?
	`
}

func scanV2SemanticGraphRows(rows *sql.Rows, types []string) ([]v2SemanticGraphEdgeRow, error) {
	typeSet := v2SemanticGraphTypeSet(types)
	if !typeSet["entity"] {
		return nil, nil
	}
	out := []v2SemanticGraphEdgeRow{}
	for rows.Next() {
		var (
			edgeID, ownerID, predicate, tier                                        string
			supportCount, sourceGroupCount                                          int
			sourceKey, sourceID, sourceTitle, sourceBody, sourceStatus, sourceOwner string
			targetKey, targetID, targetType, targetTitle, targetBody, targetStatus  string
			targetOwner                                                             string
			sourceRecordedAt, targetRecordedAt                                      time.Time
		)
		if err := rows.Scan(
			&edgeID, &ownerID, &predicate, &tier, &supportCount, &sourceGroupCount,
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
		out = append(out, v2SemanticGraphEdgeRow{
			source: V2SemanticGraphNode{
				Key:            sourceKey,
				ID:             sourceID,
				Type:           "entity",
				Title:          sourceTitle,
				Body:           sourceBody,
				Status:         sourceStatus,
				OwnerProfileID: sourceOwner,
				RecordedAt:     &sourceTime,
			},
			target: V2SemanticGraphNode{
				Key:            targetKey,
				ID:             targetID,
				Type:           targetType,
				Title:          targetTitle,
				Body:           targetBody,
				Status:         targetStatus,
				OwnerProfileID: targetOwner,
				RecordedAt:     &targetTime,
			},
			edge: V2SemanticGraphEdge{
				ID:               edgeID,
				RelationshipID:   edgeID,
				Source:           sourceKey,
				Target:           targetKey,
				Relationship:     predicate,
				Directed:         true,
				OwnerProfileID:   ownerID,
				Tier:             tier,
				SupportCount:     supportCount,
				SourceGroupCount: sourceGroupCount,
			},
		})
	}
	return out, rows.Err()
}

func loadV2SemanticEntityGraphNode(ctx context.Context, tx *gorm.DB, teamID, entityID string) (*V2SemanticGraphNode, error) {
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
		    AND name_kind = 'canonical'
		    AND valid_to IS NULL
		  ORDER BY created_at DESC, entity_name_id DESC
		  LIMIT 1
		) name ON true
		WHERE e.team_id = ?::uuid
		  AND e.entity_id = ?::uuid
		  AND e.status = 'active'
		LIMIT 1
	`, teamID, entityID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	var node V2SemanticGraphNode
	var recordedAt time.Time
	if err := rows.Scan(&node.Key, &node.ID, &node.Title, &node.Body, &node.Status, &node.OwnerProfileID, &recordedAt); err != nil {
		return nil, err
	}
	node.Type = "entity"
	t := recordedAt.UTC()
	node.RecordedAt = &t
	return &node, rows.Err()
}

func loadV2SemanticValueGraphNode(ctx context.Context, tx *gorm.DB, teamID, valueID string) (*V2SemanticGraphNode, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT ('value:' || value_id::text), value_id::text,
		       COALESCE(NULLIF(display, ''), canonical_value), value_type,
		       'active', owner_profile_id::text, created_at
		FROM value_records
		WHERE team_id = ?::uuid
		  AND value_id = ?::uuid
		LIMIT 1
	`, teamID, valueID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	var node V2SemanticGraphNode
	var recordedAt time.Time
	if err := rows.Scan(&node.Key, &node.ID, &node.Title, &node.Body, &node.Status, &node.OwnerProfileID, &recordedAt); err != nil {
		return nil, err
	}
	node.Type = "value"
	t := recordedAt.UTC()
	node.RecordedAt = &t
	return &node, rows.Err()
}

func v2SemanticGraphSnapshot(input V2SemanticGraphQuery, rows []v2SemanticGraphEdgeRow) *V2SemanticGraphSnapshot {
	nodes := []V2SemanticGraphNode{}
	edges := []V2SemanticGraphEdge{}
	seenNodes := map[string]struct{}{}
	seenEdges := map[string]struct{}{}
	for _, row := range rows {
		for _, node := range []V2SemanticGraphNode{row.source, row.target} {
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
	snapshot := &V2SemanticGraphSnapshot{
		Scope:     input.Scope,
		Query:     input.Query,
		Depth:     input.Depth,
		Limit:     input.Limit,
		Truncated: len(edges) >= input.Limit,
		Nodes:     nodes,
		Edges:     edges,
	}
	if input.Scope == "local" {
		snapshot.Anchor = &V2SemanticGraphAnchor{
			Type: input.AnchorType,
			ID:   input.AnchorID,
			Key:  v2SemanticGraphNodeKey(input.AnchorType, input.AnchorID),
		}
	}
	return snapshot
}

func normalizeV2TracePredicateKeys(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) == 30 {
			break
		}
	}
	return out
}

func normalizeV2SemanticGraphTypes(values []string) []string {
	set := v2SemanticGraphTypeSet(values)
	out := make([]string, 0, len(set))
	for _, value := range []string{"entity", "value"} {
		if set[value] {
			out = append(out, value)
		}
	}
	return out
}

func v2SemanticGraphTypeSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range values {
		if normalized := normalizeV2SemanticGraphNodeType(raw); normalized != "" {
			out[normalized] = true
		}
	}
	if len(out) == 0 {
		out["entity"] = true
		out["value"] = true
	}
	return out
}

func normalizeV2SemanticGraphNodeType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "entity", "entities":
		return "entity"
	case "value", "values":
		return "value"
	default:
		return ""
	}
}

func v2SemanticGraphNodeKey(nodeType, id string) string {
	nodeType = normalizeV2SemanticGraphNodeType(nodeType)
	id = strings.TrimSpace(id)
	if nodeType == "" || id == "" {
		return ""
	}
	return nodeType + ":" + id
}
