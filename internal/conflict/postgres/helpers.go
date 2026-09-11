package postgres

import (
	"strings"

	"github.com/google/uuid"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func activeSemanticSpaceGenerationSQL(alias string) string {
	return storagepostgres.ActiveSemanticSpaceGenerationSQL(alias)
}

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
