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

var ErrV2TraceAuthContext = errors.New("v2 trace: authenticated actor context is required")

type V2SemanticTraceStore interface {
	TraceRelationship(ctx context.Context, input repository.V2TraceRelationshipInput) (*repository.V2RelationshipTraceResult, error)
}

type v2SemanticTraceService struct {
	store V2SemanticTraceStore
}

var _ Service = (*v2SemanticTraceService)(nil)

func NewV2Semantic(store V2SemanticTraceStore) Service {
	return &v2SemanticTraceService{store: store}
}

type V2SemanticTrace struct {
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

func (s *v2SemanticTraceService) Trace(ctx context.Context, _ string, req TraceRequest) (*TraceResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("v2 trace: semantic store is required")
	}
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil {
		return nil, ErrV2TraceAuthContext
	}
	relationshipID := strings.TrimSpace(req.RelationshipID)
	if relationshipID == "" {
		relationshipID = strings.TrimSpace(req.ID)
	}
	if relationshipID == "" {
		return nil, errors.New("v2 trace: relationship_id is required")
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
	})
	if err != nil {
		return nil, fmt.Errorf("v2 trace: %w", err)
	}
	return &TraceResult{V2Semantic: v2SemanticTraceFromRepository(trace)}, nil
}

func (s *v2SemanticTraceService) Assemble(context.Context, string, AssembleRequest) (*AssembleResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("v2 context assemble: semantic store is required")
	}
	return nil, errors.New("v2 context assemble: not implemented")
}

func v2SemanticTraceFromRepository(trace *repository.V2RelationshipTraceResult) *V2SemanticTrace {
	if trace == nil {
		return &V2SemanticTrace{}
	}
	return &V2SemanticTrace{
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
