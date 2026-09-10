package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

func normalizeFullTextSearchInput(input searchcontract.FullTextSearchInput) searchcontract.FullTextSearchInput {
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

func NormalizeFullTextSearchInput(input searchcontract.FullTextSearchInput) searchcontract.FullTextSearchInput {
	return normalizeFullTextSearchInput(input)
}

func validateFullTextSearchInput(input searchcontract.FullTextSearchInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.Query == "" {
		return errors.New("query is required")
	}
	if input.SourceKind != "" && !validSearchSourceKind(input.SourceKind) {
		return fmt.Errorf("unsupported source_kind %q", input.SourceKind)
	}
	return nil
}

func ValidateFullTextSearchInput(input searchcontract.FullTextSearchInput) error {
	return validateFullTextSearchInput(input)
}

func normalizeExactVectorSearchInput(input searchcontract.ExactVectorSearchInput) searchcontract.ExactVectorSearchInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
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

func NormalizeExactVectorSearchInput(input searchcontract.ExactVectorSearchInput) searchcontract.ExactVectorSearchInput {
	return normalizeExactVectorSearchInput(input)
}

func validateExactVectorSearchInput(input searchcontract.ExactVectorSearchInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.EmbeddingContractID != "" {
		if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
			return fmt.Errorf("embedding_contract_id is invalid: %w", err)
		}
	}
	if input.SourceKind != "" && !validSearchSourceKind(input.SourceKind) {
		return fmt.Errorf("unsupported source_kind %q", input.SourceKind)
	}
	if len(input.QueryEmbedding) == 0 {
		return errors.New("query_embedding is required")
	}
	return nil
}

func ValidateExactVectorSearchInput(input searchcontract.ExactVectorSearchInput) error {
	return validateExactVectorSearchInput(input)
}

func validSearchSourceKind(kind string) bool {
	return kind == "evidence" || kind == "relationship" || kind == "entity"
}

func ValidSearchSourceKind(kind string) bool { return validSearchSourceKind(kind) }

func defaultProjectionFormat(sourceKind string) int {
	if sourceKind == "relationship" {
		return 2
	}
	return 1
}

func DefaultProjectionFormat(sourceKind string) int { return defaultProjectionFormat(sourceKind) }

func vectorLiteral(values []float32) (string, error) {
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

func VectorLiteral(values []float32) (string, error) { return vectorLiteral(values) }

func searchMissingIndexCompatibility(contract *searchcontract.ActiveSearchContract, indexDefinition string) []string {
	normalized := strings.Join(strings.Fields(strings.ToLower(indexDefinition)), " ")
	requirements := []struct {
		name  string
		token string
	}{
		{name: "hnsw access method", token: "using hnsw"},
		{name: "operator class", token: strings.ToLower(strings.TrimSpace(contract.OperatorClass))},
		{name: "embedding contract predicate", token: strings.ToLower(strings.TrimSpace(contract.EmbeddingContractID))},
		{name: "embedding dimension predicate", token: fmt.Sprintf("embedding_dimensions = %d", contract.EmbeddingDimensions)},
		{name: "current search-state predicate", token: "search_state = 'current'"},
		{name: "non-null embedding predicate", token: "embedding is not null"},
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
	if !searchIndexExpressionCompatible(contract, indexDefinition) {
		missing = append(missing, "indexed expression")
	}
	return missing
}

func SearchMissingIndexCompatibility(contract *searchcontract.ActiveSearchContract, indexDefinition string) []string {
	return searchMissingIndexCompatibility(contract, indexDefinition)
}

func searchIndexExpressionCompatible(contract *searchcontract.ActiveSearchContract, indexDefinition string) bool {
	token := searchIndexExpressionToken(contract)
	if token == "" {
		return true
	}
	normalized := strings.Join(strings.Fields(strings.ToLower(indexDefinition)), " ")
	if contract.IndexStrategy != string(domain.VectorIndexBinaryHNSW) {
		return strings.Contains(normalized, token)
	}
	canonical := strings.NewReplacer("(", "", ")", "", " ", "").Replace(normalized)
	return strings.Contains(canonical, fmt.Sprintf("binary_quantizeembedding::bit%d", contract.EmbeddingDimensions))
}

func SearchIndexExpressionCompatible(contract *searchcontract.ActiveSearchContract, indexDefinition string) bool {
	return searchIndexExpressionCompatible(contract, indexDefinition)
}

func searchIndexExpressionToken(contract *searchcontract.ActiveSearchContract) string {
	expression := strings.ToLower(strings.TrimSpace(contract.IndexedExpression))
	switch {
	case strings.Contains(expression, "binary_quantize") && strings.Contains(expression, "bit("):
		return fmt.Sprintf("binary_quantize(embedding)::bit(%d)", contract.EmbeddingDimensions)
	case strings.Contains(expression, "halfvec"):
		return fmt.Sprintf("halfvec(%d)", contract.EmbeddingDimensions)
	case strings.Contains(expression, "vector("):
		return fmt.Sprintf("vector(%d)", contract.EmbeddingDimensions)
	case contract.IndexStrategy == string(domain.VectorIndexBinaryHNSW):
		return fmt.Sprintf("binary_quantize(embedding)::bit(%d)", contract.EmbeddingDimensions)
	case contract.IndexStrategy == string(domain.VectorIndexHalfvecHNSW):
		return fmt.Sprintf("halfvec(%d)", contract.EmbeddingDimensions)
	case expression != "":
		return "embedding"
	default:
		return ""
	}
}

func searchDocumentHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
