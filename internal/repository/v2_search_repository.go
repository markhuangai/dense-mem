package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const defaultV2SearchProfileKey = "default"

var (
	ErrV2SearchStaleVersion    = errors.New("v2 search stale source or document version")
	ErrV2SearchProfileMismatch = errors.New("v2 search profile mismatch")
	ErrV2EmbeddingLeaseLost    = errors.New("v2 embedding lease lost")
)

type V2SearchRepositoryImpl struct {
	db  *gorm.DB
	rls v2RLSHelper
}

var _ V2SearchRepository = (*V2SearchRepositoryImpl)(nil)

func NewV2SearchRepository(db *gorm.DB, rls *postgres.RLS) *V2SearchRepositoryImpl {
	return &V2SearchRepositoryImpl{db: db, rls: rls}
}

func (r *V2SearchRepositoryImpl) GetActiveSearchProfile(ctx context.Context, profileKey string) (*V2SearchProfile, error) {
	profileKey = normalizeV2SearchProfileKey(profileKey)
	var profile V2SearchProfile
	err := r.db.WithContext(ctx).Raw(`
		SELECT
		    search.profile_key,
		    contract.embedding_contract_id::text,
		    search.search_index_profile_id::text,
		    ranking.ranking_profile_id::text,
		    contract.dimensions,
		    contract.provider,
		    contract.model,
		    contract.distance_metric,
		    contract.vector_normalization,
		    contract.document_format_version,
		    contract.query_format_version,
		    search.ann_strategy,
		    search.operator_class,
		    search.indexed_expression,
		    search.physical_index_name,
		    search.exact_max_rows,
		    search.candidate_limit,
		    search.allow_exact_fallback
		FROM search_index_profiles AS search
		JOIN embedding_contracts AS contract
		  ON contract.embedding_contract_id = search.embedding_contract_id
		 AND contract.dimensions = search.embedding_dimensions
		JOIN ranking_profiles AS ranking
		  ON ranking.profile_key = search.profile_key
		 AND ranking.activation_state = 'active'
		WHERE search.profile_key = ?
		  AND search.activation_state = 'active'
		  AND contract.lifecycle_state = 'active'
		ORDER BY search.version DESC, ranking.version DESC
		LIMIT 1
	`, profileKey).Row().Scan(
		&profile.ProfileKey,
		&profile.EmbeddingContractID,
		&profile.SearchIndexProfileID,
		&profile.RankingProfileID,
		&profile.EmbeddingDimensions,
		&profile.EmbeddingProvider,
		&profile.EmbeddingModel,
		&profile.DistanceMetric,
		&profile.VectorNormalization,
		&profile.DocumentFormatVersion,
		&profile.QueryFormatVersion,
		&profile.IndexStrategy,
		&profile.OperatorClass,
		&profile.IndexedExpression,
		&profile.PhysicalIndexName,
		&profile.ExactMaxRows,
		&profile.CandidateLimit,
		&profile.AllowExactFallback,
	)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: active search profile %q not found", ErrV2SearchProfileMismatch, profileKey)
	}
	if err != nil {
		return nil, fmt.Errorf("v2 search: load active profile: %w", err)
	}
	if profile.ProfileKey == "" {
		return nil, fmt.Errorf("%w: active search profile %q not found", ErrV2SearchProfileMismatch, profileKey)
	}
	return &profile, nil
}

func (r *V2SearchRepositoryImpl) CheckSearchReadiness(ctx context.Context, profileKey string) (*V2SearchReadiness, error) {
	profileKey = normalizeV2SearchProfileKey(profileKey)
	profile, err := r.GetActiveSearchProfile(ctx, profileKey)
	if err != nil {
		if errors.Is(err, ErrV2SearchProfileMismatch) {
			return &V2SearchReadiness{
				ProfileKey: profileKey,
				Ready:      false,
				Reasons: []V2SearchReadinessReason{{
					Code:    "inactive_search_profile",
					Message: fmt.Sprintf("active search profile %q is not available", profileKey),
				}},
			}, nil
		}
		return nil, err
	}
	readiness := &V2SearchReadiness{ProfileKey: profile.ProfileKey, Ready: true, Profile: profile}
	var vectorPresent bool
	if err := r.db.WithContext(ctx).Raw(`
		SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')
	`).Scan(&vectorPresent).Error; err != nil {
		return nil, fmt.Errorf("v2 search: readiness extension check: %w", err)
	}
	if !vectorPresent {
		readiness.Ready = false
		readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
			Code:    "missing_pgvector_extension",
			Message: "pgvector extension is not installed",
		})
	}
	if profile.IndexStrategy != string(domain.V2VectorIndexExact) {
		var indexDefinition string
		if err := r.db.WithContext(ctx).Raw(`
			SELECT COALESCE((
			    SELECT indexdef
			    FROM pg_indexes
			    WHERE schemaname = current_schema()
			      AND indexname = ?
			), '')
		`, profile.PhysicalIndexName).Scan(&indexDefinition).Error; err != nil {
			return nil, fmt.Errorf("v2 search: readiness index check: %w", err)
		}
		if indexDefinition == "" {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code:    "missing_physical_index",
				Message: fmt.Sprintf("physical index %q is missing", profile.PhysicalIndexName),
			})
		} else if missing := v2SearchMissingIndexCompatibility(profile, indexDefinition); len(missing) > 0 {
			readiness.Ready = false
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code: "incompatible_physical_index",
				Message: fmt.Sprintf(
					"physical index %q is incompatible with profile %q: missing %s",
					profile.PhysicalIndexName,
					profile.ProfileKey,
					strings.Join(missing, ", "),
				),
			})
		}
	}
	stats, err := r.GetEmbeddingQueueStats(ctx, V2EmbeddingQueueStatsInput{
		EmbeddingContractID: profile.EmbeddingContractID,
		EmbeddingDimensions: profile.EmbeddingDimensions,
	})
	if err != nil {
		return nil, err
	}
	if stats.CutoverBlocking {
		readiness.Ready = false
		if stats.Queued+stats.Processing > 0 {
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code:    "embedding_backlog_pending",
				Message: fmt.Sprintf("%d embedding jobs are queued or processing for profile %q", stats.Queued+stats.Processing, profile.ProfileKey),
			})
		}
		if stats.ExpiredLeases > 0 {
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code:    "expired_embedding_leases",
				Message: fmt.Sprintf("%d embedding jobs have expired leases for profile %q", stats.ExpiredLeases, profile.ProfileKey),
			})
		}
		if stats.TerminalFailures > 0 {
			readiness.Reasons = append(readiness.Reasons, V2SearchReadinessReason{
				Code:    "terminal_embedding_failures",
				Message: fmt.Sprintf("%d terminal embedding jobs exist for profile %q", stats.TerminalFailures, profile.ProfileKey),
			})
		}
	}
	return readiness, nil
}

func (r *V2SearchRepositoryImpl) UpsertSearchDocument(
	ctx context.Context,
	input V2UpsertSearchDocumentInput,
) (*V2SearchDocumentResult, error) {
	input = normalizeV2UpsertSearchDocumentInput(input)
	if err := validateV2UpsertSearchDocumentInput(input); err != nil {
		return nil, err
	}
	profile, err := r.profileForDocument(ctx, input)
	if err != nil {
		return nil, err
	}
	var result *V2SearchDocumentResult
	err = r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		metadata, err := marshalV2SearchJSON(input.Metadata)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			WITH upserted AS (
				INSERT INTO search_documents (
				    team_id, owner_profile_id, source_kind, source_id, source_version,
				    document_version, embedding_contract_id, embedding_dimensions,
				    search_state, document_text, document_hash, metadata
				) VALUES (
				    ?::uuid, ?::uuid, ?, ?::uuid, ?, 1, ?::uuid, ?,
				    'pending', ?, ?, ?::jsonb
				)
				ON CONFLICT (team_id, source_kind, source_id, embedding_contract_id)
				DO UPDATE SET
				    owner_profile_id = EXCLUDED.owner_profile_id,
				    source_version = EXCLUDED.source_version,
				    document_version = CASE
				        WHEN search_documents.document_hash = EXCLUDED.document_hash
				        THEN search_documents.document_version
				        ELSE search_documents.document_version + 1
				    END,
				    search_state = CASE
				        WHEN search_documents.document_hash = EXCLUDED.document_hash
				         AND search_documents.search_state = 'current'
				        THEN 'current'
				        ELSE 'pending'
				    END,
				    document_text = EXCLUDED.document_text,
				    document_hash = EXCLUDED.document_hash,
				    embedding_error = CASE
				        WHEN search_documents.document_hash = EXCLUDED.document_hash
				        THEN search_documents.embedding_error
				        ELSE ''
				    END,
				    metadata = EXCLUDED.metadata,
				    updated_at = now()
				RETURNING team_id::text, search_document_id::text, owner_profile_id::text,
				          source_kind, source_id::text, source_version, document_version,
				          embedding_contract_id::text, embedding_dimensions, search_state
			)
			SELECT * FROM upserted
		`, input.TeamID, input.OwnerProfileID, input.SourceKind, input.SourceID, input.SourceVersion,
			profile.EmbeddingContractID, profile.EmbeddingDimensions, input.DocumentText,
			input.DocumentHash, string(metadata)).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			_ = rows.Close()
			return rows.Err()
		}
		loaded := V2SearchDocumentResult{}
		if err := rows.Scan(
			&loaded.TeamID,
			&loaded.SearchDocumentID,
			&loaded.OwnerProfileID,
			&loaded.SourceKind,
			&loaded.SourceID,
			&loaded.SourceVersion,
			&loaded.DocumentVersion,
			&loaded.EmbeddingContractID,
			&loaded.EmbeddingDimensions,
			&loaded.SearchState,
		); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		jobID, err := enqueueV2EmbeddingJob(ctx, tx, loaded)
		if err != nil {
			return err
		}
		loaded.QueuedJobID = jobID
		result = &loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 search: upsert document: %w", err)
	}
	return result, nil
}

func (r *V2SearchRepositoryImpl) SearchFullText(ctx context.Context, input V2FullTextSearchInput) ([]V2SearchHit, error) {
	input = normalizeV2FullTextSearchInput(input)
	if err := validateV2FullTextSearchInput(input); err != nil {
		return nil, err
	}
	hits := []V2SearchHit{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		sourceFilter := ""
		args := []any{input.Query, input.TeamID, input.Query}
		if input.SourceKind != "" {
			sourceFilter = "AND source_kind = ?"
			args = append(args, input.SourceKind)
		}
		args = append(args, input.Limit)
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, search_document_id::text, source_kind, source_id::text,
			       source_version, document_version, embedding_contract_id::text,
			       0::double precision AS distance,
			       ts_rank_cd(search_tsv, plainto_tsquery('simple', ?))::double precision AS text_rank
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND search_state IN ('pending', 'current')
			  AND search_tsv @@ plainto_tsquery('simple', ?)
			  `+sourceFilter+`
			ORDER BY text_rank DESC, updated_at DESC, search_document_id ASC
			LIMIT ?
		`, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			hit, err := scanV2SearchHit(rows)
			if err != nil {
				return err
			}
			hits = append(hits, hit)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 search: full-text search: %w", err)
	}
	return hits, nil
}

func (r *V2SearchRepositoryImpl) SearchExactVector(ctx context.Context, input V2ExactVectorSearchInput) ([]V2SearchHit, error) {
	input = normalizeV2ExactVectorSearchInput(input)
	if err := validateV2ExactVectorSearchInput(input); err != nil {
		return nil, err
	}
	profile, err := r.profileForVectorSearch(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(input.QueryEmbedding) != profile.EmbeddingDimensions {
		return nil, fmt.Errorf("%w: profile dimensions %d, query dimensions %d", ErrV2SearchProfileMismatch, profile.EmbeddingDimensions, len(input.QueryEmbedding))
	}
	vectorLiteral, err := v2VectorLiteral(input.QueryEmbedding)
	if err != nil {
		return nil, err
	}
	hits := []V2SearchHit{}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		sourceFilter := ""
		args := []any{vectorLiteral, input.TeamID, profile.EmbeddingContractID, profile.EmbeddingDimensions}
		if input.SourceKind != "" {
			sourceFilter = "AND source_kind = ?"
			args = append(args, input.SourceKind)
		}
		args = append(args, vectorLiteral, input.Limit)
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, search_document_id::text, source_kind, source_id::text,
			       source_version, document_version, embedding_contract_id::text,
			       (embedding <=> ?::vector)::double precision AS distance,
			       0::double precision AS text_rank
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND embedding_contract_id = ?::uuid
			  AND embedding_dimensions = ?
			  AND search_state = 'current'
			  AND embedding IS NOT NULL
			  `+sourceFilter+`
			ORDER BY embedding <=> ?::vector ASC, search_document_id ASC
			LIMIT ?
		`, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			hit, err := scanV2SearchHit(rows)
			if err != nil {
				return err
			}
			hits = append(hits, hit)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 search: exact vector search: %w", err)
	}
	return hits, nil
}

func (r *V2SearchRepositoryImpl) profileForDocument(ctx context.Context, input V2UpsertSearchDocumentInput) (*V2SearchProfile, error) {
	if input.EmbeddingContractID == "" {
		return r.GetActiveSearchProfile(ctx, input.ProfileKey)
	}
	profile, err := r.GetActiveSearchProfile(ctx, input.ProfileKey)
	if err != nil {
		return nil, err
	}
	if profile.EmbeddingContractID != input.EmbeddingContractID {
		return nil, fmt.Errorf("%w: requested contract %s is not active profile contract %s", ErrV2SearchProfileMismatch, input.EmbeddingContractID, profile.EmbeddingContractID)
	}
	return profile, nil
}

func (r *V2SearchRepositoryImpl) profileForVectorSearch(ctx context.Context, input V2ExactVectorSearchInput) (*V2SearchProfile, error) {
	profile, err := r.GetActiveSearchProfile(ctx, input.ProfileKey)
	if err != nil {
		return nil, err
	}
	if input.EmbeddingContractID != "" && input.EmbeddingContractID != profile.EmbeddingContractID {
		return nil, fmt.Errorf("%w: requested contract %s is not active profile contract %s", ErrV2SearchProfileMismatch, input.EmbeddingContractID, profile.EmbeddingContractID)
	}
	return profile, nil
}

func enqueueV2EmbeddingJob(ctx context.Context, tx *gorm.DB, document V2SearchDocumentResult) (string, error) {
	if document.SearchState != string(domain.V2SearchProjectionPending) {
		return "", nil
	}
	var jobID string
	err := tx.WithContext(ctx).Raw(`
		INSERT INTO embedding_jobs (
		    team_id, search_document_id, owner_profile_id, source_kind, source_id,
		    source_version, document_version, embedding_contract_id, embedding_dimensions
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?::uuid,
		    ?, ?, ?::uuid, ?
		)
		ON CONFLICT (
		    team_id, source_kind, source_id, source_version,
		    document_version, embedding_contract_id
		) DO NOTHING
		RETURNING embedding_job_id::text
	`, document.TeamID, document.SearchDocumentID, document.OwnerProfileID,
		document.SourceKind, document.SourceID, document.SourceVersion,
		document.DocumentVersion, document.EmbeddingContractID,
		document.EmbeddingDimensions).Scan(&jobID).Error
	if err != nil {
		return "", err
	}
	return jobID, nil
}

type v2SearchHitScanner interface {
	Scan(dest ...any) error
}

func scanV2SearchHit(scanner v2SearchHitScanner) (V2SearchHit, error) {
	var hit V2SearchHit
	err := scanner.Scan(
		&hit.TeamID,
		&hit.SearchDocumentID,
		&hit.SourceKind,
		&hit.SourceID,
		&hit.SourceVersion,
		&hit.DocumentVersion,
		&hit.EmbeddingContractID,
		&hit.Distance,
		&hit.TextRank,
	)
	return hit, err
}

func normalizeV2SearchProfileKey(profileKey string) string {
	profileKey = strings.TrimSpace(profileKey)
	if profileKey == "" {
		return defaultV2SearchProfileKey
	}
	return profileKey
}

func normalizeV2UpsertSearchDocumentInput(input V2UpsertSearchDocumentInput) V2UpsertSearchDocumentInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.ProfileKey = normalizeV2SearchProfileKey(input.ProfileKey)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.DocumentText = strings.TrimSpace(input.DocumentText)
	input.DocumentHash = strings.TrimSpace(input.DocumentHash)
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	if input.DocumentHash == "" && input.DocumentText != "" {
		sum := sha256.Sum256([]byte(input.DocumentText))
		input.DocumentHash = hex.EncodeToString(sum[:])
	}
	return input
}

func validateV2UpsertSearchDocumentInput(input V2UpsertSearchDocumentInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if !v2ValidSearchSourceKind(input.SourceKind) {
		return fmt.Errorf("unsupported source_kind %q", input.SourceKind)
	}
	if _, err := uuid.Parse(input.SourceID); err != nil {
		return fmt.Errorf("source_id is required: %w", err)
	}
	if input.SourceVersion < 1 {
		return errors.New("source_version must be greater than zero")
	}
	if input.DocumentText == "" {
		return errors.New("document_text is required")
	}
	if input.DocumentHash == "" {
		return errors.New("document_hash is required")
	}
	if input.EmbeddingContractID != "" {
		if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
			return fmt.Errorf("embedding_contract_id is invalid: %w", err)
		}
	}
	return nil
}

func normalizeV2FullTextSearchInput(input V2FullTextSearchInput) V2FullTextSearchInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	if input.Limit <= 0 {
		input.Limit = 10
	}
	if input.Limit > 100 {
		input.Limit = 100
	}
	return input
}

func validateV2FullTextSearchInput(input V2FullTextSearchInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.Query == "" {
		return errors.New("query is required")
	}
	if input.SourceKind != "" && !v2ValidSearchSourceKind(input.SourceKind) {
		return fmt.Errorf("unsupported source_kind %q", input.SourceKind)
	}
	return nil
}

func normalizeV2ExactVectorSearchInput(input V2ExactVectorSearchInput) V2ExactVectorSearchInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ProfileKey = normalizeV2SearchProfileKey(input.ProfileKey)
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	if input.Limit <= 0 {
		input.Limit = 10
	}
	if input.Limit > 100 {
		input.Limit = 100
	}
	return input
}

func validateV2ExactVectorSearchInput(input V2ExactVectorSearchInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.EmbeddingContractID != "" {
		if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
			return fmt.Errorf("embedding_contract_id is invalid: %w", err)
		}
	}
	if input.SourceKind != "" && !v2ValidSearchSourceKind(input.SourceKind) {
		return fmt.Errorf("unsupported source_kind %q", input.SourceKind)
	}
	if len(input.QueryEmbedding) == 0 {
		return errors.New("query_embedding is required")
	}
	return nil
}

func v2ValidSearchSourceKind(kind string) bool {
	return kind == "evidence" || kind == "relationship" || kind == "entity"
}

func marshalV2SearchJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}
	return encoded, nil
}

func v2VectorLiteral(values []float32) (string, error) {
	parts := make([]string, len(values))
	for i, value := range values {
		f := float64(value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "", fmt.Errorf("embedding contains non-finite value at index %d", i)
		}
		parts[i] = strconv.FormatFloat(f, 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}

func v2SearchMissingIndexCompatibility(profile *V2SearchProfile, indexDefinition string) []string {
	normalized := strings.Join(strings.Fields(strings.ToLower(indexDefinition)), " ")
	requirements := []struct {
		name  string
		token string
	}{
		{name: "hnsw access method", token: "using hnsw"},
		{name: "operator class", token: strings.ToLower(strings.TrimSpace(profile.OperatorClass))},
		{name: "embedding contract predicate", token: strings.ToLower(strings.TrimSpace(profile.EmbeddingContractID))},
		{name: "embedding dimension predicate", token: fmt.Sprintf("embedding_dimensions = %d", profile.EmbeddingDimensions)},
		{name: "current search-state predicate", token: "search_state = 'current'"},
		{name: "non-null embedding predicate", token: "embedding is not null"},
	}
	if token := v2SearchIndexExpressionToken(profile); token != "" {
		requirements = append(requirements, struct {
			name  string
			token string
		}{name: "indexed expression", token: token})
	}
	missing := make([]string, 0)
	for _, requirement := range requirements {
		if requirement.token == "" {
			missing = append(missing, requirement.name)
			continue
		}
		if !strings.Contains(normalized, requirement.token) {
			missing = append(missing, requirement.name)
		}
	}
	return missing
}

func v2SearchIndexExpressionToken(profile *V2SearchProfile) string {
	expression := strings.ToLower(strings.TrimSpace(profile.IndexedExpression))
	switch {
	case strings.Contains(expression, "halfvec"):
		return fmt.Sprintf("halfvec(%d)", profile.EmbeddingDimensions)
	case strings.Contains(expression, "vector("):
		return fmt.Sprintf("vector(%d)", profile.EmbeddingDimensions)
	case profile.IndexStrategy == string(domain.V2VectorIndexHalfvecHNSW):
		return fmt.Sprintf("halfvec(%d)", profile.EmbeddingDimensions)
	case expression != "":
		return "embedding"
	default:
		return ""
	}
}

func (r *V2SearchRepositoryImpl) withTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(tx *gorm.DB) error) error {
	if r.rls == nil {
		return errors.New("v2 search: rls helper is required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, fn)
}

func (r *V2SearchRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if r.rls == nil {
		return errors.New("v2 search: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}

func (r *V2SearchRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls == nil {
		return errors.New("v2 search: rls helper is required")
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}
