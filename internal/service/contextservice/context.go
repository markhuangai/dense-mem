// Package contextservice builds bounded relationship traces from Dense-Mem's semantic graph.
package contextservice

import "context"

// Service exposes the supported read-only relationship trace operation.
type Service interface {
	Trace(ctx context.Context, profileID string, req TraceRequest) (*TraceResult, error)
}

// TraceRequest selects one Relationship to expand.
type TraceRequest struct {
	RelationshipID         string   `json:"relationship_id"`
	IncludeEvidenceContent *bool    `json:"include_evidence_content,omitempty"`
	IncludeVerification    *bool    `json:"include_verification,omitempty"`
	IncludeTransitions     *bool    `json:"include_transitions,omitempty"`
	MaxDepth               int      `json:"max_depth,omitempty"`
	MaxEdges               int      `json:"max_edges,omitempty"`
	PredicateKeys          []string `json:"predicate_keys,omitempty"`
	Topic                  string   `json:"topic,omitempty"`
	MinRelevance           *float64 `json:"min_relevance,omitempty"`
}

// TraceResult holds the public semantic lineage response.
type TraceResult struct {
	Semantic *SemanticTrace `json:"semantic,omitempty"`
}
