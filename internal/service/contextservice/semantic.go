package contextservice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

type SemanticTraceStore interface {
	TraceRelationship(ctx context.Context, teamID string, relationshipID string) (*domain.SemanticTraceResult, error)
}

type semanticContextService struct {
	store  SemanticTraceStore
	recall recallservice.RecallService
}

var _ Service = (*semanticContextService)(nil)

func NewSemantic(store SemanticTraceStore, recall recallservice.RecallService) Service {
	return &semanticContextService{store: store, recall: recall}
}

func (s *semanticContextService) Trace(ctx context.Context, profileID string, req TraceRequest) (*TraceResult, error) {
	if s.store == nil {
		return nil, errors.New("semantic context trace: store is required")
	}
	teamID := strings.TrimSpace(profileID)
	if teamID == "" {
		return nil, errors.New("semantic context trace: team id is required")
	}
	relationshipID := strings.TrimSpace(req.RelationshipID)
	if relationshipID == "" && strings.TrimSpace(req.Type) == "relationship" {
		relationshipID = strings.TrimSpace(req.ID)
	}
	if relationshipID == "" {
		return nil, errors.New("semantic context trace: relationship_id is required")
	}
	trace, err := s.store.TraceRelationship(ctx, teamID, relationshipID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("semantic context trace: relationship not found")
		}
		return nil, err
	}
	evidence := append([]domain.SemanticEvidenceFragment(nil), trace.Evidence...)
	includeContent := true
	if req.IncludeEvidenceContent != nil {
		includeContent = *req.IncludeEvidenceContent
	} else if req.IncludeFragments != nil {
		includeContent = *req.IncludeFragments
	}
	if !includeContent {
		for i := range evidence {
			evidence[i].Content = ""
		}
	}
	return &TraceResult{
		Anchor: TraceAnchor{
			Type:         "relationship",
			Relationship: trace.Relationship,
		},
		SemanticEvidence: evidence,
		SemanticSupports: append([]domain.SemanticRelationshipSupport(nil), trace.Supports...),
	}, nil
}

func (s *semanticContextService) Assemble(ctx context.Context, profileID string, req AssembleRequest) (*AssembleResult, error) {
	if s.recall == nil {
		return nil, errors.New("semantic context assemble: recall service is required")
	}
	teamID := strings.TrimSpace(profileID)
	if teamID == "" {
		return nil, errors.New("semantic context assemble: team id is required")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, errors.New("semantic context assemble: query is required")
	}
	limit := clampInt(req.Limit, defaultContextLimit, maxContextLimit)
	maxChars := clampRange(req.MaxChars, defaultMaxChars, minMaxChars, maxMaxChars)
	includeEvidence := false
	if req.IncludeEvidence != nil {
		includeEvidence = *req.IncludeEvidence
	}
	hits, err := s.recall.Recall(ctx, teamID, recallservice.RecallRequest{
		Query:           query,
		Limit:           limit,
		ValidAt:         req.ValidAt,
		KnownAt:         req.KnownAt,
		IncludeEvidence: includeEvidence,
	})
	if err != nil {
		return nil, err
	}
	items := make([]ContextItem, 0, len(hits))
	for _, hit := range hits {
		switch {
		case hit.Relationship != nil:
			item := ContextItem{
				Type:         "relationship",
				ID:           hit.Relationship.RelationshipID,
				Score:        hit.Score,
				Relationship: hit.Relationship,
				Supports:     append([]domain.SemanticRelationshipSupport(nil), hit.Supports...),
			}
			if includeEvidence && s.store != nil {
				trace, err := s.store.TraceRelationship(ctx, teamID, hit.Relationship.RelationshipID)
				if err != nil {
					return nil, err
				}
				item.Supports = append([]domain.SemanticRelationshipSupport(nil), trace.Supports...)
				item.SemanticEvidence = append([]domain.SemanticEvidenceFragment(nil), trace.Evidence...)
			}
			items = append(items, item)
		case hit.Evidence != nil:
			items = append(items, ContextItem{
				Type:     "evidence",
				ID:       hit.Evidence.FragmentID,
				Score:    hit.Score,
				Evidence: hit.Evidence,
			})
		}
	}
	block, truncated := buildSemanticContextBlock(query, items, maxChars)
	return &AssembleResult{
		Query:        query,
		ContextBlock: block,
		Items:        items,
		Truncated:    truncated,
	}, nil
}

func buildSemanticContextBlock(query string, items []ContextItem, maxChars int) (string, bool) {
	var b strings.Builder
	truncated := false
	add := func(line string) bool {
		if b.Len()+len(line)+1 > maxChars {
			truncated = true
			return false
		}
		b.WriteString(line)
		b.WriteByte('\n')
		return true
	}
	add("Dense-Mem semantic context")
	add("Query: " + singleLine(query, 300))
	for _, item := range items {
		switch item.Type {
		case "relationship":
			rel := item.Relationship
			if rel == nil {
				continue
			}
			object := firstNonEmpty(rel.ObjectValue, rel.ObjectEntityID)
			if !add(fmt.Sprintf("- [relationship:%s] %s %s %s (%s, score %.4f)", rel.RelationshipID, rel.SubjectEntityID, rel.Predicate, singleLine(object, 260), rel.Tier, item.Score)) {
				return b.String(), truncated
			}
			for _, evidence := range item.SemanticEvidence {
				if !add(fmt.Sprintf("  support [evidence:%s] %s", evidence.FragmentID, singleLine(evidence.Content, 500))) {
					return b.String(), truncated
				}
			}
		case "evidence":
			evidence := item.Evidence
			if evidence == nil {
				continue
			}
			if !add(fmt.Sprintf("- [evidence:%s] %s", evidence.FragmentID, singleLine(evidence.Content, 700))) {
				return b.String(), truncated
			}
		}
	}
	return b.String(), truncated
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
