// Package contract contains the graph capability's dependency-safe contracts.
package contract

import (
	"context"
	"time"
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
