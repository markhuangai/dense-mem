package repository

import (
	"context"
	"fmt"
	"sort"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *SearchRepositoryImpl) RecallRelationships(ctx context.Context, input RecallRelationshipsInput) (*RecallRelationshipsResult, error) {
	input = normalizeRecallRelationshipsInput(input)
	if err := validateRecallRelationshipsInput(input); err != nil {
		return nil, err
	}
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	overfetch := recallOverfetchLimit(input.Limit)
	acc := map[string]*relationshipRecallCandidate{}
	var textHits, vectorHits, expansionHits []SearchHit
	vectorState := string(domain.SearchProjectionPending)
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		vectorState, err = relationshipProjectionSearchState(ctx, tx, input.TeamID, contract)
		if err != nil {
			return err
		}
		if input.Query != "" {
			textHits, err = searchRecallRelationshipFullText(ctx, tx, input, contract, overfetch)
			if err != nil {
				return err
			}
		}
		if len(input.QueryEmbedding) > 0 && vectorState == string(domain.SearchProjectionCurrent) {
			vectorHits, err = searchRecallRelationshipVector(ctx, tx, input, contract, overfetch)
			if err != nil {
				return err
			}
		}
		if len(input.ExpandFromEntityIDs) > 0 {
			expansionHits, err = searchRecallRelationshipEntityExpansion(ctx, tx, input, contract, overfetch)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("recall: search relationships: %w", err)
	}
	knownRelationships := recallStringSet(input.KnownRelationshipIDs)
	addRecallRelationshipBranch(acc, textHits, knownRelationships, 1)
	addRecallRelationshipBranch(acc, vectorHits, knownRelationships, 1)
	addRecallRelationshipBranch(acc, expansionHits, knownRelationships, 0.5)
	candidates := sortedRecallRelationshipCandidates(acc)
	if len(candidates) == 0 {
		return &RecallRelationshipsResult{
			TeamID:        input.TeamID,
			SearchState:   vectorState,
			VectorOmitted: len(input.QueryEmbedding) > 0 && vectorState != string(domain.SearchProjectionCurrent),
			Results:       []RecallRelationshipHit{},
		}, nil
	}
	candidateIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidateIDs = append(candidateIDs, candidate.RelationshipID)
	}
	hydrated := map[string]RecallRelationshipHit{}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		hydrated, err = hydrateRecallRelationships(ctx, tx, input, contract, candidateIDs)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("recall: hydrate relationships: %w", err)
	}
	results := make([]RecallRelationshipHit, 0, input.Limit)
	seenGroups := map[string]struct{}{}
	searchState := vectorState
	for _, candidate := range candidates {
		hit, ok := hydrated[candidate.RelationshipID]
		if !ok {
			continue
		}
		if hit.SemanticGroupKey != "" {
			if _, seen := seenGroups[hit.SemanticGroupKey]; seen {
				continue
			}
			seenGroups[hit.SemanticGroupKey] = struct{}{}
		}
		hit.Score = candidate.Score
		hit.SearchState = recallCombinedSearchState(candidate.SearchState, hit.SearchState)
		if hit.SearchState == string(domain.SearchProjectionPending) || hit.SearchState == string(domain.SearchProjectionFailed) {
			searchState = recallCombinedSearchState(searchState, hit.SearchState)
		}
		results = append(results, hit)
		if len(results) == input.Limit {
			break
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if !results[i].CreatedAt.Equal(results[j].CreatedAt) {
			return results[i].CreatedAt.Before(results[j].CreatedAt)
		}
		return results[i].RelationshipID < results[j].RelationshipID
	})
	for i := range results {
		results[i].Rank = i + 1
	}
	return &RecallRelationshipsResult{
		TeamID:        input.TeamID,
		SearchState:   searchState,
		VectorOmitted: len(input.QueryEmbedding) > 0 && vectorState != string(domain.SearchProjectionCurrent),
		Results:       results,
	}, nil
}

type relationshipRecallCandidate struct {
	RelationshipID string
	Score          float64
	BestBranchRank int
	SearchState    string
}

func relationshipProjectionSearchState(ctx context.Context, tx *gorm.DB, teamID string, contract *ActiveSearchContract) (string, error) {
	var latestState string
	var eligibleCount, currentCount, pendingCount, failedCount int64
	err := tx.WithContext(ctx).Raw(`
	WITH `+recallRelationshipGenerationScopeSQL+`,
	selected_generation AS (
	    SELECT generation.state, scope.projection_generation_id
	    FROM recall_relationship_generation AS scope
	    LEFT JOIN search_projection_generations AS generation
	      ON generation.team_id = ?::uuid
	     AND generation.projection_generation_id = scope.projection_generation_id
	),
	eligible AS (
		    SELECT relationship.relationship_id
		    FROM relationship_records AS relationship
		    WHERE relationship.team_id = ?::uuid
		      AND relationship.identity_alias_of_relationship_id IS NULL
		      AND relationship.status = 'active'
		      AND relationship.support_count > 0
		)
	SELECT COALESCE((SELECT state FROM selected_generation), '') AS latest_state,
		       COUNT(eligible.relationship_id) AS eligible_count,
		       COUNT(document.search_document_id) FILTER (WHERE document.search_state = 'current') AS current_count,
		       COUNT(document.search_document_id) FILTER (WHERE document.search_state = 'pending') AS pending_count,
		       COUNT(document.search_document_id) FILTER (WHERE document.search_state = 'failed') AS failed_count
		FROM eligible
		 LEFT JOIN search_documents AS document
		  ON document.team_id = ?::uuid
		 AND document.source_kind = 'relationship'
		 AND document.source_id = eligible.relationship_id
		 AND document.embedding_contract_id = ?::uuid
		 AND document.embedding_dimensions = ?
			 AND document.projection_format_version = 2
			 AND (
	             document.projection_generation_id = (SELECT projection_generation_id FROM selected_generation)
	             OR (
	                 document.projection_generation_id IS NULL
	                 AND (
	                     (SELECT projection_generation_id FROM selected_generation) IS NULL
	                     OR COALESCE(document.metadata->>'`+relationshipForegroundRecallGenerationMetadataKey+`', '') = (SELECT projection_generation_id::text FROM selected_generation)
	                 )
	             )
	         )
	`, teamID, teamID, teamID, teamID, contract.EmbeddingContractID, contract.EmbeddingDimensions).Row().Scan(
		&latestState,
		&eligibleCount,
		&currentCount,
		&pendingCount,
		&failedCount,
	)
	if err != nil {
		return "", err
	}
	switch latestState {
	case "current":
		if eligibleCount == 0 || currentCount == eligibleCount {
			return string(domain.SearchProjectionCurrent), nil
		}
	case "failed":
		return string(domain.SearchProjectionFailed), nil
	case "":
		if eligibleCount == 0 || currentCount == eligibleCount {
			return string(domain.SearchProjectionCurrent), nil
		}
		if failedCount > 0 && pendingCount == 0 {
			return string(domain.SearchProjectionFailed), nil
		}
	}
	return string(domain.SearchProjectionPending), nil
}

func searchRecallRelationshipVector(
	ctx context.Context,
	tx *gorm.DB,
	input RecallRelationshipsInput,
	contract *ActiveSearchContract,
	limit int,
) ([]SearchHit, error) {
	switch contract.IndexStrategy {
	case string(domain.VectorIndexExact):
		return searchRecallRelationshipExactVector(ctx, tx, input, contract, limit)
	case string(domain.VectorIndexVectorHNSW), string(domain.VectorIndexHalfvecHNSW), string(domain.VectorIndexBinaryHNSW):
		return searchRecallRelationshipANNVector(ctx, tx, input, contract, limit)
	default:
		return nil, fmt.Errorf("%w: unsupported relationship recall vector index strategy %q", ErrSearchContractMismatch, contract.IndexStrategy)
	}
}

func searchRecallRelationshipExactVector(
	ctx context.Context,
	tx *gorm.DB,
	input RecallRelationshipsInput,
	contract *ActiveSearchContract,
	limit int,
) ([]SearchHit, error) {
	if len(input.QueryEmbedding) != contract.EmbeddingDimensions {
		return nil, fmt.Errorf("%w: contract dimensions %d, query dimensions %d", ErrSearchContractMismatch, contract.EmbeddingDimensions, len(input.QueryEmbedding))
	}
	if contract.IndexStrategy != string(domain.VectorIndexExact) && !contract.AllowExactFallback {
		return nil, fmt.Errorf("%w: active search contract does not allow exact vector search", ErrSearchContractMismatch)
	}
	if contract.DistanceMetric != string(domain.VectorDistanceCosine) {
		return nil, fmt.Errorf("%w: exact vector search supports %s distance only", ErrSearchContractMismatch, domain.VectorDistanceCosine)
	}
	if contract.ExactMaxRows > 0 {
		var candidateCount int64
		if err := tx.WithContext(ctx).Raw(`
			WITH generation_count AS (
			    SELECT count(*) AS value
			    FROM search_projection_generations
			    WHERE team_id = ?::uuid
			      AND source_kind = 'relationship'
			      AND projection_format_version = 2
			),
			current_generation AS (
			    SELECT choice.projection_generation_id
			    FROM (
			        SELECT projection_generation_id, 0 AS priority, generation
			        FROM search_projection_generations
			        WHERE team_id = ?::uuid
			          AND source_kind = 'relationship'
			          AND projection_format_version = 2
			          AND state = 'current'
			        UNION ALL
			        SELECT NULL::uuid, 1 AS priority, 0 AS generation
			        FROM generation_count
			        WHERE value = 0
			    ) AS choice
			    ORDER BY choice.priority ASC, choice.generation DESC
			    LIMIT 1
			)
			SELECT count(*)
			FROM (
			    SELECT document.search_document_id
			    FROM current_generation
			    JOIN search_documents AS document
			      ON document.team_id = ?::uuid
			     AND document.source_kind = 'relationship'
			     AND document.embedding_contract_id = ?::uuid
			     AND document.embedding_dimensions = ?
				     AND document.projection_format_version = 2
				     AND (
				         document.projection_generation_id = current_generation.projection_generation_id
				         OR (
				             document.projection_generation_id IS NULL
				             AND (
				                 current_generation.projection_generation_id IS NULL
				                 OR COALESCE(document.metadata->>'`+relationshipForegroundRecallGenerationMetadataKey+`', '') = current_generation.projection_generation_id::text
				             )
				         )
				     )
			     AND document.search_state = 'current'
			     AND document.embedding IS NOT NULL
			    LIMIT ?
			) AS exact_candidates
		`, input.TeamID, input.TeamID, input.TeamID, contract.EmbeddingContractID, contract.EmbeddingDimensions, contract.ExactMaxRows+1).Scan(&candidateCount).Error; err != nil {
			return nil, err
		}
		if candidateCount > int64(contract.ExactMaxRows) {
			return nil, fmt.Errorf("%w: exact vector candidates %d exceed contract max %d", ErrSearchContractMismatch, candidateCount, contract.ExactMaxRows)
		}
	}
	vectorLiteral, err := vectorLiteral(input.QueryEmbedding)
	if err != nil {
		return nil, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH generation_count AS (
		    SELECT count(*) AS value
		    FROM search_projection_generations
		    WHERE team_id = ?::uuid
		      AND source_kind = 'relationship'
		      AND projection_format_version = 2
		),
		current_generation AS (
		    SELECT choice.projection_generation_id
		    FROM (
		        SELECT projection_generation_id, 0 AS priority, generation
		        FROM search_projection_generations
		        WHERE team_id = ?::uuid
		          AND source_kind = 'relationship'
		          AND projection_format_version = 2
		          AND state = 'current'
		        UNION ALL
		        SELECT NULL::uuid, 1 AS priority, 0 AS generation
		        FROM generation_count
		        WHERE value = 0
		    ) AS choice
		    ORDER BY choice.priority ASC, choice.generation DESC
		    LIMIT 1
		)
		SELECT document.team_id::text, document.search_document_id::text, document.source_kind,
		       document.source_id::text, document.source_version, document.document_version,
		       document.embedding_contract_id::text, document.search_state,
		       (document.embedding <=> ?::vector)::double precision AS distance,
		       0::double precision AS text_rank
		FROM current_generation
		JOIN search_documents AS document
		  ON document.team_id = ?::uuid
		 AND document.source_kind = 'relationship'
		 AND document.embedding_contract_id = ?::uuid
			 AND document.embedding_dimensions = ?
			 AND document.projection_format_version = 2
			 AND (
			     document.projection_generation_id = current_generation.projection_generation_id
			     OR (
			         document.projection_generation_id IS NULL
			         AND (
			             current_generation.projection_generation_id IS NULL
			             OR COALESCE(document.metadata->>'`+relationshipForegroundRecallGenerationMetadataKey+`', '') = current_generation.projection_generation_id::text
			         )
			     )
			 )
		 AND document.search_state = 'current'
		 AND document.embedding IS NOT NULL
		ORDER BY document.embedding <=> ?::vector ASC, document.search_document_id ASC
		LIMIT ?
	`, input.TeamID, input.TeamID, vectorLiteral, input.TeamID, contract.EmbeddingContractID,
		contract.EmbeddingDimensions, vectorLiteral, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchHits(rows)
}

func searchRecallRelationshipANNVector(
	ctx context.Context,
	tx *gorm.DB,
	input RecallRelationshipsInput,
	contract *ActiveSearchContract,
	limit int,
) ([]SearchHit, error) {
	if len(input.QueryEmbedding) != contract.EmbeddingDimensions {
		return nil, fmt.Errorf("%w: contract dimensions %d, query dimensions %d", ErrSearchContractMismatch, contract.EmbeddingDimensions, len(input.QueryEmbedding))
	}
	annDistance, err := recallANNDistanceExpression(contract)
	if err != nil {
		return nil, err
	}
	contractLiteral, err := recallEmbeddingContractLiteral(contract.EmbeddingContractID)
	if err != nil {
		return nil, err
	}
	vectorLiteral, err := vectorLiteral(input.QueryEmbedding)
	if err != nil {
		return nil, err
	}
	candidateLimit := recallANNCandidateLimit(contract, limit)
	if err := setRecallANNQueryEFSearch(ctx, tx, contract, candidateLimit); err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
		WITH generation_count AS (
		    SELECT count(*) AS value
		    FROM search_projection_generations
		    WHERE team_id = ?::uuid
		      AND source_kind = 'relationship'
		      AND projection_format_version = 2
		),
		current_generation AS (
		    SELECT choice.projection_generation_id
		    FROM (
		        SELECT projection_generation_id, 0 AS priority, generation
		        FROM search_projection_generations
		        WHERE team_id = ?::uuid
		          AND source_kind = 'relationship'
		          AND projection_format_version = 2
		          AND state = 'current'
		        UNION ALL
		        SELECT NULL::uuid, 1 AS priority, 0 AS generation
		        FROM generation_count
		        WHERE value = 0
		    ) AS choice
		    ORDER BY choice.priority ASC, choice.generation DESC
		    LIMIT 1
		),
		ann_candidates AS MATERIALIZED (
			SELECT document.team_id, document.search_document_id
			FROM current_generation
			JOIN search_documents AS document
			  ON document.team_id = ?::uuid
			 AND document.source_kind = 'relationship'
			 AND document.embedding_contract_id = %s::uuid
				 AND document.embedding_dimensions = %d
				 AND document.projection_format_version = 2
				 AND (
				     document.projection_generation_id = current_generation.projection_generation_id
				     OR (
				         document.projection_generation_id IS NULL
				         AND (
				             current_generation.projection_generation_id IS NULL
				             OR COALESCE(document.metadata->>'`+relationshipForegroundRecallGenerationMetadataKey+`', '') = current_generation.projection_generation_id::text
				         )
				     )
				 )
			 AND document.search_state = 'current'
			 AND document.embedding IS NOT NULL
			ORDER BY %s ASC, document.search_document_id ASC
			LIMIT ?
		)
		SELECT document.team_id::text,
		       document.search_document_id::text,
		       document.source_kind,
		       document.source_id::text,
		       document.source_version,
		       document.document_version,
		       document.embedding_contract_id::text,
		       document.search_state,
		       (document.embedding <=> ?::vector)::double precision AS distance,
		       0::double precision AS text_rank
		FROM ann_candidates AS candidate
		JOIN search_documents AS document
		  ON document.team_id = candidate.team_id
		 AND document.search_document_id = candidate.search_document_id
		ORDER BY document.embedding <=> ?::vector ASC, document.search_document_id ASC
		LIMIT ?
	`, contractLiteral, contract.EmbeddingDimensions, annDistance)
	rows, err := tx.WithContext(ctx).Raw(
		query,
		input.TeamID,
		input.TeamID,
		input.TeamID,
		vectorLiteral,
		candidateLimit,
		vectorLiteral,
		vectorLiteral,
		limit,
	).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchHits(rows)
}

func searchRecallRelationshipEntityExpansion(
	ctx context.Context,
	tx *gorm.DB,
	input RecallRelationshipsInput,
	contract *ActiveSearchContract,
	limit int,
) ([]SearchHit, error) {
	eventAt := recallEventAt(input.ValidAt, input.KnownAt)
	rows, err := tx.WithContext(ctx).Raw(`
		WITH `+recallRelationshipGenerationScopeSQL+`
		SELECT document.team_id::text, document.search_document_id::text, document.source_kind,
		       document.source_id::text, document.source_version, document.document_version,
		       document.embedding_contract_id::text, document.search_state,
		       0::double precision AS distance,
		       0::double precision AS text_rank
		FROM recall_relationship_generation AS generation
		JOIN relationship_records AS relationship
		  ON relationship.team_id = ?::uuid
		JOIN search_documents AS document
		  ON document.team_id = relationship.team_id
		 AND document.source_kind = 'relationship'
		 AND document.source_id = relationship.relationship_id
		 AND document.embedding_contract_id = ?::uuid
		 AND document.projection_format_version = 2
		 AND `+recallRelationshipGenerationDocumentSQL+`
			 AND (document.search_state IN ('pending', 'current', 'failed') OR (?::timestamptz IS NOT NULL AND document.search_state = 'not_required'))
		LEFT JOIN LATERAL (
		    SELECT transition.to_status AS status
		    FROM relationship_transition_events AS transition
		    WHERE ?::timestamptz IS NOT NULL
		      AND transition.team_id = relationship.team_id
		      AND transition.relationship_id = relationship.relationship_id
		      AND transition.created_at <= ?::timestamptz
		    ORDER BY transition.created_at DESC, transition.transition_id DESC
		    LIMIT 1
		) AS known_status ON TRUE
		WHERE relationship.identity_alias_of_relationship_id IS NULL
		  AND (
		      COALESCE(known_status.status, relationship.status) = 'active'
		      OR (
		          COALESCE(known_status.status, relationship.status) = 'superseded'
		          AND (
		              (?::timestamptz IS NOT NULL
		               AND (relationship.valid_from IS NULL OR relationship.valid_from <= ?::timestamptz)
		               AND (relationship.valid_to IS NULL OR relationship.valid_to > ?::timestamptz))
		              OR (?::timestamptz IS NOT NULL
		                  AND relationship.created_at <= ?::timestamptz
		                  AND (relationship.recorded_to IS NULL OR relationship.recorded_to > ?::timestamptz))
		          )
		      )
		  )
		  AND (?::timestamptz IS NOT NULL OR relationship.support_count > 0)
		  AND (
		      relationship.subject_entity_id = ANY(?::uuid[])
		      OR relationship.object_entity_id = ANY(?::uuid[])
		  )
		  AND (
		      ?::timestamptz IS NULL
		      OR ((relationship.valid_from IS NULL OR relationship.valid_from <= ?::timestamptz)
		          AND (relationship.valid_to IS NULL OR relationship.valid_to > ?::timestamptz))
		  )
		  AND (
		      ?::timestamptz IS NULL
		      OR (relationship.created_at <= ?::timestamptz
		          AND (relationship.recorded_to IS NULL OR relationship.recorded_to > ?::timestamptz))
		  )
		  AND (
		      cardinality(?::uuid[]) = 0
		      OR relationship.relationship_id <> ALL(?::uuid[])
		  )
		ORDER BY relationship.updated_at DESC, relationship.relationship_id ASC
		LIMIT ?
	`, input.TeamID, input.TeamID, contract.EmbeddingContractID, eventAt,
		eventAt, eventAt,
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt,
		eventAt,
		pq.Array(input.ExpandFromEntityIDs), pq.Array(input.ExpandFromEntityIDs),
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt,
		pq.Array(input.KnownRelationshipIDs), pq.Array(input.KnownRelationshipIDs),
		limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchHits(rows)
}

func hydrateRecallRelationships(
	ctx context.Context,
	tx *gorm.DB,
	input RecallRelationshipsInput,
	contract *ActiveSearchContract,
	relationshipIDs []string,
) (map[string]RecallRelationshipHit, error) {
	eventAt := recallEventAt(input.ValidAt, input.KnownAt)
	rows, err := tx.WithContext(ctx).Raw(`
		WITH requested AS (
			SELECT unnest(?::uuid[]) AS relationship_id
		),
		`+recallRelationshipGenerationScopeSQL+`,
		known_groups AS (
			SELECT DISTINCT relationship.semantic_group_key
			FROM relationship_records AS relationship
			WHERE relationship.team_id = ?::uuid
			  AND cardinality(?::uuid[]) > 0
			  AND relationship.relationship_id = ANY(?::uuid[])
		)
		SELECT relationship.team_id::text,
		       relationship.relationship_id::text,
		       relationship.semantic_group_key,
		       relationship.subject_entity_id::text,
		       COALESCE(NULLIF(subject_name.display_name, ''), relationship.subject_entity_id::text) AS subject_name,
		       relationship.predicate_key,
		       COALESCE(relationship.object_entity_id::text, '') AS object_entity_id,
		       COALESCE(relationship.object_value_id::text, '') AS object_value_id,
		       COALESCE(NULLIF(object_name.display_name, ''), NULLIF(value_record.display, ''), value_record.canonical_value, '') AS object_name,
		       COALESCE(value_record.value_type, '') AS object_value_type,
		       COALESCE(value_record.canonical_value, '') AS object_value,
		       relationship.polarity,
		       COALESCE(relationship.scope_key, '') AS scope_key,
		       relationship.valid_from,
		       relationship.search_state,
		       relationship.support_count,
		       relationship.source_group_count,
		       relationship.created_at
		FROM (
		    SELECT relationship.*,
		           document.search_state
		    FROM requested
		    JOIN relationship_records AS relationship
		      ON relationship.team_id = ?::uuid
		     AND relationship.relationship_id = requested.relationship_id
		    JOIN recall_relationship_generation AS generation
		      ON TRUE
		    JOIN search_documents AS document
		      ON document.team_id = relationship.team_id
		     AND document.source_kind = 'relationship'
		     AND document.source_id = relationship.relationship_id
		     AND document.embedding_contract_id = ?::uuid
		     AND document.projection_format_version = 2
		     AND `+recallRelationshipGenerationDocumentSQL+`
			 AND (document.search_state IN ('pending', 'current', 'failed') OR (?::timestamptz IS NOT NULL AND document.search_state = 'not_required'))
		    LEFT JOIN LATERAL (
		        SELECT transition.to_status AS status
		        FROM relationship_transition_events AS transition
		        WHERE ?::timestamptz IS NOT NULL
		          AND transition.team_id = relationship.team_id
		          AND transition.relationship_id = relationship.relationship_id
		          AND transition.created_at <= ?::timestamptz
		        ORDER BY transition.created_at DESC, transition.transition_id DESC
		        LIMIT 1
		    ) AS known_status ON TRUE
		    LEFT JOIN known_groups
		      ON known_groups.semantic_group_key = relationship.semantic_group_key
		    WHERE relationship.identity_alias_of_relationship_id IS NULL
		      AND (
		          COALESCE(known_status.status, relationship.status) = 'active'
		          OR (
		              COALESCE(known_status.status, relationship.status) = 'superseded'
		              AND (
		                  (?::timestamptz IS NOT NULL
		                   AND (relationship.valid_from IS NULL OR relationship.valid_from <= ?::timestamptz)
		                   AND (relationship.valid_to IS NULL OR relationship.valid_to > ?::timestamptz))
		                  OR (?::timestamptz IS NOT NULL
		                      AND relationship.created_at <= ?::timestamptz
		                      AND (relationship.recorded_to IS NULL OR relationship.recorded_to > ?::timestamptz))
		              )
		          )
		      )
		      AND (?::timestamptz IS NOT NULL OR relationship.support_count > 0)
				AND known_groups.semantic_group_key IS NULL
				AND (cardinality(?::text[]) = 0 OR relationship.semantic_group_key <> ALL(?::text[]))
		      AND (
		          ?::timestamptz IS NULL
		          OR ((relationship.valid_from IS NULL OR relationship.valid_from <= ?::timestamptz)
		              AND (relationship.valid_to IS NULL OR relationship.valid_to > ?::timestamptz))
		      )
		      AND (
		          ?::timestamptz IS NULL
		          OR (relationship.created_at <= ?::timestamptz
		              AND (relationship.recorded_to IS NULL OR relationship.recorded_to > ?::timestamptz))
		      )
		) AS relationship
		LEFT JOIN entity_names AS subject_name
		  ON subject_name.team_id = relationship.team_id
		 AND subject_name.entity_id = relationship.subject_entity_id
		 AND subject_name.name_kind = 'canonical'
		 AND subject_name.valid_to IS NULL
		LEFT JOIN entity_names AS object_name
		  ON object_name.team_id = relationship.team_id
		 AND object_name.entity_id = relationship.object_entity_id
		 AND object_name.name_kind = 'canonical'
		 AND object_name.valid_to IS NULL
		LEFT JOIN value_records AS value_record
		  ON value_record.team_id = relationship.team_id
		 AND value_record.value_id = relationship.object_value_id
			`, pq.Array(relationshipIDs), input.TeamID, input.TeamID,
		pq.Array(input.KnownRelationshipIDs), pq.Array(input.KnownRelationshipIDs),
		input.TeamID, contract.EmbeddingContractID, eventAt,
		eventAt, eventAt,
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt,
		eventAt,
		pq.Array(input.ExcludedGroupKeys), pq.Array(input.ExcludedGroupKeys),
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]RecallRelationshipHit)
	for rows.Next() {
		var hit RecallRelationshipHit
		if err := rows.Scan(
			&hit.TeamID,
			&hit.RelationshipID,
			&hit.SemanticGroupKey,
			&hit.SubjectEntityID,
			&hit.SubjectName,
			&hit.PredicateKey,
			&hit.ObjectEntityID,
			&hit.ObjectValueID,
			&hit.ObjectName,
			&hit.ObjectValueType,
			&hit.ObjectValue,
			&hit.Polarity,
			&hit.ScopeKey,
			&hit.ValidFrom,
			&hit.SearchState,
			&hit.SupportCount,
			&hit.SourceGroupCount,
			&hit.CreatedAt,
		); err != nil {
			return nil, err
		}
		out[hit.RelationshipID] = hit
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := hydrateRecallRelationshipEvidenceIDs(ctx, tx, input, out); err != nil {
		return nil, err
	}
	for relationshipID, hit := range out {
		if len(hit.EvidenceIDs) == 0 || recallEvidenceOverlaps(hit.EvidenceIDs, input.KnownEvidenceIDs) {
			delete(out, relationshipID)
		}
	}
	if err := hydrateRecallRelationshipEquivalents(ctx, tx, input, out); err != nil {
		return nil, err
	}
	return out, nil
}

func recallEvidenceOverlaps(evidenceIDs, knownEvidenceIDs []string) bool {
	if len(evidenceIDs) == 0 || len(knownEvidenceIDs) == 0 {
		return false
	}
	known := make(map[string]struct{}, len(knownEvidenceIDs))
	for _, evidenceID := range knownEvidenceIDs {
		known[evidenceID] = struct{}{}
	}
	for _, evidenceID := range evidenceIDs {
		if _, ok := known[evidenceID]; ok {
			return true
		}
	}
	return false
}

func hydrateRecallRelationshipEvidenceIDs(ctx context.Context, tx *gorm.DB, input RecallRelationshipsInput, hits map[string]RecallRelationshipHit) error {
	if len(hits) == 0 {
		return nil
	}
	eventAt := recallEventAt(input.ValidAt, input.KnownAt)
	relationshipIDs := make([]string, 0, len(hits))
	for relationshipID := range hits {
		relationshipIDs = append(relationshipIDs, relationshipID)
	}
	sort.Strings(relationshipIDs)
	rows, err := tx.WithContext(ctx).Raw(`
		WITH requested AS (
		    SELECT unnest(?::uuid[]) AS relationship_id
		),
		latest AS (
		    SELECT DISTINCT ON (team_id, support_id)
		           team_id, support_id, decision
		    FROM relationship_support_decision_events
		    WHERE team_id = ?::uuid
		      AND (?::timestamptz IS NULL OR created_at <= ?::timestamptz)
		    ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
		),
		effective_support AS (
		    SELECT support.relationship_id::text,
		           support.fragment_id::text,
		           support.created_at
		    FROM requested
		    JOIN relationship_evidence_supports AS support
		      ON support.team_id = ?::uuid
		     AND support.relationship_id = requested.relationship_id
		    JOIN latest
		      ON latest.team_id = support.team_id
		     AND latest.support_id = support.support_id
		     AND latest.decision IN ('grant', 'reinstate')
		    LEFT JOIN evidence_quarantines AS quarantine
		      ON quarantine.team_id = support.team_id
		     AND quarantine.fragment_id = support.fragment_id
		     AND quarantine.status = 'active'
		    LEFT JOIN evidence_sources AS source
		      ON source.team_id = support.team_id
		     AND source.source_id = support.source_id
		    WHERE quarantine.quarantine_id IS NULL
		      AND (?::timestamptz IS NULL OR support.created_at <= ?::timestamptz)
		      AND (
		          support.source_id IS NULL
		          OR source.current_revision_id = support.source_revision_id
		      )
		)
		SELECT relationship_id,
		       array_agg(fragment_id ORDER BY created_at ASC, fragment_id ASC)::text[] AS evidence_ids
		FROM effective_support
		GROUP BY relationship_id
	`, pq.Array(relationshipIDs), input.TeamID, eventAt, eventAt, input.TeamID, eventAt, eventAt).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var relationshipID string
		var evidenceIDs []string
		if err := rows.Scan(&relationshipID, pq.Array(&evidenceIDs)); err != nil {
			return err
		}
		hit := hits[relationshipID]
		hit.EvidenceIDs = evidenceIDs
		hits[relationshipID] = hit
	}
	return rows.Err()
}

func hydrateRecallRelationshipEquivalents(ctx context.Context, tx *gorm.DB, input RecallRelationshipsInput, hits map[string]RecallRelationshipHit) error {
	if len(hits) == 0 {
		return nil
	}
	eventAt := recallEventAt(input.ValidAt, input.KnownAt)
	relationshipIDs := make([]string, 0, len(hits))
	for relationshipID := range hits {
		relationshipIDs = append(relationshipIDs, relationshipID)
	}
	sort.Strings(relationshipIDs)
	rows, err := tx.WithContext(ctx).Raw(`
		WITH requested AS (
		    SELECT unnest(?::uuid[]) AS relationship_id
		),
		selected AS (
		    SELECT relationship.relationship_id,
		           relationship.semantic_group_key
		    FROM requested
		    JOIN relationship_records AS relationship
		      ON relationship.team_id = ?::uuid
		     AND relationship.relationship_id = requested.relationship_id
		    WHERE COALESCE(relationship.semantic_group_key, '') <> ''
		),
		equivalents AS (
		    SELECT selected.relationship_id::text AS representative_id,
		           candidate.relationship_id::text AS equivalent_id
		    FROM selected
		    JOIN relationship_records AS candidate
		      ON candidate.team_id = ?::uuid
		     AND candidate.semantic_group_key = selected.semantic_group_key
		    LEFT JOIN LATERAL (
		        SELECT transition.to_status AS status
		        FROM relationship_transition_events AS transition
		        WHERE ?::timestamptz IS NOT NULL
		          AND transition.team_id = candidate.team_id
		          AND transition.relationship_id = candidate.relationship_id
		          AND transition.created_at <= ?::timestamptz
		        ORDER BY transition.created_at DESC, transition.transition_id DESC
		        LIMIT 1
		    ) AS known_status ON TRUE
		    WHERE candidate.relationship_id <> selected.relationship_id
		      AND candidate.identity_alias_of_relationship_id IS NULL
		      AND (
		          COALESCE(known_status.status, candidate.status) = 'active'
		          OR (
		              COALESCE(known_status.status, candidate.status) = 'superseded'
		              AND (
		                  (?::timestamptz IS NOT NULL
		                   AND (candidate.valid_from IS NULL OR candidate.valid_from <= ?::timestamptz)
		                   AND (candidate.valid_to IS NULL OR candidate.valid_to > ?::timestamptz))
		                  OR (?::timestamptz IS NOT NULL
		                      AND candidate.created_at <= ?::timestamptz
		                      AND (candidate.recorded_to IS NULL OR candidate.recorded_to > ?::timestamptz))
		              )
		          )
		      )
		      AND (
		          (?::timestamptz IS NULL AND candidate.support_count > 0)
		          OR (
		              ?::timestamptz IS NOT NULL
		              AND EXISTS (
		                  SELECT 1
		                  FROM relationship_evidence_supports AS support
		                  JOIN LATERAL (
		                      SELECT decision.decision
		                      FROM relationship_support_decision_events AS decision
		                      WHERE decision.team_id = support.team_id
		                        AND decision.support_id = support.support_id
		                        AND decision.created_at <= ?::timestamptz
		                      ORDER BY decision.created_at DESC, decision.support_decision_id DESC
		                      LIMIT 1
		                  ) AS latest ON latest.decision IN ('grant', 'reinstate')
		                  LEFT JOIN evidence_quarantines AS quarantine
		                    ON quarantine.team_id = support.team_id
		                   AND quarantine.fragment_id = support.fragment_id
		                   AND quarantine.status = 'active'
		                  LEFT JOIN evidence_sources AS source
		                    ON source.team_id = support.team_id
		                   AND source.source_id = support.source_id
		                  WHERE support.team_id = candidate.team_id
		                    AND support.relationship_id = candidate.relationship_id
		                    AND support.created_at <= ?::timestamptz
		                    AND quarantine.quarantine_id IS NULL
		                    AND (
		                        support.source_id IS NULL
		                        OR source.current_revision_id = support.source_revision_id
		                    )
		              )
		          )
		      )
		      AND (
		          ?::timestamptz IS NULL
		          OR ((candidate.valid_from IS NULL OR candidate.valid_from <= ?::timestamptz)
		              AND (candidate.valid_to IS NULL OR candidate.valid_to > ?::timestamptz))
		      )
		      AND (
		          ?::timestamptz IS NULL
		          OR (candidate.created_at <= ?::timestamptz
		              AND (candidate.recorded_to IS NULL OR candidate.recorded_to > ?::timestamptz))
		      )
		)
		SELECT representative_id,
		       array_agg(equivalent_id ORDER BY equivalent_id ASC)::text[] AS equivalent_ids
		FROM equivalents
		GROUP BY representative_id
		`, pq.Array(relationshipIDs), input.TeamID, input.TeamID,
		eventAt, eventAt,
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt,
		eventAt, eventAt, eventAt, eventAt,
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var relationshipID string
		var equivalentIDs []string
		if err := rows.Scan(&relationshipID, pq.Array(&equivalentIDs)); err != nil {
			return err
		}
		hit := hits[relationshipID]
		hit.EquivalentRelationshipIDs = equivalentIDs
		hits[relationshipID] = hit
	}
	return rows.Err()
}

func addRecallRelationshipBranch(acc map[string]*relationshipRecallCandidate, hits []SearchHit, knownRelationships map[string]struct{}, weight float64) {
	for i, hit := range hits {
		if hit.SourceKind != "relationship" || hit.SourceID == "" {
			continue
		}
		if _, known := knownRelationships[hit.SourceID]; known {
			continue
		}
		branchRank := i + 1
		candidate := acc[hit.SourceID]
		if candidate == nil {
			candidate = &relationshipRecallCandidate{
				RelationshipID: hit.SourceID,
				BestBranchRank: branchRank,
				SearchState:    hit.SearchState,
			}
			acc[hit.SourceID] = candidate
		}
		candidate.Score += weight / (recallRRFConstant + float64(branchRank))
		if branchRank < candidate.BestBranchRank {
			candidate.BestBranchRank = branchRank
		}
		candidate.SearchState = recallCombinedSearchState(candidate.SearchState, hit.SearchState)
	}
}

func sortedRecallRelationshipCandidates(acc map[string]*relationshipRecallCandidate) []relationshipRecallCandidate {
	out := make([]relationshipRecallCandidate, 0, len(acc))
	for _, candidate := range acc {
		out = append(out, *candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].BestBranchRank != out[j].BestBranchRank {
			return out[i].BestBranchRank < out[j].BestBranchRank
		}
		return out[i].RelationshipID < out[j].RelationshipID
	})
	return out
}
