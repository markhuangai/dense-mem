package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

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

func loadEvidenceConflictPositions(ctx context.Context, tx *gorm.DB, teamID, conflictID string) ([]EvidenceConflictPositionRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`SELECT conflict_id::text, position_id::text, position_key, canonical_evidence_id::text, canonical_owner_profile_id::text, occurrence_id::text, occurrence_owner_profile_id::text, quote, span_start, span_end, authority, submitted, created_at FROM evidence_conflict_positions WHERE team_id = ?::uuid AND conflict_id = ?::uuid ORDER BY position_key, position_id`, teamID, conflictID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EvidenceConflictPositionRecord{}
	for rows.Next() {
		var item EvidenceConflictPositionRecord
		if err := rows.Scan(&item.ConflictID, &item.PositionID, &item.PositionKey, &item.CanonicalEvidenceID, &item.CanonicalOwnerProfileID, &item.OccurrenceID, &item.OccurrenceOwnerProfileID, &item.Quote, &item.SpanStart, &item.SpanEnd, &item.Authority, &item.Submitted, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
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
