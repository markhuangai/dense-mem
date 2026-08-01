package evalharness

import "strings"

func submissionProcessingState(out map[string]any) string {
	return stringValue(out["processing_state"])
}

func submissionSearchState(out map[string]any) string {
	return stringValue(out["search_state"])
}

func submissionErrorCode(out map[string]any) string {
	errorsValue, _ := out["errors"].([]any)
	for _, raw := range errorsValue {
		item, _ := raw.(map[string]any)
		if code := strings.TrimSpace(stringValue(item["code"])); code != "" {
			return code
		}
	}
	return ""
}
