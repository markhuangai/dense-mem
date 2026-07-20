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
	v2SearchDefaultProvider              = "openai"
	v2SearchDefaultVectorNormalization   = "provider"
	v2SearchDefaultDocumentFormatVersion = 1
	v2SearchDefaultQueryFormatVersion    = 1
	v2SearchDefaultExactMaxRows          = 10000
	v2SearchDefaultCandidateLimit        = 200
	v2SearchDefaultHNSWM                 = 16
	v2SearchDefaultHNSWEFConstruction    = 64
	v2SearchDefaultQueryEFSearch         = 40
)

type v2EmbeddingContractDefinition struct {
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

type v2SearchIndexGenerationDefinition struct {
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

func (r *V2SearchRepositoryImpl) EnsureActiveSearchContract(
	ctx context.Context,
	input V2EnsureActiveSearchContractInput,
) (*V2EnsureActiveSearchContractResult, error) {
	input = normalizeV2EnsureActiveSearchContractInput(input)
	if err := validateV2EnsureActiveSearchContractInput(input); err != nil {
		return nil, err
	}
	if active, err := r.GetActiveSearchContract(ctx); err == nil {
		if err := r.requireSingleActiveEmbeddingContract(ctx); err != nil {
			return nil, err
		}
		if err := validateV2ActiveContractMatchesConfig(active, input); err != nil {
			return nil, err
		}
		created, err := r.ensureV2SearchPhysicalIndex(ctx, active)
		if err != nil {
			return nil, err
		}
		return &V2EnsureActiveSearchContractResult{Contract: active, CreatedPhysicalIndex: created}, nil
	} else if !errors.Is(err, ErrV2SearchContractMismatch) {
		return nil, err
	}

	result := &V2EnsureActiveSearchContractResult{}
	var generation v2SearchIndexGenerationDefinition
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		contract, created, err := ensureV2EmbeddingContractInTx(ctx, tx, input)
		if err != nil {
			return err
		}
		result.CreatedContract = created
		generation, created, err = ensureV2SearchGenerationInTx(ctx, tx, contract, input)
		if err != nil {
			return err
		}
		result.CreatedGeneration = created
		return nil
	})
	if err != nil {
		return nil, err
	}

	if generation.AnnStrategy != string(domain.V2VectorIndexExact) {
		if err := r.createV2SearchPhysicalIndex(ctx, generation); err != nil {
			_ = r.markV2SearchGenerationFailed(ctx, generation.SearchIndexGenerationID, err)
			return nil, err
		}
		result.CreatedPhysicalIndex = true
	}
	if generation.ActivationState != string(domain.V2SearchIndexGenerationActive) {
		if err := r.activateV2SearchGeneration(ctx, generation.SearchIndexGenerationID); err != nil {
			return nil, err
		}
	}

	active, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateV2ActiveContractMatchesConfig(active, input); err != nil {
		return nil, err
	}
	result.Contract = active
	return result, nil
}

func (r *V2SearchRepositoryImpl) requireSingleActiveEmbeddingContract(ctx context.Context) error {
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
		return fmt.Errorf("%w: expected one active embedding contract, found %d", ErrV2SearchContractMismatch, count)
	}
	return nil
}

func normalizeV2EnsureActiveSearchContractInput(input V2EnsureActiveSearchContractInput) V2EnsureActiveSearchContractInput {
	input.Provider = strings.TrimSpace(input.Provider)
	if input.Provider == "" {
		input.Provider = v2SearchDefaultProvider
	}
	input.Model = strings.TrimSpace(input.Model)
	input.VectorNormalization = strings.TrimSpace(input.VectorNormalization)
	if input.VectorNormalization == "" {
		input.VectorNormalization = v2SearchDefaultVectorNormalization
	}
	if input.DocumentFormatVersion <= 0 {
		input.DocumentFormatVersion = v2SearchDefaultDocumentFormatVersion
	}
	if input.QueryFormatVersion <= 0 {
		input.QueryFormatVersion = v2SearchDefaultQueryFormatVersion
	}
	if input.ExactMaxRows <= 0 {
		input.ExactMaxRows = v2SearchDefaultExactMaxRows
	}
	if input.CandidateLimit <= 0 {
		input.CandidateLimit = v2SearchDefaultCandidateLimit
	}
	return input
}

func validateV2EnsureActiveSearchContractInput(input V2EnsureActiveSearchContractInput) error {
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

func validateV2ActiveContractMatchesConfig(contract *V2ActiveSearchContract, input V2EnsureActiveSearchContractInput) error {
	if contract == nil {
		return fmt.Errorf("%w: active search contract not found", ErrV2SearchContractMismatch)
	}
	if contract.EmbeddingProvider != input.Provider ||
		contract.EmbeddingModel != input.Model ||
		contract.EmbeddingDimensions != input.Dimensions ||
		contract.DistanceMetric != string(domain.V2VectorDistanceCosine) ||
		contract.VectorNormalization != input.VectorNormalization ||
		contract.DocumentFormatVersion != input.DocumentFormatVersion ||
		contract.QueryFormatVersion != input.QueryFormatVersion {
		return fmt.Errorf(
			"%w: configured embedding contract %s/%s/%d %s doc%d query%d, stored active contract %s/%s/%d %s doc%d query%d",
			ErrV2SearchContractMismatch,
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
	expected := deriveV2SearchGenerationSpec(contract.EmbeddingContractID, input)
	if contract.IndexStrategy != expected.AnnStrategy ||
		contract.OperatorClass != expected.OperatorClass ||
		contract.IndexedExpression != expected.IndexedExpression ||
		contract.PhysicalIndexName != expected.PhysicalIndexName {
		return fmt.Errorf(
			"%w: active search generation %s/%s/%s, configured generation %s/%s/%s",
			ErrV2SearchContractMismatch,
			contract.IndexStrategy,
			contract.OperatorClass,
			contract.IndexedExpression,
			expected.AnnStrategy,
			expected.OperatorClass,
			expected.IndexedExpression,
		)
	}
	return nil
}

func ensureV2EmbeddingContractInTx(
	ctx context.Context,
	tx *gorm.DB,
	input V2EnsureActiveSearchContractInput,
) (v2EmbeddingContractDefinition, bool, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT embedding_contract_id::text, contract_key, version, provider,
		       model, dimensions, distance_metric, vector_normalization,
		       document_format_version, query_format_version
		FROM embedding_contracts
		WHERE lifecycle_state = 'active'
		ORDER BY version DESC, created_at DESC, embedding_contract_id DESC
	`).Rows()
	if err != nil {
		return v2EmbeddingContractDefinition{}, false, fmt.Errorf("v2 search bootstrap: list active embedding contracts: %w", err)
	}
	defer rows.Close()
	contracts := make([]v2EmbeddingContractDefinition, 0, 1)
	for rows.Next() {
		contract, err := scanV2EmbeddingContractDefinition(rows)
		if err != nil {
			return v2EmbeddingContractDefinition{}, false, err
		}
		contracts = append(contracts, contract)
	}
	if err := rows.Err(); err != nil {
		return v2EmbeddingContractDefinition{}, false, err
	}
	if len(contracts) > 1 {
		return v2EmbeddingContractDefinition{}, false, fmt.Errorf("%w: expected one active embedding contract, found %d", ErrV2SearchContractMismatch, len(contracts))
	}
	if len(contracts) == 1 {
		if err := validateV2EmbeddingContractDefinitionMatchesConfig(contracts[0], input); err != nil {
			return v2EmbeddingContractDefinition{}, false, err
		}
		return contracts[0], false, nil
	}

	var total int64
	if err := tx.WithContext(ctx).Raw(`SELECT count(*) FROM embedding_contracts`).Scan(&total).Error; err != nil {
		return v2EmbeddingContractDefinition{}, false, fmt.Errorf("v2 search bootstrap: count embedding contracts: %w", err)
	}
	if total > 0 {
		return v2EmbeddingContractDefinition{}, false, fmt.Errorf("%w: embedding contracts exist but none is active", ErrV2SearchContractMismatch)
	}

	contractID := uuid.NewString()
	contract := v2EmbeddingContractDefinition{
		EmbeddingContractID:   contractID,
		ContractKey:           v2EmbeddingContractKey(input),
		Version:               1,
		Provider:              input.Provider,
		Model:                 input.Model,
		Dimensions:            input.Dimensions,
		DistanceMetric:        string(domain.V2VectorDistanceCosine),
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
		return v2EmbeddingContractDefinition{}, false, fmt.Errorf("v2 search bootstrap: insert embedding contract: %w", err)
	}
	return contract, true, nil
}

func validateV2EmbeddingContractDefinitionMatchesConfig(
	contract v2EmbeddingContractDefinition,
	input V2EnsureActiveSearchContractInput,
) error {
	if contract.Provider != input.Provider ||
		contract.Model != input.Model ||
		contract.Dimensions != input.Dimensions ||
		contract.DistanceMetric != string(domain.V2VectorDistanceCosine) ||
		contract.VectorNormalization != input.VectorNormalization ||
		contract.DocumentFormatVersion != input.DocumentFormatVersion ||
		contract.QueryFormatVersion != input.QueryFormatVersion {
		return fmt.Errorf(
			"%w: configured embedding contract %s/%s/%d %s doc%d query%d, stored active contract %s/%s/%d %s doc%d query%d",
			ErrV2SearchContractMismatch,
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

func ensureV2SearchGenerationInTx(
	ctx context.Context,
	tx *gorm.DB,
	contract v2EmbeddingContractDefinition,
	input V2EnsureActiveSearchContractInput,
) (v2SearchIndexGenerationDefinition, bool, error) {
	spec := deriveV2SearchGenerationSpec(contract.EmbeddingContractID, input)
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
		return v2SearchIndexGenerationDefinition{}, false, fmt.Errorf("v2 search bootstrap: list search generations: %w", err)
	}
	defer rows.Close()

	var latest *v2SearchIndexGenerationDefinition
	for rows.Next() {
		generation, err := scanV2SearchGenerationDefinition(rows)
		if err != nil {
			return v2SearchIndexGenerationDefinition{}, false, err
		}
		if latest == nil {
			latest = &generation
		}
		if generation.ActivationState == string(domain.V2SearchIndexGenerationActive) ||
			generation.ActivationState == string(domain.V2SearchIndexGenerationBuilding) {
			if err := validateV2SearchGenerationMatchesSpec(generation, spec); err != nil {
				return v2SearchIndexGenerationDefinition{}, false, err
			}
			return generation, false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return v2SearchIndexGenerationDefinition{}, false, err
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
		return v2SearchIndexGenerationDefinition{}, false, fmt.Errorf("v2 search bootstrap: insert search generation: %w", err)
	}
	if spec.AnnStrategy == string(domain.V2VectorIndexExact) {
		spec.ActivationState = string(domain.V2SearchIndexGenerationActive)
	} else {
		spec.ActivationState = string(domain.V2SearchIndexGenerationBuilding)
	}
	return spec, true, nil
}

func validateV2SearchGenerationMatchesSpec(
	generation v2SearchIndexGenerationDefinition,
	spec v2SearchIndexGenerationDefinition,
) error {
	if generation.AnnStrategy != spec.AnnStrategy ||
		generation.OperatorClass != spec.OperatorClass ||
		generation.IndexedExpression != spec.IndexedExpression ||
		generation.PhysicalIndexName != spec.PhysicalIndexName ||
		generation.EmbeddingDimensions != spec.EmbeddingDimensions ||
		generation.ExactMaxRows != spec.ExactMaxRows ||
		generation.CandidateLimit != spec.CandidateLimit ||
		generation.AllowExactFallback != spec.AllowExactFallback {
		return fmt.Errorf("%w: existing search generation does not match configured embedding contract", ErrV2SearchContractMismatch)
	}
	return nil
}

func deriveV2SearchGenerationSpec(
	contractID string,
	input V2EnsureActiveSearchContractInput,
) v2SearchIndexGenerationDefinition {
	spec := v2SearchIndexGenerationDefinition{
		EmbeddingContractID: contractID,
		EmbeddingDimensions: input.Dimensions,
		AnnStrategy:         string(domain.V2VectorIndexExact),
		HNSWM:               v2SearchDefaultHNSWM,
		HNSWEFConstruction:  v2SearchDefaultHNSWEFConstruction,
		QueryEFSearch:       v2SearchDefaultQueryEFSearch,
		ExactMaxRows:        input.ExactMaxRows,
		CandidateLimit:      input.CandidateLimit,
	}
	switch {
	case input.Dimensions <= 2000:
		spec.AnnStrategy = string(domain.V2VectorIndexVectorHNSW)
		spec.OperatorClass = "vector_cosine_ops"
		spec.IndexedExpression = fmt.Sprintf("embedding::vector(%d)", input.Dimensions)
	case input.Dimensions <= 4000:
		spec.AnnStrategy = string(domain.V2VectorIndexHalfvecHNSW)
		spec.OperatorClass = "halfvec_cosine_ops"
		spec.IndexedExpression = fmt.Sprintf("embedding::halfvec(%d)", input.Dimensions)
	}
	if spec.AnnStrategy != string(domain.V2VectorIndexExact) {
		spec.PhysicalIndexName = v2DerivedSearchIndexName(contractID, input.Dimensions, spec.AnnStrategy)
	}
	return spec
}

func (r *V2SearchRepositoryImpl) ensureV2SearchPhysicalIndex(
	ctx context.Context,
	contract *V2ActiveSearchContract,
) (bool, error) {
	if contract.IndexStrategy == string(domain.V2VectorIndexExact) {
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
		return false, fmt.Errorf("v2 search bootstrap: check physical index: %w", err)
	}
	if indexDefinition != "" {
		return false, nil
	}
	generation := v2SearchIndexGenerationDefinition{
		SearchIndexGenerationID: contract.SearchIndexGenerationID,
		EmbeddingContractID:     contract.EmbeddingContractID,
		EmbeddingDimensions:     contract.EmbeddingDimensions,
		AnnStrategy:             contract.IndexStrategy,
		OperatorClass:           contract.OperatorClass,
		IndexedExpression:       contract.IndexedExpression,
		PhysicalIndexName:       contract.PhysicalIndexName,
		HNSWM:                   v2SearchDefaultHNSWM,
		HNSWEFConstruction:      v2SearchDefaultHNSWEFConstruction,
		QueryEFSearch:           v2SearchDefaultQueryEFSearch,
	}
	if err := r.createV2SearchPhysicalIndex(ctx, generation); err != nil {
		return false, err
	}
	return true, nil
}

func (r *V2SearchRepositoryImpl) createV2SearchPhysicalIndex(
	ctx context.Context,
	generation v2SearchIndexGenerationDefinition,
) error {
	if generation.AnnStrategy == string(domain.V2VectorIndexExact) {
		return nil
	}
	if _, err := uuid.Parse(generation.EmbeddingContractID); err != nil {
		return fmt.Errorf("v2 search bootstrap: invalid embedding contract id: %w", err)
	}
	if generation.EmbeddingDimensions < 1 || generation.EmbeddingDimensions > 4000 {
		return fmt.Errorf("v2 search bootstrap: HNSW dimensions out of range: %d", generation.EmbeddingDimensions)
	}
	expression := ""
	switch generation.AnnStrategy {
	case string(domain.V2VectorIndexVectorHNSW):
		expression = fmt.Sprintf("embedding::vector(%d)", generation.EmbeddingDimensions)
	case string(domain.V2VectorIndexHalfvecHNSW):
		expression = fmt.Sprintf("embedding::halfvec(%d)", generation.EmbeddingDimensions)
	default:
		return fmt.Errorf("v2 search bootstrap: unsupported ann strategy %q", generation.AnnStrategy)
	}
	indexName, err := validateV2SearchIndexName(generation.PhysicalIndexName)
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
		v2SearchDefaultHNSWM, v2SearchDefaultHNSWEFConstruction,
		generation.EmbeddingContractID, generation.EmbeddingDimensions)
	if _, err := sqlDB.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("v2 search bootstrap: create physical index %q: %w", indexName, err)
	}
	return nil
}

func validateV2SearchIndexName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 63 {
		return "", fmt.Errorf("v2 search bootstrap: invalid physical index name %q", name)
	}
	for i, r := range name {
		valid := r == '_' || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')
		if !valid {
			return "", fmt.Errorf("v2 search bootstrap: invalid physical index name %q", name)
		}
	}
	return name, nil
}

func (r *V2SearchRepositoryImpl) activateV2SearchGeneration(ctx context.Context, generationID string) error {
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

func (r *V2SearchRepositoryImpl) markV2SearchGenerationFailed(ctx context.Context, generationID string, cause error) error {
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

func scanV2EmbeddingContractDefinition(scanner interface {
	Scan(dest ...any) error
}) (v2EmbeddingContractDefinition, error) {
	var contract v2EmbeddingContractDefinition
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

func scanV2SearchGenerationDefinition(scanner interface {
	Scan(dest ...any) error
}) (v2SearchIndexGenerationDefinition, error) {
	var generation v2SearchIndexGenerationDefinition
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

func v2EmbeddingContractKey(input V2EnsureActiveSearchContractInput) string {
	return fmt.Sprintf(
		"%s:%s:%d:%s:%s:doc%d:query%d",
		input.Provider,
		input.Model,
		input.Dimensions,
		domain.V2VectorDistanceCosine,
		input.VectorNormalization,
		input.DocumentFormatVersion,
		input.QueryFormatVersion,
	)
}

func v2DerivedSearchIndexName(contractID string, dimensions int, strategy string) string {
	compact := strings.ReplaceAll(strings.ToLower(contractID), "-", "")
	if len(compact) > 12 {
		compact = compact[:12]
	}
	strategy = strings.TrimSuffix(strategy, "_hnsw")
	return fmt.Sprintf("v2_search_%s_%d_%s_hnsw_idx", compact, dimensions, strategy)
}

func (r *V2SearchRepositoryImpl) sqlDB() (*sql.DB, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("v2 search: sql database: %w", err)
	}
	return sqlDB, nil
}
