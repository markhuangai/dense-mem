package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func semanticPrincipal(ctx context.Context, fallbackProfileID string) (teamID, teamName, ownerProfileID, ownerProfileName string) {
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok {
		if actor.TeamID != uuid.Nil {
			teamID = actor.TeamID.String()
			teamName = actor.TeamName
		}
		if actor.ProfileID != uuid.Nil {
			ownerProfileID = actor.ProfileID.String()
			ownerProfileName = actor.ProfileName
		}
	}
	if teamID == "" {
		teamID = strings.TrimSpace(fallbackProfileID)
	}
	if ownerProfileID == "" {
		ownerProfileID = teamID
	}
	return teamID, teamName, ownerProfileID, ownerProfileName
}

func annotateSemanticReviewRelationships(review semanticReviewResult) []repository.SemanticRelationshipInput {
	relationships := append([]repository.SemanticRelationshipInput(nil), review.Relationships...)
	for i := range relationships {
		relationships[i].ExtractionModel = strings.TrimSpace(review.Model)
		relationships[i].ExtractionRawJSON = strings.TrimSpace(review.RawJSON)
	}
	return relationships
}

func semanticEvidenceInputs(input []EvidenceInput) []repository.SemanticEvidenceInput {
	out := make([]repository.SemanticEvidenceInput, 0, len(input))
	for _, item := range input {
		sourceType := normalizeSourceType(item.SourceType)
		authority := normalizeAuthority(item.Authority)
		out = append(out, repository.SemanticEvidenceInput{
			Content:        strings.TrimSpace(item.Content),
			Source:         item.Source,
			SourceDocID:    semanticSourceDocID(item.Metadata, item.Source),
			SourceGroup:    strings.TrimSpace(item.SourceGroup),
			SourceType:     sourceType,
			Authority:      authority,
			Labels:         append([]string(nil), item.Labels...),
			Metadata:       item.Metadata,
			IdempotencyKey: item.IdempotencyKey,
		})
	}
	return out
}

func semanticEvidenceInputsWithSecurity(input []domain.MemoryEvidence, assessments []domain.EvidenceSecurityAssessment) []repository.SemanticEvidenceInput {
	out := semanticEvidenceInputsFromMemory(input)
	for i := range out {
		if i >= len(assessments) {
			continue
		}
		assessment := assessments[i]
		out[i].SecurityDecision = assessment.Decision
		out[i].SecurityAssessment = &assessment
	}
	return out
}

func semanticEvidenceInputsFromMemory(input []domain.MemoryEvidence) []repository.SemanticEvidenceInput {
	out := make([]repository.SemanticEvidenceInput, 0, len(input))
	for _, item := range input {
		sourceType := normalizeSourceType(item.SourceType)
		authority := normalizeAuthority(item.Authority)
		out = append(out, repository.SemanticEvidenceInput{
			Content:        strings.TrimSpace(item.Content),
			Source:         item.Source,
			SourceDocID:    semanticSourceDocID(item.Metadata, item.Source),
			SourceGroup:    strings.TrimSpace(item.SourceGroup),
			SourceType:     sourceType,
			Authority:      authority,
			Labels:         append([]string(nil), item.Labels...),
			Metadata:       item.Metadata,
			IdempotencyKey: item.IdempotencyKey,
		})
	}
	return out
}

func normalizeSourceType(value string) domain.SourceType {
	sourceType, err := parseSourceType(value)
	if err != nil {
		return domain.SourceTypeConversation
	}
	return sourceType
}

func normalizeAuthority(value string) domain.Authority {
	authority, err := parseAuthority(value)
	if err != nil {
		return domain.AuthorityPrimary
	}
	return authority
}

func parseSourceType(value string) (domain.SourceType, error) {
	sourceType := domain.SourceType(strings.ToLower(strings.TrimSpace(value)))
	if sourceType == "" {
		return domain.SourceTypeConversation, nil
	}
	if !sourceType.IsValid() {
		return "", errors.New("invalid source_type")
	}
	return sourceType, nil
}

func parseAuthority(value string) (domain.Authority, error) {
	authority := domain.Authority(strings.ToLower(strings.TrimSpace(value)))
	if authority == "" {
		return domain.AuthorityPrimary, nil
	}
	if !authority.IsValid() {
		return "", errors.New("invalid authority")
	}
	return authority, nil
}

func semanticSourceDocID(metadata map[string]any, source string) string {
	for _, key := range []string{"source_doc_id", "sourceDocID", "doc_id", "document_id"} {
		if value, ok := metadata[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
				return text
			}
		}
	}
	return strings.TrimSpace(source)
}

func semanticPlacementCategory(tier domain.SemanticRelationshipTier) domain.MemoryPlacementCategory {
	switch tier {
	case domain.SemanticTierFact:
		return domain.MemoryPlacementPromotedFact
	case domain.SemanticTierValidatedClaim:
		return domain.MemoryPlacementValidatedClaim
	default:
		return domain.MemoryPlacementCandidateClaim
	}
}

func semanticPlacementRelationshipCategory(relationship domain.SemanticRelationship) domain.MemoryPlacementCategory {
	switch relationship.Status {
	case domain.SemanticStatusPendingEvidence:
		return domain.MemoryPlacementNeedsEvidence
	case domain.SemanticStatusRejected:
		return domain.MemoryPlacementRejectedFalse
	case domain.SemanticStatusNeedsReview:
		return domain.MemoryPlacementCandidateClaim
	default:
		return semanticPlacementCategory(relationship.Tier)
	}
}

func semanticPlacementRelationshipReason(relationship domain.SemanticRelationship) string {
	switch relationship.Status {
	case domain.SemanticStatusPendingEvidence:
		return "Dense-Mem retained this relationship as a candidate pending stronger evidence."
	case domain.SemanticStatusRejected:
		return "Dense-Mem retained the rejected relationship for audit; it is excluded from recall."
	case domain.SemanticStatusNeedsReview:
		return "Dense-Mem retained this relationship for review; it is excluded from recall."
	default:
		return "Dense-Mem placed this evidence as an active semantic relationship."
	}
}

func semanticPlacementRelationshipOutcomeCategory(relationship domain.SemanticRelationship) string {
	switch relationship.Status {
	case domain.SemanticStatusPendingEvidence:
		return "relationship_pending_evidence"
	case domain.SemanticStatusNeedsReview:
		return "relationship_needs_review"
	case domain.SemanticStatusRejected:
		return "relationship_rejected"
	default:
		switch relationship.Tier {
		case domain.SemanticTierFact:
			return "relationship_fact"
		default:
			return "relationship_validated_claim"
		}
	}
}

func semanticPlacementObservationOutcomeCategory(input repository.SemanticRelationshipInput) string {
	category := strings.TrimSpace(input.PlacementOutcomeCategory)
	switch category {
	case "identity_needs_review", "predicate_needs_review", "relationship_needs_review":
		return category
	default:
		return "relationship_needs_review"
	}
}

func semanticPlacementObservationOutcomeReason(input repository.SemanticRelationshipInput) string {
	if reason := strings.TrimSpace(input.PlacementReviewMessage); reason != "" {
		return reason
	}
	return "Dense-Mem preserved the observation for review; no canonical relationship was created."
}

func semanticPlacementReviewTask(category, reason string) *domain.MemoryPlacementReviewTask {
	switch category {
	case "identity_needs_review":
		return &domain.MemoryPlacementReviewTask{
			Type:    category,
			Message: reason,
		}
	case "predicate_needs_review":
		return &domain.MemoryPlacementReviewTask{
			Type:    category,
			Message: reason,
		}
	case "relationship_needs_review":
		return &domain.MemoryPlacementReviewTask{
			Type:    category,
			Message: reason,
		}
	default:
		return nil
	}
}

func semanticPlacementRelationshipOutcomes(relationships []repository.SemanticRelationshipInput, stored *repository.SemanticRememberResult) map[int][]domain.MemoryPlacementRelationshipOutcome {
	out := map[int][]domain.MemoryPlacementRelationshipOutcome{}
	storedByInput := map[int]domain.SemanticRelationship{}
	if stored != nil {
		for i, relationship := range stored.Relationships {
			if i >= len(stored.RelationshipInputIndexes) {
				continue
			}
			inputIndex := stored.RelationshipInputIndexes[i]
			storedByInput[inputIndex] = relationship
		}
	}
	for inputIndex, relationshipInput := range relationships {
		evidenceIndex := relationshipInput.EvidenceIndex
		if evidenceIndex < 0 {
			continue
		}
		if relationship, ok := storedByInput[inputIndex]; ok {
			out[evidenceIndex] = append(out[evidenceIndex], domain.MemoryPlacementRelationshipOutcome{
				RelationshipID:     relationship.RelationshipID,
				OwnerProfileID:     relationship.OwnerProfileID,
				Tier:               relationship.Tier,
				RelationshipStatus: relationship.Status,
				Category:           semanticPlacementRelationshipOutcomeCategory(relationship),
				Reason:             semanticPlacementRelationshipReason(relationship),
			})
			continue
		}
		category := semanticPlacementObservationOutcomeCategory(relationshipInput)
		reason := semanticPlacementObservationOutcomeReason(relationshipInput)
		out[evidenceIndex] = append(out[evidenceIndex], domain.MemoryPlacementRelationshipOutcome{
			Category:   category,
			Reason:     reason,
			ReviewTask: semanticPlacementReviewTask(category, reason),
		})
	}
	return out
}

func looksFalse(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	for _, marker := range []string{
		"false memory",
		"not true",
		"incorrect",
		"contradicts",
		"contradicted",
		"reject this memory",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
