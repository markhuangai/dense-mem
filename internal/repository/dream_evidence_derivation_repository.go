package repository

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

func insertHypothesisEvidenceDerivations(
	ctx context.Context,
	tx *gorm.DB,
	teamID, hypothesisID string,
	derivations []EvidenceDerivationSource,
) error {
	for _, derivation := range derivations {
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO hypothesis_evidence_derivation_sources (
			    team_id, hypothesis_id, space_id, space_generation, evidence_id, fragment_id,
			    source_id, source_revision_id, source_group_key, span_start, span_end, quote, authority
			)
			SELECT ?::uuid, hypothesis.hypothesis_id, hypothesis.space_id, hypothesis.space_generation,
			       ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?, ?, ?, ?, ?
			FROM hypotheses AS hypothesis
			WHERE hypothesis.team_id = ?::uuid
			  AND hypothesis.space_id = dense_mem_team_shared_space(hypothesis.team_id)
			  AND hypothesis.space_generation = dense_mem_team_shared_generation(hypothesis.team_id)
			  AND hypothesis.hypothesis_id = ?::uuid
		`, teamID, derivation.EvidenceID, derivation.FragmentID,
			derivation.SourceID, derivation.SourceRevisionID, derivation.SourceGroupKey,
			derivation.SpanStart, derivation.SpanEnd, derivation.Quote, derivation.Authority,
			teamID, hypothesisID).Error; err != nil {
			return fmt.Errorf("insert evidence derivation: %w", err)
		}
	}
	return nil
}
