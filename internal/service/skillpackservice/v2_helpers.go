package skillpackservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func v2MemoryPackActor(ctx context.Context) (requestctx.ActorProfile, error) {
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return requestctx.ActorProfile{}, ErrV2MemoryPackAuthContext
	}
	return actor, nil
}

func v2MemoryPackEndpointText(endpoint V2MemoryPackEndpoint) string {
	if endpoint.Kind == "value" {
		return endpoint.Value
	}
	return endpoint.DisplayName
}

func v2MemoryPackSupportedPredicate(predicate string) bool {
	switch strings.TrimSpace(predicate) {
	case "works_on", "uses", "primary_database", "released":
		return true
	default:
		return false
	}
}

func v2MemoryPackGraphNodes(nodes []repository.V2SemanticGraphNode) map[string]string {
	out := map[string]string{}
	for _, node := range nodes {
		out[node.Key] = node.Title
	}
	return out
}

func v2MemoryPackSortedKeys(values map[string]V2MemoryPackEvidenceFragment) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func v2MemoryPackShortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func v2MemoryPackCopyMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func v2MemoryPackSupportOmissions(includeSupport bool, canonical []byte) []string {
	if includeSupport {
		return nil
	}
	if len(canonical) == 0 {
		return nil
	}
	return []string{"support evidence omitted by request"}
}

func boolPtr(value bool) *bool {
	return &value
}
