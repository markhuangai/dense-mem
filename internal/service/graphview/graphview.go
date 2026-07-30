package graphview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
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

type SemanticStore interface {
	SemanticGraph(ctx context.Context, input repository.SemanticGraphQuery) (*repository.SemanticGraphSnapshot, error)
	SemanticGraphNodeDetail(ctx context.Context, input repository.SemanticGraphNodeDetailInput) (*repository.SemanticGraphNode, error)
}

type Service interface {
	Graph(ctx context.Context, profileID string, query Query) (*Snapshot, error)
	NodeDetail(ctx context.Context, profileID string, nodeType string, nodeID string) (*Node, error)
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

type semanticService struct {
	store SemanticStore
}

var _ Service = (*semanticService)(nil)

func NewSemantic(store SemanticStore) Service {
	return &semanticService{store: store}
}

type normalizedSemanticQuery struct {
	scope      string
	search     string
	types      []string
	anchorType string
	anchorID   string
	depth      int
	limit      int
}

func (s *semanticService) Graph(ctx context.Context, teamID string, query Query) (*Snapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("graph view semantic store is not configured")
	}
	normalized, err := normalizeSemanticQuery(query)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.store.SemanticGraph(ctx, repository.SemanticGraphQuery{
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
		return nil, fmt.Errorf("semantic graph view: %w", err)
	}
	return snapshotFromSemantic(snapshot), nil
}

func (s *semanticService) NodeDetail(ctx context.Context, teamID string, nodeType string, nodeID string) (*Node, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("graph view semantic store is not configured")
	}
	normalizedType := normalizeSemanticGraphType(nodeType)
	normalizedID := strings.TrimSpace(nodeID)
	if normalizedType == "" && strings.TrimSpace(nodeType) != "" {
		return nil, ErrInvalidNodeType
	}
	if normalizedType == "" || normalizedID == "" {
		return nil, ErrMissingNode
	}
	node, err := s.store.SemanticGraphNodeDetail(ctx, repository.SemanticGraphNodeDetailInput{
		TeamID:   strings.TrimSpace(teamID),
		NodeType: normalizedType,
		NodeID:   normalizedID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("semantic graph node detail: %w", err)
	}
	if node == nil {
		return nil, ErrNodeNotFound
	}
	return nodeFromSemantic(*node), nil
}

func normalizeSemanticQuery(query Query) (normalizedSemanticQuery, error) {
	scope := strings.ToLower(strings.TrimSpace(query.Scope))
	if scope == "" || scope != ScopeLocal {
		scope = ScopeOverview
	}
	normalized := normalizedSemanticQuery{
		scope:  scope,
		search: strings.ToLower(strings.TrimSpace(query.Query)),
		types:  normalizeSemanticGraphTypes(query.Types),
		limit:  clamp(query.Limit, DefaultLimit, MaxLimit),
		depth:  clamp(query.Depth, DefaultDepth, MaxDepth),
	}
	if normalized.scope == ScopeLocal {
		normalized.anchorType = normalizeSemanticGraphType(query.AnchorType)
		normalized.anchorID = strings.TrimSpace(query.AnchorID)
		if normalized.anchorType == "" && strings.TrimSpace(query.AnchorType) != "" {
			return normalizedSemanticQuery{}, ErrInvalidAnchorType
		}
		if normalized.anchorType == "" || normalized.anchorID == "" {
			return normalizedSemanticQuery{}, ErrMissingAnchor
		}
	}
	return normalized, nil
}

func normalizeSemanticGraphTypes(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		normalized := normalizeSemanticGraphType(raw)
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

func normalizeSemanticGraphType(raw string) string {
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

func snapshotFromSemantic(snapshot *repository.SemanticGraphSnapshot) *Snapshot {
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
		out.Nodes = append(out.Nodes, *nodeFromSemantic(node))
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

func nodeFromSemantic(node repository.SemanticGraphNode) *Node {
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

func truncateText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:maxRunes])) + "..."
}
