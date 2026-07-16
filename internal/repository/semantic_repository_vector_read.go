package repository

import (
	"context"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func searchRecallVectorEvidenceCandidates(ctx context.Context, tx *gorm.DB, scope domain.SemanticRecallSearchScope, vectorLiteral string) ([]domain.SemanticRecallCandidate, error) {
	annLimit := scope.BranchLimit * 4
	if annLimit > 800 {
		annLimit = 800
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH
		params AS (
			SELECT ?::vector AS query_vector,
			       ?::text AS embedding_contract_id,
			       ?::timestamptz AS valid_at,
			       ?::timestamptz AS known_at,
			       ?::int AS branch_limit,
			       ?::int AS ann_limit
		),
		scope AS (
			SELECT ?::uuid AS team_id,
			       ?::uuid[] AS known_evidence_ids,
			       ?::uuid[] AS known_relationship_ids
		),
		eligible_relationships AS MATERIALIZED (
			SELECT r.*
			FROM semantic_relationship_records r
			CROSS JOIN params p
			CROSS JOIN scope s
			WHERE r.team_id = s.team_id
			  AND r.status = 'active'
			  AND r.tier IN ('validated_claim', 'fact')
			  AND r.relationship_id <> ALL(s.known_relationship_ids)
			  AND (r.valid_from IS NULL OR r.valid_from <= p.valid_at)
			  AND (r.valid_to IS NULL OR r.valid_to > p.valid_at)
			  AND r.recorded_at <= p.known_at
			  AND (r.recorded_to IS NULL OR r.recorded_to > p.known_at)
		),
		eligible_evidence AS MATERIALIZED (
			SELECT e.*
			FROM semantic_evidence_fragments e
			CROSS JOIN params p
			CROSS JOIN scope s
			WHERE e.team_id = s.team_id
			  AND e.created_at <= p.known_at
			  AND e.fragment_id <> ALL(s.known_evidence_ids)
		),
		evidence_features AS (
			SELECT support.fragment_id AS evidence_id,
			       bool_or(r.tier = 'fact') AS fact_support,
			       count(DISTINCT NULLIF(support.source_group, ''))::int AS source_group_count,
			       max(r.valid_from) AS latest_valid_from,
			       max(r.recorded_at) AS latest_recorded_at,
			       array_agg(DISTINCT r.relationship_id::text ORDER BY r.relationship_id::text) AS relationship_ids
			FROM semantic_relationship_supports support
			JOIN eligible_relationships r
			  ON r.team_id = support.team_id AND r.relationship_id = support.relationship_id
			GROUP BY support.fragment_id
		),
		evidence_ann AS MATERIALIZED (
			SELECT d.source_id
			FROM semantic_search_documents d
			JOIN eligible_evidence e ON e.team_id = d.team_id AND e.fragment_id = d.source_id
			CROSS JOIN params p
			CROSS JOIN scope s
			WHERE d.team_id = s.team_id
			  AND d.source_type = 'evidence'
			  AND d.search_state = 'current'
			  AND d.embedding_contract_id = p.embedding_contract_id
			  AND d.embedding IS NOT NULL
			ORDER BY d.embedding <=> p.query_vector,
			         d.updated_at DESC, d.search_document_id ASC
			LIMIT (SELECT ann_limit FROM params)
		),
		evidence_hits AS (
			SELECT d.source_id AS evidence_id,
			       row_number() OVER (
			         ORDER BY d.embedding <=> p.query_vector,
			                  d.updated_at DESC,
			                  d.search_document_id ASC
			       ) AS branch_rank,
			       (1 - (d.embedding <=> p.query_vector))::float8 AS raw_score
			FROM evidence_ann ann
			JOIN semantic_search_documents d ON d.source_id = ann.source_id AND d.source_type = 'evidence'
			CROSS JOIN params p
			CROSS JOIN scope s
			WHERE d.team_id = s.team_id
			LIMIT (SELECT branch_limit FROM params)
		),
		candidate_rows AS (
			SELECT evidence_id,
			       'evidence_vector'::text AS branch,
			       branch_rank,
			       raw_score,
			       COALESCE(evidence_features.relationship_ids, ARRAY[]::text[]) AS relationship_ids
			FROM evidence_hits
			LEFT JOIN evidence_features USING (evidence_id)
		)
		SELECT candidate_rows.evidence_id::text,
		       candidate_rows.branch,
		       candidate_rows.branch_rank::int,
		       candidate_rows.raw_score,
		       false AS exact_match,
		       false AS precise_match,
		       false AS phrase_match,
		       false AS all_hard_anchors_matched,
		       COALESCE(evidence_features.fact_support, false),
		       COALESCE(evidence_features.source_group_count, 0),
		       evidence_features.latest_valid_from,
		       evidence_features.latest_recorded_at,
		       candidate_rows.relationship_ids,
		       ARRAY[]::text[] AS matched_entity_ids
		FROM candidate_rows
		LEFT JOIN evidence_features ON evidence_features.evidence_id = candidate_rows.evidence_id
		ORDER BY candidate_rows.branch, candidate_rows.branch_rank
	`, vectorLiteral, scope.EmbeddingContractID, scope.ValidAt, scope.KnownAt, scope.BranchLimit, annLimit,
		scope.TeamID, pq.Array(scope.KnownEvidenceIDs), pq.Array(scope.KnownRelationshipIDs)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSemanticRecallCandidates(rows)
}

func searchRecallVectorEntitySeeds(ctx context.Context, tx *gorm.DB, scope domain.SemanticRecallSearchScope, vectorLiteral string) ([]domain.SemanticRecallEntitySeed, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH
		params AS (
			SELECT ?::vector AS query_vector,
			       ?::text AS embedding_contract_id
		),
		scope AS (
			SELECT ?::uuid AS team_id
		),
		entity_ann AS MATERIALIZED (
			SELECT d.source_id AS entity_id
			FROM semantic_search_documents d
			JOIN semantic_entities e ON e.team_id = d.team_id AND e.entity_id = d.source_id
			CROSS JOIN params p
			CROSS JOIN scope s
			WHERE d.team_id = s.team_id
			  AND d.source_type = 'entity'
			  AND d.search_state = 'current'
			  AND d.embedding_contract_id = p.embedding_contract_id
			  AND d.embedding IS NOT NULL
			  AND e.status = 'active'
			ORDER BY d.embedding <=> p.query_vector,
			         d.updated_at DESC, d.search_document_id ASC
			LIMIT 80
		)
		SELECT d.source_id::text AS entity_id,
		       row_number() OVER (
		         ORDER BY d.embedding <=> p.query_vector,
		                  d.updated_at DESC,
		                  d.search_document_id ASC
		       )::int AS branch_rank,
		       false AS exact,
		       false AS hard_anchor,
		       false AS explicit,
		       (1 - (d.embedding <=> p.query_vector))::float8 AS score
		FROM entity_ann ann
		JOIN semantic_search_documents d
		  ON d.source_type = 'entity' AND d.source_id = ann.entity_id
		CROSS JOIN params p
		CROSS JOIN scope s
		WHERE d.team_id = s.team_id
		LIMIT 20
	`, vectorLiteral, scope.EmbeddingContractID, scope.TeamID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSemanticRecallEntitySeeds(rows)
}
