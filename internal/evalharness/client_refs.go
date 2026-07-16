package evalharness

import (
	"encoding/json"
	"fmt"
	"strings"
)

func seedMetadata(item CorpusItem) map[string]any {
	out := map[string]any{}
	for key, value := range item.Metadata {
		out[key] = value
	}
	out["source_doc_id"] = item.SourceDocID
	out["source_dataset"] = item.SourceDataset
	out["eval_seed"] = true
	return out
}

func sourceDocIDsFromKnowledgeItem(kind string, item map[string]any, fragmentSourceDocIDs, claimSourceDocIDs map[string][]string) []string {
	sourceDocID := firstNonEmpty(
		nestedString(item, "metadata", "source_doc_id"),
		nestedString(item, "classification", "source_doc_id"),
		nestedString(item, "classification", "eval_source_doc_id"),
	)
	sourceDocIDs := []string{}
	if sourceDocID != "" {
		sourceDocIDs = append(sourceDocIDs, sourceDocID)
	}
	switch kind {
	case "claim":
		for _, fragmentID := range stringsFromAny(item["supported_by"]) {
			sourceDocIDs = append(sourceDocIDs, fragmentSourceDocIDs[fragmentID]...)
		}
	case "fact":
		sourceDocIDs = append(sourceDocIDs, claimSourceDocIDs[stringValue(item["promoted_from_claim_id"])]...)
	}
	return uniqueNonEmpty(sourceDocIDs)
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

func dreamSourceRefsFromKnowledgeItem(item map[string]any) []Ref {
	raw, ok := item["source_refs"].([]any)
	if !ok {
		return nil
	}
	refs := make([]Ref, 0, len(raw))
	for _, value := range raw {
		m, ok := value.(map[string]any)
		if !ok {
			continue
		}
		ref := Ref{
			Type: stringValue(m["type"]),
			ID:   stringValue(m["id"]),
		}
		if ref.Type != "" && ref.ID != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func knowledgeItemID(kind string, item map[string]any) string {
	switch kind {
	case "fragment":
		return firstNonEmpty(stringValue(item["fragment_id"]), stringValue(item["id"]))
	case "claim":
		return firstNonEmpty(stringValue(item["claim_id"]), stringValue(item["id"]))
	case "fact":
		return firstNonEmpty(stringValue(item["fact_id"]), stringValue(item["id"]))
	case "dream":
		return firstNonEmpty(stringValue(item["dream_id"]), stringValue(item["id"]))
	default:
		return stringValue(item["id"])
	}
}

func fragmentIDFromRemember(out map[string]any) string {
	fragment, _ := out["fragment"].(map[string]any)
	if id := firstNonEmpty(stringValue(fragment["id"]), stringValue(fragment["fragment_id"])); id != "" {
		return id
	}
	evidence, _ := out["evidence"].([]any)
	if len(evidence) == 0 {
		return ""
	}
	first, _ := evidence[0].(map[string]any)
	return firstNonEmpty(stringValue(first["id"]), stringValue(first["fragment_id"]))
}

func refsFromPlacement(out map[string]any, sourceDocID string) []Ref {
	if strings.TrimSpace(sourceDocID) == "" {
		return nil
	}
	return refsFromPlacementBatch(out, []CorpusItem{{SourceDocID: sourceDocID}})[sourceDocID]
}

func refsFromPlacementBatch(out map[string]any, items []CorpusItem) map[string][]Ref {
	placement, _ := out["placement"].(map[string]any)
	placementItems, _ := placement["items"].([]any)
	refsBySourceDocID := map[string][]Ref{}
	for ordinal, raw := range placementItems {
		item, _ := raw.(map[string]any)
		sourceDocID := sourceDocIDForPlacementItem(item, ordinal, items)
		if sourceDocID == "" {
			continue
		}
		if id := stringValue(item["fragment_id"]); id != "" {
			refsBySourceDocID[sourceDocID] = append(refsBySourceDocID[sourceDocID],
				Ref{Type: "evidence", ID: id, SourceDocID: sourceDocID},
				Ref{Type: "fragment", ID: id, SourceDocID: sourceDocID},
			)
		}
	}
	for ordinal, raw := range placementItems {
		item, _ := raw.(map[string]any)
		sourceDocID := sourceDocIDForPlacementItem(item, ordinal, items)
		if sourceDocID == "" {
			continue
		}
		for _, id := range relationshipIDsFromPlacementItem(item) {
			refsBySourceDocID[sourceDocID] = append(refsBySourceDocID[sourceDocID], Ref{Type: "relationship", ID: id, SourceDocID: sourceDocID})
		}
	}
	return refsBySourceDocID
}

func relationshipIDsFromPlacementItem(item map[string]any) []string {
	raw, ok := item["relationship_outcomes"].([]any)
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(raw))
	for _, value := range raw {
		outcome, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if id := stringValue(outcome["relationship_id"]); id != "" {
			ids = append(ids, id)
		}
	}
	return uniqueNonEmpty(ids)
}

func sourceDocIDForPlacementItem(item map[string]any, ordinal int, corpus []CorpusItem) string {
	if sourceDocID := stringValue(item["source_doc_id"]); sourceDocID != "" {
		return sourceDocID
	}
	evidenceIndex := intValue(item["evidence_index"])
	if _, hasEvidenceIndex := item["evidence_index"]; hasEvidenceIndex && evidenceIndex >= 0 && evidenceIndex < len(corpus) {
		return strings.TrimSpace(corpus[evidenceIndex].SourceDocID)
	}
	if ordinal >= 0 && ordinal < len(corpus) {
		return strings.TrimSpace(corpus[ordinal].SourceDocID)
	}
	return ""
}

func refsFromRememberBatch(out map[string]any, items []CorpusItem) map[string][]Ref {
	refsBySourceDocID := map[string][]Ref{}
	evidence, _ := out["evidence"].([]any)
	for index, raw := range evidence {
		if index >= len(items) {
			break
		}
		first, _ := raw.(map[string]any)
		if id := firstNonEmpty(stringValue(first["id"]), stringValue(first["fragment_id"])); id != "" {
			sourceDocID := strings.TrimSpace(items[index].SourceDocID)
			refsBySourceDocID[sourceDocID] = append(refsBySourceDocID[sourceDocID], Ref{Type: "fragment", ID: id, SourceDocID: sourceDocID})
		}
	}
	if len(refsBySourceDocID) == 0 && len(items) == 1 {
		if fragmentID := fragmentIDFromRemember(out); fragmentID != "" {
			sourceDocID := strings.TrimSpace(items[0].SourceDocID)
			refsBySourceDocID[sourceDocID] = append(refsBySourceDocID[sourceDocID], Ref{Type: "fragment", ID: fragmentID, SourceDocID: sourceDocID})
		}
	}
	return refsBySourceDocID
}

func sourceDocIDRange(items []CorpusItem) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		return items[0].SourceDocID
	}
	return fmt.Sprintf("%s..%s (%d items)", items[0].SourceDocID, items[len(items)-1].SourceDocID, len(items))
}

func traceFromToolOutput(tc Case, out map[string]any) RecallTrace {
	initialResponse, _ := out["initial_response"].(map[string]any)
	return normalizeRecallTrace(RecallTrace{
		CaseID:              tc.CaseID,
		Query:               firstNonEmpty(stringValue(out["query"]), tc.Query),
		RankedRefs:          refsFromAny(out["ranked_refs"]),
		InitialResponse:     initialResponse,
		FrontierRefs:        frontierRefsFromInitialResponse(initialResponse),
		ContextRefs:         refsFromAny(out["context_refs"]),
		ContextEvidenceRefs: refsFromAny(out["context_evidence_refs"]),
		DreamRefs:           refsFromAny(out["dream_refs"]),
		LatencyMS:           int64Value(out["latency_ms"]),
		ContextBlockChars:   intValue(out["context_block_chars"]),
		Raw:                 out,
	})
}

func traceFromRecallMemoryResponse(tc Case, response map[string]any, latencyMS int64) RecallTrace {
	rankedRefs := recallResultRefs(response)
	return normalizeRecallTrace(RecallTrace{
		CaseID:              tc.CaseID,
		Query:               tc.Query,
		RankedRefs:          rankedRefs,
		InitialResponse:     response,
		FrontierRefs:        frontierRefsFromInitialResponse(response),
		ContextEvidenceRefs: evidenceRefsFromInitialResponse(response),
		DreamRefs:           hypothesisRefsFromInitialResponse(response),
		LatencyMS:           latencyMS,
		Raw: map[string]any{
			"case_id":          tc.CaseID,
			"query":            tc.Query,
			"ranked_refs":      refsToAny(rankedRefs),
			"initial_response": response,
			"latency_ms":       latencyMS,
		},
	})
}

func normalizeRecallTrace(trace RecallTrace) RecallTrace {
	if len(trace.RankedRefs) == 0 && trace.InitialResponse != nil {
		trace.RankedRefs = recallResultRefs(trace.InitialResponse)
	}
	if trace.FrontierRefs == nil {
		trace.FrontierRefs = frontierRefsFromInitialResponse(trace.InitialResponse)
	}
	if trace.ContextEvidenceRefs == nil {
		trace.ContextEvidenceRefs = evidenceRefsFromInitialResponse(trace.InitialResponse)
	}
	if trace.DreamRefs == nil {
		trace.DreamRefs = hypothesisRefsFromInitialResponse(trace.InitialResponse)
	}
	return trace
}

func recallResultRefs(response map[string]any) []Ref {
	raw, ok := response["results"].([]any)
	if !ok {
		return nil
	}
	refs := make([]Ref, 0, len(raw))
	for _, item := range raw {
		result, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ref, ok := recallRefFromResult(result, len(refs)+1)
		if !ok {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

func recallRefFromResult(result map[string]any, rank int) (Ref, bool) {
	sourceDocID := sourceDocIDFromRecallResult(result)
	if id := firstNonEmpty(stringValue(result["evidence_id"]), stringValue(result["fragment_id"])); id != "" {
		return Ref{Type: "evidence", ID: id, SourceDocID: sourceDocID, Rank: rank}, true
	}
	if id := stringValue(result["relationship_id"]); id != "" {
		return Ref{Type: "relationship", ID: id, SourceDocID: sourceDocID, Rank: rank}, true
	}
	if id := firstNonEmpty(stringValue(result["id"]), nestedPathString(result, "fragment", "id")); id != "" {
		return Ref{Type: "fragment", ID: id, SourceDocID: sourceDocID, Rank: rank}, true
	}
	return Ref{}, false
}

func sourceDocIDFromRecallResult(result map[string]any) string {
	return firstNonEmpty(
		stringValue(result["source_doc_id"]),
		nestedPathString(result, "metadata", "source_doc_id"),
		nestedPathString(result, "fragment", "source_doc_id"),
		nestedPathString(result, "fragment", "metadata", "source_doc_id"),
		nestedPathString(result, "classification", "source_doc_id"),
		nestedPathString(result, "classification", "eval_source_doc_id"),
	)
}

func frontierRefsFromInitialResponse(response map[string]any) []Ref {
	if response == nil {
		return nil
	}
	raw, ok := response["discovery_paths"].([]any)
	if !ok {
		raw, ok = response["connections"].([]any)
		if !ok {
			return nil
		}
		return legacyConnectionRefs(raw)
	}
	refs := make([]Ref, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		path, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for _, rawID := range anySlice(path["evidence_ids"]) {
			id := stringValue(rawID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			refs = append(refs, Ref{Type: "evidence", ID: id, Rank: len(refs) + 1})
		}
	}
	return refs
}

func legacyConnectionRefs(raw []any) []Ref {
	refs := make([]Ref, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		hint, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := stringValue(hint["relationship_id"])
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		refs = append(refs, Ref{Type: "relationship", ID: id, Rank: len(refs) + 1})
	}
	return refs
}

func evidenceRefsFromInitialResponse(response map[string]any) []Ref {
	if response == nil {
		return nil
	}
	raw, ok := response["results"].([]any)
	if !ok {
		return evidenceRefsFromArray(response["evidences"])
	}
	refs := evidenceRefsFromArray(raw)
	if len(refs) == 0 {
		return evidenceRefsFromArray(response["evidences"])
	}
	return refs
}

func evidenceRefsFromArray(value any) []Ref {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	refs := make([]Ref, 0, len(raw))
	for _, item := range raw {
		evidence, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ref, ok := recallRefFromResult(evidence, len(refs)+1)
		if !ok || ref.Type == "relationship" {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

func hypothesisRefsFromInitialResponse(response map[string]any) []Ref {
	if response == nil {
		return nil
	}
	raw, ok := response["related_hypotheses"].([]any)
	if !ok {
		raw, ok = response["related_dreams"].([]any)
		if !ok {
			return nil
		}
	}
	refs := make([]Ref, 0, len(raw))
	for _, item := range raw {
		hypothesis, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := firstNonEmpty(stringValue(hypothesis["hypothesis_id"]), stringValue(hypothesis["dream_id"]), stringValue(hypothesis["id"]))
		if id == "" {
			continue
		}
		refs = append(refs, Ref{Type: "dream", ID: id, Rank: len(refs) + 1})
	}
	return refs
}

func refsToAny(refs []Ref) []any {
	out := make([]any, 0, len(refs))
	for _, ref := range refs {
		item := map[string]any{"type": ref.Type, "id": ref.ID, "rank": ref.Rank}
		if ref.SourceDocID != "" {
			item["source_doc_id"] = ref.SourceDocID
		}
		out = append(out, item)
	}
	return out
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
		refs = append(refs, Ref{
			Type:        stringValue(m["type"]),
			ID:          stringValue(m["id"]),
			SourceDocID: stringValue(m["source_doc_id"]),
			Rank:        intValue(m["rank"]),
		})
	}
	return refs
}

func endpoint(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

func nestedString(m map[string]any, objectKey, valueKey string) string {
	nested, _ := m[objectKey].(map[string]any)
	return stringValue(nested[valueKey])
}

func nestedPathString(value any, path ...string) string {
	for _, key := range path {
		nested, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = nested[key]
	}
	return stringValue(value)
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

func anySlice(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	default:
		return nil
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
