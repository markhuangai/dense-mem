package memorypack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func memoryPackActor(ctx context.Context) (requestctx.Actor, error) {
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return requestctx.Actor{}, ErrMemoryPackAuthContext
	}
	return actor, nil
}

func MemoryPackSortedEvidenceIDs(values map[string]MemoryPackEvidence) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func memoryPackShortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func skillPackFilename(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "memory-pack"
	}
	return slug + ".memory-pack.json"
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
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

func canonicalMemoryPackRelationshipID(value string) string {
	value = strings.TrimSpace(value)
	parsed, err := uuid.Parse(value)
	if err != nil {
		return value
	}
	return parsed.String()
}

func uniqueMemoryPackRelationshipIDs(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		normalized = append(normalized, canonicalMemoryPackRelationshipID(value))
	}
	return uniqueStrings(normalized)
}

func omitMemoryPackEntityNames(item *MemoryPackRelationship) {
	if item == nil {
		return
	}
	item.Subject.DisplayName = ""
	if item.Object.Kind != "value" {
		item.Object.DisplayName = ""
	}
}

func MemoryPackCopyMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func MemoryPackSupportOmissions(includeSupport bool, canonical []byte) []string {
	if includeSupport || len(canonical) == 0 {
		return nil
	}
	return []string{"support evidence omitted by request"}
}

func boolPtr(value bool) *bool {
	return &value
}
