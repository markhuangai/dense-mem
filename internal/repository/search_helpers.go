package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func normalizeUpsertSearchDocumentInput(input UpsertSearchDocumentInput) UpsertSearchDocumentInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
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

func validateUpsertSearchDocumentInput(input UpsertSearchDocumentInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if !validSearchSourceKind(input.SourceKind) {
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

func normalizeClaimEmbeddingJobsInput(input ClaimEmbeddingJobsInput) ClaimEmbeddingJobsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if input.Limit <= 0 {
		input.Limit = 10
	}
	if input.Limit > 100 {
		input.Limit = 100
	}
	if input.Lease <= 0 {
		input.Lease = time.Minute
	}
	return input
}

func validateClaimEmbeddingJobsInput(input ClaimEmbeddingJobsInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if input.Lease < time.Second {
		return errors.New("lease must be at least one second")
	}
	return nil
}

func normalizeCompleteEmbeddingJobInput(input CompleteEmbeddingJobInput) CompleteEmbeddingJobInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.EmbeddingJobID = strings.TrimSpace(input.EmbeddingJobID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	return input
}

func validateCompleteEmbeddingJobInput(input CompleteEmbeddingJobInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.EmbeddingJobID); err != nil {
		return fmt.Errorf("embedding_job_id is required: %w", err)
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if input.ExpectedAttempts < 1 {
		return errors.New("expected_attempts must be greater than zero")
	}
	if len(input.Embedding) == 0 {
		return errors.New("embedding is required")
	}
	return nil
}

func normalizeFullTextSearchInput(input FullTextSearchInput) FullTextSearchInput {
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

func validateFullTextSearchInput(input FullTextSearchInput) error {
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

func normalizeExactVectorSearchInput(input ExactVectorSearchInput) ExactVectorSearchInput {
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

func validateExactVectorSearchInput(input ExactVectorSearchInput) error {
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

func validSearchSourceKind(kind string) bool {
	return kind == "evidence" || kind == "relationship" || kind == "entity"
}

func marshalSearchJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}
	return encoded, nil
}

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

func searchMissingIndexCompatibility(contract *ActiveSearchContract, indexDefinition string) []string {
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
	if token := searchIndexExpressionToken(contract); token != "" {
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

func searchIndexExpressionToken(contract *ActiveSearchContract) string {
	expression := strings.ToLower(strings.TrimSpace(contract.IndexedExpression))
	switch {
	case strings.Contains(expression, "halfvec"):
		return fmt.Sprintf("halfvec(%d)", contract.EmbeddingDimensions)
	case strings.Contains(expression, "vector("):
		return fmt.Sprintf("vector(%d)", contract.EmbeddingDimensions)
	case contract.IndexStrategy == string(domain.VectorIndexHalfvecHNSW):
		return fmt.Sprintf("halfvec(%d)", contract.EmbeddingDimensions)
	case expression != "":
		return "embedding"
	default:
		return ""
	}
}
