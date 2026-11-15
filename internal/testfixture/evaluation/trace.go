package evaluation

import (
	"encoding/json"
	"strings"
)

func traceFromToolOutput(tc Case, out map[string]any) RecallTrace {
	return RecallTrace{
		CaseID:              tc.CaseID,
		Query:               firstNonEmpty(stringValue(out["query"]), tc.Query),
		RankedRefs:          refsFromAny(out["ranked_refs"]),
		ContextRefs:         refsFromAny(out["context_refs"]),
		ContextEvidenceRefs: refsFromAny(out["context_evidence_refs"]),
		LatencyMS:           int64Value(out["latency_ms"]),
		ContextBlockChars:   intValue(out["context_block_chars"]),
		Raw:                 out,
	}
}

func refsFromAny(value any) []Ref {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	refs := make([]Ref, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		truncated, _ := m["equivalents_truncated"].(bool)
		refs = append(refs, Ref{
			Type:                  stringValue(m["type"]),
			ID:                    stringValue(m["id"]),
			Rank:                  intValue(m["rank"]),
			EquivalentEvidenceIDs: stringsFromAny(m["equivalent_evidence_ids"]),
			EquivalentsTruncated:  truncated,
		})
	}
	return refs
}

func stringsFromAny(value any) []string {
	switch raw := value.(type) {
	case []string:
		return uniqueNonEmpty(raw)
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if value := stringValue(item); value != "" {
				out = append(out, value)
			}
		}
		return uniqueNonEmpty(out)
	default:
		return nil
	}
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}

func int64Value(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		i, _ := v.Int64()
		return i
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
