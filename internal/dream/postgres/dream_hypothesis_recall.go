package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/lib/pq"
	"gorm.io/gorm"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

func (r *Store) RecallHypotheses(ctx context.Context, input RecallHypothesesInput) ([]HypothesisRecord, error) {
	input, err := dreamcontract.NormalizeRecallHypothesesInput(input)
	if err != nil {
		return nil, err
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
			ORDER BY `+recallHypothesisRankingSQL+`,
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

var recallHypothesisRankingSQL = buildRecallHypothesisRankingSQL()

func buildRecallHypothesisRankingSQL() string {
	expressions := map[dreamcontract.RecallHypothesisMatchCategory]string{
		dreamcontract.RecallHypothesisSourceOverlap:   "relevance.source_overlap",
		dreamcontract.RecallHypothesisEndpointOverlap: "relevance.endpoint_overlap",
		dreamcontract.RecallHypothesisStatementMatch:  "relevance.statement_match",
		dreamcontract.RecallHypothesisRationaleMatch:  "relevance.rationale_match",
	}
	var query strings.Builder
	query.WriteString("CASE")
	for rank, category := range dreamcontract.RecallHypothesisMatchOrder() {
		if category == dreamcontract.RecallHypothesisFallback {
			fmt.Fprintf(&query, " ELSE %d", rank)
			continue
		}
		expression, ok := expressions[category]
		if !ok {
			panic("dream: unsupported hypothesis recall match category")
		}
		fmt.Fprintf(&query, " WHEN %s THEN %d", expression, rank)
	}
	query.WriteString(" END")
	return query.String()
}
