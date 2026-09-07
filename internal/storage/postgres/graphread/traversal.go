// Package graphread contains bounded, database-independent graph traversal
// mechanics used by PostgreSQL graph and trace adapters.
package graphread

import (
	"context"
	"database/sql"
	"strings"
	"time"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
)

type Row struct {
	Source graphcontract.Node
	Target graphcontract.Node
	Edge   graphcontract.Edge
}

type BatchLoader func(context.Context, []string, int) ([]Row, error)

func ScanRows(rows *sql.Rows, types []string) ([]Row, error) {
	typeSet := TypeSet(types)
	if !typeSet["entity"] {
		return nil, nil
	}
	out := []Row{}
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
		out = append(out, Row{
			Source: graphcontract.Node{
				Key:            sourceKey,
				ID:             sourceID,
				Type:           "entity",
				Title:          sourceTitle,
				Body:           sourceBody,
				Status:         sourceStatus,
				OwnerProfileID: sourceOwner,
				RecordedAt:     &sourceTime,
			},
			Target: graphcontract.Node{
				Key:            targetKey,
				ID:             targetID,
				Type:           targetType,
				Title:          targetTitle,
				Body:           targetBody,
				Status:         targetStatus,
				OwnerProfileID: targetOwner,
				RecordedAt:     &targetTime,
			},
			Edge: graphcontract.Edge{
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

func NormalizeTypes(values []string) []string {
	set := TypeSet(values)
	out := make([]string, 0, len(set))
	for _, value := range []string{"entity", "value"} {
		if set[value] {
			out = append(out, value)
		}
	}
	return out
}

func TypeSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range values {
		if normalized := NormalizeNodeType(raw); normalized != "" {
			out[normalized] = true
		}
	}
	if len(out) == 0 {
		out["entity"] = true
		out["value"] = true
	}
	return out
}

func NormalizeNodeType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "entity", "entities":
		return "entity"
	case "value", "values":
		return "value"
	default:
		return ""
	}
}

// Traverse expands a bounded local graph while preserving the existing
// breadth-first ordering and de-duplication rules. Scope and authorization are
// owned by the loader; this package only controls traversal bounds.
func Traverse(
	ctx context.Context,
	anchor string,
	depth int,
	limit int,
	load BatchLoader,
) ([]Row, error) {
	if anchor == "" || depth <= 0 || limit <= 0 {
		return nil, nil
	}
	frontier := []string{anchor}
	seenNodes := map[string]struct{}{anchor: {}}
	seenEdges := map[string]struct{}{}
	out := make([]Row, 0, limit)
	for level := 0; level < depth && len(frontier) > 0 && len(out) < limit; level++ {
		batch, err := load(ctx, frontier, limit-len(out))
		if err != nil {
			return nil, err
		}
		next := make([]string, 0, len(batch)*2)
		for _, row := range batch {
			if _, seen := seenEdges[row.Edge.ID]; seen {
				continue
			}
			seenEdges[row.Edge.ID] = struct{}{}
			out = append(out, row)
			for _, key := range []string{row.Source.Key, row.Target.Key} {
				if _, seen := seenNodes[key]; seen || key == "" {
					continue
				}
				seenNodes[key] = struct{}{}
				next = append(next, key)
			}
			if len(out) == limit {
				break
			}
		}
		frontier = next
	}
	return out, nil
}

func Snapshot(input graphcontract.Query, rows []Row) *graphcontract.Snapshot {
	nodes := make([]graphcontract.Node, 0, len(rows)*2)
	edges := make([]graphcontract.Edge, 0, len(rows))
	seenNodes := map[string]struct{}{}
	seenEdges := map[string]struct{}{}
	for _, row := range rows {
		for _, node := range []graphcontract.Node{row.Source, row.Target} {
			if node.Key == "" {
				continue
			}
			if _, seen := seenNodes[node.Key]; seen {
				continue
			}
			seenNodes[node.Key] = struct{}{}
			nodes = append(nodes, node)
		}
		if row.Edge.ID == "" {
			continue
		}
		if _, seen := seenEdges[row.Edge.ID]; seen {
			continue
		}
		seenEdges[row.Edge.ID] = struct{}{}
		edges = append(edges, row.Edge)
	}
	snapshot := &graphcontract.Snapshot{
		Scope:     input.Scope,
		Query:     input.Query,
		Depth:     input.Depth,
		Limit:     input.Limit,
		Truncated: len(edges) >= input.Limit,
		Nodes:     nodes,
		Edges:     edges,
	}
	if input.Scope == "local" {
		snapshot.Anchor = &graphcontract.Anchor{
			Type: input.AnchorType,
			ID:   input.AnchorID,
			Key:  input.AnchorType + ":" + input.AnchorID,
		}
	}
	return snapshot
}
