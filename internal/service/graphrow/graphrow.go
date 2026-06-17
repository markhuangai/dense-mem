package graphrow

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func String(row map[string]any, key string) string {
	value, _ := row[key].(string)
	return value
}

func Int(row map[string]any, key string) int {
	return IntValue(row[key])
}

func IntValue(raw any) int {
	switch value := raw.(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	}
	return 0
}

func Float64(row map[string]any, key string) float64 {
	return Float64Value(row[key])
}

func Float64Value(raw any) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int32:
		return float64(value)
	case int64:
		return float64(value)
	}
	return 0
}

func Time(row map[string]any, key string) time.Time {
	value, _ := row[key].(time.Time)
	return value
}

func TimePtr(row map[string]any, key string) *time.Time {
	value, ok := row[key].(time.Time)
	if !ok {
		return nil
	}
	return &value
}

func StringSlice(row map[string]any, key string) []string {
	switch raw := row[key].(type) {
	case []string:
		return append([]string(nil), raw...)
	case []any:
		values := make([]string, 0, len(raw))
		for _, item := range raw {
			if value, ok := item.(string); ok && value != "" {
				values = append(values, value)
			}
		}
		return values
	default:
		return nil
	}
}

func Evidence(row map[string]any, key string) []domain.Evidence {
	raw, ok := row[key].([]any)
	if !ok {
		return nil
	}

	evidence := make([]domain.Evidence, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fragmentID := String(entry, "fragment_id")
		if fragmentID == "" {
			continue
		}
		evidence = append(evidence, domain.Evidence{
			FragmentID:        fragmentID,
			Speaker:           String(entry, "speaker"),
			SpanStart:         Int(entry, "span_start"),
			SpanEnd:           Int(entry, "span_end"),
			ExtractConf:       Float64(entry, "extract_conf"),
			ExtractionModel:   String(entry, "extraction_model"),
			ExtractionVersion: String(entry, "extraction_version"),
			PipelineRunID:     String(entry, "pipeline_run_id"),
			Authority:         domain.Authority(String(entry, "authority")),
		})
	}
	return evidence
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
