package skillpackservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

// ErrMemoryPackRelationshipNotActive identifies a caller-selected Relationship
// that was found but is no longer eligible for export.
var ErrMemoryPackRelationshipNotActive = errors.New("memory pack export relationship is not active")

var _ MemoryPackService = (*memoryPackService)(nil)

func NewMemoryPackService(deps MemoryPackDependencies) MemoryPackService {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &memoryPackService{deps: deps, now: now}
}

func (s *memoryPackService) Export(ctx context.Context, req ExportRequest) (*ExportResult, error) {
	actor, err := memoryPackActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.Semantic == nil {
		return nil, errors.New("memory pack export: semantic reader is required")
	}
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("memory pack export: name is required")
	}
	relationshipIDs := uniqueStrings(req.RelationshipIDs)
	if len(relationshipIDs) == 0 {
		return nil, errors.New("memory pack export: relationship_ids is required")
	}
	includeSupport := req.IncludeSupport == nil || *req.IncludeSupport
	now := s.now().UTC()
	artifact := MemoryPackArtifact{
		Format:      MemoryPackFormat,
		PackID:      "pack_" + memoryPackShortHash(strings.Join(relationshipIDs, "\x00")+now.Format(time.RFC3339Nano)),
		Name:        strings.TrimSpace(req.Name),
		Description: strings.TrimSpace(req.Description),
		CreatedAt:   now.Format(time.RFC3339Nano),
		Source: MemoryPackSource{
			TeamID:     actor.TeamID.String(),
			ExportedBy: actor.OwnerID.String(),
		},
		Relationships: []MemoryPackRelationship{},
	}
	evidence := map[string]MemoryPackEvidence{}
	supports := []MemoryPackEvidenceSupport{}
	for _, relationshipID := range relationshipIDs {
		trace, err := s.deps.Semantic.TraceRelationship(ctx, repository.TraceRelationshipInput{
			TeamID:                  actor.TeamID.String(),
			RelationshipID:          relationshipID,
			IncludeEvidenceContent:  boolPtr(includeSupport),
			MaxEvents:               100,
			MaxFragmentContentRunes: 8000,
		})
		if err != nil {
			if errors.Is(err, repository.ErrTraceRelationshipNotFound) {
				return nil, repository.ErrTraceRelationshipNotFound
			}
			return nil, err
		}
		if trace.Relationship == nil {
			return nil, fmt.Errorf("memory pack export: relationship %s not found", relationshipID)
		}
		if trace.Relationship.Status != string(domain.RelationshipStatusActive) {
			return nil, fmt.Errorf("%w: %s", ErrMemoryPackRelationshipNotActive, relationshipID)
		}
		item := memoryPackRelationshipFromTrace(trace.Relationship)
		if includeSupport {
			for _, support := range trace.EvidenceSupports {
				if support.FragmentID != "" {
					item.SupportEvidenceIDs = append(item.SupportEvidenceIDs, support.FragmentID)
				}
				supports = append(supports, MemoryPackEvidenceSupport{
					RelationshipItemID: item.ItemID,
					EvidenceID:         support.FragmentID,
					Quote:              support.Quote,
					SpanStart:          support.SpanStart,
					SpanEnd:            support.SpanEnd,
					Metadata:           MemoryPackCopyMap(support.Metadata),
				})
			}
			for _, fragment := range trace.EvidenceFragments {
				if fragment.FragmentID == "" {
					continue
				}
				evidence[fragment.FragmentID] = MemoryPackEvidence{
					EvidenceID:       fragment.FragmentID,
					Content:          fragment.Content,
					ContentHash:      fragment.ContentHash,
					SourceType:       fragment.SourceType,
					Authority:        fragment.Authority,
					SourceRef:        fragment.SourceRef,
					SourceKey:        fragment.SourceKey,
					SourceRevisionID: fragment.SourceRevisionID,
					Labels:           append([]string(nil), fragment.Labels...),
					Metadata:         MemoryPackCopyMap(fragment.Metadata),
				}
			}
		}
		item.SupportEvidenceIDs = uniqueStrings(item.SupportEvidenceIDs)
		artifact.Relationships = append(artifact.Relationships, item)
	}
	if includeSupport {
		for _, id := range MemoryPackSortedEvidenceIDs(evidence) {
			artifact.Evidence = append(artifact.Evidence, evidence[id])
		}
		artifact.EvidenceSupports = supports
	}
	canonical, hash, err := canonicalMemoryPackArtifact(artifact)
	if err != nil {
		return nil, err
	}
	artifact.ContentSHA256 = hash
	canonicalWithHash, err := marshalMemoryPackArtifact(artifact)
	if err != nil {
		return nil, err
	}
	return &ExportResult{
		Artifact:      artifact,
		CanonicalJSON: string(canonicalWithHash),
		SHA256:        hash,
		ItemCount:     len(artifact.Relationships),
		Filename:      skillPackFilename(artifact.Name),
		ContentType:   "application/json",
		Omissions:     MemoryPackSupportOmissions(includeSupport, canonical),
	}, nil
}
