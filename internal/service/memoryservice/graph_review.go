package memoryservice

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/placementreview"
)

const (
	placementPipelineVersion = "semantic-edge-v2"
	maxProposalEntities      = 100
	maxProposalRelationships = 200
)

func validateProposal(evidence []domain.MemoryEvidence, proposal domain.MemoryProposal) error {
	if len(proposal.Entities) == 0 {
		return errors.New("memory service: proposal.entities is required")
	}
	if len(proposal.Relationships) == 0 {
		return errors.New("memory service: proposal.relationships is required")
	}
	if len(proposal.Entities) > maxProposalEntities {
		return fmt.Errorf("memory service: proposal.entities exceeds %d", maxProposalEntities)
	}
	if len(proposal.Relationships) > maxProposalRelationships {
		return fmt.Errorf("memory service: proposal.relationships exceeds %d", maxProposalRelationships)
	}

	entityRefs := make(map[string]struct{}, len(proposal.Entities))
	for i, entity := range proposal.Entities {
		ref := strings.TrimSpace(entity.Ref)
		if ref == "" || strings.TrimSpace(entity.Name) == "" || strings.TrimSpace(entity.Type) == "" {
			return fmt.Errorf("memory service: proposal.entities[%d] requires ref, name, and type", i)
		}
		if _, exists := entityRefs[ref]; exists {
			return fmt.Errorf("memory service: duplicate entity ref %q", ref)
		}
		entityRefs[ref] = struct{}{}
	}

	proposalIDs := make(map[string]struct{}, len(proposal.Relationships))
	for i := range proposal.Relationships {
		if err := validateRelationshipProposal(i, evidence, entityRefs, proposal.Relationships[i]); err != nil {
			return err
		}
		proposalID := strings.TrimSpace(proposal.Relationships[i].ProposalID)
		if _, exists := proposalIDs[proposalID]; exists {
			return fmt.Errorf("memory service: duplicate proposal_id %q", proposalID)
		}
		proposalIDs[proposalID] = struct{}{}
	}
	return nil
}

func validateRelationshipProposal(index int, evidence []domain.MemoryEvidence, entityRefs map[string]struct{}, proposal domain.MemoryRelationshipProposal) error {
	prefix := fmt.Sprintf("memory service: proposal.relationships[%d]", index)
	if strings.TrimSpace(proposal.ProposalID) == "" || strings.TrimSpace(proposal.Predicate) == "" {
		return fmt.Errorf("%s requires proposal_id and predicate", prefix)
	}
	if _, exists := entityRefs[strings.TrimSpace(proposal.SubjectRef)]; !exists {
		return fmt.Errorf("%s subject_ref is unknown", prefix)
	}
	_, objectEntityExists := entityRefs[strings.TrimSpace(proposal.ObjectRef)]
	if objectEntityExists == (proposal.ObjectValue != nil) {
		return fmt.Errorf("%s requires exactly one object_ref or object_value", prefix)
	}
	if proposal.ObjectValue != nil {
		if !proposal.ObjectValue.Type.IsValid() || strings.TrimSpace(proposal.ObjectValue.Value) == "" {
			return fmt.Errorf("%s object_value is invalid", prefix)
		}
	}
	if !proposal.PolicyFamily.IsValid() {
		return fmt.Errorf("%s policy_family is invalid", prefix)
	}
	if !proposal.Polarity.IsValid() || !proposal.Modality.IsValid() {
		return fmt.Errorf("%s polarity or modality is invalid", prefix)
	}
	if proposal.ValidFrom != nil && proposal.ValidTo != nil && !proposal.ValidTo.After(*proposal.ValidFrom) {
		return fmt.Errorf("%s valid_to must be after valid_from", prefix)
	}
	if len(proposal.Evidence) == 0 {
		return fmt.Errorf("%s evidence is required", prefix)
	}
	seenEvidence := map[int]struct{}{}
	for _, ref := range proposal.Evidence {
		if ref.EvidenceIndex < 0 || ref.EvidenceIndex >= len(evidence) {
			return fmt.Errorf("%s evidence_index %d is invalid", prefix, ref.EvidenceIndex)
		}
		contentLength := utf8.RuneCountInString(evidence[ref.EvidenceIndex].Content)
		if ref.Start < 0 || ref.End <= ref.Start || ref.End > contentLength {
			return fmt.Errorf("%s evidence span is invalid", prefix)
		}
		if _, exists := seenEvidence[ref.EvidenceIndex]; exists {
			return fmt.Errorf("%s repeats evidence_index %d", prefix, ref.EvidenceIndex)
		}
		seenEvidence[ref.EvidenceIndex] = struct{}{}
	}
	return nil
}

func normalizeReviewedResult(evidence []domain.MemoryEvidence, result placementreview.Result) (placementreview.Result, error) {
	proposal := domain.MemoryProposal{
		Entities:      make([]domain.MemoryEntityProposal, 0, len(result.Entities)),
		Relationships: make([]domain.MemoryRelationshipProposal, 0, len(result.Relationships)),
	}
	for i := range result.Entities {
		entity := &result.Entities[i]
		entity.Proposal.Ref = strings.TrimSpace(entity.Proposal.Ref)
		entity.Proposal.Name = strings.TrimSpace(entity.Proposal.Name)
		entity.Proposal.Type = strings.TrimSpace(entity.Proposal.Type)
		if !entity.ResolutionStatus.IsValid() {
			return placementreview.Result{}, fmt.Errorf("memory service: reviewer entity[%d] resolution_status is invalid", i)
		}
		if entity.ResolutionConf < 0 || entity.ResolutionConf > 1 {
			return placementreview.Result{}, fmt.Errorf("memory service: reviewer entity[%d] resolution_conf is invalid", i)
		}
		proposal.Entities = append(proposal.Entities, entity.Proposal)
	}
	for i := range result.Relationships {
		relationship := &result.Relationships[i]
		relationship.Proposal.Predicate = strings.TrimSpace(relationship.Proposal.Predicate)
		relationship.Rationale = strings.TrimSpace(relationship.Rationale)
		if !relationship.Atomic {
			return placementreview.Result{}, fmt.Errorf("memory service: reviewer relationship[%d] is not atomic", i)
		}
		if relationship.ExtractConf < 0 || relationship.ExtractConf > 1 {
			return placementreview.Result{}, fmt.Errorf("memory service: reviewer relationship[%d] extract_conf is invalid", i)
		}
		proposal.Relationships = append(proposal.Relationships, relationship.Proposal)
	}
	if err := validateProposal(evidence, proposal); err != nil {
		return placementreview.Result{}, fmt.Errorf("memory service: reviewer returned invalid proposal: %w", err)
	}
	sort.SliceStable(result.Relationships, func(i, j int) bool {
		return result.Relationships[i].Proposal.ProposalID < result.Relationships[j].Proposal.ProposalID
	})
	return result, nil
}
