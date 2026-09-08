package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

func normalizeUpsertSearchDocumentInput(input UpsertSearchDocumentInput) UpsertSearchDocumentInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.SourceID = strings.TrimSpace(input.SourceID)
	if input.ProjectionFormat <= 0 {
		input.ProjectionFormat = defaultProjectionFormat(input.SourceKind)
	}
	input.ProjectionGenerationID = strings.TrimSpace(input.ProjectionGenerationID)
	input.DocumentText = strings.TrimSpace(input.DocumentText)
	input.DocumentHash = strings.TrimSpace(input.DocumentHash)
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	input.SpaceKind = strings.TrimSpace(input.SpaceKind)
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
	if input.SpaceID != "" {
		if _, err := uuid.Parse(input.SpaceID); err != nil {
			return fmt.Errorf("space_id is invalid: %w", err)
		}
	}
	if input.SpaceGeneration < 0 {
		return errors.New("space_generation must not be negative")
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
	if input.ProjectionFormat < 1 {
		return errors.New("projection_format_version must be greater than zero")
	}
	if input.SourceKind == "relationship" && input.ProjectionFormat != 2 {
		return errors.New("relationship projection_format_version must be 2")
	}
	if input.ProjectionGenerationID != "" {
		if _, err := uuid.Parse(input.ProjectionGenerationID); err != nil {
			return fmt.Errorf("projection_generation_id is invalid: %w", err)
		}
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

func validSearchSourceKind(kind string) bool {
	return kind == "evidence" || kind == "relationship" || kind == "entity"
}

func defaultProjectionFormat(sourceKind string) int {
	if sourceKind == "relationship" {
		return 2
	}
	return 1
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
