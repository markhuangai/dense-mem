// Package contract contains the graph capability's dependency-safe contracts.
package contract

import (
	"context"
	"strings"
	"time"
)

const (
	ScopeOverview = "overview"
	ScopeLocal    = "local"
	DefaultLimit  = 80
	DefaultDepth  = 2
	MaxDepth      = 5
)

// Query is the caller-owned graph request. Memory-space scope is derived by
// the adapter from authenticated context and is intentionally absent here.
type Query struct {
	TeamID       string
	Scope        string
	Query        string
	Types        []string
	AnchorType   string
	AnchorID     string
	Depth        int
	Limit        int
	MinRelevance float64
}

func NormalizeQuery(input Query) Query {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	if input.Scope != ScopeLocal {
		input.Scope = ScopeOverview
	}
	input.Query = strings.ToLower(strings.TrimSpace(input.Query))
	input.Types = NormalizeTypes(input.Types)
	input.AnchorType = NormalizeNodeType(input.AnchorType)
	input.AnchorID = strings.TrimSpace(input.AnchorID)
	if input.Depth <= 0 {
		input.Depth = DefaultDepth
	} else if input.Depth > MaxDepth {
		input.Depth = MaxDepth
	}
	if input.Limit <= 0 {
		input.Limit = DefaultLimit
	}
	return input
}

func NormalizeTypes(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		normalized := NormalizeNodeType(raw)
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

type NodeDetailInput struct {
	TeamID   string
	NodeType string
	NodeID   string
}

type Anchor struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	Key  string `json:"key,omitempty"`
}

type Node struct {
	Key            string     `json:"key,omitempty"`
	ID             string     `json:"id,omitempty"`
	Type           string     `json:"type,omitempty"`
	Title          string     `json:"title,omitempty"`
	Body           string     `json:"body,omitempty"`
	Status         string     `json:"status,omitempty"`
	OwnerProfileID string     `json:"owner_profile_id,omitempty"`
	RecordedAt     *time.Time `json:"recorded_at,omitempty"`
}

type Edge struct {
	ID               string `json:"id,omitempty"`
	RelationshipID   string `json:"relationship_id,omitempty"`
	Source           string `json:"source,omitempty"`
	Target           string `json:"target,omitempty"`
	Relationship     string `json:"relationship,omitempty"`
	Directed         bool   `json:"directed,omitempty"`
	OwnerProfileID   string `json:"owner_profile_id,omitempty"`
	SupportCount     int    `json:"support_count,omitempty"`
	SourceGroupCount int    `json:"source_group_count,omitempty"`
}

type Snapshot struct {
	Scope     string  `json:"scope,omitempty"`
	Query     string  `json:"query,omitempty"`
	Anchor    *Anchor `json:"anchor,omitempty"`
	Depth     int     `json:"depth,omitempty"`
	Limit     int     `json:"limit,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
	Nodes     []Node  `json:"nodes,omitempty"`
	Edges     []Edge  `json:"edges,omitempty"`
}

type Store interface {
	SemanticGraph(context.Context, Query) (*Snapshot, error)
	SemanticGraphNodeDetail(context.Context, NodeDetailInput) (*Node, error)
}
