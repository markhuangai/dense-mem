package graphview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/service/graphrow"
	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const (
	ScopeOverview = "overview"
	ScopeLocal    = "local"

	DefaultLimit = 80
	MaxLimit     = 180
	DefaultDepth = 1
	MaxDepth     = 2

	maxNodeBodyRunes = 420
)

var (
	ErrMissingAnchor     = errors.New("graph local view requires anchor_type and anchor_id")
	ErrInvalidAnchorType = errors.New("unsupported graph anchor_type")
)

type ScopedReader interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4jdriver.ResultSummary, []map[string]any, error)
}

type Service interface {
	Graph(ctx context.Context, profileID string, query Query) (*Snapshot, error)
}

type Query struct {
	Scope      string
	Query      string
	Types      []string
	AnchorType string
	AnchorID   string
	Depth      int
	Limit      int
}

type Snapshot struct {
	Scope     string  `json:"scope"`
	Query     string  `json:"query,omitempty"`
	Anchor    *Anchor `json:"anchor,omitempty"`
	Depth     int     `json:"depth"`
	Limit     int     `json:"limit"`
	Truncated bool    `json:"truncated"`
	Nodes     []Node  `json:"nodes"`
	Edges     []Edge  `json:"edges"`
}

type Anchor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Key  string `json:"key"`
}

type Node struct {
	Key         string     `json:"key"`
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Title       string     `json:"title"`
	Body        string     `json:"body,omitempty"`
	Status      string     `json:"status,omitempty"`
	CommunityID string     `json:"community_id,omitempty"`
	Source      string     `json:"source,omitempty"`
	Score       float64    `json:"score,omitempty"`
	RecordedAt  *time.Time `json:"recorded_at,omitempty"`
}

type Edge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	Relationship string `json:"relationship"`
	Directed     bool   `json:"directed"`
}

type service struct {
	reader ScopedReader
}

var _ Service = (*service)(nil)

func New(reader ScopedReader) Service {
	return &service{reader: reader}
}

type normalizedQuery struct {
	scope           string
	search          string
	includeFact     bool
	includeClaim    bool
	includeFragment bool
	includeDream    bool
	anchorType      string
	anchorID        string
	depth           int
	limit           int
	edgeLimit       int64
}

func (s *service) Graph(ctx context.Context, profileID string, query Query) (*Snapshot, error) {
	if s == nil || s.reader == nil {
		return nil, errors.New("graph view reader is not configured")
	}
	normalized, err := normalizeQuery(query)
	if err != nil {
		return nil, err
	}

	var rows []map[string]any
	if normalized.scope == ScopeLocal {
		_, rows, err = s.reader.ScopedRead(ctx, profileID, localNodesCypher(normalized.depth), normalized.params())
	} else {
		_, rows, err = s.reader.ScopedRead(ctx, profileID, overviewNodesCypher, normalized.params())
	}
	if err != nil {
		return nil, fmt.Errorf("graph view nodes: %w", err)
	}

	nodes := nodesFromRows(rows)
	edges := []Edge{}
	if len(nodes) > 0 {
		nodeKeys := make([]string, 0, len(nodes))
		for _, node := range nodes {
			nodeKeys = append(nodeKeys, node.Key)
		}
		_, edgeRows, err := s.reader.ScopedRead(ctx, profileID, graphEdgesCypher, map[string]any{
			"nodeKeys":          nodeKeys,
			"relationshipTypes": relationshipTypes,
			"edgeLimit":         normalized.edgeLimit,
		})
		if err != nil {
			return nil, fmt.Errorf("graph view edges: %w", err)
		}
		edges = edgesFromRows(edgeRows)
	}

	snapshot := &Snapshot{
		Scope:     normalized.scope,
		Query:     normalized.search,
		Depth:     normalized.depth,
		Limit:     normalized.limit,
		Truncated: len(nodes) >= normalized.limit,
		Nodes:     nodes,
		Edges:     edges,
	}
	if normalized.scope == ScopeLocal {
		snapshot.Anchor = &Anchor{
			Type: normalized.anchorType,
			ID:   normalized.anchorID,
			Key:  normalized.anchorType + ":" + normalized.anchorID,
		}
	}
	return snapshot, nil
}

func (q normalizedQuery) params() map[string]any {
	return map[string]any{
		"query":             q.search,
		"includeFact":       q.includeFact,
		"includeClaim":      q.includeClaim,
		"includeFragment":   q.includeFragment,
		"includeDream":      q.includeDream,
		"anchorType":        q.anchorType,
		"anchorID":          q.anchorID,
		"relationshipTypes": relationshipTypes,
		"limit":             int64(q.limit),
	}
}

func normalizeQuery(query Query) (normalizedQuery, error) {
	scope := strings.ToLower(strings.TrimSpace(query.Scope))
	if scope == "" {
		scope = ScopeOverview
	}
	if scope != ScopeLocal {
		scope = ScopeOverview
	}

	types := normalizeTypes(query.Types)
	normalized := normalizedQuery{
		scope:           scope,
		search:          strings.ToLower(strings.TrimSpace(query.Query)),
		includeFact:     types["fact"],
		includeClaim:    types["claim"],
		includeFragment: types["fragment"],
		includeDream:    types["dream"],
		limit:           clamp(query.Limit, DefaultLimit, MaxLimit),
		depth:           clamp(query.Depth, DefaultDepth, MaxDepth),
	}
	normalized.edgeLimit = int64(clamp(normalized.limit*4, 120, 720))

	if normalized.scope == ScopeLocal {
		normalized.anchorType = normalizeType(query.AnchorType)
		normalized.anchorID = strings.TrimSpace(query.AnchorID)
		if normalized.anchorType == "" && strings.TrimSpace(query.AnchorType) != "" {
			return normalizedQuery{}, ErrInvalidAnchorType
		}
		if normalized.anchorType == "" || normalized.anchorID == "" {
			return normalizedQuery{}, ErrMissingAnchor
		}
	}
	return normalized, nil
}

func normalizeTypes(values []string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range values {
		if normalized := normalizeType(raw); normalized != "" {
			out[normalized] = true
		}
	}
	if len(out) == 0 {
		out["fact"] = true
		out["claim"] = true
		out["fragment"] = true
	}
	return out
}

func normalizeType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "fact", "facts":
		return "fact"
	case "claim", "claims":
		return "claim"
	case "fragment", "fragments", "source_fragment", "sourcefragment":
		return "fragment"
	case "dream", "dreams":
		return "dream"
	default:
		return ""
	}
}

func clamp(value, defaultValue, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func nodesFromRows(rows []map[string]any) []Node {
	nodes := make([]Node, 0, len(rows))
	seen := map[string]struct{}{}
	for _, row := range rows {
		node := Node{
			Key:         graphrow.String(row, "key"),
			ID:          graphrow.String(row, "id"),
			Type:        graphrow.String(row, "type"),
			Title:       truncateText(graphrow.String(row, "title"), 160),
			Body:        truncateText(graphrow.String(row, "body"), maxNodeBodyRunes),
			Status:      graphrow.String(row, "status"),
			CommunityID: graphrow.String(row, "community_id"),
			Source:      graphrow.String(row, "source"),
			Score:       graphrow.Float64(row, "score"),
			RecordedAt:  graphrow.TimePtr(row, "recorded_at"),
		}
		if node.Key == "" || node.ID == "" || node.Type == "" {
			continue
		}
		if node.Title == "" {
			node.Title = node.ID
		}
		if _, exists := seen[node.Key]; exists {
			continue
		}
		seen[node.Key] = struct{}{}
		nodes = append(nodes, node)
	}
	return nodes
}

func edgesFromRows(rows []map[string]any) []Edge {
	edges := make([]Edge, 0, len(rows))
	seen := map[string]struct{}{}
	for _, row := range rows {
		edge := Edge{
			ID:           graphrow.String(row, "id"),
			Source:       graphrow.String(row, "source"),
			Target:       graphrow.String(row, "target"),
			Relationship: graphrow.String(row, "relationship"),
			Directed:     true,
		}
		if edge.Source == "" || edge.Target == "" || edge.Relationship == "" {
			continue
		}
		key := edge.ID
		if key == "" {
			key = edge.Source + "|" + edge.Relationship + "|" + edge.Target
			edge.ID = key
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		edges = append(edges, edge)
	}
	return edges
}

func truncateText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:maxRunes])) + "..."
}

var relationshipTypes = []string{
	"SUPPORTED_BY",
	"PROMOTES_TO",
	"CONTRADICTS",
	"SUPERSEDED_BY",
	"OVERLAYS",
	"ALIGNS_WITH",
	"DREAMS_FROM",
}

const overviewNodesCypher = `
CALL {
  MATCH (f:Fact {team_id: $profileId})
  WHERE $includeFact
    AND coalesce(f.status, '') IN ['active', 'needs_revalidation']
    AND ($query = '' OR toLower(coalesce(f.subject, '') + ' ' + coalesce(f.predicate, '') + ' ' + coalesce(f.object, '')) CONTAINS $query)
  RETURN 'fact' AS type,
         f.fact_id AS id,
         'fact:' + f.fact_id AS key,
         trim(coalesce(f.subject, '') + ' ' + coalesce(f.predicate, '') + ' ' + coalesce(f.object, '')) AS title,
         coalesce(f.object, '') AS body,
         coalesce(f.status, '') AS status,
         toString(f.community_id) AS community_id,
         '' AS source,
         coalesce(f.truth_score, 0.0) AS score,
         f.recorded_at AS recorded_at
  UNION ALL
  MATCH (c:Claim {team_id: $profileId})
  WHERE $includeClaim
    AND coalesce(c.status, 'candidate') <> 'rejected'
    AND ($query = '' OR toLower(coalesce(c.subject, '') + ' ' + coalesce(c.predicate, '') + ' ' + coalesce(c.object, '')) CONTAINS $query)
  RETURN 'claim' AS type,
         c.claim_id AS id,
         'claim:' + c.claim_id AS key,
         trim(coalesce(c.subject, '') + ' ' + coalesce(c.predicate, '') + ' ' + coalesce(c.object, '')) AS title,
         coalesce(c.object, '') AS body,
         coalesce(c.status, '') AS status,
         toString(c.community_id) AS community_id,
         '' AS source,
         coalesce(c.resolution_conf, c.extract_conf, c.source_quality, 0.0) AS score,
         c.recorded_at AS recorded_at
  UNION ALL
  MATCH (sf:SourceFragment {team_id: $profileId})
  WHERE $includeFragment
    AND coalesce(sf.status, 'active') <> 'retracted'
    AND ($query = '' OR toLower(coalesce(sf.content, '') + ' ' + coalesce(sf.source, '')) CONTAINS $query)
  RETURN 'fragment' AS type,
         sf.fragment_id AS id,
         'fragment:' + sf.fragment_id AS key,
         substring(coalesce(sf.content, ''), 0, 160) AS title,
         substring(coalesce(sf.content, ''), 0, 600) AS body,
         coalesce(sf.status, 'active') AS status,
         toString(sf.community_id) AS community_id,
         coalesce(sf.source, '') AS source,
         coalesce(sf.source_quality, 0.0) AS score,
         sf.created_at AS recorded_at
  UNION ALL
  MATCH (d:Dream {team_id: $profileId})
  WHERE $includeDream
    AND coalesce(d.status, '') IN ['proposed', 'reinforced']
    AND ($query = '' OR toLower(coalesce(d.hypothesis, '') + ' ' + coalesce(d.what_if, '') + ' ' + coalesce(d.possible_outcome, '')) CONTAINS $query)
  RETURN 'dream' AS type,
         d.dream_id AS id,
         'dream:' + d.dream_id AS key,
         coalesce(d.hypothesis, '') AS title,
         trim(coalesce(d.what_if, '') + ' ' + coalesce(d.possible_outcome, '')) AS body,
         coalesce(d.status, '') AS status,
         toString(d.community_id) AS community_id,
         coalesce(d.generator_model, '') AS source,
         coalesce(d.confidence, d.likelihood, 0.0) AS score,
         d.updated_at AS recorded_at
}
RETURN type, id, key, title, body, status, community_id, source, score, recorded_at
ORDER BY recorded_at DESC, key ASC
LIMIT $limit`

func localNodesCypher(depth int) string {
	return fmt.Sprintf(`
MATCH (anchor)
WHERE anchor.team_id = $profileId
  AND (
    ($anchorType = 'fact' AND anchor:Fact AND anchor.fact_id = $anchorID AND coalesce(anchor.status, '') IN ['active', 'needs_revalidation']) OR
    ($anchorType = 'claim' AND anchor:Claim AND anchor.claim_id = $anchorID AND coalesce(anchor.status, 'candidate') <> 'rejected') OR
    ($anchorType = 'fragment' AND anchor:SourceFragment AND anchor.fragment_id = $anchorID AND coalesce(anchor.status, 'active') <> 'retracted') OR
    ($anchorType = 'dream' AND anchor:Dream AND anchor.dream_id = $anchorID AND coalesce(anchor.status, '') IN ['proposed', 'reinforced'])
  )
CALL {
  WITH anchor
  RETURN anchor AS n
  UNION
  WITH anchor
  MATCH p = (anchor)-[*1..%d]-(n)
  WHERE all(rel IN relationships(p) WHERE rel.team_id = $profileId AND type(rel) IN $relationshipTypes)
    AND all(node IN nodes(p) WHERE node.team_id = $profileId)
  UNWIND nodes(p) AS n
  RETURN DISTINCT n
}
WITH anchor, n
WHERE elementId(n) = elementId(anchor) OR (
  ($includeFact AND n:Fact AND coalesce(n.status, '') IN ['active', 'needs_revalidation']) OR
  ($includeClaim AND n:Claim AND coalesce(n.status, 'candidate') <> 'rejected') OR
  ($includeFragment AND n:SourceFragment AND coalesce(n.status, 'active') <> 'retracted') OR
  ($includeDream AND n:Dream AND coalesce(n.status, '') IN ['proposed', 'reinforced'])
)
WITH DISTINCT anchor, n
RETURN
       CASE
         WHEN n:Fact THEN 'fact'
         WHEN n:Claim THEN 'claim'
         WHEN n:SourceFragment THEN 'fragment'
         WHEN n:Dream THEN 'dream'
         ELSE ''
       END AS type,
       CASE
         WHEN n:Fact THEN n.fact_id
         WHEN n:Claim THEN n.claim_id
         WHEN n:SourceFragment THEN n.fragment_id
         WHEN n:Dream THEN n.dream_id
         ELSE ''
       END AS id,
       CASE
         WHEN n:Fact THEN 'fact:' + n.fact_id
         WHEN n:Claim THEN 'claim:' + n.claim_id
         WHEN n:SourceFragment THEN 'fragment:' + n.fragment_id
         WHEN n:Dream THEN 'dream:' + n.dream_id
         ELSE ''
       END AS key,
       CASE
         WHEN n:Fact THEN trim(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, ''))
         WHEN n:Claim THEN trim(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, ''))
         WHEN n:SourceFragment THEN substring(coalesce(n.content, ''), 0, 160)
         WHEN n:Dream THEN coalesce(n.hypothesis, '')
         ELSE ''
       END AS title,
       CASE
         WHEN n:Fact THEN coalesce(n.object, '')
         WHEN n:Claim THEN coalesce(n.object, '')
         WHEN n:SourceFragment THEN substring(coalesce(n.content, ''), 0, 600)
         WHEN n:Dream THEN trim(coalesce(n.what_if, '') + ' ' + coalesce(n.possible_outcome, ''))
         ELSE ''
       END AS body,
       coalesce(n.status, CASE WHEN n:SourceFragment THEN 'active' ELSE '' END) AS status,
       toString(n.community_id) AS community_id,
       CASE
         WHEN n:SourceFragment THEN coalesce(n.source, '')
         WHEN n:Dream THEN coalesce(n.generator_model, '')
         ELSE ''
       END AS source,
       CASE
         WHEN n:Fact THEN coalesce(n.truth_score, 0.0)
         WHEN n:Claim THEN coalesce(n.resolution_conf, n.extract_conf, n.source_quality, 0.0)
         WHEN n:SourceFragment THEN coalesce(n.source_quality, 0.0)
         WHEN n:Dream THEN coalesce(n.confidence, n.likelihood, 0.0)
         ELSE 0.0
       END AS score,
       CASE
         WHEN n:Dream THEN n.updated_at
         WHEN n:SourceFragment THEN n.created_at
         ELSE n.recorded_at
       END AS recorded_at
ORDER BY CASE WHEN elementId(n) = elementId(anchor) THEN 0 ELSE 1 END, recorded_at DESC, key ASC
LIMIT $limit`, depth)
}

const graphEdgesCypher = `
MATCH (a)-[r]->(b)
WHERE a.team_id = $profileId
  AND b.team_id = $profileId
  AND r.team_id = $profileId
  AND type(r) IN $relationshipTypes
WITH a, r, b,
     CASE
       WHEN a:Fact THEN 'fact:' + a.fact_id
       WHEN a:Claim THEN 'claim:' + a.claim_id
       WHEN a:SourceFragment THEN 'fragment:' + a.fragment_id
       WHEN a:Dream THEN 'dream:' + a.dream_id
       ELSE ''
     END AS source_key,
     CASE
       WHEN b:Fact THEN 'fact:' + b.fact_id
       WHEN b:Claim THEN 'claim:' + b.claim_id
       WHEN b:SourceFragment THEN 'fragment:' + b.fragment_id
       WHEN b:Dream THEN 'dream:' + b.dream_id
       ELSE ''
     END AS target_key
WHERE source_key IN $nodeKeys
  AND target_key IN $nodeKeys
RETURN elementId(r) AS id,
       source_key AS source,
       target_key AS target,
       type(r) AS relationship
ORDER BY relationship ASC, source ASC, target ASC
LIMIT $edgeLimit`
