package graphview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
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
	ErrMissingNode       = errors.New("graph node detail requires type and id")
	ErrInvalidNodeType   = errors.New("unsupported graph node type")
	ErrNodeNotFound      = errors.New("graph node not found")
)

type ScopedReader interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4jdriver.ResultSummary, []map[string]any, error)
}

type V2SemanticStore interface {
	SemanticGraph(ctx context.Context, input repository.V2SemanticGraphQuery) (*repository.V2SemanticGraphSnapshot, error)
	SemanticGraphNodeDetail(ctx context.Context, input repository.V2SemanticGraphNodeDetailInput) (*repository.V2SemanticGraphNode, error)
}

type Service interface {
	Graph(ctx context.Context, profileID string, query Query) (*Snapshot, error)
	NodeDetail(ctx context.Context, profileID string, nodeType string, nodeID string) (*Node, error)
}

type Query struct {
	Scope             string
	Query             string
	Types             []string
	AnchorType        string
	AnchorID          string
	Depth             int
	Limit             int
	IncludeSuperseded bool
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

type v2SemanticService struct {
	store V2SemanticStore
}

var _ Service = (*service)(nil)
var _ Service = (*v2SemanticService)(nil)

func New(reader ScopedReader) Service {
	return &service{reader: reader}
}

func NewV2Semantic(store V2SemanticStore) Service {
	return &v2SemanticService{store: store}
}

type normalizedQuery struct {
	scope             string
	search            string
	includeFact       bool
	includeClaim      bool
	includeFragment   bool
	includeDream      bool
	anchorType        string
	anchorID          string
	depth             int
	limit             int
	includeSuperseded bool
}

type normalizedV2SemanticQuery struct {
	scope      string
	search     string
	types      []string
	anchorType string
	anchorID   string
	depth      int
	limit      int
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
		})
		if err != nil {
			return nil, fmt.Errorf("graph view edges: %w", err)
		}
		edges = edgesFromRows(edgeRows)
	}

	truncated := false
	if normalized.scope == ScopeLocal {
		truncated = len(nodes) >= normalized.limit
	}
	snapshot := &Snapshot{
		Scope:     normalized.scope,
		Query:     normalized.search,
		Depth:     normalized.depth,
		Limit:     normalized.limit,
		Truncated: truncated,
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

func (s *service) NodeDetail(ctx context.Context, profileID string, nodeType string, nodeID string) (*Node, error) {
	if s == nil || s.reader == nil {
		return nil, errors.New("graph view reader is not configured")
	}
	normalizedType := normalizeType(nodeType)
	normalizedID := strings.TrimSpace(nodeID)
	if normalizedType == "" && strings.TrimSpace(nodeType) != "" {
		return nil, ErrInvalidNodeType
	}
	if normalizedType == "" || normalizedID == "" {
		return nil, ErrMissingNode
	}

	_, rows, err := s.reader.ScopedRead(ctx, profileID, graphNodeDetailCypher, map[string]any{
		"nodeType": normalizedType,
		"nodeID":   normalizedID,
	})
	if err != nil {
		return nil, fmt.Errorf("graph node detail: %w", err)
	}
	nodes := nodesFromRows(rows)
	if len(nodes) == 0 {
		return nil, ErrNodeNotFound
	}
	return &nodes[0], nil
}

func (s *v2SemanticService) Graph(ctx context.Context, teamID string, query Query) (*Snapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("graph view semantic store is not configured")
	}
	normalized, err := normalizeV2SemanticQuery(query)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.store.SemanticGraph(ctx, repository.V2SemanticGraphQuery{
		TeamID:     strings.TrimSpace(teamID),
		Scope:      normalized.scope,
		Query:      normalized.search,
		Types:      normalized.types,
		AnchorType: normalized.anchorType,
		AnchorID:   normalized.anchorID,
		Depth:      normalized.depth,
		Limit:      normalized.limit,
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic graph view: %w", err)
	}
	return snapshotFromV2Semantic(snapshot), nil
}

func (s *v2SemanticService) NodeDetail(ctx context.Context, teamID string, nodeType string, nodeID string) (*Node, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("graph view semantic store is not configured")
	}
	normalizedType := normalizeV2SemanticGraphType(nodeType)
	normalizedID := strings.TrimSpace(nodeID)
	if normalizedType == "" && strings.TrimSpace(nodeType) != "" {
		return nil, ErrInvalidNodeType
	}
	if normalizedType == "" || normalizedID == "" {
		return nil, ErrMissingNode
	}
	node, err := s.store.SemanticGraphNodeDetail(ctx, repository.V2SemanticGraphNodeDetailInput{
		TeamID:   strings.TrimSpace(teamID),
		NodeType: normalizedType,
		NodeID:   normalizedID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("v2 semantic graph node detail: %w", err)
	}
	if node == nil {
		return nil, ErrNodeNotFound
	}
	return nodeFromV2Semantic(*node), nil
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
		"includeSuperseded": q.includeSuperseded,
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
		scope:             scope,
		search:            strings.ToLower(strings.TrimSpace(query.Query)),
		includeFact:       types["fact"],
		includeClaim:      types["claim"],
		includeFragment:   types["fragment"],
		includeDream:      types["dream"],
		limit:             clamp(query.Limit, DefaultLimit, MaxLimit),
		depth:             clamp(query.Depth, DefaultDepth, MaxDepth),
		includeSuperseded: query.IncludeSuperseded,
	}

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

func normalizeV2SemanticQuery(query Query) (normalizedV2SemanticQuery, error) {
	scope := strings.ToLower(strings.TrimSpace(query.Scope))
	if scope == "" || scope != ScopeLocal {
		scope = ScopeOverview
	}
	normalized := normalizedV2SemanticQuery{
		scope:  scope,
		search: strings.ToLower(strings.TrimSpace(query.Query)),
		types:  normalizeV2SemanticGraphTypes(query.Types),
		limit:  clamp(query.Limit, DefaultLimit, MaxLimit),
		depth:  clamp(query.Depth, DefaultDepth, MaxDepth),
	}
	if normalized.scope == ScopeLocal {
		normalized.anchorType = normalizeV2SemanticGraphType(query.AnchorType)
		normalized.anchorID = strings.TrimSpace(query.AnchorID)
		if normalized.anchorType == "" && strings.TrimSpace(query.AnchorType) != "" {
			return normalizedV2SemanticQuery{}, ErrInvalidAnchorType
		}
		if normalized.anchorType == "" || normalized.anchorID == "" {
			return normalizedV2SemanticQuery{}, ErrMissingAnchor
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
		out["dream"] = true
	}
	return out
}

func normalizeV2SemanticGraphTypes(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		normalized := normalizeV2SemanticGraphType(raw)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return []string{"entity", "value"}
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

func normalizeV2SemanticGraphType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "entity", "entities":
		return "entity"
	case "value", "values":
		return "value"
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

func snapshotFromV2Semantic(snapshot *repository.V2SemanticGraphSnapshot) *Snapshot {
	if snapshot == nil {
		return &Snapshot{Nodes: []Node{}, Edges: []Edge{}}
	}
	out := &Snapshot{
		Scope:     snapshot.Scope,
		Query:     snapshot.Query,
		Depth:     snapshot.Depth,
		Limit:     snapshot.Limit,
		Truncated: snapshot.Truncated,
		Nodes:     make([]Node, 0, len(snapshot.Nodes)),
		Edges:     make([]Edge, 0, len(snapshot.Edges)),
	}
	if snapshot.Anchor != nil {
		out.Anchor = &Anchor{
			Type: snapshot.Anchor.Type,
			ID:   snapshot.Anchor.ID,
			Key:  snapshot.Anchor.Key,
		}
	}
	for _, node := range snapshot.Nodes {
		out.Nodes = append(out.Nodes, *nodeFromV2Semantic(node))
	}
	for _, edge := range snapshot.Edges {
		out.Edges = append(out.Edges, Edge{
			ID:           edge.ID,
			Source:       edge.Source,
			Target:       edge.Target,
			Relationship: edge.Relationship,
			Directed:     edge.Directed,
		})
	}
	return out
}

func nodeFromV2Semantic(node repository.V2SemanticGraphNode) *Node {
	title := truncateText(node.Title, 160)
	if title == "" {
		title = node.ID
	}
	return &Node{
		Key:        node.Key,
		ID:         node.ID,
		Type:       node.Type,
		Title:      title,
		Body:       truncateText(node.Body, maxNodeBodyRunes),
		Status:     node.Status,
		RecordedAt: node.RecordedAt,
	}
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
    AND (
      coalesce(f.status, '') IN ['active', 'needs_revalidation'] OR
      ($includeSuperseded AND coalesce(f.status, '') = 'superseded')
    )
    AND ($query = '' OR toLower(coalesce(f.subject, '') + ' ' + coalesce(f.predicate, '') + ' ' + coalesce(f.object, '')) CONTAINS $query)
  RETURN collect(f) AS seeds
}
WITH seeds AS factSeeds
CALL {
  MATCH (c:Claim {team_id: $profileId})
  WHERE $includeClaim
    AND coalesce(c.status, 'candidate') <> 'rejected'
    AND ($query = '' OR toLower(coalesce(c.subject, '') + ' ' + coalesce(c.predicate, '') + ' ' + coalesce(c.object, '')) CONTAINS $query)
  RETURN collect(c) AS seeds
}
WITH factSeeds, seeds AS claimSeeds
CALL {
  MATCH (sf:SourceFragment {team_id: $profileId})
  WHERE $includeFragment
    AND coalesce(sf.status, 'active') <> 'retracted'
    AND ($query = '' OR toLower(coalesce(sf.content, '') + ' ' + coalesce(sf.source, '')) CONTAINS $query)
  RETURN collect(sf) AS seeds
}
WITH factSeeds, claimSeeds, seeds AS fragmentSeeds
CALL {
  MATCH (d:Dream {team_id: $profileId})
  WHERE $includeDream
    AND coalesce(d.status, '') IN ['proposed', 'reinforced']
    AND ($query = '' OR toLower(coalesce(d.hypothesis, '') + ' ' + coalesce(d.what_if, '') + ' ' + coalesce(d.possible_outcome, '')) CONTAINS $query)
  RETURN collect(d) AS seeds
}
WITH factSeeds + claimSeeds + fragmentSeeds + seeds AS seedRows
UNWIND seedRows AS seed
WITH DISTINCT seed
CALL {
  WITH seed
  RETURN seed AS n, 1 AS seed_rank
  UNION
  WITH seed
  MATCH (seed)-[r]-(neighbor)
  WHERE r.team_id = $profileId
    AND type(r) IN $relationshipTypes
    AND neighbor.team_id = $profileId
  RETURN neighbor AS n, 0 AS seed_rank
}
WITH n, max(seed_rank) AS seed_rank
WHERE (
  ($includeFact AND n:Fact AND (
    coalesce(n.status, '') IN ['active', 'needs_revalidation'] OR
    ($includeSuperseded AND coalesce(n.status, '') = 'superseded')
  )) OR
  ($includeClaim AND n:Claim AND coalesce(n.status, 'candidate') <> 'rejected') OR
  ($includeFragment AND n:SourceFragment AND coalesce(n.status, 'active') <> 'retracted') OR
  ($includeDream AND n:Dream AND coalesce(n.status, '') IN ['proposed', 'reinforced'])
)
WITH n, seed_rank,
     CASE
       WHEN n:Fact THEN 'fact:' + n.fact_id
       WHEN n:Claim THEN 'claim:' + n.claim_id
       WHEN n:SourceFragment THEN 'fragment:' + n.fragment_id
       WHEN n:Dream THEN 'dream:' + n.dream_id
       ELSE ''
     END AS key,
     CASE
       WHEN n:Dream THEN n.updated_at
       WHEN n:SourceFragment THEN n.created_at
       ELSE n.recorded_at
     END AS recorded_at
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
       key AS key,
       CASE
         WHEN n:Fact THEN trim(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, ''))
         WHEN n:Claim THEN trim(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, ''))
         WHEN n:SourceFragment THEN substring(coalesce(n.content, ''), 0, 160)
         WHEN n:Dream THEN coalesce(n.hypothesis, '')
         ELSE ''
       END AS title
ORDER BY seed_rank DESC, recorded_at DESC, key ASC
`

func localNodesCypher(depth int) string {
	return fmt.Sprintf(`
MATCH (anchor)
	WHERE anchor.team_id = $profileId
	  AND (
	    ($anchorType = 'fact' AND anchor:Fact AND anchor.fact_id = $anchorID AND (
	      coalesce(anchor.status, '') IN ['active', 'needs_revalidation'] OR
	      ($includeSuperseded AND coalesce(anchor.status, '') = 'superseded')
	    )) OR
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
	  ($includeFact AND n:Fact AND (
	    coalesce(n.status, '') IN ['active', 'needs_revalidation'] OR
	    ($includeSuperseded AND coalesce(n.status, '') = 'superseded')
	  )) OR
	  ($includeClaim AND n:Claim AND coalesce(n.status, 'candidate') <> 'rejected') OR
	  ($includeFragment AND n:SourceFragment AND coalesce(n.status, 'active') <> 'retracted') OR
	  ($includeDream AND n:Dream AND coalesce(n.status, '') IN ['proposed', 'reinforced'])
	)
WITH DISTINCT anchor, n,
     CASE
       WHEN n:Dream THEN n.updated_at
       WHEN n:SourceFragment THEN n.created_at
       ELSE n.recorded_at
     END AS recorded_at
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
	       END AS title
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
`

const graphNodeDetailCypher = `
MATCH (n)
WHERE n.team_id = $profileId
  AND (
    ($nodeType = 'fact' AND n:Fact AND n.fact_id = $nodeID AND coalesce(n.status, '') IN ['active', 'needs_revalidation', 'superseded']) OR
    ($nodeType = 'claim' AND n:Claim AND n.claim_id = $nodeID AND coalesce(n.status, 'candidate') <> 'rejected') OR
    ($nodeType = 'fragment' AND n:SourceFragment AND n.fragment_id = $nodeID AND coalesce(n.status, 'active') <> 'retracted') OR
    ($nodeType = 'dream' AND n:Dream AND n.dream_id = $nodeID AND coalesce(n.status, '') IN ['proposed', 'reinforced'])
  )
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
         WHEN n:SourceFragment THEN coalesce(n.content, '')
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
LIMIT 1
`
