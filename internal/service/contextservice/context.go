// Package contextservice builds bounded memory traces and prompt-ready context
// blocks from Dense-Mem's semantic graph.
package contextservice

import (
	"context"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	AnchorFact  = "fact"
	AnchorClaim = "claim"
)

// Service exposes read-only memory context helpers.
type Service interface {
	Trace(ctx context.Context, profileID string, req TraceRequest) (*TraceResult, error)
	Assemble(ctx context.Context, profileID string, req AssembleRequest) (*AssembleResult, error)
}

// TraceRequest selects one memory anchor to expand.
type TraceRequest struct {
	Type                   string   `json:"type"`
	ID                     string   `json:"id"`
	MaxRelated             int      `json:"max_related,omitempty"`
	IncludeFragments       *bool    `json:"include_fragments,omitempty"`
	RelationshipID         string   `json:"relationship_id,omitempty"`
	IncludeEvidenceContent *bool    `json:"include_evidence_content,omitempty"`
	IncludeVerification    *bool    `json:"include_verification,omitempty"`
	IncludeTransitions     *bool    `json:"include_transitions,omitempty"`
	MaxDepth               int      `json:"max_depth,omitempty"`
	MaxEdges               int      `json:"max_edges,omitempty"`
	MaxChars               int      `json:"max_chars,omitempty"`
	PredicateKeys          []string `json:"predicate_keys,omitempty"`
	Topic                  string   `json:"topic,omitempty"`
	MinRelevance           *float64 `json:"min_relevance,omitempty"`
}

// TraceResult is a bounded graph lineage response for one anchor.
type TraceResult struct {
	Anchor              TraceAnchor        `json:"anchor"`
	PromotedFromClaim   *domain.Claim      `json:"promoted_from_claim,omitempty"`
	SupportingFragments []*domain.Fragment `json:"supporting_fragments,omitempty"`
	Related             []RelatedMemory    `json:"related,omitempty"`
	Edges               []TraceEdge        `json:"edges,omitempty"`
	MissingFragmentIDs  []string           `json:"missing_fragment_ids,omitempty"`
	Semantic            *SemanticTrace     `json:"semantic,omitempty"`
}

// TraceAnchor contains exactly one fact or claim.
type TraceAnchor struct {
	Type  string        `json:"type"`
	Fact  *domain.Fact  `json:"fact,omitempty"`
	Claim *domain.Claim `json:"claim,omitempty"`
}

// RelatedMemory is one bounded neighbor reached from the anchor.
type RelatedMemory struct {
	Type     string        `json:"type"`
	Relation string        `json:"relation"`
	Fact     *domain.Fact  `json:"fact,omitempty"`
	Claim    *domain.Claim `json:"claim,omitempty"`
}

// TraceEdge describes a relationship included in the trace.
type TraceEdge struct {
	FromType     string `json:"from_type"`
	FromID       string `json:"from_id"`
	Relationship string `json:"relationship"`
	ToType       string `json:"to_type"`
	ToID         string `json:"to_id"`
}

// AssembleRequest asks Dense-Mem to build a prompt-ready context block.
type AssembleRequest struct {
	Query           string     `json:"query"`
	Limit           int        `json:"limit,omitempty"`
	MaxChars        int        `json:"max_chars,omitempty"`
	IncludeEvidence *bool      `json:"include_evidence,omitempty"`
	ValidAt         *time.Time `json:"valid_at,omitempty"`
	KnownAt         *time.Time `json:"known_at,omitempty"`
}

// AssembleResult returns structured context and a bounded text block.
type AssembleResult struct {
	Query          string          `json:"query"`
	ContextBlock   string          `json:"context_block"`
	Items          []ContextItem   `json:"items"`
	Clarifications []Clarification `json:"clarifications,omitempty"`
	RelatedDreams  []*domain.Dream `json:"related_dreams,omitempty"`
	Truncated      bool            `json:"truncated"`
}

// Clarification tells the host LLM what evidence or decision is needed.
type Clarification struct {
	ID               string         `json:"id"`
	Type             string         `json:"type"`
	Question         string         `json:"question"`
	ClaimID          string         `json:"claim_id,omitempty"`
	Candidate        *MemoryTriple  `json:"candidate,omitempty"`
	ConflictingFacts []*domain.Fact `json:"conflicting_facts,omitempty"`
	Options          []string       `json:"options,omitempty"`
}

// MemoryTriple is a compact subject-predicate-object statement.
type MemoryTriple struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// ContextItem is one recall hit prepared for context.
type ContextItem struct {
	Type              string             `json:"type"`
	ID                string             `json:"id"`
	Score             float64            `json:"score,omitempty"`
	Fact              *domain.Fact       `json:"fact,omitempty"`
	Claim             *domain.Claim      `json:"claim,omitempty"`
	Fragment          *domain.Fragment   `json:"fragment,omitempty"`
	EvidenceFragments []*domain.Fragment `json:"evidence_fragments,omitempty"`
}
