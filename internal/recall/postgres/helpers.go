package postgres

import (
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func uuidPtrValue(id *uuid.UUID) any {
	if id == nil || *id == uuid.Nil {
		return nil
	}
	return id.String()
}

func parseNullableUUID(raw sql.NullString) *uuid.UUID {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	parsed, err := uuid.Parse(raw.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func activeSemanticSpaceGenerationSQL(alias string) string {
	return storagepostgres.ActiveSemanticSpaceGenerationSQL(alias)
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

type searchHitScanner interface {
	Scan(dest ...any) error
}

func scanSearchHit(scanner searchHitScanner) (SearchHit, error) {
	var hit SearchHit
	err := scanner.Scan(
		&hit.TeamID,
		&hit.SearchDocumentID,
		&hit.SourceKind,
		&hit.SourceID,
		&hit.SourceVersion,
		&hit.DocumentVersion,
		&hit.EmbeddingContractID,
		&hit.SearchState,
		&hit.Distance,
		&hit.TextRank,
	)
	return hit, err
}
