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
	TraceRelationship(ctx context.Context, input repository.TraceRelationshipInput) (*repository.RelationshipTraceResult, error)
}

type semanticTraceService struct {
	store SemanticTraceStore
}

var _ Service = (*semanticTraceService)(nil)

func NewSemantic(store SemanticTraceStore) Service {
	return &semanticTraceService{store: store}
}

type SemanticTrace struct {
	Relationship           *repository.RelationshipTraceRecord            `json:"relationship,omitempty"`
	Observations           []repository.RelationshipObservationRecord     `json:"observations,omitempty"`
	EvidenceSupports       []repository.RelationshipEvidenceSupportRecord `json:"evidence_supports,omitempty"`
	SupportDecisionEvents  []repository.RelationshipSupportDecisionEvent  `json:"support_decision_events,omitempty"`
	EvidenceFragments      []repository.TraceEvidenceFragment             `json:"evidence_fragments,omitempty"`
	VerificationEvents     []repository.RelationshipVerificationEvent     `json:"verification_events,omitempty"`
	Transitions            []repository.RelationshipTransitionEvent       `json:"transitions,omitempty"`
	Conflicts              []repository.RelationshipConflictCaseRecord    `json:"conflicts,omitempty"`
	CrossProfileReferences []repository.RelationshipCrossReferenceRecord  `json:"cross_profile_references,omitempty"`
	IdentityCorrections    []repository.EntityCorrectionEventRecord       `json:"identity_corrections,omitempty"`
	SupersessionLineage    []repository.RelationshipTraceRecord           `json:"supersession_lineage,omitempty"`
	SearchDocuments        []repository.TraceSearchDocument               `json:"search_documents,omitempty"`
	EmbeddingJobs          []repository.TraceEmbeddingJob                 `json:"embedding_jobs,omitempty"`
	SemanticNodes          []repository.SemanticGraphNode                 `json:"semantic_nodes,omitempty"`
	SemanticEdges          []repository.SemanticGraphEdge                 `json:"semantic_edges,omitempty"`
	VisitedEntityIDs       []string                                       `json:"visited_entity_ids,omitempty"`
	StoppedReason          string                                         `json:"stopped_reason,omitempty"`
	Truncated              bool                                           `json:"truncated,omitempty"`
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

	trace, err := s.store.TraceRelationship(ctx, repository.TraceRelationshipInput{
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

func semanticTraceFromRepository(trace *repository.RelationshipTraceResult) *SemanticTrace {
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
		Conflicts:              trace.Conflicts,
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
