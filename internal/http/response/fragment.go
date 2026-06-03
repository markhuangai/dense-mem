package response

import (
	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
)

// ToFragmentResponse converts a domain Fragment to a DTO FragmentResponse.
// The embedding vector is deliberately excluded from the response (AC-28).
func ToFragmentResponse(f *domain.Fragment) *dto.FragmentResponse {
	if f == nil {
		return nil
	}

	return &dto.FragmentResponse{
		ID:                   f.FragmentID,
		FragmentID:           f.FragmentID, // alias; same value as ID for UAT-02/UAT-03 compatibility
		TeamID:               f.ProfileID,
		OwnerProfileID:       f.OwnerProfileID,
		OwnerProfileName:     f.OwnerProfileName,
		CreatedByProfileID:   f.CreatedByProfileID,
		CreatedByProfileName: f.CreatedByProfileName,
		Content:              f.Content,
		SourceType:           string(f.SourceType),
		Source:               f.Source,
		Authority:            string(f.Authority),
		Labels:               f.Labels,
		Metadata:             f.Metadata,
		ContentHash:          f.ContentHash,
		IdempotencyKey:       f.IdempotencyKey,
		EmbeddingModel:       f.EmbeddingModel,
		EmbeddingDimensions:  f.EmbeddingDimensions,
		SourceQuality:        f.SourceQuality,
		Classification:       f.Classification,
		Status:               string(f.Status),
		RecordedTo:           f.RecordedTo,
		CreatedAt:            f.CreatedAt,
		UpdatedAt:            f.UpdatedAt,
	}
}
