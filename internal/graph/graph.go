package graph

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
)

const (
	ScopeOverview = graphcontract.ScopeOverview
	ScopeLocal    = graphcontract.ScopeLocal

	DefaultLimit = graphcontract.DefaultLimit
	DefaultDepth = graphcontract.DefaultDepth
	MaxDepth     = graphcontract.MaxDepth

	maxNodeBodyRunes = 420
)

var (
	ErrMissingAnchor     = errors.New("graph local view requires anchor_type and anchor_id")
	ErrInvalidAnchorType = errors.New("unsupported graph anchor_type")
	ErrMissingNode       = errors.New("graph node detail requires type and id")
	ErrInvalidNodeType   = errors.New("unsupported graph node type")
	ErrNodeNotFound      = errors.New("graph node not found")
)

type Store = graphcontract.Store

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
	store Store
}

var _ Service = (*semanticService)(nil)

func New(store Store) Service {
	return &semanticService{store: store}
}

func (s *semanticService) Graph(ctx context.Context, teamID string, query Query) (*Snapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("graph view semantic store is not configured")
	}
	normalized, err := normalizeSemanticQuery(query)
	if err != nil {
		return nil, err
	}
	normalized.TeamID = strings.TrimSpace(teamID)
	snapshot, err := s.store.SemanticGraph(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("semantic graph view: %w", err)
	}
	return snapshotFromSemantic(snapshot), nil
}

func (s *semanticService) NodeDetail(ctx context.Context, teamID string, nodeType string, nodeID string) (*Node, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("graph view semantic store is not configured")
	}
	normalizedType := graphcontract.NormalizeNodeType(nodeType)
	normalizedID := strings.TrimSpace(nodeID)
	if normalizedType == "" && strings.TrimSpace(nodeType) != "" {
		return nil, ErrInvalidNodeType
	}
	if normalizedType == "" || normalizedID == "" {
		return nil, ErrMissingNode
	}
	node, err := s.store.SemanticGraphNodeDetail(ctx, graphcontract.NodeDetailInput{
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

func normalizeSemanticQuery(query Query) (graphcontract.Query, error) {
	normalized := graphcontract.NormalizeQuery(graphcontract.Query{
		Scope: query.Scope, Query: query.Query, Types: query.Types,
		AnchorType: query.AnchorType, AnchorID: query.AnchorID,
		Depth: query.Depth, Limit: query.Limit,
	})
	if normalized.Scope != ScopeLocal {
		normalized.AnchorType = ""
		normalized.AnchorID = ""
		return normalized, nil
	}
	if normalized.AnchorType == "" && strings.TrimSpace(query.AnchorType) != "" {
		return graphcontract.Query{}, ErrInvalidAnchorType
	}
	if normalized.AnchorType == "" || normalized.AnchorID == "" {
		return graphcontract.Query{}, ErrMissingAnchor
	}
	return normalized, nil
}

func snapshotFromSemantic(snapshot *graphcontract.Snapshot) *Snapshot {
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

func nodeFromSemantic(node graphcontract.Node) *Node {
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
