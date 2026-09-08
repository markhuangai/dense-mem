package postgres

import (
	"strings"

	"github.com/google/uuid"
)

const relationshipForegroundRecallGenerationMetadataKey = "relationship_foreground_recall_generation_id"

func normalizeRecallUUIDList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if _, err := uuid.Parse(value); err != nil {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
