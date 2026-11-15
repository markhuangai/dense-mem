package evalharness

import (
	"encoding/json"
	"math"
	"strings"
)

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func submissionProcessingState(out map[string]any) string {
	return stringValue(out["processing_state"])
}

func submissionErrorMessage(out map[string]any) string {
	errorsValue, _ := out["errors"].([]any)
	for _, raw := range errorsValue {
		item, _ := raw.(map[string]any)
		if message := stringValue(item["message"]); message != "" {
			return message
		}
		if code := stringValue(item["code"]); code != "" {
			return code
		}
	}
	items, _ := out["evidence"].([]any)
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]any)
		itemErrors, _ := item["errors"].([]any)
		for _, rawError := range itemErrors {
			errItem, _ := rawError.(map[string]any)
			if message := stringValue(errItem["message"]); message != "" {
				return message
			}
			if code := stringValue(errItem["code"]); code != "" {
				return code
			}
		}
	}
	return ""
}

func integerValue(value any) bool {
	switch number := value.(type) {
	case int, int64:
		return true
	case json.Number:
		_, err := number.Int64()
		return err == nil
	case float64:
		return !math.IsInf(number, 0) && !math.IsNaN(number) && number == math.Trunc(number) && number >= 0 && number <= float64(math.MaxInt32)
	default:
		return false
	}
}
