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
		if strings.TrimSpace(value) != "" && value != uuid.Nil.String() {
			return value
		}
	}
	return ""
}
