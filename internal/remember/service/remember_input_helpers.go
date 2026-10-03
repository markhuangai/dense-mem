package service

import (
	"fmt"
	"strconv"
	"strings"
)

func ValidateRelationshipCoverage(evidenceCount int, relationships []map[string]any) error {
	if len(relationships) == 0 {
		return &RememberValidationError{Issues: []RememberValidationIssue{{
			Path: "/relationships", Code: "required", Message: "relationships must contain at least one proposal citing submitted evidence",
		}}}
	}
	covered := make([]bool, evidenceCount)
	for _, relationship := range relationships {
		for _, raw := range rememberArrayValues(relationship["evidence_indices"]) {
			if _, stringIndex := raw.(string); stringIndex {
				continue
			}
			if index, ok := rememberEvidenceIndex(raw); ok && index >= 0 && index < evidenceCount {
				covered[index] = true
			}
		}
	}
	validation := &RememberValidationError{}
	for index, present := range covered {
		if !present {
			if len(validation.Issues) == maxRememberEvidenceItems {
				validation.IssuesTruncated = true
				break
			}
			validation.Issues = append(validation.Issues, RememberValidationIssue{
				Path: fmt.Sprintf("/evidence/%d", index), Code: "coverage",
				Message: "every evidence item must be cited by at least one relationship proposal through evidence_indices",
			})
		}
	}
	if len(validation.Issues) > 0 {
		return validation
	}
	return nil
}

func rememberArrayValues(raw any) []any {
	switch values := raw.(type) {
	case []int:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out
	case []any:
		return values
	case []map[string]any:
		out := make([]any, 0, len(values))
		for _, value := range values {
			out = append(out, value)
		}
		return out
	case []string:
		out := make([]any, 0, len(values))
		for _, value := range values {
			out = append(out, value)
		}
		return out
	default:
		return nil
	}
}

func rememberEvidenceIndex(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int8:
		return int(value), true
	case int16:
		return int(value), true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case uint:
		return int(value), true
	case uint8:
		return int(value), true
	case uint16:
		return int(value), true
	case uint32:
		return int(value), true
	case uint64:
		return int(value), true
	case float64:
		if value == float64(int(value)) {
			return int(value), true
		}
	case float32:
		if value == float32(int(value)) {
			return int(value), true
		}
	case string:
		index, err := strconv.Atoi(strings.TrimSpace(value))
		return index, err == nil
	}
	return 0, false
}
