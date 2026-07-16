package repository

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func upsertSemanticValue(ctx context.Context, tx *gorm.DB, input SemanticRememberInput, display string) (domain.SemanticValue, error) {
	value := normalizeSemanticValue(display)
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO semantic_values (
		    team_id, owner_profile_id, value_type, canonical_value, display_value,
		    metadata, status, created_at, updated_at
		) VALUES (
		    ?, ?, ?, ?, ?, '{}'::jsonb, 'active', now(), now()
		)
		ON CONFLICT (team_id, value_type, canonical_value) WHERE status = 'active'
		DO UPDATE SET
		    display_value = COALESCE(NULLIF(semantic_values.display_value, ''), EXCLUDED.display_value),
		    updated_at = semantic_values.updated_at
		RETURNING team_id::text, value_id::text, owner_profile_id::text,
		          value_type, canonical_value, display_value, metadata::text, created_at, updated_at
	`, input.TeamID, input.OwnerProfileID, string(value.ValueType), value.CanonicalValue, value.DisplayValue).Rows()
	if err != nil {
		return domain.SemanticValue{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return domain.SemanticValue{}, sql.ErrNoRows
	}
	var stored domain.SemanticValue
	var valueType string
	if err := rows.Scan(
		&stored.TeamID,
		&stored.ValueID,
		&stored.OwnerProfileID,
		&valueType,
		&stored.CanonicalValue,
		&stored.DisplayValue,
		(*jsonMapScanner)(&stored.Metadata),
		&stored.CreatedAt,
		&stored.UpdatedAt,
	); err != nil {
		return domain.SemanticValue{}, err
	}
	stored.ValueType = domain.SemanticValueType(valueType)
	return stored, rows.Err()
}

func normalizeSemanticValue(display string) domain.SemanticValue {
	display = strings.TrimSpace(display)
	valueType := domain.SemanticValueString
	canonical := strings.ToLower(display)
	if parsed, err := strconv.ParseBool(strings.ToLower(display)); err == nil {
		valueType = domain.SemanticValueBoolean
		canonical = strconv.FormatBool(parsed)
	} else if parsed, err := strconv.ParseFloat(display, 64); err == nil {
		valueType = domain.SemanticValueNumber
		canonical = strconv.FormatFloat(parsed, 'f', -1, 64)
	} else if parsed, err := time.Parse("2006-01-02", display); err == nil {
		valueType = domain.SemanticValueDate
		canonical = parsed.Format("2006-01-02")
	} else if parsed, err := time.Parse(time.RFC3339, display); err == nil {
		valueType = domain.SemanticValueDateTime
		canonical = parsed.UTC().Format(time.RFC3339)
	}
	return domain.SemanticValue{
		ValueType:      valueType,
		CanonicalValue: canonical,
		DisplayValue:   display,
	}
}
