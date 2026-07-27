package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	searchDefaultProvider              = "openai"
	searchDefaultVectorNormalization   = "provider"
	searchDefaultDocumentFormatVersion = 1
	searchDefaultQueryFormatVersion    = 1
	searchDefaultExactMaxRows          = 10000
	searchDefaultCandidateLimit        = 200
	searchDefaultHNSWM                 = 16
	searchDefaultHNSWEFConstruction    = 64
	searchDefaultQueryEFSearch         = 40
)

type embeddingContractDefinition struct {
	EmbeddingContractID   string
	ContractKey           string
	Version               int
	Provider              string
	Model                 string
	Dimensions            int
	DistanceMetric        string
	VectorNormalization   string
	DocumentFormatVersion int
	QueryFormatVersion    int
}

type searchIndexGenerationDefinition struct {
	SearchIndexGenerationID string
	Generation              int
	EmbeddingContractID     string
	EmbeddingDimensions     int
	AnnStrategy             string
	OperatorClass           string
	IndexedExpression       string
	PhysicalIndexName       string
	HNSWM                   int
	HNSWEFConstruction      int
	QueryEFSearch           int
	ExactMaxRows            int
	CandidateLimit          int
	AllowExactFallback      bool
	ActivationState         string
}

func (r *SearchRepositoryImpl) EnsureActiveSearchContract(
	ctx context.Context,
	input EnsureActiveSearchContractInput,
) (*EnsureActiveSearchContractResult, error) {
	input = normalizeEnsureActiveSearchContractInput(input)
	if err := validateEnsureActiveSearchContractInput(input); err != nil {
		return nil, err
	}
	if active, err := r.GetActiveSearchContract(ctx); err == nil {
		if err := r.requireSingleActiveEmbeddingContract(ctx); err != nil {
			return nil, err
		}
		if err := validateActiveContractMatchesConfig(active, input); err != nil {
			return nil, err
		}
		created, err := r.ensureSearchPhysicalIndex(ctx, active)
		if err != nil {
			return nil, err
		}
		return &EnsureActiveSearchContractResult{Contract: active, CreatedPhysicalIndex: created}, nil
	} else if !errors.Is(err, ErrSearchContractMismatch) {
		return nil, err
	}

	result := &EnsureActiveSearchContractResult{}
	var generation searchIndexGenerationDefinition
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		contract, created, err := ensureEmbeddingContractInTx(ctx, tx, input)
		if err != nil {
			return err
		}
		result.CreatedContract = created
		generation, created, err = ensureSearchGenerationInTx(ctx, tx, contract, input)
		if err != nil {
			return err
		}
		result.CreatedGeneration = created
		return nil
	})
	if err != nil {
		return nil, err
	}

	if generation.AnnStrategy != string(domain.VectorIndexExact) {
		if err := r.createSearchPhysicalIndex(ctx, generation); err != nil {
			_ = r.markSearchGenerationFailed(ctx, generation.SearchIndexGenerationID, err)
			return nil, err
		}
		result.CreatedPhysicalIndex = true
	}
	if generation.ActivationState != string(domain.SearchIndexGenerationActive) {
		if err := r.activateSearchGeneration(ctx, generation.SearchIndexGenerationID); err != nil {
			return nil, err
		}
	}

	active, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateActiveContractMatchesConfig(active, input); err != nil {
		return nil, err
	}
	result.Contract = active
	return result, nil
}

func (r *SearchRepositoryImpl) requireSingleActiveEmbeddingContract(ctx context.Context) error {
	var count int64
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT count(*)
			FROM embedding_contracts
			WHERE lifecycle_state = 'active'
		`).Scan(&count).Error
	})
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: expected one active embedding contract, found %d", ErrSearchContractMismatch, count)
	}
	return nil
}

func normalizeEnsureActiveSearchContractInput(input EnsureActiveSearchContractInput) EnsureActiveSearchContractInput {
	input.Provider = strings.TrimSpace(input.Provider)
	if input.Provider == "" {
		input.Provider = searchDefaultProvider
	}
	input.Model = strings.TrimSpace(input.Model)
	input.VectorNormalization = strings.TrimSpace(input.VectorNormalization)
	if input.VectorNormalization == "" {
		input.VectorNormalization = searchDefaultVectorNormalization
	}
	if input.DocumentFormatVersion <= 0 {
		input.DocumentFormatVersion = searchDefaultDocumentFormatVersion
	}
	if input.QueryFormatVersion <= 0 {
		input.QueryFormatVersion = searchDefaultQueryFormatVersion
	}
	if input.ExactMaxRows <= 0 {
		input.ExactMaxRows = searchDefaultExactMaxRows
	}
	if input.CandidateLimit <= 0 {
		input.CandidateLimit = searchDefaultCandidateLimit
	}
	return input
}

func validateEnsureActiveSearchContractInput(input EnsureActiveSearchContractInput) error {
	if input.Provider == "" {
		return errors.New("provider is required")
	}
	if input.Model == "" {
		return errors.New("model is required")
	}
	if input.Dimensions < 1 || input.Dimensions > 16000 {
		return errors.New("dimensions must be between 1 and 16000")
	}
	if input.VectorNormalization != "provider" && input.VectorNormalization != "unit" && input.VectorNormalization != "none" {
		return fmt.Errorf("unsupported vector_normalization %q", input.VectorNormalization)
	}
	return nil
}

func validateActiveContractMatchesConfig(contract *ActiveSearchContract, input EnsureActiveSearchContractInput) error {
	if contract == nil {
		return fmt.Errorf("%w: active search contract not found", ErrSearchContractMismatch)
	}
	if contract.EmbeddingProvider != input.Provider ||
		contract.EmbeddingModel != input.Model ||
		contract.EmbeddingDimensions != input.Dimensions ||
		contract.DistanceMetric != string(domain.VectorDistanceCosine) ||
		contract.VectorNormalization != input.VectorNormalization ||
		contract.DocumentFormatVersion != input.DocumentFormatVersion ||
		contract.QueryFormatVersion != input.QueryFormatVersion {
		return fmt.Errorf(
			"%w: configured embedding contract %s/%s/%d %s doc%d query%d, stored active contract %s/%s/%d %s doc%d query%d",
			ErrSearchContractMismatch,
			input.Provider,
			input.Model,
			input.Dimensions,
			input.VectorNormalization,
			input.DocumentFormatVersion,
			input.QueryFormatVersion,
			contract.EmbeddingProvider,
			contract.EmbeddingModel,
			contract.EmbeddingDimensions,
			contract.VectorNormalization,
			contract.DocumentFormatVersion,
			contract.QueryFormatVersion,
		)
	}
	expected := deriveSearchGenerationSpec(contract.EmbeddingContractID, input)
	if contract.IndexStrategy != expected.AnnStrategy ||
		contract.OperatorClass != expected.OperatorClass ||
		contract.IndexedExpression != expected.IndexedExpression ||
		!searchPhysicalIndexNameMatchesSpec(contract.PhysicalIndexName, expected) {
		return fmt.Errorf(
			"%w: active search generation strategy=%q operator_class=%q indexed_expression=%q physical_index_name=%q, "+
				"configured generation strategy=%q operator_class=%q indexed_expression=%q physical_index_name=%q",
			ErrSearchContractMismatch,
			contract.IndexStrategy,
			contract.OperatorClass,
			contract.IndexedExpression,
			contract.PhysicalIndexName,
			expected.AnnStrategy,
			expected.OperatorClass,
			expected.IndexedExpression,
			expected.PhysicalIndexName,
		)
	}
	return nil
}

func ensureEmbeddingContractInTx(
	ctx context.Context,
	tx *gorm.DB,
	input EnsureActiveSearchContractInput,
) (embeddingContractDefinition, bool, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT embedding_contract_id::text, contract_key, version, provider,
		       model, dimensions, distance_metric, vector_normalization,
		       document_format_version, query_format_version
		FROM embedding_contracts
		WHERE lifecycle_state = 'active'
		ORDER BY version DESC, created_at DESC, embedding_contract_id DESC
	`).Rows()
	if err != nil {
		return embeddingContractDefinition{}, false, fmt.Errorf("search bootstrap: list active embedding contracts: %w", err)
	}
	defer rows.Close()
	contracts := make([]embeddingContractDefinition, 0, 1)
	for rows.Next() {
		contract, err := scanEmbeddingContractDefinition(rows)
		if err != nil {
			return embeddingContractDefinition{}, false, err
		}
		contracts = append(contracts, contract)
	}
	if err := rows.Err(); err != nil {
		return embeddingContractDefinition{}, false, err
	}
	if len(contracts) > 1 {
		return embeddingContractDefinition{}, false, fmt.Errorf("%w: expected one active embedding contract, found %d", ErrSearchContractMismatch, len(contracts))
	}
	if len(contracts) == 1 {
		if err := validateEmbeddingContractDefinitionMatchesConfig(contracts[0], input); err != nil {
			return embeddingContractDefinition{}, false, err
		}
		return contracts[0], false, nil
	}

	var total int64
	if err := tx.WithContext(ctx).Raw(`SELECT count(*) FROM embedding_contracts`).Scan(&total).Error; err != nil {
		return embeddingContractDefinition{}, false, fmt.Errorf("search bootstrap: count embedding contracts: %w", err)
	}
	if total > 0 {
		return embeddingContractDefinition{}, false, fmt.Errorf("%w: embedding contracts exist but none is active", ErrSearchContractMismatch)
	}

	contractID := uuid.NewString()
	contract := embeddingContractDefinition{
		EmbeddingContractID:   contractID,
		ContractKey:           embeddingContractKey(input),
		Version:               1,
		Provider:              input.Provider,
		Model:                 input.Model,
		Dimensions:            input.Dimensions,
		DistanceMetric:        string(domain.VectorDistanceCosine),
		VectorNormalization:   input.VectorNormalization,
		DocumentFormatVersion: input.DocumentFormatVersion,
		QueryFormatVersion:    input.QueryFormatVersion,
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO embedding_contracts (
		    embedding_contract_id, contract_key, version, provider, model,
		    dimensions, distance_metric, vector_normalization,
		    document_format_version, query_format_version, lifecycle_state,
		    metadata
		) VALUES (
		    ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active',
		    '{"source":"config_bootstrap"}'::jsonb
		)
	`, contract.EmbeddingContractID, contract.ContractKey, contract.Version,
		contract.Provider, contract.Model, contract.Dimensions,
		contract.DistanceMetric, contract.VectorNormalization,
		contract.DocumentFormatVersion, contract.QueryFormatVersion).Error; err != nil {
		return embeddingContractDefinition{}, false, fmt.Errorf("search bootstrap: insert embedding contract: %w", err)
	}
	return contract, true, nil
}

func validateEmbeddingContractDefinitionMatchesConfig(
	contract embeddingContractDefinition,
	input EnsureActiveSearchContractInput,
) error {
	if contract.Provider != input.Provider ||
		contract.Model != input.Model ||
		contract.Dimensions != input.Dimensions ||
		contract.DistanceMetric != string(domain.VectorDistanceCosine) ||
		contract.VectorNormalization != input.VectorNormalization ||
		contract.DocumentFormatVersion != input.DocumentFormatVersion ||
		contract.QueryFormatVersion != input.QueryFormatVersion {
		return fmt.Errorf(
			"%w: configured embedding contract %s/%s/%d %s doc%d query%d, stored active contract %s/%s/%d %s doc%d query%d",
			ErrSearchContractMismatch,
			input.Provider,
			input.Model,
			input.Dimensions,
			input.VectorNormalization,
			input.DocumentFormatVersion,
			input.QueryFormatVersion,
			contract.Provider,
			contract.Model,
			contract.Dimensions,
			contract.VectorNormalization,
			contract.DocumentFormatVersion,
			contract.QueryFormatVersion,
		)
	}
	return nil
}

func ensureSearchGenerationInTx(
	ctx context.Context,
	tx *gorm.DB,
	contract embeddingContractDefinition,
	input EnsureActiveSearchContractInput,
) (searchIndexGenerationDefinition, bool, error) {
	spec := deriveSearchGenerationSpec(contract.EmbeddingContractID, input)
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT search_index_generation_id::text, generation,
		       embedding_contract_id::text, embedding_dimensions,
		       ann_strategy, operator_class, indexed_expression,
		       physical_index_name, hnsw_m, hnsw_ef_construction,
		       query_ef_search, exact_max_rows, candidate_limit,
		       allow_exact_fallback, activation_state
		FROM search_index_generations
		WHERE embedding_contract_id = ?::uuid
		ORDER BY generation DESC, created_at DESC, search_index_generation_id DESC
	`, contract.EmbeddingContractID).Rows()
	if err != nil {
		return searchIndexGenerationDefinition{}, false, fmt.Errorf("search bootstrap: list search generations: %w", err)
	}
	defer rows.Close()

	var latest *searchIndexGenerationDefinition
	for rows.Next() {
		generation, err := scanSearchGenerationDefinition(rows)
		if err != nil {
			return searchIndexGenerationDefinition{}, false, err
		}
		if latest == nil {
			latest = &generation
		}
		if generation.ActivationState == string(domain.SearchIndexGenerationActive) ||
			generation.ActivationState == string(domain.SearchIndexGenerationBuilding) {
			if err := validateSearchGenerationMatchesSpec(generation, spec); err != nil {
				return searchIndexGenerationDefinition{}, false, err
			}
			return generation, false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return searchIndexGenerationDefinition{}, false, err
	}

	nextGeneration := 1
	if latest != nil {
		nextGeneration = latest.Generation + 1
	}
	spec.SearchIndexGenerationID = uuid.NewString()
	spec.Generation = nextGeneration
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO search_index_generations (
		    search_index_generation_id, generation, embedding_contract_id,
		    embedding_dimensions, ann_strategy, operator_class,
		    indexed_expression, physical_index_name, hnsw_m,
		    hnsw_ef_construction, query_ef_search, exact_max_rows,
		    candidate_limit, allow_exact_fallback, activation_state,
		    activated_at, metadata
		) VALUES (
		    ?::uuid, ?, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		    CASE WHEN ? = 'exact' THEN 'active' ELSE 'building' END,
		    CASE WHEN ? = 'exact' THEN now() ELSE NULL END,
		    '{"source":"config_bootstrap"}'::jsonb
		)
	`, spec.SearchIndexGenerationID, spec.Generation, spec.EmbeddingContractID,
		spec.EmbeddingDimensions, spec.AnnStrategy, spec.OperatorClass,
		spec.IndexedExpression, spec.PhysicalIndexName, spec.HNSWM,
		spec.HNSWEFConstruction, spec.QueryEFSearch, spec.ExactMaxRows,
		spec.CandidateLimit, spec.AllowExactFallback, spec.AnnStrategy,
		spec.AnnStrategy).Error; err != nil {
		return searchIndexGenerationDefinition{}, false, fmt.Errorf("search bootstrap: insert search generation: %w", err)
	}
	if spec.AnnStrategy == string(domain.VectorIndexExact) {
		spec.ActivationState = string(domain.SearchIndexGenerationActive)
	} else {
		spec.ActivationState = string(domain.SearchIndexGenerationBuilding)
	}
	return spec, true, nil
}

func validateSearchGenerationMatchesSpec(
	generation searchIndexGenerationDefinition,
	spec searchIndexGenerationDefinition,
) error {
	if generation.AnnStrategy != spec.AnnStrategy ||
		generation.OperatorClass != spec.OperatorClass ||
		generation.IndexedExpression != spec.IndexedExpression ||
		!searchPhysicalIndexNameMatchesSpec(generation.PhysicalIndexName, spec) ||
		generation.EmbeddingDimensions != spec.EmbeddingDimensions ||
		generation.ExactMaxRows != spec.ExactMaxRows ||
		generation.CandidateLimit != spec.CandidateLimit ||
		generation.AllowExactFallback != spec.AllowExactFallback {
		return fmt.Errorf(
			"%w: existing search generation physical_index_name=%q does not match configured embedding contract physical_index_name=%q",
			ErrSearchContractMismatch,
			generation.PhysicalIndexName,
			spec.PhysicalIndexName,
		)
	}
	return nil
}

func deriveSearchGenerationSpec(
	contractID string,
	input EnsureActiveSearchContractInput,
) searchIndexGenerationDefinition {
	spec := searchIndexGenerationDefinition{
		EmbeddingContractID: contractID,
		EmbeddingDimensions: input.Dimensions,
		AnnStrategy:         string(domain.VectorIndexExact),
		HNSWM:               searchDefaultHNSWM,
		HNSWEFConstruction:  searchDefaultHNSWEFConstruction,
		QueryEFSearch:       searchDefaultQueryEFSearch,
		ExactMaxRows:        input.ExactMaxRows,
		CandidateLimit:      input.CandidateLimit,
	}
	switch {
	case input.Dimensions <= 2000:
		spec.AnnStrategy = string(domain.VectorIndexVectorHNSW)
		spec.OperatorClass = "vector_cosine_ops"
		spec.IndexedExpression = fmt.Sprintf("embedding::vector(%d)", input.Dimensions)
	case input.Dimensions <= 4000:
		spec.AnnStrategy = string(domain.VectorIndexHalfvecHNSW)
		spec.OperatorClass = "halfvec_cosine_ops"
		spec.IndexedExpression = fmt.Sprintf("embedding::halfvec(%d)", input.Dimensions)
	}
	if spec.AnnStrategy != string(domain.VectorIndexExact) {
		spec.PhysicalIndexName = derivedSearchIndexName(contractID, input.Dimensions, spec.AnnStrategy)
	}
	return spec
}

func (r *SearchRepositoryImpl) ensureSearchPhysicalIndex(
	ctx context.Context,
	contract *ActiveSearchContract,
) (bool, error) {
	if contract.IndexStrategy == string(domain.VectorIndexExact) {
		return false, nil
	}
	var indexDefinition string
	db, err := r.database()
	if err != nil {
		return false, err
	}
	if err := db.WithContext(ctx).Raw(`
		SELECT COALESCE((
		    SELECT indexdef
		    FROM pg_indexes
		    WHERE schemaname = current_schema()
		      AND indexname = ?
		), '')
	`, contract.PhysicalIndexName).Scan(&indexDefinition).Error; err != nil {
		return false, fmt.Errorf("search bootstrap: check physical index: %w", err)
	}
	if indexDefinition != "" {
		return false, nil
	}
	generation := searchIndexGenerationDefinition{
		SearchIndexGenerationID: contract.SearchIndexGenerationID,
		EmbeddingContractID:     contract.EmbeddingContractID,
		EmbeddingDimensions:     contract.EmbeddingDimensions,
		AnnStrategy:             contract.IndexStrategy,
		OperatorClass:           contract.OperatorClass,
		IndexedExpression:       contract.IndexedExpression,
		PhysicalIndexName:       contract.PhysicalIndexName,
		HNSWM:                   searchDefaultHNSWM,
		HNSWEFConstruction:      searchDefaultHNSWEFConstruction,
		QueryEFSearch:           searchDefaultQueryEFSearch,
	}
	if err := r.createSearchPhysicalIndex(ctx, generation); err != nil {
		return false, err
	}
	return true, nil
}

func (r *SearchRepositoryImpl) createSearchPhysicalIndex(
	ctx context.Context,
	generation searchIndexGenerationDefinition,
) error {
	if generation.AnnStrategy == string(domain.VectorIndexExact) {
		return nil
	}
	if _, err := uuid.Parse(generation.EmbeddingContractID); err != nil {
		return fmt.Errorf("search bootstrap: invalid embedding contract id: %w", err)
	}
	if generation.EmbeddingDimensions < 1 || generation.EmbeddingDimensions > 4000 {
		return fmt.Errorf("search bootstrap: HNSW dimensions out of range: %d", generation.EmbeddingDimensions)
	}
	expression := ""
	switch generation.AnnStrategy {
	case string(domain.VectorIndexVectorHNSW):
		expression = fmt.Sprintf("embedding::vector(%d)", generation.EmbeddingDimensions)
	case string(domain.VectorIndexHalfvecHNSW):
		expression = fmt.Sprintf("embedding::halfvec(%d)", generation.EmbeddingDimensions)
	default:
		return fmt.Errorf("search bootstrap: unsupported ann strategy %q", generation.AnnStrategy)
	}
	indexName, err := validateSearchIndexName(generation.PhysicalIndexName)
	if err != nil {
		return err
	}
	sqlDB, err := r.sqlDB()
	if err != nil {
		return err
	}
	ddl := fmt.Sprintf(`
		CREATE INDEX CONCURRENTLY IF NOT EXISTS %s
		    ON search_documents
		    USING hnsw ((%s) %s)
		    WITH (m = %d, ef_construction = %d)
		    WHERE embedding_contract_id = '%s'::uuid
		      AND embedding_dimensions = %d
		      AND search_state = 'current'
		      AND embedding IS NOT NULL
	`, pq.QuoteIdentifier(indexName), expression, generation.OperatorClass,
		searchDefaultHNSWM, searchDefaultHNSWEFConstruction,
		generation.EmbeddingContractID, generation.EmbeddingDimensions)
	if _, err := sqlDB.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("search bootstrap: create physical index %q: %w", indexName, err)
	}
	return nil
}

func validateSearchIndexName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 63 {
		return "", fmt.Errorf("search bootstrap: invalid physical index name %q", name)
	}
	for i, r := range name {
		valid := r == '_' || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')
		if !valid {
			return "", fmt.Errorf("search bootstrap: invalid physical index name %q", name)
		}
	}
	return name, nil
}

func (r *SearchRepositoryImpl) activateSearchGeneration(ctx context.Context, generationID string) error {
	return r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Exec(`
			UPDATE search_index_generations
			SET activation_state = 'active',
			    failure_reason = '',
			    activated_at = now()
			WHERE search_index_generation_id = ?::uuid
			  AND activation_state = 'building'
		`, generationID).Error
	})
}

func (r *SearchRepositoryImpl) markSearchGenerationFailed(ctx context.Context, generationID string, cause error) error {
	message := "physical index creation failed"
	if cause != nil {
		message = cause.Error()
	}
	return r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Exec(`
			UPDATE search_index_generations
			SET activation_state = 'failed',
			    failure_reason = ?
			WHERE search_index_generation_id = ?::uuid
			  AND activation_state = 'building'
		`, message, generationID).Error
	})
}

func scanEmbeddingContractDefinition(scanner interface {
	Scan(dest ...any) error
}) (embeddingContractDefinition, error) {
	var contract embeddingContractDefinition
	err := scanner.Scan(
		&contract.EmbeddingContractID,
		&contract.ContractKey,
		&contract.Version,
		&contract.Provider,
		&contract.Model,
		&contract.Dimensions,
		&contract.DistanceMetric,
		&contract.VectorNormalization,
		&contract.DocumentFormatVersion,
		&contract.QueryFormatVersion,
	)
	return contract, err
}

func scanSearchGenerationDefinition(scanner interface {
	Scan(dest ...any) error
}) (searchIndexGenerationDefinition, error) {
	var generation searchIndexGenerationDefinition
	err := scanner.Scan(
		&generation.SearchIndexGenerationID,
		&generation.Generation,
		&generation.EmbeddingContractID,
		&generation.EmbeddingDimensions,
		&generation.AnnStrategy,
		&generation.OperatorClass,
		&generation.IndexedExpression,
		&generation.PhysicalIndexName,
		&generation.HNSWM,
		&generation.HNSWEFConstruction,
		&generation.QueryEFSearch,
		&generation.ExactMaxRows,
		&generation.CandidateLimit,
		&generation.AllowExactFallback,
		&generation.ActivationState,
	)
	return generation, err
}

func embeddingContractKey(input EnsureActiveSearchContractInput) string {
	return fmt.Sprintf(
		"%s:%s:%d:%s:%s:doc%d:query%d",
		input.Provider,
		input.Model,
		input.Dimensions,
		domain.VectorDistanceCosine,
		input.VectorNormalization,
		input.DocumentFormatVersion,
		input.QueryFormatVersion,
	)
}

func derivedSearchIndexName(contractID string, dimensions int, strategy string) string {
	compact := strings.ReplaceAll(strings.ToLower(contractID), "-", "")
	if len(compact) > 12 {
		compact = compact[:12]
	}
	strategy = strings.TrimSuffix(strategy, "_hnsw")
	return fmt.Sprintf("search_%s_%d_%s_hnsw_idx", compact, dimensions, strategy)
}

func searchPhysicalIndexNameMatchesSpec(
	physicalIndexName string,
	spec searchIndexGenerationDefinition,
) bool {
	if physicalIndexName == spec.PhysicalIndexName {
		return true
	}
	if spec.PhysicalIndexName == "" {
		return false
	}
	legacyName := "v2_" + derivedSearchIndexName(
		spec.EmbeddingContractID,
		spec.EmbeddingDimensions,
		spec.AnnStrategy,
	)
	return physicalIndexName == legacyName
}

func (r *SearchRepositoryImpl) sqlDB() (*sql.DB, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("search: sql database: %w", err)
	}
	return sqlDB, nil
}
