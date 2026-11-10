package evalharness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func canonicalRetainedRememberObjects(value any) ([]map[string]any, error) {
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

func canonicalRetainedRememberEvidence(item map[string]any) {
	for _, field := range []string{
		"source_type", "source", "source_group", "authority", "source_key",
		"source_revision", "previous_source_revision",
	} {
		canonicalRetainedRememberTrimString(item, field, true)
	}
	if forceInsert, ok := item["force_insert"].(bool); ok && !forceInsert {
		delete(item, "force_insert")
	}
	canonicalRetainedRememberStringSet(item, "supersedes_evidence_ids")
	canonicalRetainedRememberStringSet(item, "labels")
	canonicalRetainedRememberDropEmptyMap(item, "metadata")
}

func canonicalRetainedRememberTrimString(item map[string]any, field string, omitEmpty bool) {
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

func canonicalRetainedRememberStringSet(item map[string]any, field string) {
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
	canonicalRetainedRememberSortSet(values)
	item[field] = values
}

func canonicalRetainedRememberSortSet(values []any) {
	sort.SliceStable(values, func(i, j int) bool {
		return canonicalRetainedRememberValueOrder(values[i]) < canonicalRetainedRememberValueOrder(values[j])
	})
}

func canonicalRetainedRememberValueOrder(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%T:%v", value, value)
	}
	return string(raw)
}

func canonicalRetainedRememberDropEmptyMap(item map[string]any, field string) {
	value, ok := item[field].(map[string]any)
	if ok && len(value) == 0 {
		delete(item, field)
	}
}

// C's retired empty-array field participates only in its sealed offline request hash.
func canonicalRetainedEvidenceRequestBodyHash(evidence any) (string, error) {
	canonical, err := canonicalRetainedRememberObjects(evidence)
	if err != nil {
		return "", err
	}
	for _, item := range canonical {
		canonicalRetainedRememberEvidence(item)
	}
	raw, err := json.Marshal(map[string]any{"contract_version": "dense-mem.v2.6.2", "evidence": canonical, "relationships": []any{}})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
