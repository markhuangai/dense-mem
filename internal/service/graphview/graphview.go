// Package graphview preserves the legacy graph application names while the
// live implementation is owned by internal/graph.
package graphview

import graphapp "github.com/markhuangai/dense-mem/internal/graph"

type SemanticStore = graphapp.Store
type Service = graphapp.Service
type Query = graphapp.Query
type Snapshot = graphapp.Snapshot
type Anchor = graphapp.Anchor
type Node = graphapp.Node
type Edge = graphapp.Edge

const (
	ScopeOverview = graphapp.ScopeOverview
	ScopeLocal    = graphapp.ScopeLocal
	DefaultLimit  = graphapp.DefaultLimit
	DefaultDepth  = graphapp.DefaultDepth
	MaxDepth      = graphapp.MaxDepth
)

var (
	ErrMissingAnchor     = graphapp.ErrMissingAnchor
	ErrInvalidAnchorType = graphapp.ErrInvalidAnchorType
	ErrMissingNode       = graphapp.ErrMissingNode
	ErrInvalidNodeType   = graphapp.ErrInvalidNodeType
	ErrNodeNotFound      = graphapp.ErrNodeNotFound
)

func NewSemantic(store SemanticStore) Service {
	return graphapp.New(store)
}
