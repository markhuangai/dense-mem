package remember

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CanonicalRequestBodyHash hashes the normalized v2.6.2 Remember body. Evidence
// and Value text remain byte-exact; ordering changes only for contract sets and
// relationship refs.
func CanonicalRequestBodyHash(
	evidence any,
	entityHints []map[string]any,
	relationshipHints []map[string]any,
) (string, error) {
	return canonicalRequestBodyHashForContract(requestHashContractVersion, evidence, entityHints, relationshipHints)
}

// CanonicalLegacyRequestBodyHash hashes the v2.6.1 Remember body retained by
// submitted Dream confirmations created before the v2.6.2 cutover.
func CanonicalLegacyRequestBodyHash(
	evidence any,
	entityHints []map[string]any,
	relationshipHints []map[string]any,
) (string, error) {
	return canonicalRequestBodyHashForContract("dense-mem.v2.6.1", evidence, entityHints, relationshipHints)
}

func canonicalRequestBodyHashForContract(
	contractVersion string,
	evidence any,
	entityHints []map[string]any,
	relationshipHints []map[string]any,
) (string, error) {
	contractVersion = strings.TrimSpace(contractVersion)
	if contractVersion == "" {
		return "", fmt.Errorf("remember request hash contract version is required")
	}
	canonicalEvidence, err := canonicalRememberObjects(evidence)
	if err != nil {
		return "", err
	}
	for _, item := range canonicalEvidence {
		canonicalRememberEvidence(item)
	}
	canonicalEntities, err := canonicalRememberObjects(entityHints)
	if err != nil {
		return "", err
	}
	for _, item := range canonicalEntities {
		canonicalRememberEntity(item)
	}
	sort.SliceStable(canonicalEntities, func(i, j int) bool {
		return canonicalRememberObjectOrder(canonicalEntities[i], canonicalEntities[j], "ref")
	})
	canonicalRelationships, err := canonicalRememberObjects(relationshipHints)
	if err != nil {
		return "", err
	}
	for _, item := range canonicalRelationships {
		canonicalRememberRelationship(item)
	}
	sort.SliceStable(canonicalRelationships, func(i, j int) bool {
		return canonicalRememberObjectOrder(canonicalRelationships[i], canonicalRelationships[j], "ref")
	})
	payload := map[string]any{
		"contract_version": contractVersion,
		"evidence":         canonicalEvidence,
		"relationships":    canonicalRelationships,
	}
	if len(canonicalEntities) > 0 {
		payload["entity_hints"] = canonicalEntities
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func canonicalRequestHashForVersion(req RememberRequest, version string) (string, error) {
	hash, err := canonicalRequestBodyHashForContract(version, req.Evidence, req.EntityHints, req.RelationshipHints)
	if err != nil {
		return "", fmt.Errorf("remember: canonical request hash: %w", err)
	}
	return hash, nil
}

func canonicalRememberObjects(value any) ([]map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return []map[string]any{}, nil
	}
	var objects []map[string]any
	if err := json.Unmarshal(raw, &objects); err != nil {
		return nil, err
	}
	if objects == nil {
		objects = []map[string]any{}
	}
	return objects, nil
}

func canonicalRememberEvidence(item map[string]any) {
	for _, field := range []string{
		"source_type", "source", "source_group", "authority", "source_key",
		"source_revision", "previous_source_revision",
	} {
		canonicalRememberTrimString(item, field, true)
	}
	canonicalRememberStringSet(item, "supersedes_evidence_ids")
	canonicalRememberStringSet(item, "labels")
	canonicalRememberDropEmptyMap(item, "metadata")
}

func canonicalRememberRelationship(item map[string]any) {
	canonicalRememberTrimString(item, "ref", false)
	canonicalRememberTrimString(item, "polarity", false)
	canonicalRememberTrimString(item, "valid_from", true)
	canonicalRememberTrimString(item, "valid_to", true)
	canonicalRememberScalarSet(item, "evidence_indices")
	canonicalRememberStringSet(item, "known_evidence_ids")
	canonicalRememberEntity(canonicalRememberMap(item["subject"]))
	if predicate := canonicalRememberMap(item["predicate"]); predicate != nil {
		canonicalRememberTrimString(predicate, "proposed_key", true)
		canonicalRememberTrimString(predicate, "known_predicate_key", true)
	}
	if object := canonicalRememberMap(item["object"]); object != nil {
		canonicalRememberEntity(canonicalRememberMap(object["entity"]))
		if value := canonicalRememberMap(object["value"]); value != nil {
			canonicalRememberTrimString(value, "type", false)
		}
	}
	if correction := canonicalRememberMap(item["correction_target"]); correction != nil {
		canonicalRememberTrimString(correction, "relationship_id", false)
	}
	if conflict := canonicalRememberMap(item["conflict_context"]); conflict != nil {
		canonicalRememberTrimString(conflict, "conflict_id", false)
	}
	canonicalRememberDropNil(item, "client_comment")
}

func canonicalRememberEntity(item map[string]any) {
	if item == nil {
		return
	}
	canonicalRememberTrimString(item, "ref", true)
	canonicalRememberTrimString(item, "entity_kind", true)
	canonicalRememberTrimString(item, "known_entity_id", true)
	canonicalRememberTrimString(item, "entity_id", true)
}

func canonicalRememberTrimString(item map[string]any, field string, omitEmpty bool) {
	if item == nil {
		return
	}
	value, exists := item[field]
	if !exists || value == nil {
		if value == nil {
			delete(item, field)
		}
		return
	}
	text, ok := value.(string)
	if !ok {
		return
	}
	text = strings.TrimSpace(text)
	if omitEmpty && text == "" {
		delete(item, field)
		return
	}
	item[field] = text
}

func canonicalRememberStringSet(item map[string]any, field string) {
	values, ok := item[field].([]any)
	if !ok || len(values) == 0 {
		delete(item, field)
		return
	}
	for index, value := range values {
		if text, ok := value.(string); ok {
			values[index] = strings.TrimSpace(text)
		}
	}
	canonicalRememberSortSet(values)
	item[field] = values
}

func canonicalRememberScalarSet(item map[string]any, field string) {
	values, ok := item[field].([]any)
	if !ok {
		return
	}
	canonicalRememberSortSet(values)
	item[field] = values
}

func canonicalRememberSortSet(values []any) {
	sort.SliceStable(values, func(i, j int) bool {
		return canonicalRememberValueOrder(values[i]) < canonicalRememberValueOrder(values[j])
	})
}

func canonicalRememberValueOrder(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%T:%v", value, value)
	}
	return string(raw)
}

func canonicalRememberObjectOrder(left, right map[string]any, field string) bool {
	leftValue, _ := left[field].(string)
	rightValue, _ := right[field].(string)
	if leftValue != rightValue {
		return leftValue < rightValue
	}
	return canonicalRememberValueOrder(left) < canonicalRememberValueOrder(right)
}

func canonicalRememberMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func canonicalRememberDropEmptyMap(item map[string]any, field string) {
	value, ok := item[field].(map[string]any)
	if ok && len(value) == 0 {
		delete(item, field)
	}
}

func canonicalRememberDropNil(item map[string]any, field string) {
	if item[field] == nil {
		delete(item, field)
	}
}
