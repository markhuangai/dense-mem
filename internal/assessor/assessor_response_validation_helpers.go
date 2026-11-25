package assessor

import (
	"fmt"
	"strings"
)

func assessmentBoundedRequiredString(value string, max int) bool {
	return strings.TrimSpace(value) != "" && len([]rune(value)) <= max
}

func assessmentSpanKey(evidenceID string, start int, end int) string {
	return fmt.Sprintf("%s:%d:%d", evidenceID, start, end)
}
