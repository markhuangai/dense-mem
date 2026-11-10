package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func CanonicalRequestHash(req RememberRequest) (string, error) { return canonicalRequestHash(req) }

func CanonicalRequestBodyHash(evidence any) (string, error) {
	canonical, err := canonicalRememberObjects(evidence)
	if err != nil {
		return "", err
	}
	for _, item := range canonical {
		canonicalRememberEvidence(item)
	}
	data, err := json.Marshal(map[string]any{"contract_version": requestHashContractVersion, "evidence": canonical})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
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
	if forceInsert, ok := item["force_insert"].(bool); ok && !forceInsert {
		delete(item, "force_insert")
	}
	canonicalRememberStringSet(item, "supersedes_evidence_ids")
	canonicalRememberStringSet(item, "labels")
	canonicalRememberDropEmptyMap(item, "metadata")
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

func canonicalRememberDropEmptyMap(item map[string]any, field string) {
	value, ok := item[field].(map[string]any)
	if ok && len(value) == 0 {
		delete(item, field)
	}
}
