package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
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
		vectorState, err = relationshipProjectionSearchState(ctx, tx, input.TeamID)
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
		if hit.SearchState == string(domain.SearchProjectionPending) {
			searchState = string(domain.SearchProjectionPending)
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

func relationshipProjectionSearchState(ctx context.Context, tx *gorm.DB, teamID string) (string, error) {
	var latestState string
	var eligibleCount, currentCount, pendingCount, failedCount int64
	err := tx.WithContext(ctx).Raw(`
		WITH latest_generation AS (
		    SELECT generation.state
		    FROM search_projection_generations AS generation
		    WHERE generation.team_id = ?::uuid
		      AND generation.source_kind = 'relationship'
		      AND generation.projection_format_version = 2
		    ORDER BY generation.generation DESC, generation.created_at DESC
		    LIMIT 1
		),
		eligible AS (
		    SELECT relationship.relationship_id
		    FROM relationship_records AS relationship
		    WHERE relationship.team_id = ?::uuid
		      AND relationship.identity_alias_of_relationship_id IS NULL
		      AND relationship.status = 'active'
		      AND relationship.support_count > 0
		)
		SELECT COALESCE((SELECT state FROM latest_generation), '') AS latest_state,
		       COUNT(eligible.relationship_id) AS eligible_count,
		       COUNT(document.search_document_id) FILTER (WHERE document.search_state = 'current') AS current_count,
		       COUNT(document.search_document_id) FILTER (WHERE document.search_state = 'pending') AS pending_count,
		       COUNT(document.search_document_id) FILTER (WHERE document.search_state = 'failed') AS failed_count
		FROM eligible
		LEFT JOIN search_documents AS document
		  ON document.team_id = ?::uuid
		 AND document.source_kind = 'relationship'
		 AND document.source_id = eligible.relationship_id
		 AND document.projection_format_version = 2
		 AND document.projection_generation_id IS NULL
	`, teamID, teamID, teamID).Row().Scan(
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
		return string(domain.SearchProjectionCurrent), nil
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

func searchRecallRelationshipFullText(
	ctx context.Context,
	tx *gorm.DB,
	input RecallRelationshipsInput,
	contract *ActiveSearchContract,
	limit int,
) ([]SearchHit, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT document.team_id::text, document.search_document_id::text, document.source_kind,
		       document.source_id::text, document.source_version, document.document_version,
		       document.embedding_contract_id::text, document.search_state,
		       0::double precision AS distance,
		       ts_rank_cd(document.search_tsv, plainto_tsquery('simple', ?))::double precision AS text_rank
		FROM search_documents AS document
		WHERE document.team_id = ?::uuid
		  AND document.source_kind = 'relationship'
		  AND document.embedding_contract_id = ?::uuid
		  AND document.projection_format_version = 2
		  AND document.search_state IN ('pending', 'current')
		  AND document.search_tsv @@ plainto_tsquery('simple', ?)
		ORDER BY text_rank DESC, document.updated_at DESC, document.search_document_id ASC
		LIMIT ?
	`, input.Query, input.TeamID, contract.EmbeddingContractID, input.Query, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchHits(rows)
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
	case string(domain.VectorIndexVectorHNSW), string(domain.VectorIndexHalfvecHNSW):
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
		     document.projection_generation_id IS NULL
		     OR document.projection_generation_id = current_generation.projection_generation_id
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
			     document.projection_generation_id IS NULL
			     OR document.projection_generation_id = current_generation.projection_generation_id
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
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT document.team_id::text, document.search_document_id::text, document.source_kind,
		       document.source_id::text, document.source_version, document.document_version,
		       document.embedding_contract_id::text, document.search_state,
		       0::double precision AS distance,
		       0::double precision AS text_rank
		FROM relationship_records AS relationship
		JOIN search_documents AS document
		  ON document.team_id = relationship.team_id
		 AND document.source_kind = 'relationship'
		 AND document.source_id = relationship.relationship_id
		 AND document.embedding_contract_id = ?::uuid
		 AND document.projection_format_version = 2
		 AND document.search_state IN ('pending', 'current')
		WHERE relationship.team_id = ?::uuid
		  AND relationship.identity_alias_of_relationship_id IS NULL
		  AND relationship.status = 'active'
		  AND relationship.support_count > 0
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
	`, contract.EmbeddingContractID, input.TeamID,
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
	rows, err := tx.WithContext(ctx).Raw(`
		WITH requested AS (
			SELECT unnest(?::uuid[]) AS relationship_id
		),
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
		    JOIN search_documents AS document
		      ON document.team_id = relationship.team_id
		     AND document.source_kind = 'relationship'
		     AND document.source_id = relationship.relationship_id
		     AND document.embedding_contract_id = ?::uuid
		     AND document.projection_format_version = 2
		     AND document.search_state IN ('pending', 'current')
		    LEFT JOIN known_groups
		      ON known_groups.semantic_group_key = relationship.semantic_group_key
		    WHERE relationship.identity_alias_of_relationship_id IS NULL
		      AND relationship.status = 'active'
		      AND relationship.support_count > 0
		      AND known_groups.semantic_group_key IS NULL
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
	`, pq.Array(relationshipIDs), input.TeamID,
		pq.Array(input.KnownRelationshipIDs), pq.Array(input.KnownRelationshipIDs),
		input.TeamID, contract.EmbeddingContractID,
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
	return out, rows.Err()
}

func normalizeRecallRelationshipsInput(input RecallRelationshipsInput) RecallRelationshipsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	input.KnownRelationshipIDs = normalizeRecallUUIDList(input.KnownRelationshipIDs)
	input.ExpandFromEntityIDs = normalizeRecallUUIDList(input.ExpandFromEntityIDs)
	if input.Limit <= 0 {
		input.Limit = defaultRelationshipRecallLimit
	}
	if input.Limit > maxRelationshipRecallLimit {
		input.Limit = maxRelationshipRecallLimit
	}
	return input
}

func validateRecallRelationshipsInput(input RecallRelationshipsInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.Query == "" && len(input.ExpandFromEntityIDs) == 0 {
		return errors.New("query or expand_from_entity_ids is required")
	}
	for label, values := range map[string][]string{
		"known_relationship_ids": input.KnownRelationshipIDs,
		"expand_from_entity_ids": input.ExpandFromEntityIDs,
	} {
		for _, value := range values {
			if _, err := uuid.Parse(value); err != nil {
				return fmt.Errorf("%s contains invalid UUID %q: %w", label, value, err)
			}
		}
	}
	return nil
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
