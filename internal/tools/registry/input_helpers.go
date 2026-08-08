package registry

import (
	"strings"

	"github.com/google/uuid"
)

func boolInput(value any) bool {
	parsed, _ := value.(bool)
	return parsed
}

func stringInput(value any) string {
	parsed, _ := value.(string)
	return strings.TrimSpace(parsed)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" && trimmed != uuid.Nil.String() {
			return trimmed
		}
	}
	return ""
}
