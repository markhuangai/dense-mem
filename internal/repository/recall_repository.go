package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	defaultRecallLimit             = 10
	maxRecallLimit                 = 50
	defaultRelationshipRecallLimit = 5
	maxRelationshipRecallLimit     = 20
	recallOverfetchMultiple        = 6
	recallOverfetchFloor           = 60
	recallOverfetchCap             = 200
	recallRRFConstant              = 60
)

var _ RecallRepository = (*SearchRepositoryImpl)(nil)

func (r *SearchRepositoryImpl) RecallEvidence(ctx context.Context, input RecallEvidenceInput) (*RecallEvidenceResult, error) {
	input = normalizeRecallEvidenceInput(input)
	if err := validateRecallEvidenceInput(input); err != nil {
		return nil, err
	}
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	overfetch := recallOverfetchLimit(input.Limit)
	acc := map[string]*recallCandidate{}
	var textHits, vectorHits, expansionHits []SearchHit
	searchState := string(domain.SearchProjectionCurrent)
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		if input.Query != "" {
			textHits, err = searchRecallFullText(ctx, tx, input, contract, overfetch)
			if err != nil {
				return err
			}
		}
		if len(input.QueryEmbedding) > 0 {
			vectorHits, err = searchRecallVector(ctx, tx, input, contract, overfetch)
			if err != nil {
				return err
			}
		}
		if len(input.ExpandFromEntityIDs) > 0 {
			expansionHits, err = searchRecallEntityExpansion(ctx, tx, input, contract, overfetch)
			if err != nil {
				return err
			}
		}
		searchState, err = recallEvidenceSearchState(ctx, tx, input, contract)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("recall: search evidence: %w", err)
	}
	knownEvidence := recallStringSet(input.KnownEvidenceIDs)
	addRecallBranch(acc, textHits, knownEvidence, 1)
	addRecallBranch(acc, vectorHits, knownEvidence, 1)
	addRecallBranch(acc, expansionHits, knownEvidence, 0.5)
	candidates := sortedRecallCandidates(acc)
	if len(candidates) == 0 {
		return &RecallEvidenceResult{
			TeamID:      input.TeamID,
			SearchState: searchState,
			Results:     []RecallEvidenceHit{},
		}, nil
	}
	candidateIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidateIDs = append(candidateIDs, candidate.EvidenceID)
	}
	hydrated := map[string]RecallEvidenceHit{}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		hydrated, err = hydrateRecallEvidence(ctx, tx, input, contract, candidateIDs)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("recall: hydrate evidence: %w", err)
	}
	results := make([]RecallEvidenceHit, 0)
	for _, candidate := range candidates {
		hit, ok := hydrated[candidate.EvidenceID]
		if !ok {
			continue
		}
		hit.Score = candidate.Score
		hit.SpaceKind = input.SpaceKind
		hit.SearchState = domain.CombineSearchProjectionStates(candidate.SearchState, hit.SearchState)
		if hit.SearchState == string(domain.SearchProjectionPending) || hit.SearchState == string(domain.SearchProjectionFailed) {
			searchState = domain.CombineSearchProjectionStates(searchState, hit.SearchState)
		}
		hit.Rank = len(results) + 1
		results = append(results, hit)
		if len(results) == input.Limit {
			break
		}
	}
	conflicts := []RelationshipConflictCaseRecord{}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		conflicts, err = loadRecallOpenConflictRecords(ctx, tx, input.TeamID, input.KnownAt, results)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("recall: load conflicts: %w", err)
	}
	return &RecallEvidenceResult{
		TeamID:      input.TeamID,
		SearchState: searchState,
		Results:     results,
		Conflicts:   conflicts,
	}, nil
}

func recallEvidenceSearchState(
	ctx context.Context,
	tx *gorm.DB,
	input RecallEvidenceInput,
	contract *ActiveSearchContract,
) (string, error) {
	eventAt := recallEventAt(input.ValidAt, input.KnownAt)
	spaceClause := recallSpacePredicate("document.space_id", input.TeamID, input.SpaceID, input.SpaceKind)
	var state string
	err := tx.WithContext(ctx).Raw(`
		WITH eligible AS NOT MATERIALIZED (
			SELECT document.search_state
			FROM search_documents AS document
			JOIN evidence_fragments AS fragment
			  ON fragment.team_id = document.team_id
			 AND fragment.fragment_id = document.source_id
			LEFT JOIN evidence_quarantines AS quarantine
			  ON quarantine.team_id = fragment.team_id
			 AND quarantine.fragment_id = fragment.fragment_id
			 AND quarantine.status = 'active'
			LEFT JOIN evidence_sources AS source
			  ON source.team_id = fragment.team_id
			 AND source.source_id = fragment.source_id
			WHERE document.team_id = ?::uuid
			  AND document.source_kind = 'evidence'
			  AND document.embedding_contract_id = ?::uuid
			  AND (document.search_state IN ('pending', 'current', 'failed')
			       OR (?::timestamptz IS NOT NULL AND document.search_state = 'not_required'))
			  AND quarantine.quarantine_id IS NULL
			  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
			  `+recallEvidenceAliasVisibilitySQL("fragment")+`
			  AND NOT EXISTS (
			      SELECT 1
			      FROM evidence_lifecycle_events AS lifecycle
			      WHERE lifecycle.team_id = fragment.team_id
			        AND lifecycle.target_fragment_id = fragment.fragment_id
			        AND (?::timestamptz IS NULL OR lifecycle.created_at <= ?::timestamptz)
			  )
			  `+recallEvidenceHistoricalSourceVisibilitySQL("fragment", "source")+`
			  AND (?::timestamptz IS NULL OR fragment.created_at <= ?::timestamptz)
			`+spaceClause+`
		)
		SELECT CASE
		           WHEN EXISTS (SELECT 1 FROM eligible WHERE search_state = 'failed') THEN 'failed'
		           WHEN EXISTS (SELECT 1 FROM eligible WHERE search_state = 'pending') THEN 'pending'
		           ELSE 'current'
		       END
	`, input.TeamID, contract.EmbeddingContractID, eventAt, input.KnownAt, eventAt, eventAt, input.KnownAt, eventAt, eventAt).Scan(&state).Error
	if err != nil {
		return "", err
	}
	if state == "" {
		state = string(domain.SearchProjectionCurrent)
	}
	return state, nil
}

type recallCandidate struct {
	EvidenceID  string
	Score       float64
	SearchState string
}

func searchRecallFullText(
	ctx context.Context,
	tx *gorm.DB,
	input RecallEvidenceInput,
	contract *ActiveSearchContract,
	limit int,
) ([]SearchHit, error) {
	eventAt := recallEventAt(input.ValidAt, input.KnownAt)
	spaceClause := recallSpacePredicate("search_documents.space_id", input.TeamID, input.SpaceID, input.SpaceKind)
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT search_documents.team_id::text, search_documents.search_document_id::text,
		       search_documents.source_kind, search_documents.source_id::text,
		       search_documents.source_version, search_documents.document_version,
		       search_documents.embedding_contract_id::text,
		       search_documents.search_state,
		       0::double precision AS distance,
		       ts_rank_cd(search_documents.search_tsv, plainto_tsquery('simple', ?))::double precision AS text_rank
		FROM search_documents
		JOIN evidence_fragments AS source_fragment
		  ON source_fragment.team_id = search_documents.team_id
		 AND source_fragment.fragment_id = search_documents.source_id
		WHERE search_documents.team_id = ?::uuid
		  AND search_documents.source_kind = 'evidence'
		  AND search_documents.embedding_contract_id = ?::uuid
		  AND (search_documents.search_state IN ('pending', 'current', 'failed') OR (?::timestamptz IS NOT NULL AND search_documents.search_state = 'not_required'))
		  AND COALESCE(source_fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  `+recallEvidenceAliasVisibilitySQL("source_fragment")+`
		  AND search_documents.search_tsv @@ plainto_tsquery('simple', ?)
	`+spaceClause+`
		ORDER BY text_rank DESC, search_documents.updated_at DESC, search_documents.search_document_id ASC
		LIMIT ?
	`, input.Query, input.TeamID, contract.EmbeddingContractID, eventAt, input.KnownAt, input.Query, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchHits(rows)
}

func searchRecallVector(
	ctx context.Context,
	tx *gorm.DB,
	input RecallEvidenceInput,
	contract *ActiveSearchContract,
	limit int,
) ([]SearchHit, error) {
	switch contract.IndexStrategy {
	case string(domain.VectorIndexExact):
		return searchRecallExactVector(ctx, tx, input, contract, limit)
	case string(domain.VectorIndexVectorHNSW), string(domain.VectorIndexHalfvecHNSW), string(domain.VectorIndexBinaryHNSW):
		return searchRecallANNVector(ctx, tx, input, contract, limit)
	default:
		return nil, fmt.Errorf("%w: unsupported recall vector index strategy %q", ErrSearchContractMismatch, contract.IndexStrategy)
	}
}

func searchRecallExactVector(
	ctx context.Context,
	tx *gorm.DB,
	input RecallEvidenceInput,
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
	spaceClause := recallSpacePredicate("search_documents.space_id", input.TeamID, input.SpaceID, input.SpaceKind)
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT search_documents.team_id::text, search_documents.search_document_id::text,
		       search_documents.source_kind, search_documents.source_id::text,
		       search_documents.source_version, search_documents.document_version,
		       search_documents.embedding_contract_id::text,
		       search_documents.search_state,
		       (search_documents.embedding <=> ?::vector)::double precision AS distance,
		       0::double precision AS text_rank
		FROM search_documents
		JOIN evidence_fragments AS source_fragment
		  ON source_fragment.team_id = search_documents.team_id
		 AND source_fragment.fragment_id = search_documents.source_id
		WHERE search_documents.team_id = ?::uuid
		  AND search_documents.source_kind = 'evidence'
		  AND search_documents.embedding_contract_id = ?::uuid
		  AND search_documents.embedding_dimensions = ?
		  AND search_documents.search_state = 'current'
		  AND search_documents.embedding IS NOT NULL
		  AND COALESCE(source_fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  `+recallEvidenceAliasVisibilitySQL("source_fragment")+`
		`+spaceClause+`
		ORDER BY search_documents.embedding <=> ?::vector ASC, search_documents.search_document_id ASC
		LIMIT ?
	`, vectorLiteral, input.TeamID, contract.EmbeddingContractID,
		contract.EmbeddingDimensions, input.KnownAt, vectorLiteral, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchHits(rows)
}

func searchRecallANNVector(
	ctx context.Context,
	tx *gorm.DB,
	input RecallEvidenceInput,
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
	spaceClause := recallSpacePredicate("search_documents.space_id", input.TeamID, input.SpaceID, input.SpaceKind)
	query := fmt.Sprintf(`
		WITH ann_candidates AS MATERIALIZED (
			SELECT search_documents.team_id, search_documents.search_document_id
			FROM search_documents
			JOIN evidence_fragments AS source_fragment
			  ON source_fragment.team_id = search_documents.team_id
			 AND source_fragment.fragment_id = search_documents.source_id
			WHERE search_documents.team_id = ?::uuid
			  AND search_documents.source_kind = 'evidence'
			  AND search_documents.embedding_contract_id = %s::uuid
			  AND search_documents.embedding_dimensions = %d
			  AND search_documents.search_state = 'current'
			  AND search_documents.embedding IS NOT NULL
			  AND COALESCE(source_fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
			  `+recallEvidenceAliasVisibilitySQL("source_fragment")+`
			%s
			ORDER BY %s ASC, search_documents.search_document_id ASC
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
	`, contractLiteral, contract.EmbeddingDimensions, spaceClause, annDistance)
	rows, err := tx.WithContext(ctx).Raw(
		query,
		input.TeamID,
		input.KnownAt,
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

func recallANNDistanceExpression(contract *ActiveSearchContract) (string, error) {
	if contract == nil {
		return "", fmt.Errorf("%w: active search contract is required", ErrSearchContractMismatch)
	}
	if contract.EmbeddingDimensions < 1 || contract.EmbeddingDimensions > domain.MaxEmbeddingDimensions {
		return "", fmt.Errorf("%w: ANN dimensions out of range: %d", ErrSearchContractMismatch, contract.EmbeddingDimensions)
	}
	switch contract.IndexStrategy {
	case string(domain.VectorIndexVectorHNSW):
		if contract.EmbeddingDimensions > 2000 {
			return "", fmt.Errorf("%w: vector ANN dimensions out of range: %d", ErrSearchContractMismatch, contract.EmbeddingDimensions)
		}
		return fmt.Sprintf("embedding::vector(%d) <=> ?::vector(%d)", contract.EmbeddingDimensions, contract.EmbeddingDimensions), nil
	case string(domain.VectorIndexHalfvecHNSW):
		if contract.EmbeddingDimensions > 4000 {
			return "", fmt.Errorf("%w: halfvec ANN dimensions out of range: %d", ErrSearchContractMismatch, contract.EmbeddingDimensions)
		}
		return fmt.Sprintf("embedding::halfvec(%d) <=> ?::halfvec(%d)", contract.EmbeddingDimensions, contract.EmbeddingDimensions), nil
	case string(domain.VectorIndexBinaryHNSW):
		return fmt.Sprintf("binary_quantize(embedding)::bit(%d) <~> binary_quantize(?::vector)::bit(%d)", contract.EmbeddingDimensions, contract.EmbeddingDimensions), nil
	default:
		return "", fmt.Errorf("%w: unsupported recall ANN strategy %q", ErrSearchContractMismatch, contract.IndexStrategy)
	}
}

func recallEmbeddingContractLiteral(contractID string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(contractID))
	if err != nil {
		return "", fmt.Errorf("%w: invalid embedding contract id: %w", ErrSearchContractMismatch, err)
	}
	return pq.QuoteLiteral(parsed.String()), nil
}

func recallANNCandidateLimit(contract *ActiveSearchContract, limit int) int {
	candidateLimit := recallOverfetchCap
	if contract != nil && contract.CandidateLimit > 0 {
		candidateLimit = contract.CandidateLimit
	}
	if candidateLimit < limit {
		candidateLimit = limit
	}
	if candidateLimit > recallOverfetchCap {
		return recallOverfetchCap
	}
	return candidateLimit
}

func recallANNQueryEFSearch(contract *ActiveSearchContract, candidateLimit int) int {
	queryEFSearch := searchDefaultQueryEFSearch
	if contract != nil && contract.QueryEFSearch > 0 {
		queryEFSearch = contract.QueryEFSearch
	}
	if candidateLimit > queryEFSearch {
		return candidateLimit
	}
	return queryEFSearch
}

func setRecallANNQueryEFSearch(
	ctx context.Context,
	tx *gorm.DB,
	contract *ActiveSearchContract,
	candidateLimit int,
) error {
	return tx.WithContext(ctx).Exec(
		`SELECT set_config('hnsw.ef_search', ?, true)`,
		strconv.Itoa(recallANNQueryEFSearch(contract, candidateLimit)),
	).Error
}

func recallEventAt(validAt, knownAt *time.Time) *time.Time {
	if knownAt != nil {
		return knownAt
	}
	return validAt
}

func searchRecallEntityExpansion(
	ctx context.Context,
	tx *gorm.DB,
	input RecallEvidenceInput,
	contract *ActiveSearchContract,
	limit int,
) ([]SearchHit, error) {
	eventAt := recallEventAt(input.ValidAt, input.KnownAt)
	spaceClause := recallSpacePredicate("document.space_id", input.TeamID, input.SpaceID, input.SpaceKind)
	rows, err := tx.WithContext(ctx).Raw(`
			WITH latest_support_decision AS (
				SELECT DISTINCT ON (team_id, support_id)
				       team_id, support_id, decision
				FROM relationship_support_decision_events
				WHERE team_id = ?::uuid
				  AND (?::timestamptz IS NULL OR created_at <= ?::timestamptz)
				ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
			)
		SELECT document.team_id::text,
		       document.search_document_id::text,
		       document.source_kind,
		       document.source_id::text,
		       document.source_version,
		       document.document_version,
		       document.embedding_contract_id::text,
		       document.search_state,
		       0::double precision AS distance,
		       0::double precision AS text_rank
		FROM relationship_records AS relationship
		JOIN relationship_evidence_supports AS support
		  ON support.team_id = relationship.team_id
		 AND support.relationship_id = relationship.relationship_id
		JOIN latest_support_decision AS latest
		  ON latest.team_id = support.team_id
		 AND latest.support_id = support.support_id
		 AND latest.decision IN ('grant', 'reinstate')
		LEFT JOIN evidence_exact_aliases AS support_alias ON support_alias.team_id = support.team_id AND support_alias.alias_fragment_id = support.fragment_id
				JOIN search_documents AS document
				  ON document.team_id = support.team_id
				 AND document.source_kind = 'evidence'
				 AND document.source_id = CASE
				     WHEN support_alias.alias_fragment_id IS NOT NULL
				          AND support_alias.created_at > COALESCE(?::timestamptz, 'infinity'::timestamptz)
				     THEN support_alias.alias_fragment_id
				     ELSE COALESCE(support_alias.canonical_fragment_id, support.fragment_id)
				 END
				 AND document.embedding_contract_id = ?::uuid
				 AND (document.search_state IN ('pending', 'current', 'failed') OR (?::timestamptz IS NOT NULL AND document.search_state = 'not_required'))
			JOIN evidence_fragments AS source_fragment
				  ON source_fragment.team_id = support.team_id
				 AND source_fragment.fragment_id = document.source_id
				LEFT JOIN evidence_quarantines AS quarantine
				  ON quarantine.team_id = support.team_id
				 AND quarantine.fragment_id = support.fragment_id
				 AND quarantine.status = 'active'
				LEFT JOIN evidence_quarantines AS canonical_quarantine ON canonical_quarantine.team_id = source_fragment.team_id
				 AND canonical_quarantine.fragment_id = source_fragment.fragment_id AND canonical_quarantine.status = 'active'
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
			WHERE relationship.team_id = ?::uuid
			  AND relationship.identity_alias_of_relationship_id IS NULL
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
		  AND quarantine.quarantine_id IS NULL
		  AND canonical_quarantine.quarantine_id IS NULL
		  AND COALESCE(source_fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
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
		          AND support.created_at <= ?::timestamptz
		          AND (relationship.recorded_to IS NULL OR relationship.recorded_to > ?::timestamptz))
		  )
		  AND (
		      cardinality(?::uuid[]) = 0
		      OR relationship.relationship_id <> ALL(?::uuid[])
		  )
		  `+spaceClause+`
		GROUP BY document.team_id, document.search_document_id, document.source_kind,
		         document.source_id, document.source_version, document.document_version,
		         document.embedding_contract_id, document.search_state
		ORDER BY max(relationship.updated_at) DESC, document.search_document_id ASC
		LIMIT ?
		`, input.TeamID, eventAt, eventAt, input.KnownAt,
		contract.EmbeddingContractID, eventAt,
		eventAt, eventAt,
		input.TeamID,
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt,
		eventAt,
		pq.Array(input.ExpandFromEntityIDs), pq.Array(input.ExpandFromEntityIDs),
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt, input.KnownAt,
		pq.Array(input.KnownRelationshipIDs), pq.Array(input.KnownRelationshipIDs),
		limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchHits(rows)
}

func hydrateRecallEvidence(
	ctx context.Context,
	tx *gorm.DB,
	input RecallEvidenceInput,
	contract *ActiveSearchContract,
	evidenceIDs []string,
) (map[string]RecallEvidenceHit, error) {
	eventAt := recallEventAt(input.ValidAt, input.KnownAt)
	spaceClause := recallSpacePredicate("fragment.space_id", input.TeamID, input.SpaceID, input.SpaceKind)
	rows, err := tx.WithContext(ctx).Raw(`
		WITH requested AS (
			SELECT unnest(?::uuid[]) AS fragment_id
		),
			latest_support_decision AS (
				SELECT DISTINCT ON (team_id, support_id)
				       team_id, support_id, decision
				FROM relationship_support_decision_events
				WHERE team_id = ?::uuid
				  AND (?::timestamptz IS NULL OR created_at <= ?::timestamptz)
				ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
				),
			eligible AS (
				SELECT fragment.fragment_id::text AS evidence_id,
				       fragment.content AS context,
				       COALESCE(NULLIF(fragment.source_ref, ''), NULLIF(fragment_source.source_key, ''), '') AS source,
				       fragment.source_type,
				       fragment.created_at,
				       document.search_state,
				       relationship.relationship_id::text AS relationship_id
				FROM requested
			JOIN evidence_fragments AS fragment
			  ON fragment.team_id = ?::uuid
			 AND fragment.fragment_id = requested.fragment_id
			JOIN search_documents AS document
			  ON document.team_id = fragment.team_id
			 AND document.source_kind = 'evidence'
			 AND document.source_id = fragment.fragment_id
			 AND document.embedding_contract_id = ?::uuid
			 AND (document.search_state IN ('pending', 'current', 'failed') OR (?::timestamptz IS NOT NULL AND document.search_state = 'not_required'))
			LEFT JOIN evidence_quarantines AS quarantine
			  ON quarantine.team_id = fragment.team_id
			 AND quarantine.fragment_id = fragment.fragment_id
			 AND quarantine.status = 'active'
			LEFT JOIN evidence_sources AS fragment_source
			  ON fragment_source.team_id = fragment.team_id
			 AND fragment_source.source_id = fragment.source_id
		LEFT JOIN relationship_evidence_supports AS support
		  ON support.team_id = fragment.team_id
		 AND (support.fragment_id = fragment.fragment_id OR EXISTS (SELECT 1 FROM evidence_exact_aliases AS support_alias
		     WHERE support_alias.team_id = support.team_id AND support_alias.alias_fragment_id = support.fragment_id
		       AND support_alias.canonical_fragment_id = fragment.fragment_id
		       AND support_alias.created_at <= COALESCE(?::timestamptz, 'infinity'::timestamptz)))
		 AND NOT EXISTS (SELECT 1 FROM evidence_quarantines AS support_quarantine
		     WHERE support_quarantine.team_id = support.team_id AND support_quarantine.fragment_id = support.fragment_id
		       AND support_quarantine.status = 'active')
		LEFT JOIN latest_support_decision AS latest
		  ON latest.team_id = support.team_id
		 AND latest.support_id = support.support_id
		 AND latest.decision IN ('grant', 'reinstate')
				LEFT JOIN evidence_sources AS support_source
				  ON support_source.team_id = support.team_id
				  AND support_source.source_id = support.source_id
				LEFT JOIN LATERAL (
				    SELECT relationship.relationship_id
				    FROM relationship_records AS relationship
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
				    WHERE relationship.team_id = support.team_id
				      AND relationship.relationship_id = support.relationship_id
				      AND latest.support_id IS NOT NULL
				      AND relationship.identity_alias_of_relationship_id IS NULL
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
				          support.source_id IS NULL
				          OR support_source.current_revision_id = support.source_revision_id
				      )
				      AND (
				          ?::timestamptz IS NULL
				          OR ((relationship.valid_from IS NULL OR relationship.valid_from <= ?::timestamptz)
				              AND (relationship.valid_to IS NULL OR relationship.valid_to > ?::timestamptz))
				      )
				      AND (
				          ?::timestamptz IS NULL
				          OR (relationship.created_at <= ?::timestamptz
				              AND support.created_at <= ?::timestamptz
				              AND (relationship.recorded_to IS NULL OR relationship.recorded_to > ?::timestamptz))
				      )
				      AND (
				          cardinality(?::uuid[]) = 0
				          OR relationship.relationship_id <> ALL(?::uuid[])
				      )
				    LIMIT 1
				) AS relationship ON TRUE
			WHERE quarantine.quarantine_id IS NULL
			  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
			  AND NOT EXISTS (
			      SELECT 1
			      FROM evidence_lifecycle_events AS lifecycle
			      WHERE lifecycle.team_id = fragment.team_id
			        AND lifecycle.target_fragment_id = fragment.fragment_id
			        AND (?::timestamptz IS NULL OR lifecycle.created_at <= ?::timestamptz)
			  )
			  `+recallEvidenceHistoricalSourceVisibilitySQL("fragment", "fragment_source")+`
			  AND (?::timestamptz IS NULL OR fragment.created_at <= ?::timestamptz)
			  `+spaceClause+`
			)
			SELECT evidence_id,
			       max(context) AS context,
			       max(source) AS source,
			       max(source_type) AS source_type,
			       max(created_at) AS created_at,
			       COALESCE(
			           array_remove(array_agg(DISTINCT relationship_id ORDER BY relationship_id), NULL),
			           ARRAY[]::text[]
		       ) AS relationship_ids,
		       CASE
		           WHEN bool_or(search_state = 'failed') THEN 'failed'
		           WHEN bool_or(search_state = 'pending') THEN 'pending'
		           ELSE 'current'
		       END AS search_state
		FROM eligible
		GROUP BY evidence_id
		`, pq.Array(evidenceIDs), input.TeamID, eventAt, eventAt,
		input.TeamID, contract.EmbeddingContractID, eventAt, input.KnownAt,
		eventAt, eventAt,
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt,
		eventAt,
		input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt, input.KnownAt,
		pq.Array(input.KnownRelationshipIDs), pq.Array(input.KnownRelationshipIDs),
		input.KnownAt, input.KnownAt,
		input.KnownAt, input.KnownAt, input.KnownAt).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]RecallEvidenceHit)
	for rows.Next() {
		var evidenceID, context, source, sourceType, searchState string
		var createdAt time.Time
		var relationshipIDs pq.StringArray
		if err := rows.Scan(&evidenceID, &context, &source, &sourceType, &createdAt, &relationshipIDs, &searchState); err != nil {
			return nil, err
		}
		out[evidenceID] = RecallEvidenceHit{
			TeamID:          input.TeamID,
			EvidenceID:      evidenceID,
			RelationshipIDs: []string(relationshipIDs),
			Context:         truncateRecallContext(context),
			Source:          source,
			SourceType:      sourceType,
			CreatedAt:       createdAt,
			SearchState:     searchState,
		}
	}
	return out, rows.Err()
}

func loadRecallOpenConflictRecords(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	knownAt *time.Time,
	results []RecallEvidenceHit,
) ([]RelationshipConflictCaseRecord, error) {
	relationshipIDs := []string{}
	seenRelationships := map[string]struct{}{}
	for _, hit := range results {
		for _, id := range hit.RelationshipIDs {
			if _, ok := seenRelationships[id]; ok {
				continue
			}
			seenRelationships[id] = struct{}{}
			relationshipIDs = append(relationshipIDs, id)
		}
	}
	conflicts, err := loadRelationshipConflictRecords(ctx, tx, teamID, relationshipIDs, knownAt)
	if err != nil {
		return nil, err
	}
	out := make([]RelationshipConflictCaseRecord, 0, len(conflicts))
	seenConflicts := map[string]struct{}{}
	for _, conflict := range conflicts {
		if conflict.Status != string(domain.RelationshipConflictOpen) &&
			conflict.Status != string(domain.RelationshipConflictOverdue) &&
			conflict.Status != string(domain.RelationshipConflictResolved) {
			continue
		}
		if _, ok := seenConflicts[conflict.ConflictID]; ok {
			continue
		}
		seenConflicts[conflict.ConflictID] = struct{}{}
		out = append(out, conflict)
	}
	return out, nil
}

func scanSearchHits(rows *sql.Rows) ([]SearchHit, error) {
	hits := []SearchHit{}
	for rows.Next() {
		hit, err := scanSearchHit(rows)
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func normalizeRecallEvidenceInput(input RecallEvidenceInput) RecallEvidenceInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	input.KnownEvidenceIDs = normalizeRecallUUIDList(input.KnownEvidenceIDs)
	input.KnownRelationshipIDs = normalizeRecallUUIDList(input.KnownRelationshipIDs)
	input.ExpandFromEntityIDs = normalizeRecallUUIDList(input.ExpandFromEntityIDs)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	input.SpaceKind = strings.TrimSpace(input.SpaceKind)
	if input.SpaceID == "" && input.SpaceKind == "" {
		input.SpaceKind = string(domain.MemorySpaceTeamShared)
	}
	if input.Limit <= 0 {
		input.Limit = defaultRecallLimit
	}
	if input.Limit > maxRecallLimit {
		input.Limit = maxRecallLimit
	}
	return input
}

// recallSpacePredicate is assembled only from validated server-owned UUIDs and
// fixed enum values. It deliberately does not accept request text so branch
// scope cannot become SQL input or an authorization override.
func recallSpacePredicate(column, teamID, spaceID, spaceKind string) string {
	generationColumn := column
	if separator := strings.LastIndexByte(column, '.'); separator >= 0 {
		generationColumn = column[:separator] + ".space_generation"
	} else {
		generationColumn = "space_generation"
	}
	if strings.TrimSpace(spaceID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(spaceID))
		if err == nil {
			literal := pq.QuoteLiteral(parsed.String())
			return fmt.Sprintf(
				" AND %s = %s::uuid AND %s = (SELECT generation FROM memory_spaces WHERE id = %s AND lifecycle_state = 'active')",
				column, literal, generationColumn, column,
			)
		}
		return " AND FALSE"
	}
	if strings.TrimSpace(spaceKind) == "" || strings.TrimSpace(spaceKind) == string(domain.MemorySpaceTeamShared) {
		parsed, err := uuid.Parse(strings.TrimSpace(teamID))
		if err == nil {
			literal := pq.QuoteLiteral(parsed.String())
			return fmt.Sprintf(
				" AND %s = dense_mem_team_shared_space(%s::uuid) AND %s = dense_mem_team_shared_generation(%s::uuid)",
				column, literal, generationColumn, literal,
			)
		}
		return " AND FALSE"
	}
	return " AND FALSE"
}

func validateRecallEvidenceInput(input RecallEvidenceInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.SpaceKind != "" && !domain.MemorySpaceKind(input.SpaceKind).Valid() {
		return fmt.Errorf("space_kind is invalid: %s", input.SpaceKind)
	}
	if input.SpaceKind != "" && input.SpaceKind != string(domain.MemorySpaceTeamShared) && input.SpaceID == "" {
		return fmt.Errorf("space_id is required for private space kind %s", input.SpaceKind)
	}
	if input.SpaceID != "" {
		if _, err := uuid.Parse(input.SpaceID); err != nil {
			return fmt.Errorf("space_id is invalid: %w", err)
		}
	}
	if input.Query == "" && len(input.ExpandFromEntityIDs) == 0 {
		return errors.New("query or expand_from_entity_ids is required")
	}
	for label, values := range map[string][]string{
		"known_evidence_ids":     input.KnownEvidenceIDs,
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

func normalizeRecallUUIDList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func recallOverfetchLimit(limit int) int {
	overfetch := limit * recallOverfetchMultiple
	if overfetch < recallOverfetchFloor {
		overfetch = recallOverfetchFloor
	}
	if overfetch > recallOverfetchCap {
		return recallOverfetchCap
	}
	return overfetch
}

func addRecallBranch(acc map[string]*recallCandidate, hits []SearchHit, knownEvidence map[string]struct{}, weight float64) {
	for i, hit := range hits {
		if hit.SourceKind != "evidence" || hit.SourceID == "" {
			continue
		}
		if _, known := knownEvidence[hit.SourceID]; known {
			continue
		}
		candidate := acc[hit.SourceID]
		if candidate == nil {
			candidate = &recallCandidate{
				EvidenceID:  hit.SourceID,
				SearchState: hit.SearchState,
			}
			acc[hit.SourceID] = candidate
		}
		candidate.Score += weight / (recallRRFConstant + float64(i+1))
		candidate.SearchState = domain.CombineSearchProjectionStates(candidate.SearchState, hit.SearchState)
	}
}

func sortedRecallCandidates(acc map[string]*recallCandidate) []recallCandidate {
	out := make([]recallCandidate, 0, len(acc))
	for _, candidate := range acc {
		out = append(out, *candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].EvidenceID < out[j].EvidenceID
	})
	return out
}

func recallStringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func truncateRecallContext(value string) string {
	value = strings.TrimSpace(value)
	const max = 2000
	if len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max])
}
