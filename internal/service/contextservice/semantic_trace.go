package contextservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

var ErrTraceAuthContext = errors.New("trace: authenticated actor context is required")

type SemanticTraceStore interface {
	TraceRelationship(ctx context.Context, input repository.V2TraceRelationshipInput) (*repository.V2RelationshipTraceResult, error)
}

type semanticTraceService struct {
	store SemanticTraceStore
}

var _ Service = (*semanticTraceService)(nil)

func NewSemantic(store SemanticTraceStore) Service {
	return &semanticTraceService{store: store}
}

type SemanticTrace struct {
	Relationship           *repository.V2RelationshipTraceRecord            `json:"relationship,omitempty"`
	Observations           []repository.V2RelationshipObservationRecord     `json:"observations,omitempty"`
	EvidenceSupports       []repository.V2RelationshipEvidenceSupportRecord `json:"evidence_supports,omitempty"`
	SupportDecisionEvents  []repository.V2RelationshipSupportDecisionEvent  `json:"support_decision_events,omitempty"`
	EvidenceFragments      []repository.V2TraceEvidenceFragment             `json:"evidence_fragments,omitempty"`
	VerificationEvents     []repository.V2RelationshipVerificationEvent     `json:"verification_events,omitempty"`
	Transitions            []repository.V2RelationshipTransitionEvent       `json:"transitions,omitempty"`
	CrossProfileReferences []repository.V2RelationshipCrossReferenceRecord  `json:"cross_profile_references,omitempty"`
	IdentityCorrections    []repository.V2EntityCorrectionEventRecord       `json:"identity_corrections,omitempty"`
	SupersessionLineage    []repository.V2RelationshipTraceRecord           `json:"supersession_lineage,omitempty"`
	SearchDocuments        []repository.V2TraceSearchDocument               `json:"search_documents,omitempty"`
	EmbeddingJobs          []repository.V2TraceEmbeddingJob                 `json:"embedding_jobs,omitempty"`
	SemanticNodes          []repository.V2SemanticGraphNode                 `json:"semantic_nodes,omitempty"`
	SemanticEdges          []repository.V2SemanticGraphEdge                 `json:"semantic_edges,omitempty"`
	VisitedEntityIDs       []string                                         `json:"visited_entity_ids,omitempty"`
	StoppedReason          string                                           `json:"stopped_reason,omitempty"`
	Truncated              bool                                             `json:"truncated,omitempty"`
}

func (s *semanticTraceService) Trace(ctx context.Context, _ string, req TraceRequest) (*TraceResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("trace: semantic store is required")
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil {
		return nil, ErrTraceAuthContext
	}
	relationshipID := strings.TrimSpace(req.RelationshipID)
	if relationshipID == "" {
		relationshipID = strings.TrimSpace(req.ID)
	}
	if relationshipID == "" {
		return nil, errors.New("trace: relationship_id is required")
	}

	trace, err := s.store.TraceRelationship(ctx, repository.V2TraceRelationshipInput{
		TeamID:                  actor.TeamID.String(),
		RelationshipID:          relationshipID,
		IncludeEvidenceContent:  req.IncludeEvidenceContent,
		IncludeVerification:     req.IncludeVerification,
		IncludeTransitions:      req.IncludeTransitions,
		MaxDepth:                req.MaxDepth,
		MaxEdges:                req.MaxEdges,
		MaxFragmentContentRunes: req.MaxChars,
		PredicateKeys:           req.PredicateKeys,
		Topic:                   req.Topic,
		MinRelevance:            req.MinRelevance,
	})
	if err != nil {
		return nil, fmt.Errorf("trace: %w", err)
	}
	return &TraceResult{Semantic: semanticTraceFromRepository(trace)}, nil
}

func (s *semanticTraceService) Assemble(context.Context, string, AssembleRequest) (*AssembleResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("context assemble: semantic store is required")
	}
	return nil, errors.New("context assemble: not implemented")
}

func semanticTraceFromRepository(trace *repository.V2RelationshipTraceResult) *SemanticTrace {
	if trace == nil {
		return &SemanticTrace{}
	}
	return &SemanticTrace{
		Relationship:           trace.Relationship,
		Observations:           trace.Observations,
		EvidenceSupports:       trace.EvidenceSupports,
		SupportDecisionEvents:  trace.SupportDecisionEvents,
		EvidenceFragments:      trace.EvidenceFragments,
		VerificationEvents:     trace.VerificationEvents,
		Transitions:            trace.Transitions,
		CrossProfileReferences: trace.CrossProfileReferences,
		IdentityCorrections:    trace.IdentityCorrections,
		SupersessionLineage:    trace.SupersessionLineage,
		SearchDocuments:        trace.SearchDocuments,
		EmbeddingJobs:          trace.EmbeddingJobs,
		SemanticNodes:          trace.SemanticNodes,
		SemanticEdges:          trace.SemanticEdges,
		VisitedEntityIDs:       trace.VisitedEntityIDs,
		StoppedReason:          trace.StoppedReason,
		Truncated:              trace.Truncated,
	}
}
