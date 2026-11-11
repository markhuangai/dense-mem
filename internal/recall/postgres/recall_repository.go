package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"

	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const (
	searchDefaultQueryEFSearch = 40
)

var _ recallcontract.Repository = (*Store)(nil)

func (r *Store) ReadEvidenceCandidates(ctx context.Context, input RecallEvidenceInput, contract *ActiveSearchContract, candidateLimit int) (*recallcontract.RecallCandidateBatch, error) {
	var textHits, vectorHits, ontologyHits []SearchHit
	searchState := string(domain.SearchProjectionCurrent)
	organizationCode := ""
	err := r.withRecallTx(ctx, input.TeamID, input.OrganizationEnabled, func(tx *gorm.DB) error {
		var err error
		input.KnownEvidenceIDs, err = storagepostgres.ResolveMemoryHandlesTx(ctx, tx, input.TeamID, "evidence_fragments", input.KnownEvidenceIDs, input.SpaceID)
		if err != nil {
			return err
		}
		if err = r.resolveTopicFilter(ctx, tx, &input); err != nil {
			return err
		}
		if input.Query != "" {
			stageCtx, stage := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageFullText)
			textHits, err = searchRecallFullText(stageCtx, tx, input, contract, candidateLimit)
			stage.Finish(err, len(textHits))
			if err != nil {
				return err
			}
		}
		if len(input.QueryEmbedding) > 0 {
			stageCtx, stage := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageVector)
			vectorHits, err = searchRecallVector(stageCtx, tx, input, contract, candidateLimit)
			stage.Finish(err, len(vectorHits))
			if err != nil {
				return err
			}
		}
		if input.Query == "" && input.TopicIDs != nil {
			ontologyHits, err = r.topicEvidenceCandidates(ctx, tx, input, contract, candidateLimit)
			if err != nil {
				return err
			}
		}
		searchState, err = recallEvidenceSearchState(ctx, tx, input, contract)
		if err != nil {
			return err
		}
		var organizationIDs []string
		if input.OrganizationEnabled {
			organizationIDs = append(organizationIDs, input.KnownEvidenceIDs...)
			for _, hits := range [][]SearchHit{textHits, vectorHits} {
				for _, hit := range hits {
					organizationIDs = append(organizationIDs, hit.SourceID)
				}
			}
			organizationIDs = domain.NormalizeReadIDList(organizationIDs)
		}
		organization, err := r.readOrganization(ctx, tx, input, nil, organizationIDs, candidateLimit)
		if err != nil {
			return err
		}
		organizationCode = organization.Degradation
		discovered, discoverErr := r.organizationEvidenceCandidates(ctx, tx, input, contract, organization, candidateLimit)
		if discoverErr != nil {
			return &ontologyReadError{cause: discoverErr}
		}
		ontologyHits = mergeRecallExpansion(ontologyHits, discovered, candidateLimit)
		// Exhaustive text and exact-vector searches already rank the full eligible pool in this snapshot.
		if len(input.QueryEmbedding) > 0 && contract.IndexStrategy != string(domain.VectorIndexExact) {
			vectorHits, err = augmentRecallVectorChannel(vectorHits, ontologyHits, candidateLimit, func(ids []string) ([]SearchHit, error) {
				return searchRecallExactVectorSources(ctx, tx, input, contract, candidateLimit, ids)
			})
			if err != nil {
				return &ontologyReadError{cause: err}
			}
		}
		return nil
	})
	var optional *ontologyReadError
	if errors.As(err, &optional) && r.snapshot == nil {
		input.OrganizationEnabled = false
		batch, requiredErr := r.ReadEvidenceCandidates(ctx, input, contract, candidateLimit)
		if batch != nil {
			batch.OrganizationDegradation = ontologyFailureCode(optional.cause)
		}
		return batch, requiredErr
	}
	if err != nil {
		return nil, err
	}
	return &recallcontract.RecallCandidateBatch{
		OrganizationDegradation: organizationCode,
		TextHits:                textHits, VectorHits: vectorHits, OntologyHits: ontologyHits, SearchState: searchState,
	}, nil
}

func (r *Store) HydrateEvidence(ctx context.Context, input RecallEvidenceInput, contract *ActiveSearchContract, candidateIDs []string) (map[string]RecallEvidenceHit, error) {
	var hydrated map[string]RecallEvidenceHit
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		input.KnownEvidenceIDs, err = storagepostgres.ResolveMemoryHandlesTx(ctx, tx, input.TeamID, "evidence_fragments", input.KnownEvidenceIDs, input.SpaceID)
		if err != nil {
			return err
		}
		if err = r.resolveTopicFilter(ctx, tx, &input); err != nil {
			return err
		}
		hydrated, err = hydrateRecallEvidence(ctx, tx, input, contract, candidateIDs)
		return err
	})
	return hydrated, err
}

func (r *Store) LoadRecallConflicts(ctx context.Context, input RecallEvidenceInput, results []RecallEvidenceHit) (*recallcontract.RecallConflicts, error) {
	var evidence []EvidenceConflictCaseRecord
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		if r.evidenceConflicts == nil {
			return errors.New("recall: evidence conflict reader is required")
		}
		var err error
		evidence, err = r.evidenceConflicts(ctx, tx, input, results)
		return err
	})
	return &recallcontract.RecallConflicts{Evidence: evidence}, err
}

func recallEvidenceSearchState(ctx context.Context, tx *gorm.DB, input RecallEvidenceInput, contract *ActiveSearchContract) (string, error) {
	filter, args := recallEvidenceEligibility(input, "fragment", true)
	parameters := []any{input.TeamID, contract.EmbeddingContractID, recallEventAt(input.ValidAt, input.KnownAt)}
	parameters = append(parameters, args...)
	var state string
	err := tx.WithContext(ctx).Raw(`SELECT CASE WHEN bool_or(document.search_state='failed') THEN 'failed'
        WHEN bool_or(document.search_state='pending') THEN 'pending' ELSE 'current' END
        FROM search_documents AS document JOIN evidence_fragments AS fragment
          ON fragment.team_id=document.team_id AND fragment.fragment_id=document.source_id
        WHERE document.team_id=?::uuid AND document.source_kind='evidence' AND document.embedding_contract_id=?::uuid
          AND (document.search_state IN ('pending','current','failed') OR (?::timestamptz IS NOT NULL AND document.search_state='not_required'))
        `+filter, parameters...).Row().Scan(&state)
	return state, err
}

func searchRecallFullText(ctx context.Context, tx *gorm.DB, input RecallEvidenceInput, contract *ActiveSearchContract, limit int) ([]SearchHit, error) {
	filter, args := recallEvidenceEligibility(input, "source_fragment", true)
	parameters := []any{input.Query, input.TeamID, contract.EmbeddingContractID, recallEventAt(input.ValidAt, input.KnownAt), input.Query}
	parameters = append(parameters, args...)
	parameters = append(parameters, limit)
	rows, err := tx.WithContext(ctx).Raw(`SELECT search_documents.team_id::text,search_documents.search_document_id::text,
        search_documents.source_kind,search_documents.source_id::text,search_documents.source_version,search_documents.document_version,
        search_documents.embedding_contract_id::text,search_documents.search_state,0::double precision,
        ts_rank_cd(search_documents.search_tsv,plainto_tsquery('simple',?))::double precision AS text_rank
        FROM search_documents `+recallEvidenceSourceJoin(false)+`
        WHERE search_documents.team_id=?::uuid AND search_documents.source_kind='evidence' AND search_documents.embedding_contract_id=?::uuid
          AND (search_documents.search_state IN ('pending','current','failed') OR (?::timestamptz IS NOT NULL AND search_documents.search_state='not_required'))
          AND search_documents.search_tsv @@ plainto_tsquery('simple',?) `+filter+`
        ORDER BY text_rank DESC,search_documents.updated_at DESC,search_documents.search_document_id ASC LIMIT ?`, parameters...).Rows()
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
	return searchRecallExactVectorSources(ctx, tx, input, contract, limit, nil)
}

func searchRecallExactVectorSources(ctx context.Context, tx *gorm.DB, input RecallEvidenceInput, contract *ActiveSearchContract, limit int, sourceIDs []string) ([]SearchHit, error) {
	if len(input.QueryEmbedding) != contract.EmbeddingDimensions {
		return nil, fmt.Errorf("%w: contract dimensions %d, query dimensions %d", ErrSearchContractMismatch, contract.EmbeddingDimensions, len(input.QueryEmbedding))
	}
	vectorLiteral, err := vectorLiteral(input.QueryEmbedding)
	if err != nil {
		return nil, err
	}
	filter, filterArgs := recallEvidenceEligibility(input, "source_fragment", true)
	sourceClause, _ := recallSourceFilter("search_documents.source_id", sourceIDs, nil)
	query := `
		SELECT search_documents.team_id::text, search_documents.search_document_id::text,
		       search_documents.source_kind, search_documents.source_id::text,
		       search_documents.source_version, search_documents.document_version,
		       search_documents.embedding_contract_id::text,
		       search_documents.search_state,
		       (search_documents.embedding <=> ?::vector)::double precision AS distance,
		       0::double precision AS text_rank
		FROM search_documents
		` + recallEvidenceSourceJoin(sourceIDs != nil) + `
		WHERE search_documents.team_id = ?::uuid
		  AND search_documents.source_kind = 'evidence'
		  AND search_documents.embedding_contract_id = ?::uuid
		  AND search_documents.embedding_dimensions = ?
		  AND search_documents.search_state = 'current'
		  AND search_documents.embedding IS NOT NULL
		` + filter + sourceClause + `
	`
	args := append([]any{vectorLiteral, input.TeamID, contract.EmbeddingContractID, contract.EmbeddingDimensions}, filterArgs...)
	if sourceIDs != nil {
		args = append(args, pq.Array(sourceIDs))
	}
	if sourceIDs != nil {
		query = recallBoundedVectorQuery(query)
		args = append(args, limit)
	} else {
		query += " ORDER BY search_documents.embedding <=> ?::vector ASC, search_documents.search_document_id ASC LIMIT ?"
		args = append(args, vectorLiteral, limit)
	}
	rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
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
	filter, filterArgs := recallEvidenceEligibility(input, "source_fragment", true)
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
	`, contractLiteral, contract.EmbeddingDimensions, filter, annDistance)
	args := append([]any{input.TeamID}, filterArgs...)
	args = append(args, vectorLiteral, candidateLimit, vectorLiteral, vectorLiteral, limit)
	rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
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
	candidateLimit := recallcontract.MaxRecallCandidateCount
	if contract != nil && contract.CandidateLimit > 0 {
		candidateLimit = contract.CandidateLimit
	}
	if candidateLimit < limit {
		candidateLimit = limit
	}
	if candidateLimit > recallcontract.MaxRecallCandidateCount {
		return recallcontract.MaxRecallCandidateCount
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

func hydrateRecallEvidence(ctx context.Context, tx *gorm.DB, input RecallEvidenceInput, contract *ActiveSearchContract, evidenceIDs []string) (map[string]RecallEvidenceHit, error) {
	filter, args := recallEvidenceEligibility(input, "fragment", false)
	parameters := []any{pq.Array(evidenceIDs), input.TeamID, contract.EmbeddingContractID, recallEventAt(input.ValidAt, input.KnownAt)}
	parameters = append(parameters, args...)
	rows, err := tx.WithContext(ctx).Raw(`SELECT fragment.fragment_id::text,fragment.content,
        COALESCE(NULLIF(fragment.source_ref,''),NULLIF(source.source_key,''),''),fragment.source_type,fragment.created_at,
        document.search_state
        FROM evidence_fragments AS fragment
        JOIN search_documents AS document ON document.team_id=fragment.team_id AND document.source_kind='evidence'
          AND document.source_id=fragment.fragment_id
        LEFT JOIN evidence_sources AS source ON source.team_id=fragment.team_id AND source.source_id=fragment.source_id
        WHERE fragment.fragment_id=ANY(?::uuid[]) AND fragment.team_id=?::uuid AND document.embedding_contract_id=?::uuid
          AND (document.search_state IN ('pending','current','failed') OR (?::timestamptz IS NOT NULL AND document.search_state='not_required'))
        `+filter, parameters...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]RecallEvidenceHit{}
	for rows.Next() {
		var hit RecallEvidenceHit
		if err := rows.Scan(&hit.EvidenceID, &hit.Context, &hit.Source, &hit.SourceType, &hit.CreatedAt, &hit.SearchState); err != nil {
			return nil, err
		}
		hit.TeamID = input.TeamID
		hit.Context = truncateRecallContext(hit.Context)
		out[hit.EvidenceID] = hit
	}
	return out, rows.Err()
}

func recallEvidenceEligibility(input RecallEvidenceInput, alias string, excludeKnown bool) (string, []any) {
	filter := recallSpacePredicate(alias+".space_id", input.TeamID, input.SpaceID, input.SpaceKind)
	filter += ` AND COALESCE(` + alias + `.metadata->>'conflict_resolution_deletion_only','')<>'true'
        AND NOT EXISTS (SELECT 1 FROM evidence_quarantines AS quarantine WHERE quarantine.team_id=` + alias + `.team_id
          AND quarantine.fragment_id=` + alias + `.fragment_id AND quarantine.status='active') ` + recallEvidenceAliasVisibilitySQL(alias) + `
        AND NOT EXISTS (SELECT 1 FROM evidence_lifecycle_events AS lifecycle WHERE lifecycle.team_id=` + alias + `.team_id
          AND lifecycle.target_fragment_id=` + alias + `.fragment_id AND (?::timestamptz IS NULL OR lifecycle.created_at<=?::timestamptz))
        AND (` + alias + `.source_id IS NULL OR EXISTS (SELECT 1 FROM evidence_sources AS eligible_source
          WHERE eligible_source.team_id=` + alias + `.team_id AND eligible_source.source_id=` + alias + `.source_id
          ` + recallEvidenceHistoricalSourceVisibilitySQL(alias, "eligible_source") + `))
        AND (?::timestamptz IS NULL OR ` + alias + `.created_at<=?::timestamptz)`
	args := []any{input.KnownAt, input.KnownAt, input.KnownAt, input.KnownAt, input.KnownAt, input.KnownAt, input.KnownAt}
	if input.TopicIDs != nil {
		if input.TopicEvidenceIDs == nil {
			filter += " AND FALSE"
		} else {
			filter += " AND " + alias + ".fragment_id=ANY(?::uuid[])"
			args = append(args, pq.Array(input.TopicEvidenceIDs))
		}
	}
	if excludeKnown && len(input.KnownEvidenceIDs) > 0 {
		filter += " AND " + alias + ".fragment_id<>ALL(?::uuid[])"
		args = append(args, pq.Array(input.KnownEvidenceIDs))
	}
	return filter, args
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

func truncateRecallContext(value string) string {
	value = strings.TrimSpace(value)
	const max = 2000
	if len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max])
}
