package graphview

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/service/graphrow"
	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

const (
	ScopeOverview = "overview"
	ScopeLocal    = "local"

	DefaultLimit = 100
	MaxLimit     = 500
	DefaultDepth = 1
	MaxDepth     = 2
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
	Key              string     `json:"key"`
	ID               string     `json:"id"`
	Type             string     `json:"type"`
	Title            string     `json:"title"`
	Body             string     `json:"body,omitempty"`
	Status           string     `json:"status,omitempty"`
	CommunityID      string     `json:"community_id,omitempty"`
	Source           string     `json:"source,omitempty"`
	Score            float64    `json:"score,omitempty"`
	RecordedAt       *time.Time `json:"recorded_at,omitempty"`
	Aliases          []string   `json:"aliases,omitempty"`
	EntityType       string     `json:"entity_type,omitempty"`
	ValueType        string     `json:"value_type,omitempty"`
	ResolutionStatus string     `json:"resolution_status,omitempty"`
	ResolutionConf   float64    `json:"resolution_conf,omitempty"`
}

type Edge struct {
	ID               string     `json:"id"`
	Source           string     `json:"source"`
	Target           string     `json:"target"`
	Relationship     string     `json:"relationship"`
	Directed         bool       `json:"directed"`
	AssertionID      string     `json:"assertion_id,omitempty"`
	Predicate        string     `json:"predicate,omitempty"`
	Tier             string     `json:"tier,omitempty"`
	Status           string     `json:"status,omitempty"`
	PolicyFamily     string     `json:"policy_family,omitempty"`
	Polarity         string     `json:"polarity,omitempty"`
	Modality         string     `json:"modality,omitempty"`
	Knowledge        string     `json:"knowledge,omitempty"`
	SupportCount     int        `json:"support_count,omitempty"`
	SourceGroupCount int        `json:"source_group_count,omitempty"`
	EvidenceIDs      []string   `json:"evidence_ids,omitempty"`
	ValidFrom        *time.Time `json:"valid_from,omitempty"`
	ValidTo          *time.Time `json:"valid_to,omitempty"`
	RecordedAt       *time.Time `json:"recorded_at,omitempty"`
	RecordedTo       *time.Time `json:"recorded_to,omitempty"`
}

type service struct {
	reader ScopedReader
}

var _ Service = (*service)(nil)

func New(reader ScopedReader) Service {
	return &service{reader: reader}
}

type normalizedQuery struct {
	scope             string
	search            string
	includeFact       bool
	includeClaim      bool
	includeFragment   bool
	includeDream      bool
	includeEntity     bool
	includeValue      bool
	includeCommunity  bool
	anchorType        string
	anchorID          string
	depth             int
	limit             int
	includeSuperseded bool
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
			"includeSuperseded": normalized.includeSuperseded,
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
	if normalized.scope == ScopeOverview {
		snapshot.Limit = 0
		snapshot.Truncated = false
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

func (q normalizedQuery) params() map[string]any {
	return map[string]any{
		"query":             q.search,
		"includeFact":       q.includeFact,
		"includeClaim":      q.includeClaim,
		"includeFragment":   q.includeFragment,
		"includeDream":      q.includeDream,
		"includeEntity":     q.includeEntity,
		"includeValue":      q.includeValue,
		"includeCommunity":  q.includeCommunity,
		"anchorType":        q.anchorType,
		"anchorID":          q.anchorID,
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
		includeEntity:     types["entity"],
		includeValue:      types["value"],
		includeCommunity:  types["community"],
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
		out["entity"] = true
		out["value"] = true
		out["community"] = true
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
	case "entity", "entities":
		return "entity"
	case "value", "values":
		return "value"
	case "community", "communities":
		return "community"
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
			Key:              graphrow.String(row, "key"),
			ID:               graphrow.String(row, "id"),
			Type:             graphrow.String(row, "type"),
			Title:            redactGraphText(graphrow.String(row, "title")),
			Body:             redactGraphText(graphrow.String(row, "body")),
			Status:           graphrow.String(row, "status"),
			CommunityID:      graphrow.String(row, "community_id"),
			Source:           redactGraphText(graphrow.String(row, "source")),
			Score:            graphrow.Float64(row, "score"),
			RecordedAt:       graphrow.TimePtr(row, "recorded_at"),
			Aliases:          redactGraphTexts(graphrow.StringSlice(row, "aliases")),
			EntityType:       graphrow.String(row, "entity_type"),
			ValueType:        graphrow.String(row, "value_type"),
			ResolutionStatus: graphrow.String(row, "resolution_status"),
			ResolutionConf:   graphrow.Float64(row, "resolution_conf"),
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
			ID:               graphrow.String(row, "id"),
			Source:           graphrow.String(row, "source"),
			Target:           graphrow.String(row, "target"),
			Relationship:     graphrow.String(row, "relationship"),
			Directed:         true,
			AssertionID:      graphrow.String(row, "assertion_id"),
			Predicate:        graphrow.String(row, "predicate"),
			Tier:             graphrow.String(row, "tier"),
			Status:           graphrow.String(row, "status"),
			PolicyFamily:     graphrow.String(row, "policy_family"),
			Polarity:         graphrow.String(row, "polarity"),
			Modality:         graphrow.String(row, "modality"),
			Knowledge:        redactGraphText(graphrow.String(row, "knowledge")),
			SupportCount:     graphrow.Int(row, "support_count"),
			SourceGroupCount: graphrow.Int(row, "source_group_count"),
			EvidenceIDs:      graphrow.StringSlice(row, "evidence_ids"),
			ValidFrom:        graphrow.TimePtr(row, "valid_from"),
			ValidTo:          graphrow.TimePtr(row, "valid_to"),
			RecordedAt:       graphrow.TimePtr(row, "recorded_at"),
			RecordedTo:       graphrow.TimePtr(row, "recorded_to"),
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

var graphSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(authorization|api[_ -]?key|token|password|secret|credential)\s*[:=]\s*[^\s,;]+`),
	regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/-]+=*`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
}

func redactGraphText(value string) string {
	value = strings.TrimSpace(value)
	for _, pattern := range graphSecretPatterns {
		value = pattern.ReplaceAllStringFunc(value, func(match string) string {
			if index := strings.IndexAny(match, ":="); index >= 0 {
				return strings.TrimSpace(match[:index]) + "=[REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	return value
}

func redactGraphTexts(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, redactGraphText(value))
	}
	return out
}

const overviewNodesCypher = `
CALL {
  MATCH (n:Entity {team_id: $profileId})
  WHERE $includeEntity
    AND ($query = '' OR toLower(coalesce(n.canonical_name, '') + ' ' + coalesce(n.normalized_name, '') + ' ' + reduce(text = '', alias IN coalesce(n.aliases, []) | text + ' ' + alias)) CONTAINS $query)
  RETURN 'entity' AS type, n.entity_id AS id, 'entity:' + n.entity_id AS key,
         n.canonical_name AS title,
         reduce(text = '', alias IN coalesce(n.aliases, []) | CASE WHEN text = '' THEN alias ELSE text + ', ' + alias END) AS body,
         n.resolution_status AS status, '' AS community_id, '' AS source, 0.0 AS score,
         n.last_seen_at AS recorded_at, coalesce(n.aliases, []) AS aliases,
         n.entity_type AS entity_type, '' AS value_type,
         n.resolution_status AS resolution_status, coalesce(n.resolution_conf, 0) AS resolution_conf
  UNION ALL
  MATCH (n:Value {team_id: $profileId})
  WHERE $includeValue
    AND ($query = '' OR toLower(coalesce(n.display, '') + ' ' + coalesce(n.value, '') + ' ' + coalesce(n.unit, '')) CONTAINS $query)
  RETURN 'value' AS type, n.value_id AS id, 'value:' + n.value_id AS key,
         coalesce(n.display, n.value) AS title,
         trim(coalesce(n.value, '') + ' ' + coalesce(n.unit, '')) AS body,
         '' AS status, '' AS community_id, '' AS source, 0.0 AS score,
         null AS recorded_at, [] AS aliases, '' AS entity_type,
         n.value_type AS value_type, '' AS resolution_status, 0.0 AS resolution_conf
  UNION ALL
  MATCH (n:Fact {team_id: $profileId})
  WHERE $includeFact
	AND ($includeSuperseded OR coalesce(n.status, '') <> 'superseded')
    AND ($query = '' OR toLower(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, '')) CONTAINS $query)
  RETURN 'fact' AS type, n.fact_id AS id, 'fact:' + n.fact_id AS key,
         trim(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, '')) AS title,
         coalesce(n.object, '') AS body, coalesce(n.status, '') AS status,
         toString(n.community_id) AS community_id, '' AS source, coalesce(n.truth_score, 0) AS score,
         n.recorded_at AS recorded_at, [] AS aliases, '' AS entity_type, '' AS value_type,
         '' AS resolution_status, 0.0 AS resolution_conf
  UNION ALL
  MATCH (n:Claim {team_id: $profileId})
  WHERE $includeClaim
	AND ($includeSuperseded OR coalesce(n.status, 'candidate') <> 'superseded')
    AND ($query = '' OR toLower(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, '')) CONTAINS $query)
  RETURN 'claim' AS type, n.claim_id AS id, 'claim:' + n.claim_id AS key,
         trim(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, '')) AS title,
         coalesce(n.object, '') AS body, coalesce(n.status, 'candidate') AS status,
         toString(n.community_id) AS community_id, '' AS source, coalesce(n.extract_conf, 0) AS score,
         n.recorded_at AS recorded_at, [] AS aliases, '' AS entity_type, '' AS value_type,
         '' AS resolution_status, 0.0 AS resolution_conf
  UNION ALL
  MATCH (n:SourceFragment {team_id: $profileId})
  WHERE $includeFragment
	AND ($includeSuperseded OR coalesce(n.status, 'active') <> 'superseded')
    AND ($query = '' OR toLower(coalesce(n.content, '') + ' ' + coalesce(n.source, '')) CONTAINS $query)
  RETURN 'fragment' AS type, n.fragment_id AS id, 'fragment:' + n.fragment_id AS key,
         substring(coalesce(n.content, ''), 0, 160) AS title,
         coalesce(n.content, '') AS body, coalesce(n.status, 'active') AS status,
         toString(n.community_id) AS community_id, coalesce(n.source, '') AS source,
         coalesce(n.source_quality, 0) AS score, n.created_at AS recorded_at,
         [] AS aliases, '' AS entity_type, '' AS value_type, '' AS resolution_status, 0.0 AS resolution_conf
  UNION ALL
  MATCH (n:Dream {team_id: $profileId})
  WHERE $includeDream
	AND ($includeSuperseded OR coalesce(n.status, '') <> 'superseded')
    AND ($query = '' OR toLower(coalesce(n.hypothesis, '') + ' ' + coalesce(n.what_if, '') + ' ' + coalesce(n.possible_outcome, '') + ' ' + coalesce(n.rationale, '')) CONTAINS $query)
  RETURN 'dream' AS type, n.dream_id AS id, 'dream:' + n.dream_id AS key,
         coalesce(n.hypothesis, '') AS title,
         trim(coalesce(n.what_if, '') + ' ' + coalesce(n.possible_outcome, '') + ' ' + coalesce(n.rationale, '')) AS body,
         coalesce(n.status, '') AS status, '' AS community_id, '' AS source,
         coalesce(n.likelihood, 0) AS score, n.updated_at AS recorded_at,
         [] AS aliases, '' AS entity_type, '' AS value_type, '' AS resolution_status, 0.0 AS resolution_conf
  UNION ALL
  MATCH (n:Community {team_id: $profileId})
  WHERE $includeCommunity
	AND ($includeSuperseded OR coalesce(n.status, 'active') <> 'superseded')
    AND ($query = '' OR toLower(coalesce(n.name, '') + ' ' + coalesce(n.summary, '')) CONTAINS $query)
  RETURN 'community' AS type, toString(n.community_id) AS id, 'community:' + toString(n.community_id) AS key,
         coalesce(n.name, 'Community ' + toString(n.community_id)) AS title,
         coalesce(n.summary, '') AS body, coalesce(n.status, 'active') AS status,
         toString(n.community_id) AS community_id, '' AS source, coalesce(n.score, 0) AS score,
         n.updated_at AS recorded_at, [] AS aliases, '' AS entity_type, '' AS value_type,
         '' AS resolution_status, 0.0 AS resolution_conf
}
RETURN type, id, key, title, body, status, community_id, source, score, recorded_at,
       aliases, entity_type, value_type, resolution_status, resolution_conf
ORDER BY type ASC, title ASC, id ASC
`

func localNodesCypher(depth int) string {
	return fmt.Sprintf(`
MATCH (anchor)
	WHERE anchor.team_id = $profileId
	  AND (
	    ($anchorType = 'fact' AND anchor:Fact AND anchor.fact_id = $anchorID) OR
		    ($anchorType = 'claim' AND anchor:Claim AND anchor.claim_id = $anchorID) OR
		    ($anchorType = 'fragment' AND anchor:SourceFragment AND anchor.fragment_id = $anchorID) OR
		    ($anchorType = 'dream' AND anchor:Dream AND anchor.dream_id = $anchorID) OR
		    ($anchorType = 'entity' AND anchor:Entity AND anchor.entity_id = $anchorID) OR
		    ($anchorType = 'value' AND anchor:Value AND anchor.value_id = $anchorID) OR
		    ($anchorType = 'community' AND anchor:Community AND toString(anchor.community_id) = $anchorID)
		  )
CALL {
  WITH anchor
  RETURN anchor AS n
  UNION
	  WITH anchor
	  MATCH p = (anchor)-[*1..%d]-(n)
	  WHERE all(rel IN relationships(p) WHERE rel.team_id = $profileId)
	    AND all(node IN nodes(p) WHERE node.team_id = $profileId)
  UNWIND nodes(p) AS n
  RETURN DISTINCT n
}
WITH anchor, n
	WHERE elementId(n) = elementId(anchor) OR ((
		  ($includeFact AND n:Fact) OR
		  ($includeClaim AND n:Claim) OR
		  ($includeFragment AND n:SourceFragment) OR
		  ($includeDream AND n:Dream) OR
		  ($includeEntity AND n:Entity) OR
		  ($includeValue AND n:Value) OR
		  ($includeCommunity AND n:Community)
		) AND ($includeSuperseded OR coalesce(n.status, '') <> 'superseded'))
WITH DISTINCT anchor, n,
	     CASE
	       WHEN n:Entity THEN n.last_seen_at
	       WHEN n:Dream THEN n.updated_at
	       WHEN n:SourceFragment THEN n.created_at
	       WHEN n:Community THEN n.updated_at
	       ELSE n.recorded_at
	     END AS recorded_at
RETURN
	       CASE
	         WHEN n:Entity THEN 'entity'
	         WHEN n:Value THEN 'value'
	         WHEN n:Fact THEN 'fact'
	         WHEN n:Claim THEN 'claim'
	         WHEN n:SourceFragment THEN 'fragment'
	         WHEN n:Dream THEN 'dream'
	         WHEN n:Community THEN 'community'
	         ELSE ''
	       END AS type,
	       CASE
	         WHEN n:Entity THEN n.entity_id
	         WHEN n:Value THEN n.value_id
	         WHEN n:Fact THEN n.fact_id
	         WHEN n:Claim THEN n.claim_id
	         WHEN n:SourceFragment THEN n.fragment_id
	         WHEN n:Dream THEN n.dream_id
	         WHEN n:Community THEN toString(n.community_id)
	         ELSE ''
	       END AS id,
	       CASE
	         WHEN n:Entity THEN 'entity:' + n.entity_id
	         WHEN n:Value THEN 'value:' + n.value_id
	         WHEN n:Fact THEN 'fact:' + n.fact_id
	         WHEN n:Claim THEN 'claim:' + n.claim_id
	         WHEN n:SourceFragment THEN 'fragment:' + n.fragment_id
	         WHEN n:Dream THEN 'dream:' + n.dream_id
	         WHEN n:Community THEN 'community:' + toString(n.community_id)
	         ELSE ''
	       END AS key,
	       CASE
	         WHEN n:Entity THEN n.canonical_name
	         WHEN n:Value THEN coalesce(n.display, n.value)
	         WHEN n:Fact THEN trim(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, ''))
	         WHEN n:Claim THEN trim(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, ''))
	         WHEN n:SourceFragment THEN substring(coalesce(n.content, ''), 0, 160)
	         WHEN n:Dream THEN coalesce(n.hypothesis, '')
	         WHEN n:Community THEN coalesce(n.name, 'Community ' + toString(n.community_id))
	         ELSE ''
	       END AS title,
	       CASE
	         WHEN n:Entity THEN reduce(text = '', alias IN coalesce(n.aliases, []) | CASE WHEN text = '' THEN alias ELSE text + ', ' + alias END)
	         WHEN n:Value THEN trim(coalesce(n.value, '') + ' ' + coalesce(n.unit, ''))
	         WHEN n:Fact THEN coalesce(n.object, '')
	         WHEN n:Claim THEN coalesce(n.object, '')
	         WHEN n:SourceFragment THEN coalesce(n.content, '')
	         WHEN n:Dream THEN trim(coalesce(n.what_if, '') + ' ' + coalesce(n.possible_outcome, '') + ' ' + coalesce(n.rationale, ''))
	         WHEN n:Community THEN coalesce(n.summary, '')
	         ELSE ''
	       END AS body,
	       coalesce(n.status, CASE WHEN n:SourceFragment THEN 'active' WHEN n:Entity THEN n.resolution_status ELSE '' END) AS status,
	       toString(n.community_id) AS community_id,
	       CASE WHEN n:SourceFragment THEN coalesce(n.source, '') ELSE '' END AS source,
	       CASE
	         WHEN n:Entity THEN coalesce(n.resolution_conf, 0.0)
	         WHEN n:Fact THEN coalesce(n.truth_score, 0.0)
	         WHEN n:Claim THEN coalesce(n.resolution_conf, n.extract_conf, n.source_quality, 0.0)
	         WHEN n:SourceFragment THEN coalesce(n.source_quality, 0.0)
	         WHEN n:Dream THEN coalesce(n.confidence, n.likelihood, 0.0)
	         ELSE 0.0
	       END AS score,
	       recorded_at,
	       CASE WHEN n:Entity THEN coalesce(n.aliases, []) ELSE [] END AS aliases,
	       CASE WHEN n:Entity THEN n.entity_type ELSE '' END AS entity_type,
	       CASE WHEN n:Value THEN n.value_type ELSE '' END AS value_type,
	       CASE WHEN n:Entity THEN n.resolution_status ELSE '' END AS resolution_status,
	       CASE WHEN n:Entity THEN coalesce(n.resolution_conf, 0.0) ELSE 0.0 END AS resolution_conf
	ORDER BY CASE WHEN elementId(n) = elementId(anchor) THEN 0 ELSE 1 END, recorded_at DESC, key ASC
	LIMIT $limit`, depth)
}

const graphEdgesCypher = `
CALL {
  MATCH (assertion:Assertion {team_id: $profileId})
  MATCH (subject:Entity {team_id: $profileId, entity_id: assertion.subject_entity_id})
  MATCH (object:Entity|Value {team_id: $profileId})
  WHERE object.graph_key = CASE
    WHEN coalesce(assertion.object_entity_id, '') <> '' THEN 'entity:' + assertion.object_entity_id
    ELSE 'value:' + assertion.object_value_id
  END
	AND ($includeSuperseded OR assertion.status <> 'superseded')
  OPTIONAL MATCH (assertion)-[:SUPPORTED_BY {team_id: $profileId}]->(fragment:SourceFragment {team_id: $profileId})
  WITH assertion, subject, object, collect(DISTINCT fragment.fragment_id) AS evidence_ids
  WHERE subject.graph_key IN $nodeKeys AND object.graph_key IN $nodeKeys
  RETURN assertion.assertion_id AS id,
         subject.graph_key AS source,
         object.graph_key AS target,
         assertion.relationship_type AS relationship,
         assertion.assertion_id AS assertion_id,
         assertion.predicate_key AS predicate,
         assertion.tier AS tier,
         assertion.status AS status,
         assertion.policy_family AS policy_family,
         assertion.polarity AS polarity,
         assertion.modality AS modality,
         trim(subject.canonical_name + ' ' + assertion.relationship_type + ' ' + CASE WHEN object:Entity THEN object.canonical_name ELSE coalesce(object.display, object.value) END) AS knowledge,
         coalesce(assertion.support_count, 0) AS support_count,
         coalesce(assertion.source_group_count, 0) AS source_group_count,
         evidence_ids,
         assertion.valid_from AS valid_from,
         assertion.valid_to AS valid_to,
         assertion.recorded_at AS recorded_at,
         assertion.recorded_to AS recorded_to
  UNION ALL
  MATCH (a)-[r]->(b)
  WHERE a.team_id = $profileId
    AND b.team_id = $profileId
    AND r.team_id = $profileId
    AND coalesce(r.semantic_projection, false) = false
	AND ($includeSuperseded OR coalesce(r.status, '') <> 'superseded')
  WITH a, r, b,
     CASE
       WHEN a:Entity THEN 'entity:' + a.entity_id
       WHEN a:Value THEN 'value:' + a.value_id
       WHEN a:Fact THEN 'fact:' + a.fact_id
       WHEN a:Claim THEN 'claim:' + a.claim_id
       WHEN a:SourceFragment THEN 'fragment:' + a.fragment_id
       WHEN a:Dream THEN 'dream:' + a.dream_id
       WHEN a:Community THEN 'community:' + toString(a.community_id)
       ELSE ''
     END AS source_key,
     CASE
       WHEN b:Entity THEN 'entity:' + b.entity_id
       WHEN b:Value THEN 'value:' + b.value_id
       WHEN b:Fact THEN 'fact:' + b.fact_id
       WHEN b:Claim THEN 'claim:' + b.claim_id
       WHEN b:SourceFragment THEN 'fragment:' + b.fragment_id
       WHEN b:Dream THEN 'dream:' + b.dream_id
       WHEN b:Community THEN 'community:' + toString(b.community_id)
       ELSE ''
     END AS target_key
  WHERE source_key IN $nodeKeys AND target_key IN $nodeKeys
  RETURN elementId(r) AS id,
         source_key AS source,
         target_key AS target,
         type(r) AS relationship,
         '' AS assertion_id,
         '' AS predicate,
         '' AS tier,
         coalesce(r.status, '') AS status,
         '' AS policy_family,
         '' AS polarity,
         '' AS modality,
         '' AS knowledge,
         0 AS support_count,
         0 AS source_group_count,
         [] AS evidence_ids,
         null AS valid_from,
         null AS valid_to,
         null AS recorded_at,
         null AS recorded_to
}
RETURN id, source, target, relationship, assertion_id, predicate, tier, status,
       policy_family, polarity, modality, knowledge, support_count,
       source_group_count, evidence_ids, valid_from, valid_to, recorded_at, recorded_to
ORDER BY relationship ASC, source ASC, target ASC, id ASC
`

const graphNodeDetailCypher = `
MATCH (n)
WHERE n.team_id = $profileId
  AND (
	($nodeType = 'entity' AND n:Entity AND n.entity_id = $nodeID) OR
	($nodeType = 'value' AND n:Value AND n.value_id = $nodeID) OR
	($nodeType = 'fact' AND n:Fact AND n.fact_id = $nodeID) OR
	($nodeType = 'claim' AND n:Claim AND n.claim_id = $nodeID) OR
	($nodeType = 'fragment' AND n:SourceFragment AND n.fragment_id = $nodeID) OR
	($nodeType = 'dream' AND n:Dream AND n.dream_id = $nodeID) OR
	($nodeType = 'community' AND n:Community AND toString(n.community_id) = $nodeID)
  )
RETURN
       CASE
		 WHEN n:Entity THEN 'entity'
		 WHEN n:Value THEN 'value'
         WHEN n:Fact THEN 'fact'
         WHEN n:Claim THEN 'claim'
         WHEN n:SourceFragment THEN 'fragment'
         WHEN n:Dream THEN 'dream'
		 WHEN n:Community THEN 'community'
         ELSE ''
       END AS type,
       CASE
		 WHEN n:Entity THEN n.entity_id
		 WHEN n:Value THEN n.value_id
         WHEN n:Fact THEN n.fact_id
         WHEN n:Claim THEN n.claim_id
         WHEN n:SourceFragment THEN n.fragment_id
         WHEN n:Dream THEN n.dream_id
		 WHEN n:Community THEN toString(n.community_id)
         ELSE ''
       END AS id,
       CASE
		 WHEN n:Entity THEN 'entity:' + n.entity_id
		 WHEN n:Value THEN 'value:' + n.value_id
         WHEN n:Fact THEN 'fact:' + n.fact_id
         WHEN n:Claim THEN 'claim:' + n.claim_id
         WHEN n:SourceFragment THEN 'fragment:' + n.fragment_id
         WHEN n:Dream THEN 'dream:' + n.dream_id
		 WHEN n:Community THEN 'community:' + toString(n.community_id)
         ELSE ''
       END AS key,
       CASE
		 WHEN n:Entity THEN n.canonical_name
		 WHEN n:Value THEN coalesce(n.display, n.value)
         WHEN n:Fact THEN trim(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, ''))
         WHEN n:Claim THEN trim(coalesce(n.subject, '') + ' ' + coalesce(n.predicate, '') + ' ' + coalesce(n.object, ''))
         WHEN n:SourceFragment THEN substring(coalesce(n.content, ''), 0, 160)
         WHEN n:Dream THEN coalesce(n.hypothesis, '')
		 WHEN n:Community THEN coalesce(n.name, 'Community ' + toString(n.community_id))
         ELSE ''
       END AS title,
       CASE
		 WHEN n:Entity THEN reduce(text = '', alias IN coalesce(n.aliases, []) | CASE WHEN text = '' THEN alias ELSE text + ', ' + alias END)
		 WHEN n:Value THEN trim(coalesce(n.value, '') + ' ' + coalesce(n.unit, ''))
         WHEN n:Fact THEN coalesce(n.object, '')
         WHEN n:Claim THEN coalesce(n.object, '')
         WHEN n:SourceFragment THEN coalesce(n.content, '')
		 WHEN n:Dream THEN trim(coalesce(n.what_if, '') + ' ' + coalesce(n.possible_outcome, '') + ' ' + coalesce(n.rationale, ''))
		 WHEN n:Community THEN coalesce(n.summary, '')
         ELSE ''
       END AS body,
	   coalesce(n.status, CASE WHEN n:SourceFragment THEN 'active' WHEN n:Entity THEN n.resolution_status ELSE '' END) AS status,
       toString(n.community_id) AS community_id,
       CASE
         WHEN n:SourceFragment THEN coalesce(n.source, '')
         ELSE ''
       END AS source,
       CASE
		 WHEN n:Entity THEN coalesce(n.resolution_conf, 0.0)
         WHEN n:Fact THEN coalesce(n.truth_score, 0.0)
         WHEN n:Claim THEN coalesce(n.resolution_conf, n.extract_conf, n.source_quality, 0.0)
         WHEN n:SourceFragment THEN coalesce(n.source_quality, 0.0)
         WHEN n:Dream THEN coalesce(n.confidence, n.likelihood, 0.0)
         ELSE 0.0
       END AS score,
       CASE
		 WHEN n:Entity THEN n.last_seen_at
         WHEN n:Dream THEN n.updated_at
         WHEN n:SourceFragment THEN n.created_at
		 WHEN n:Community THEN n.updated_at
         ELSE n.recorded_at
	   END AS recorded_at,
	   CASE WHEN n:Entity THEN coalesce(n.aliases, []) ELSE [] END AS aliases,
	   CASE WHEN n:Entity THEN n.entity_type ELSE '' END AS entity_type,
	   CASE WHEN n:Value THEN n.value_type ELSE '' END AS value_type,
	   CASE WHEN n:Entity THEN n.resolution_status ELSE '' END AS resolution_status,
	   CASE WHEN n:Entity THEN coalesce(n.resolution_conf, 0.0) ELSE 0.0 END AS resolution_conf
LIMIT 1
`
