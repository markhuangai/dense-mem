package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

func (r *Store) RecallHypotheses(ctx context.Context, input RecallHypothesesInput) ([]HypothesisRecord, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	if input.Limit <= 0 {
		input.Limit = 5
	}
	if input.Limit > 20 {
		input.Limit = 20
	}
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	var err error
	if input.EvidenceIDs, err = normalizeRecallHypothesisContextIDs(input.EvidenceIDs); err != nil {
		return nil, fmt.Errorf("recall context evidence IDs: %w", err)
	}
	if input.RelationshipIDs, err = normalizeRecallHypothesisContextIDs(input.RelationshipIDs); err != nil {
		return nil, fmt.Errorf("recall context Relationship IDs: %w", err)
	}
	if input.EntityIDs, err = normalizeRecallHypothesisContextIDs(input.EntityIDs); err != nil {
		return nil, fmt.Errorf("recall context entity IDs: %w", err)
	}
	if input.ValueIDs, err = normalizeRecallHypothesisContextIDs(input.ValueIDs); err != nil {
		return nil, fmt.Errorf("recall context Value IDs: %w", err)
	}

	pattern := "%" + input.Query + "%"
	records := []HypothesisRecord{}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		query := `
			WITH recall_context AS (
				SELECT ?::uuid[] AS evidence_ids,
				       ?::uuid[] AS relationship_ids,
				       ?::uuid[] AS entity_ids,
				       ?::uuid[] AS value_ids
			)
		` + hypothesisSelectSQL(`
			CROSS JOIN recall_context
			CROSS JOIN LATERAL (
				SELECT
					EXISTS (
						SELECT 1 FROM hypothesis_derivation_sources derivation
						WHERE derivation.team_id = hypotheses.team_id
						  AND derivation.space_id = hypotheses.space_id
						  AND derivation.space_generation = hypotheses.space_generation
						  AND derivation.hypothesis_id = hypotheses.hypothesis_id
						  AND (derivation.relationship_id = ANY(recall_context.relationship_ids)
						       OR derivation.fragment_id = ANY(recall_context.evidence_ids))
					) OR EXISTS (
						SELECT 1 FROM hypothesis_evidence_derivation_sources derivation
						WHERE derivation.team_id = hypotheses.team_id
						  AND derivation.space_id = hypotheses.space_id
						  AND derivation.space_generation = hypotheses.space_generation
						  AND derivation.hypothesis_id = hypotheses.hypothesis_id
						  AND derivation.evidence_id = ANY(recall_context.evidence_ids)
					) AS source_overlap,
					COALESCE(hypotheses.subject_entity_id = ANY(recall_context.entity_ids), false)
						OR COALESCE(hypotheses.object_entity_id = ANY(recall_context.entity_ids), false)
						OR COALESCE(hypotheses.object_value_id = ANY(recall_context.value_ids), false)
						AS endpoint_overlap,
					(? <> '' AND hypotheses.statement ILIKE ?) AS statement_match,
					(? <> '' AND hypotheses.rationale ILIKE ?) AS rationale_match
			) AS relevance
			WHERE team_id = ?::uuid
			  AND space_id = dense_mem_team_shared_space(team_id)
			  AND space_generation = dense_mem_team_shared_generation(team_id)
			  AND canonical_hypothesis_id IS NULL
			  AND status IN ('proposed', 'reinforced')
			  AND NOT (`+hypothesisSourceIneligiblePredicateSQL+`)
			  AND (
			      (? = '' AND cardinality(recall_context.evidence_ids) = 0
			                    AND cardinality(recall_context.relationship_ids) = 0
			                    AND cardinality(recall_context.entity_ids) = 0
			                    AND cardinality(recall_context.value_ids) = 0)
			      OR relevance.source_overlap
			      OR relevance.endpoint_overlap
			      OR relevance.statement_match
			      OR relevance.rationale_match
			  )
			ORDER BY CASE
			           WHEN relevance.source_overlap THEN 0
			           WHEN relevance.endpoint_overlap THEN 1
			           WHEN relevance.statement_match THEN 2
			           WHEN relevance.rationale_match THEN 3
			           ELSE 4
			         END,
			         updated_at DESC,
			         hypothesis_id
			LIMIT ?
		`)
		rows, err := tx.WithContext(ctx).Raw(query,
			pq.Array(input.EvidenceIDs),
			pq.Array(input.RelationshipIDs),
			pq.Array(input.EntityIDs),
			pq.Array(input.ValueIDs),
			input.Query, pattern, input.Query, pattern,
			input.TeamID, input.Query, input.Limit,
		).Rows()
		if err != nil {
			return err
		}
		records, err = scanHypothesisRecords(rows)
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return hydrateDreamHypothesisDerivations(ctx, tx, input.TeamID, records)
	})
	if err != nil {
		return nil, fmt.Errorf("dream: recall hypotheses: %w", err)
	}
	return records, nil
}

func normalizeRecallHypothesisContextIDs(values []string) ([]string, error) {
	out := make([]string, 0, min(len(values), dreamcontract.MaxRecallHypothesisContextIDs))
	seen := make(map[string]struct{}, cap(out))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := uuid.Parse(value)
		if err != nil {
			return nil, err
		}
		value = parsed.String()
		if _, exists := seen[value]; exists {
			continue
		}
		if len(out) == dreamcontract.MaxRecallHypothesisContextIDs {
			break
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}
