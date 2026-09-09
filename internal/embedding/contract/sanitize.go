package contract

import (
	"regexp"
	"strings"
)

const redactedPlaceholder = "[REDACTED]"

type sanitizedError struct {
	msg  string
	orig error
}

func (e *sanitizedError) Error() string { return e.msg }
func (e *sanitizedError) Unwrap() error { return e.orig }

func SanitizeError(err error, apiKey string) error {
	if err == nil {
		return nil
	}
	return &sanitizedError{msg: "embedding provider error: " + scrubMessage(err.Error(), apiKey), orig: err}
}

func scrubMessage(msg string, apiKey string) string {
	result := msg
	if apiKey != "" {
		result = strings.ReplaceAll(result, apiKey, redactedPlaceholder)
	}
	bearerPattern := regexp.MustCompile(`(?i)Bearer\s+[^\s]+`)
	result = bearerPattern.ReplaceAllString(result, "Bearer "+redactedPlaceholder)
	skPattern := regexp.MustCompile(`sk-[a-zA-Z0-9]+`)
	return skPattern.ReplaceAllString(result, redactedPlaceholder)
}

func ScrubLogFields(fields ...any) []any {
	if len(fields) == 0 {
		return fields
	}
	result := make([]any, len(fields))
	for i, field := range fields {
		if value, ok := field.(string); ok {
			result[i] = scrubMessage(value, "")
		} else {
			result[i] = field
		}
	}
	return result
}
