package evalharness

import "strings"

func placementProcessingState(out map[string]any) string {
	if status := nestedString(out, "placement", "status"); status != "" {
		return status
	}
	return stringValue(out["processing_state"])
}

func placementSearchState(out map[string]any) string {
	if state := stringValue(out["search_state"]); state != "" {
		return state
	}
	if state := nestedString(out, "placement", "search_state"); state != "" {
		return state
	}
	items, _ := out["items"].([]any)
	state := ""
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if itemState := stringValue(item["search_state"]); itemState != "" {
			state = combinePlacementSearchState(state, itemState)
		}
	}
	return state
}

func combinePlacementSearchState(left, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "failed" || right == "failed" {
		return "failed"
	}
	if left == "pending" || right == "pending" {
		return "pending"
	}
	if left == "current" || right == "current" {
		return "current"
	}
	if left == "not_required" || right == "not_required" {
		return "not_required"
	}
	return firstNonEmpty(left, right)
}

func placementErrorMessage(out map[string]any) string {
	if cause := nestedString(out, "placement", "error"); cause != "" {
		return cause
	}
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
	items, _ := out["items"].([]any)
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
