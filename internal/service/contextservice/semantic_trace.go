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

var (
	ErrTraceAuthContext            = errors.New("trace: authenticated actor context is required")
	ErrTraceRelationshipNotFound   = errors.New("not_found: relationship not found")
	ErrTraceRepositoryTeamMismatch = errors.New("trace: repository returned a mismatched team")
)

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
	Relationship                  *repository.RelationshipTraceRecord            `json:"relationship,omitempty"`
	Observations                  []repository.RelationshipObservationRecord     `json:"observations,omitempty"`
	EvidenceSupports              []repository.RelationshipEvidenceSupportRecord `json:"evidence_supports,omitempty"`
	EvidenceSupportDecisionEvents []repository.RelationshipSupportDecisionEvent  `json:"evidence_support_decision_events,omitempty"`
	Evidence                      []repository.TraceEvidenceFragment             `json:"evidence,omitempty"`
	EvidenceLifecycleEvents       []repository.TraceEvidenceLifecycleEvent       `json:"evidence_lifecycle_events,omitempty"`
	VerificationEvents            []repository.RelationshipVerificationEvent     `json:"verification_events,omitempty"`
	Transitions                   []repository.RelationshipTransitionEvent       `json:"transitions,omitempty"`
	Conflicts                     []repository.RelationshipConflictCaseRecord    `json:"conflicts,omitempty"`
	CrossProfileReferences        []repository.RelationshipCrossReferenceRecord  `json:"cross_profile_references,omitempty"`
	IdentityCorrections           []repository.EntityCorrectionEventRecord       `json:"identity_corrections,omitempty"`
	SupersessionLineage           []repository.RelationshipTraceRecord           `json:"supersession_lineage,omitempty"`
	SearchDocuments               []repository.TraceSearchDocument               `json:"search_documents,omitempty"`
	SemanticNodes                 []repository.SemanticGraphNode                 `json:"semantic_nodes,omitempty"`
	SemanticEdges                 []repository.SemanticGraphEdge                 `json:"semantic_edges,omitempty"`
	VisitedEntityIDs              []string                                       `json:"visited_entity_ids,omitempty"`
	StoppedReason                 string                                         `json:"stopped_reason,omitempty"`
	Truncated                     bool                                           `json:"truncated,omitempty"`
}

func (s *semanticTraceService) Trace(ctx context.Context, _ string, req TraceRequest) (*TraceResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("trace: semantic store is required")
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil {
		return nil, ErrTraceAuthContext
	}
	relationshipID := strings.TrimSpace(req.RelationshipID)
	if relationshipID == "" {
		return nil, errors.New("trace: relationship_id is required")
	}

	trace, err := s.store.TraceRelationship(ctx, repository.TraceRelationshipInput{
		TeamID:                 actor.TeamID.String(),
		RelationshipID:         relationshipID,
		IncludeEvidenceContent: req.IncludeEvidenceContent,
		IncludeVerification:    req.IncludeVerification,
		IncludeTransitions:     req.IncludeTransitions,
		MaxDepth:               req.MaxDepth,
		MaxEdges:               req.MaxEdges,
		PredicateKeys:          req.PredicateKeys,
		Topic:                  req.Topic,
		MinRelevance:           req.MinRelevance,
	})
	if err != nil {
		if errors.Is(err, repository.ErrTraceRelationshipNotFound) {
			return nil, ErrTraceRelationshipNotFound
		}
		return nil, fmt.Errorf("trace: %w", err)
	}
	if err := validateTraceConflictTeams(trace, actor.TeamID.String()); err != nil {
		return nil, err
	}
	return &TraceResult{Semantic: semanticTraceFromRepository(trace)}, nil
}

func validateTraceConflictTeams(trace *repository.RelationshipTraceResult, expectedTeamID string) error {
	if trace == nil {
		return nil
	}
	for _, conflict := range trace.Conflicts {
		if conflict.TeamID != expectedTeamID {
			return ErrTraceRepositoryTeamMismatch
		}
	}
	return nil
}

func semanticTraceFromRepository(trace *repository.RelationshipTraceResult) *SemanticTrace {
	if trace == nil {
		return &SemanticTrace{}
	}
	return &SemanticTrace{
		Relationship:                  trace.Relationship,
		Observations:                  trace.Observations,
		EvidenceSupports:              trace.EvidenceSupports,
		EvidenceSupportDecisionEvents: trace.SupportDecisionEvents,
		Evidence:                      trace.EvidenceFragments,
		EvidenceLifecycleEvents:       trace.EvidenceLifecycleEvents,
		VerificationEvents:            trace.VerificationEvents,
		Transitions:                   trace.Transitions,
		Conflicts:                     trace.Conflicts,
		CrossProfileReferences:        trace.CrossProfileReferences,
		IdentityCorrections:           trace.IdentityCorrections,
		SupersessionLineage:           trace.SupersessionLineage,
		SearchDocuments:               trace.SearchDocuments,
		SemanticNodes:                 trace.SemanticNodes,
		SemanticEdges:                 trace.SemanticEdges,
		VisitedEntityIDs:              trace.VisitedEntityIDs,
		StoppedReason:                 trace.StoppedReason,
		Truncated:                     trace.Truncated,
	}
}
