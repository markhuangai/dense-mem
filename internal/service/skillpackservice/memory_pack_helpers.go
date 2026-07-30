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

func memoryPackActor(ctx context.Context) (requestctx.ActorProfile, error) {
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return requestctx.ActorProfile{}, ErrMemoryPackAuthContext
	}
	return actor, nil
}

func MemoryPackEndpointText(endpoint MemoryPackEndpoint) string {
	if endpoint.Kind == "value" {
		return endpoint.Value
	}
	return endpoint.DisplayName
}

func MemoryPackSupportedPredicate(predicate string) bool {
	switch strings.TrimSpace(predicate) {
	case "works_on", "uses", "primary_database", "released":
		return true
	default:
		return false
	}
}

func memoryPackGraphNodes(nodes []repository.SemanticGraphNode) map[string]repository.SemanticGraphNode {
	out := map[string]repository.SemanticGraphNode{}
	for _, node := range nodes {
		out[node.Key] = node
	}
	return out
}

func memoryPackCandidateFromEdge(
	edge repository.SemanticGraphEdge,
	nodes map[string]repository.SemanticGraphNode,
) MemoryPackCandidate {
	subject := nodes[edge.Source]
	object := nodes[edge.Target]
	candidate := MemoryPackCandidate{
		RelationshipID:   edge.RelationshipID,
		PredicateKey:     edge.Relationship,
		SubjectEntityID:  subject.ID,
		Subject:          subject.Title,
		Object:           object.Title,
		Polarity:         "+",
		SupportCount:     edge.SupportCount,
		SourceGroupCount: edge.SourceGroupCount,
	}
	if object.Type == "value" {
		candidate.ObjectValueID = object.ID
		candidate.ObjectValueType = object.Body
	} else {
		candidate.ObjectEntityID = object.ID
	}
	return candidate
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

func clampLimit(value, fallback, maxValue int) int {
	if value <= 0 {
		return fallback
	}
	if value > maxValue {
		return maxValue
	}
	return value
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
