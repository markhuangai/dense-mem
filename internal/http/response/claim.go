package response

import (
	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
)

// ToClaimResponse converts a domain Claim to a DTO ClaimResponse.
// duplicateOf is reserved for future deduplication signals; pass "" when not applicable.
// Returns nil when c is nil.
func ToClaimResponse(c *domain.Claim, _ string) *dto.ClaimResponse {
	if c == nil {
		return nil
	}

	return &dto.ClaimResponse{
		ClaimID:              c.ClaimID,
		ProfileID:            c.ProfileID,
		CreatedByProfileID:   c.CreatedByProfileID,
		CreatedByProfileName: c.CreatedByProfileName,
		Subject:              c.Subject,
		Predicate:            c.Predicate,
		Object:               c.Object,
		Modality:             string(c.Modality),
		Polarity:             string(c.Polarity),
		Speaker:              c.Speaker,
		SpanStart:            c.SpanStart,
		SpanEnd:              c.SpanEnd,
		ValidFrom:            c.ValidFrom,
		ValidTo:              c.ValidTo,
		RecordedAt:           c.RecordedAt,
		RecordedTo:           c.RecordedTo,
		ExtractConf:          c.ExtractConf,
		ResolutionConf:       c.ResolutionConf,
		SourceQuality:        c.SourceQuality,
		EntailmentVerdict:    string(c.EntailmentVerdict),
		Status:               string(c.Status),
		ExtractionModel:      c.ExtractionModel,
		ExtractionVersion:    c.ExtractionVersion,
		VerifierModel:        c.VerifierModel,
		PipelineRunID:        c.PipelineRunID,
		ContentHash:          c.ContentHash,
		IdempotencyKey:       c.IdempotencyKey,
		Classification:       c.Classification,
		SupportedBy:          c.SupportedBy,
		Evidence:             toEvidenceResponses(c.Evidence),
	}
}

// ToListClaimsResponse converts a slice of domain Claims to a paginated DTO response.
// nextCursor is the opaque pagination token returned to the caller; pass "" when there are no more pages.
func ToListClaimsResponse(claims []domain.Claim, nextCursor string) *dto.ListClaimsResponse {
	items := make([]dto.ClaimResponse, 0, len(claims))
	for i := range claims {
		r := ToClaimResponse(&claims[i], "")
		if r != nil {
			items = append(items, *r)
		}
	}

	return &dto.ListClaimsResponse{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    nextCursor != "",
	}
}
