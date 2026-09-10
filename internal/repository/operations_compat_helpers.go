package repository

import (
	"database/sql"
	"strings"

	"github.com/google/uuid"
)

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

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
