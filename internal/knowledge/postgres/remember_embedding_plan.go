package postgres

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// PlanRememberEmbeddings renders the exact document batch for a
// request-owned Remember. It performs reads only; no ingest, evidence,
// semantic, search, or attempt row is written before embedding succeeds.
func (r *Store) PlanRememberEmbeddings(
	ctx context.Context,
	input SynchronousRememberCommitInput,
) (*InlineEmbeddingPlan, error) {
	input = normalizeSynchronousRememberCommitInput(input)
	if err := validateSynchronousRememberCommitInput(input); err != nil {
		return nil, fmt.Errorf("%w: invalid synchronous Remember input: %w", ErrInlineEmbeddingPlanMismatch, err)
	}
	plan := &InlineEmbeddingPlan{Documents: []SearchDocumentForEmbedding{}}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		contract, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		plan.EmbeddingContractID = contract.EmbeddingContractID
		plan.EmbeddingDimensions = contract.EmbeddingDimensions
		plan.EmbeddingModel = contract.EmbeddingModel
		plan.SearchIndexGenerationID = contract.SearchIndexGenerationID
		plan.IndexGeneration = contract.IndexGeneration
		seenHashes := map[string]struct{}{}
		add := func(sourceKind, sourceID, text string) error {
			text = strings.TrimSpace(text)
			if text == "" {
				return fmt.Errorf("%w: empty %s document", ErrInlineEmbeddingPlanMismatch, sourceKind)
			}
			hash := searchDocumentHash(text)
			if _, exists := seenHashes[hash]; exists {
				return nil
			}
			seenHashes[hash] = struct{}{}
			plan.Documents = append(plan.Documents, SearchDocumentForEmbedding{
				SearchDocumentResult: SearchDocumentResult{
					TeamID: input.TeamID, SearchDocumentID: "plan:" + sourceID,
					OwnerProfileID: input.OwnerProfileID, SourceKind: sourceKind, SourceID: sourceID,
					SourceVersion: 1, ProjectionFormat: defaultProjectionFormat(sourceKind),
					EmbeddingContractID: contract.EmbeddingContractID,
					EmbeddingDimensions: contract.EmbeddingDimensions,
				},
				DocumentText: text, DocumentHash: hash,
			})
			return nil
		}
		for index, evidence := range input.Evidence {
			if rememberEvidenceDuplicateWasPreassessed(input, index) {
				continue
			}
			if err := add("evidence", evidence.FragmentID, evidence.Content); err != nil {
				return err
			}
		}
		if len(plan.Documents) > 256 {
			return fmt.Errorf("%w: more than 256 unique search documents", ErrInlineEmbeddingPlanTooLarge)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repository: plan Remember embeddings: %w", err)
	}
	return plan, nil
}

func rememberEvidenceDuplicateWasPreassessed(input SynchronousRememberCommitInput, evidenceIndex int) bool {
	for _, resolution := range input.DuplicateResolutions {
		if resolution.EvidenceIndex == evidenceIndex {
			if resolution.Exact {
				return true
			}
			if evidenceIndex < 0 || evidenceIndex >= len(input.Evidence) {
				return false
			}
			return rememberEvidenceRequiresDuplicateAssessment(input.Evidence[evidenceIndex])
		}
	}
	return false
}
