package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *SemanticRepositoryImpl) SearchRecallLexicalCandidates(ctx context.Context, scope domain.SemanticRecallSearchScope) (domain.SemanticRecallCandidateBatch, error) {
	scope = normalizeSemanticRecallScope(scope)
	if scope.TeamID == "" || scope.Features.Query == "" {
		return domain.SemanticRecallCandidateBatch{}, nil
	}
	var batch domain.SemanticRecallCandidateBatch
	err := r.withTeamTx(ctx, scope.TeamID, func(tx *gorm.DB) error {
		candidates, err := searchRecallLexicalEvidenceCandidates(ctx, tx, scope)
		if err != nil {
			return err
		}
		seeds, err := searchRecallLexicalEntitySeeds(ctx, tx, scope)
		if err != nil {
			return err
		}
		batch.Candidates = candidates
		batch.EntitySeeds = seeds
		return nil
	})
	if err != nil {
		return domain.SemanticRecallCandidateBatch{}, fmt.Errorf("semantic recall lexical candidates: %w", err)
	}
	return batch, nil
}

func (r *SemanticRepositoryImpl) SearchRecallVectorCandidates(ctx context.Context, scope domain.SemanticRecallSearchScope) (domain.SemanticRecallCandidateBatch, error) {
	scope = normalizeSemanticRecallScope(scope)
	if scope.TeamID == "" || len(scope.Embedding) == 0 || strings.TrimSpace(scope.EmbeddingContractID) == "" {
		return domain.SemanticRecallCandidateBatch{}, nil
	}
	vectorLiteral := semanticVectorLiteral(scope.Embedding)
	if vectorLiteral == "" {
		return domain.SemanticRecallCandidateBatch{}, nil
	}
	var batch domain.SemanticRecallCandidateBatch
	err := r.withTeamTx(ctx, scope.TeamID, func(tx *gorm.DB) error {
		if err := setSemanticVectorSearchSettings(ctx, tx); err != nil {
			return err
		}
		candidates, err := searchRecallVectorEvidenceCandidates(ctx, tx, scope, vectorLiteral)
		if err != nil {
			return err
		}
		seeds, err := searchRecallVectorEntitySeeds(ctx, tx, scope, vectorLiteral)
		if err != nil {
			return err
		}
		batch.Candidates = candidates
		batch.EntitySeeds = seeds
		return nil
	})
	if err != nil {
		return domain.SemanticRecallCandidateBatch{}, fmt.Errorf("semantic recall vector candidates: %w", err)
	}
	return batch, nil
}

func (r *SemanticRepositoryImpl) SearchRecallAdjacencyCandidates(ctx context.Context, scope domain.SemanticRecallSearchScope, seeds []domain.SemanticRecallEntitySeed) ([]domain.SemanticRecallCandidate, error) {
	scope = normalizeSemanticRecallScope(scope)
	if scope.TeamID == "" {
		return nil, nil
	}
	seedIDs, exactSeedIDs, hardAnchorSeedIDs := semanticRecallSeedArrays(seeds)
	if len(seedIDs) == 0 && len(scope.ExpandFromEntityIDs) == 0 && len(scope.KnownRelationshipIDs) == 0 {
		return nil, nil
	}
	var candidates []domain.SemanticRecallCandidate
	err := r.withTeamTx(ctx, scope.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH
			params AS (
				SELECT websearch_to_tsquery('english', ?) AS relaxed_query,
				       ?::text[] AS hard_anchors,
				       ?::text[] AS phrases,
				       ?::timestamptz AS valid_at,
				       ?::timestamptz AS known_at,
				       ?::int AS branch_limit
			),
			scope AS (
				SELECT ?::uuid AS team_id,
				       ?::uuid[] AS known_evidence_ids,
				       ?::uuid[] AS known_relationship_ids,
				       ?::uuid[] AS seed_entity_ids,
				       ?::uuid[] AS exact_seed_entity_ids,
				       ?::uuid[] AS hard_seed_entity_ids,
				       ?::uuid[] AS explicit_entity_ids
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
			seed_entities AS (
				SELECT e.entity_id,
				       e.entity_id = ANY(s.exact_seed_entity_ids) AS exact_seed,
				       e.entity_id = ANY(s.hard_seed_entity_ids) AS hard_seed,
				       e.entity_id = ANY(s.explicit_entity_ids) AS explicit_seed,
				       array_position(s.seed_entity_ids, e.entity_id) AS seed_rank
				FROM semantic_entities e
				CROSS JOIN scope s
				WHERE e.team_id = s.team_id
				  AND e.status = 'active'
				  AND (
				    e.entity_id = ANY(s.seed_entity_ids)
				    OR e.entity_id = ANY(s.explicit_entity_ids)
				  )
				UNION
				SELECT endpoint.entity_id,
				       true AS exact_seed,
				       false AS hard_seed,
				       false AS explicit_seed,
				       100 AS seed_rank
				FROM (
					SELECT r.subject_entity_id AS entity_id
					FROM semantic_relationship_records r
					CROSS JOIN params p
					CROSS JOIN scope s
					WHERE r.team_id = s.team_id
					  AND r.relationship_id = ANY(s.known_relationship_ids)
					  AND r.status = 'active'
					  AND r.tier IN ('validated_claim', 'fact')
					  AND (r.valid_from IS NULL OR r.valid_from <= p.valid_at)
					  AND (r.valid_to IS NULL OR r.valid_to > p.valid_at)
					  AND r.recorded_at <= p.known_at
					  AND (r.recorded_to IS NULL OR r.recorded_to > p.known_at)
					UNION
					SELECT r.object_entity_id
					FROM semantic_relationship_records r
					CROSS JOIN params p
					CROSS JOIN scope s
					WHERE r.team_id = s.team_id
					  AND r.relationship_id = ANY(s.known_relationship_ids)
					  AND r.object_entity_id IS NOT NULL
					  AND r.status = 'active'
					  AND r.tier IN ('validated_claim', 'fact')
					  AND (r.valid_from IS NULL OR r.valid_from <= p.valid_at)
					  AND (r.valid_to IS NULL OR r.valid_to > p.valid_at)
					  AND r.recorded_at <= p.known_at
					  AND (r.recorded_to IS NULL OR r.recorded_to > p.known_at)
				) endpoint
			),
			adjacent_relationships AS (
				SELECT r.relationship_id,
				       support.fragment_id AS evidence_id,
				       seed.entity_id AS matched_entity_id,
				       seed.exact_seed,
				       seed.hard_seed,
				       seed.explicit_seed,
				       COALESCE(seed.seed_rank, 1000) AS seed_rank,
				       COALESCE(ts_rank_cd(to_tsvector('english', rel_doc.document_text), p.relaxed_query), 0) AS relationship_text_score,
				       COALESCE(max(ts_rank_cd(to_tsvector('english', evidence_doc.document_text), p.relaxed_query)), 0) AS evidence_text_score,
				       bool_or(
				         COALESCE((
				           SELECT bool_or(strpos(lower(COALESCE(rel_doc.document_text, '') || ' ' || COALESCE(evidence_doc.document_text, '')), phrase) > 0)
				           FROM unnest(p.phrases) AS phrase
				         ), false)
				       ) AS phrase_match,
				       bool_or(
				         COALESCE((
				           SELECT bool_and(strpos(lower(COALESCE(rel_doc.document_text, '') || ' ' || COALESCE(evidence_doc.document_text, '') || ' ' || r.relationship_id::text), anchor) > 0)
				           FROM unnest(p.hard_anchors) AS anchor
				         ), false)
				       ) AS hard_anchor_match
				FROM seed_entities seed
				JOIN eligible_relationships r
				  ON r.subject_entity_id = seed.entity_id OR r.object_entity_id = seed.entity_id
				JOIN semantic_relationship_supports support
				  ON support.team_id = r.team_id AND support.relationship_id = r.relationship_id
				JOIN eligible_evidence evidence
				  ON evidence.team_id = support.team_id AND evidence.fragment_id = support.fragment_id
				CROSS JOIN params p
				LEFT JOIN semantic_search_documents rel_doc
				  ON rel_doc.team_id = r.team_id
				 AND rel_doc.source_type = 'relationship'
				 AND rel_doc.source_id = r.relationship_id
				LEFT JOIN semantic_search_documents evidence_doc
				  ON evidence_doc.team_id = evidence.team_id
				 AND evidence_doc.source_type = 'evidence'
				 AND evidence_doc.source_id = evidence.fragment_id
				WHERE seed.exact_seed
				   OR seed.hard_seed
				   OR seed.explicit_seed
				   OR to_tsvector('english', COALESCE(rel_doc.document_text, '')) @@ p.relaxed_query
				   OR to_tsvector('english', COALESCE(evidence_doc.document_text, '')) @@ p.relaxed_query
				GROUP BY r.relationship_id, support.fragment_id, seed.entity_id, seed.exact_seed,
				         seed.hard_seed, seed.explicit_seed, seed.seed_rank, rel_doc.document_text,
				         p.relaxed_query, p.phrases, p.hard_anchors
			),
			ranked AS (
				SELECT evidence_id,
				       row_number() OVER (
				         ORDER BY max(exact_seed::int) DESC,
				                  max(hard_seed::int) DESC,
				                  max(explicit_seed::int) DESC,
				                  max(relationship_text_score + evidence_text_score) DESC,
				                  min(seed_rank) ASC,
				                  evidence_id ASC
				       ) AS branch_rank,
				       max(relationship_text_score + evidence_text_score) AS raw_score,
				       bool_or(exact_seed OR hard_seed) AS exact_match,
				       bool_or(relationship_text_score > 0 OR evidence_text_score > 0) AS precise_match,
				       bool_or(phrase_match) AS phrase_match,
				       bool_or(hard_anchor_match) AS all_hard_anchors_matched,
				       array_agg(DISTINCT relationship_id::text ORDER BY relationship_id::text) AS relationship_ids,
				       array_agg(DISTINCT matched_entity_id::text ORDER BY matched_entity_id::text) AS matched_entity_ids
				FROM adjacent_relationships
				GROUP BY evidence_id
			),
			features AS (
				SELECT support.fragment_id AS evidence_id,
				       bool_or(r.tier = 'fact') AS fact_support,
				       count(DISTINCT NULLIF(support.source_group, ''))::int AS source_group_count,
				       max(r.valid_from) AS latest_valid_from,
				       max(r.recorded_at) AS latest_recorded_at
				FROM semantic_relationship_supports support
				JOIN eligible_relationships r
				  ON r.team_id = support.team_id AND r.relationship_id = support.relationship_id
				GROUP BY support.fragment_id
			)
			SELECT ranked.evidence_id::text,
			       'adjacency',
			       ranked.branch_rank::int,
			       ranked.raw_score::float8,
			       ranked.exact_match,
			       ranked.precise_match,
			       ranked.phrase_match,
			       ranked.all_hard_anchors_matched,
			       COALESCE(features.fact_support, false),
			       COALESCE(features.source_group_count, 0),
			       features.latest_valid_from,
			       features.latest_recorded_at,
			       ranked.relationship_ids,
			       ranked.matched_entity_ids
			FROM ranked
			LEFT JOIN features ON features.evidence_id = ranked.evidence_id
			ORDER BY ranked.branch_rank ASC
			LIMIT (SELECT branch_limit FROM params)
		`, scope.Features.RelaxedQuery, pq.Array(lowerStrings(scope.Features.HardAnchors)), pq.Array(lowerStrings(scope.Features.EntityPhrases)),
			scope.ValidAt, scope.KnownAt, scope.BranchLimit, scope.TeamID, pq.Array(scope.KnownEvidenceIDs),
			pq.Array(scope.KnownRelationshipIDs), pq.Array(seedIDs), pq.Array(exactSeedIDs), pq.Array(hardAnchorSeedIDs),
			pq.Array(scope.ExpandFromEntityIDs)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		candidates, err = scanSemanticRecallCandidates(rows)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("semantic recall adjacency candidates: %w", err)
	}
	return candidates, nil
}

func (r *SemanticRepositoryImpl) HydrateRecallEvidence(ctx context.Context, scope domain.SemanticRecallSearchScope, evidenceIDs, preferredRelationshipIDs []string) ([]domain.SemanticRecallResult, error) {
	scope = normalizeSemanticRecallScope(scope)
	evidenceIDs = uniqueRepositoryStrings(evidenceIDs)
	preferredRelationshipIDs = uniqueRepositoryStrings(preferredRelationshipIDs)
	if scope.TeamID == "" || len(evidenceIDs) == 0 {
		return nil, nil
	}
	var results []domain.SemanticRecallResult
	err := r.withTeamTx(ctx, scope.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH
			params AS (
				SELECT ?::timestamptz AS valid_at,
				       ?::timestamptz AS known_at
			),
			scope AS (
				SELECT ?::uuid AS team_id,
				       ?::uuid[] AS evidence_ids,
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
			)
			SELECT `+semanticEvidenceColumns("e")+`
			FROM semantic_evidence_fragments e
			CROSS JOIN params p
			CROSS JOIN scope s
			WHERE e.team_id = s.team_id
			  AND e.fragment_id = ANY(s.evidence_ids)
			  AND e.created_at <= p.known_at
			ORDER BY array_position(s.evidence_ids, e.fragment_id)
		`, scope.ValidAt, scope.KnownAt, scope.TeamID, pq.Array(evidenceIDs), pq.Array(scope.KnownRelationshipIDs)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		results = make([]domain.SemanticRecallResult, 0, len(evidenceIDs))
		for rows.Next() {
			evidence, err := scanSemanticEvidence(rows)
			if err != nil {
				return err
			}
			results = append(results, domain.SemanticRecallResult{Evidence: &evidence})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		relationships, supports, err := listSemanticEvidenceDiscoveryRelationships(ctx, tx, scope, evidenceIDs, preferredRelationshipIDs)
		if err != nil {
			return err
		}
		for i := range results {
			evidenceID := results[i].Evidence.FragmentID
			results[i].Relationships = relationships[evidenceID]
			results[i].Supports = supports[evidenceID]
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("semantic recall hydrate evidence: %w", err)
	}
	return results, nil
}

func searchRecallLexicalEvidenceCandidates(ctx context.Context, tx *gorm.DB, scope domain.SemanticRecallSearchScope) ([]domain.SemanticRecallCandidate, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH
		params AS (
			SELECT websearch_to_tsquery('english', ?) AS precise_query,
			       websearch_to_tsquery('english', ?) AS relaxed_query,
			       ?::text[] AS hard_anchors,
			       ?::text[] AS phrases,
			       ?::timestamptz AS valid_at,
			       ?::timestamptz AS known_at,
			       ?::int AS branch_limit
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
			       max(r.recorded_at) AS latest_recorded_at
			FROM semantic_relationship_supports support
			JOIN eligible_relationships r
			  ON r.team_id = support.team_id AND r.relationship_id = support.relationship_id
			GROUP BY support.fragment_id
		),
		support_relationships AS (
			SELECT support.fragment_id AS evidence_id,
			       array_agg(DISTINCT support.relationship_id::text ORDER BY support.relationship_id::text) AS relationship_ids
			FROM semantic_relationship_supports support
			JOIN eligible_relationships r
			  ON r.team_id = support.team_id AND r.relationship_id = support.relationship_id
			GROUP BY support.fragment_id
		),
		exact_hits AS (
			SELECT evidence_id,
			       row_number() OVER (ORDER BY exact_rank ASC, evidence_id ASC) AS branch_rank,
			       relationship_ids
			FROM (
				SELECT e.fragment_id AS evidence_id,
				       1 AS exact_rank,
				       COALESCE(support_relationships.relationship_ids, ARRAY[]::text[]) AS relationship_ids
				FROM eligible_evidence e
				CROSS JOIN params p
				LEFT JOIN support_relationships ON support_relationships.evidence_id = e.fragment_id
				WHERE lower(e.fragment_id::text) = ANY(p.hard_anchors)
				   OR lower(e.source_doc_id) = ANY(p.hard_anchors)
			) exact_sources
			LIMIT (SELECT branch_limit FROM params)
		),
		evidence_text_inputs AS (
			SELECT d.source_id AS evidence_id,
			       ts_rank_cd(to_tsvector('english', d.document_text), p.precise_query) AS raw_score,
			       true AS precise_match,
			       COALESCE((
			         SELECT bool_or(strpos(lower(d.document_text), phrase) > 0)
			         FROM unnest(p.phrases) AS phrase
			       ), false) AS phrase_match,
			       COALESCE((
			         SELECT bool_and(strpos(lower(d.document_text || ' ' || e.source_doc_id || ' ' || e.fragment_id::text), anchor) > 0)
			         FROM unnest(p.hard_anchors) AS anchor
			       ), false) AS hard_anchor_match
			FROM semantic_search_documents d
			JOIN eligible_evidence e ON e.team_id = d.team_id AND e.fragment_id = d.source_id
			CROSS JOIN params p
			CROSS JOIN scope s
			WHERE d.team_id = s.team_id
			  AND d.source_type = 'evidence'
			  AND d.search_state IN ('current', 'pending')
			  AND to_tsvector('english', d.document_text) @@ p.precise_query
			UNION ALL
			SELECT d.source_id,
			       ts_rank_cd(to_tsvector('english', d.document_text), p.relaxed_query),
			       false,
			       COALESCE((
			         SELECT bool_or(strpos(lower(d.document_text), phrase) > 0)
			         FROM unnest(p.phrases) AS phrase
			       ), false),
			       COALESCE((
			         SELECT bool_and(strpos(lower(d.document_text || ' ' || e.source_doc_id || ' ' || e.fragment_id::text), anchor) > 0)
			         FROM unnest(p.hard_anchors) AS anchor
			       ), false)
			FROM semantic_search_documents d
			JOIN eligible_evidence e ON e.team_id = d.team_id AND e.fragment_id = d.source_id
			CROSS JOIN params p
			CROSS JOIN scope s
			WHERE d.team_id = s.team_id
			  AND d.source_type = 'evidence'
			  AND d.search_state IN ('current', 'pending')
			  AND to_tsvector('english', d.document_text) @@ p.relaxed_query
		),
		evidence_text_hits AS (
			SELECT evidence_id,
			       row_number() OVER (
			         ORDER BY bool_or(precise_match)::int DESC,
			                  max(raw_score) DESC,
			                  evidence_id ASC
			       ) AS branch_rank,
			       max(raw_score) AS raw_score,
			       bool_or(precise_match) AS precise_match,
			       bool_or(phrase_match) AS phrase_match,
			       bool_or(hard_anchor_match) AS all_hard_anchors_matched
			FROM evidence_text_inputs
			GROUP BY evidence_id
			LIMIT (SELECT branch_limit FROM params)
		),
		candidate_rows AS (
			SELECT evidence_id,
			       'exact'::text AS branch,
			       branch_rank,
			       1.0::float8 AS raw_score,
			       true AS exact_match,
			       true AS precise_match,
			       true AS phrase_match,
			       true AS all_hard_anchors_matched,
			       relationship_ids,
			       ARRAY[]::text[] AS matched_entity_ids
			FROM exact_hits
			UNION ALL
			SELECT evidence_id,
			       'evidence_text',
			       branch_rank,
			       raw_score,
			       false,
			       precise_match,
			       phrase_match,
			       all_hard_anchors_matched,
			       COALESCE(support_relationships.relationship_ids, ARRAY[]::text[]),
			       ARRAY[]::text[]
			FROM evidence_text_hits
			LEFT JOIN support_relationships USING (evidence_id)
		)
		SELECT candidate_rows.evidence_id::text,
		       candidate_rows.branch,
		       candidate_rows.branch_rank::int,
		       candidate_rows.raw_score,
		       candidate_rows.exact_match,
		       candidate_rows.precise_match,
		       candidate_rows.phrase_match,
		       candidate_rows.all_hard_anchors_matched,
		       COALESCE(evidence_features.fact_support, false),
		       COALESCE(evidence_features.source_group_count, 0),
		       evidence_features.latest_valid_from,
		       evidence_features.latest_recorded_at,
		       candidate_rows.relationship_ids,
		       candidate_rows.matched_entity_ids
		FROM candidate_rows
		LEFT JOIN evidence_features ON evidence_features.evidence_id = candidate_rows.evidence_id
		ORDER BY candidate_rows.branch, candidate_rows.branch_rank
	`, scope.Features.ContentQuery, scope.Features.RelaxedQuery, pq.Array(lowerStrings(scope.Features.HardAnchors)),
		pq.Array(lowerStrings(scope.Features.EntityPhrases)), scope.ValidAt, scope.KnownAt, scope.BranchLimit,
		scope.TeamID, pq.Array(scope.KnownEvidenceIDs), pq.Array(scope.KnownRelationshipIDs)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSemanticRecallCandidates(rows)
}

func searchRecallLexicalEntitySeeds(ctx context.Context, tx *gorm.DB, scope domain.SemanticRecallSearchScope) ([]domain.SemanticRecallEntitySeed, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH
		params AS (
			SELECT websearch_to_tsquery('english', ?) AS relaxed_query,
			       ?::text[] AS hard_anchors,
			       ?::text[] AS phrases
		),
		scope AS (
			SELECT ?::uuid AS team_id
		),
		exact_hits AS (
			SELECT e.entity_id,
			       row_number() OVER (ORDER BY length(n.name) DESC, e.updated_at DESC, e.entity_id ASC) AS branch_rank,
			       lower(e.entity_id::text) = ANY(p.hard_anchors) AS hard_anchor
			FROM semantic_entities e
			JOIN semantic_entity_names n ON n.team_id = e.team_id AND n.entity_id = e.entity_id
			CROSS JOIN params p
			CROSS JOIN scope s
			WHERE e.team_id = s.team_id
			  AND e.status = 'active'
			  AND (
			    lower(n.name) = ANY(p.phrases)
			    OR lower(e.canonical_name) = ANY(p.phrases)
			    OR lower(e.entity_id::text) = ANY(p.hard_anchors)
			  )
			LIMIT 20
		),
		text_hits AS (
			SELECT d.source_id AS entity_id,
			       row_number() OVER (
			         ORDER BY ts_rank_cd(to_tsvector('english', d.document_text), p.relaxed_query) DESC,
			                  d.updated_at DESC, d.search_document_id ASC
			       ) AS branch_rank,
			       ts_rank_cd(to_tsvector('english', d.document_text), p.relaxed_query) AS raw_score
			FROM semantic_search_documents d
			JOIN semantic_entities e ON e.team_id = d.team_id AND e.entity_id = d.source_id
			CROSS JOIN params p
			CROSS JOIN scope s
			WHERE d.team_id = s.team_id
			  AND d.source_type = 'entity'
			  AND d.search_state IN ('current', 'pending')
			  AND e.status = 'active'
			  AND to_tsvector('english', d.document_text) @@ p.relaxed_query
			LIMIT 20
		),
		seeds AS (
			SELECT entity_id,
			       min(branch_rank) AS branch_rank,
			       bool_or(exact) AS exact,
			       bool_or(hard_anchor) AS hard_anchor,
			       max(score) AS score
			FROM (
				SELECT entity_id, branch_rank, true AS exact, hard_anchor, 1.0::float8 AS score FROM exact_hits
				UNION ALL
				SELECT entity_id, branch_rank, false, false, raw_score FROM text_hits
			) seed_rows
			GROUP BY entity_id
		)
		SELECT entity_id::text,
		       row_number() OVER (ORDER BY exact::int DESC, hard_anchor::int DESC, score DESC, branch_rank ASC, entity_id ASC)::int,
		       exact,
		       hard_anchor,
		       false AS explicit,
		       score::float8
		FROM seeds
		ORDER BY exact::int DESC, hard_anchor::int DESC, score DESC, branch_rank ASC, entity_id ASC
		LIMIT 20
	`, scope.Features.RelaxedQuery, pq.Array(lowerStrings(scope.Features.HardAnchors)), pq.Array(lowerStrings(scope.Features.EntityPhrases)), scope.TeamID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSemanticRecallEntitySeeds(rows)
}

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
			ORDER BY (d.embedding::halfvec(3072)) <=> (p.query_vector::halfvec(3072)),
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
			ORDER BY (d.embedding::halfvec(3072)) <=> (p.query_vector::halfvec(3072)),
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

func listSemanticEvidenceDiscoveryRelationships(ctx context.Context, tx *gorm.DB, scope domain.SemanticRecallSearchScope, evidenceIDs, preferredRelationshipIDs []string) (map[string][]domain.SemanticRelationship, map[string][]domain.SemanticRelationshipSupport, error) {
	relationshipsByEvidenceID := make(map[string][]domain.SemanticRelationship, len(evidenceIDs))
	supportsByEvidenceID := make(map[string][]domain.SemanticRelationshipSupport, len(evidenceIDs))
	if len(evidenceIDs) == 0 {
		return relationshipsByEvidenceID, supportsByEvidenceID, nil
	}
	perEvidence := scope.RelationshipsPerEvidence
	if perEvidence <= 0 {
		perEvidence = 2
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH
		params AS (
			SELECT websearch_to_tsquery('english', ?) AS relaxed_query,
			       ?::timestamptz AS valid_at,
			       ?::timestamptz AS known_at,
			       ?::int AS per_evidence
		),
		scope AS (
			SELECT ?::uuid AS team_id,
			       ?::uuid[] AS evidence_ids,
			       ?::uuid[] AS preferred_relationship_ids,
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
		ranked AS (
			SELECT support.fragment_id,
			       support.relationship_id,
			       support.evidence_index,
			       support.quote,
			       support.created_at AS support_created_at,
			       row_number() OVER (
			         PARTITION BY support.fragment_id
			         ORDER BY CASE WHEN support.relationship_id = ANY(s.preferred_relationship_ids) THEN 0 ELSE 1 END,
			                  array_position(s.preferred_relationship_ids, support.relationship_id),
			                  ts_rank_cd(to_tsvector('english', COALESCE(rel_doc.document_text, '')), p.relaxed_query) DESC,
			                  CASE WHEN r.tier = 'fact' THEN 0 ELSE 1 END,
			                  r.support_count DESC,
			                  r.updated_at DESC,
			                  r.relationship_id ASC
			       ) AS per_evidence_rank
			FROM semantic_relationship_supports support
			JOIN eligible_relationships r
			  ON r.team_id = support.team_id AND r.relationship_id = support.relationship_id
			CROSS JOIN params p
			CROSS JOIN scope s
			LEFT JOIN semantic_search_documents rel_doc
			  ON rel_doc.team_id = r.team_id
			 AND rel_doc.source_type = 'relationship'
			 AND rel_doc.source_id = r.relationship_id
			WHERE support.team_id = s.team_id
			  AND support.fragment_id = ANY(s.evidence_ids)
		)
		SELECT ranked.fragment_id::text,
		       r.team_id::text, r.relationship_id::text, r.owner_profile_id::text,
		       profile.name, r.subject_entity_id::text, subject.canonical_name, subject.kind,
		       r.predicate, r.polarity, COALESCE(r.object_entity_id::text, ''),
		       COALESCE(object.canonical_name, ''), COALESCE(object.kind, ''),
		       r.object_value, r.object_kind, r.tier, r.status, r.confidence,
		       r.support_count, r.source_group_count, r.semantic_group_key,
		       COALESCE(primary_group.source_group, ''), r.version,
		       r.valid_from, r.valid_to, r.recorded_at, r.recorded_to,
		       r.created_at, r.updated_at,
		       ranked.evidence_index, ranked.quote, ranked.support_created_at
		FROM ranked
		JOIN semantic_relationship_records r
		  ON r.team_id = ?::uuid AND r.relationship_id = ranked.relationship_id
		JOIN semantic_profile_refs profile
		  ON profile.team_id = r.team_id AND profile.profile_id = r.owner_profile_id
		JOIN semantic_entities subject
		  ON subject.team_id = r.team_id AND subject.entity_id = r.subject_entity_id
		LEFT JOIN semantic_entities object
		  ON object.team_id = r.team_id AND object.entity_id = r.object_entity_id
		LEFT JOIN LATERAL (
		  SELECT support.source_group
		  FROM semantic_relationship_supports support
		  WHERE support.team_id = r.team_id
		    AND support.relationship_id = r.relationship_id
		    AND support.source_group <> ''
		  GROUP BY support.source_group
		  ORDER BY count(*) DESC, support.source_group ASC
		  LIMIT 1
		) primary_group ON true
		CROSS JOIN params p
		CROSS JOIN scope s
		WHERE ranked.per_evidence_rank <= p.per_evidence
		ORDER BY array_position(s.evidence_ids, ranked.fragment_id),
		         ranked.per_evidence_rank,
		         ranked.relationship_id
	`, scope.Features.RelaxedQuery, scope.ValidAt, scope.KnownAt, perEvidence, scope.TeamID,
		pq.Array(evidenceIDs), pq.Array(preferredRelationshipIDs), pq.Array(scope.KnownRelationshipIDs), scope.TeamID).Rows()
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var evidenceID string
		var rel domain.SemanticRelationship
		var support domain.SemanticRelationshipSupport
		var subjectKind, objectEntityKind, tier, status string
		var validFrom, validTo, recordedTo sql.NullTime
		if err := rows.Scan(
			&evidenceID,
			&rel.TeamID,
			&rel.RelationshipID,
			&rel.OwnerProfileID,
			&rel.OwnerProfileName,
			&rel.SubjectEntityID,
			&rel.SubjectEntityName,
			&subjectKind,
			&rel.Predicate,
			&rel.Polarity,
			&rel.ObjectEntityID,
			&rel.ObjectEntityName,
			&objectEntityKind,
			&rel.ObjectValue,
			&rel.ObjectKind,
			&tier,
			&status,
			&rel.Confidence,
			&rel.SupportCount,
			&rel.SourceGroupCount,
			&rel.SemanticGroupKey,
			&rel.PrimarySourceGroup,
			&rel.Version,
			&validFrom,
			&validTo,
			&rel.RecordedAt,
			&recordedTo,
			&rel.CreatedAt,
			&rel.UpdatedAt,
			&support.EvidenceIndex,
			&support.Quote,
			&support.CreatedAt,
		); err != nil {
			return nil, nil, err
		}
		rel.SubjectEntityKind = domain.SemanticEntityKind(subjectKind)
		rel.ObjectEntityKind = domain.SemanticEntityKind(objectEntityKind)
		rel.Tier = domain.SemanticRelationshipTier(tier)
		rel.Status = domain.SemanticRelationshipStatus(status)
		rel.ValidFrom = sqlTimePtr(validFrom)
		rel.ValidTo = sqlTimePtr(validTo)
		rel.RecordedTo = sqlTimePtr(recordedTo)
		support.TeamID = rel.TeamID
		support.RelationshipID = rel.RelationshipID
		support.FragmentID = evidenceID
		relationshipsByEvidenceID[evidenceID] = append(relationshipsByEvidenceID[evidenceID], rel)
		supportsByEvidenceID[evidenceID] = append(supportsByEvidenceID[evidenceID], support)
	}
	return relationshipsByEvidenceID, supportsByEvidenceID, rows.Err()
}
