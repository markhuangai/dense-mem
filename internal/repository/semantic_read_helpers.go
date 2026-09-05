package repository

import (
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// These helpers are shared by repository read adapters. They contain only
// bounded decoding and normalization, not capability policy.
func jSONMap(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "[]" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		return out
	}
	return map[string]any{"raw": raw}
}

func jSON(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		return out
	}
	return map[string]any{"raw": raw}
}

func boolDefault(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

func clampInt(value, defaultValue, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func defaultPositiveInt(value, defaultValue int) int {
	if value <= 0 {
		return defaultValue
	}
	return value
}

func normalizeOptionalRelevance(value *float64) *float64 {
	if value == nil {
		return nil
	}
	normalized := normalizeRelevance(*value)
	return &normalized
}

func normalizeRelevance(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
