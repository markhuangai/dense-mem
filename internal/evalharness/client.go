package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPClient struct {
	BaseURL      string
	APIKey       string
	ControlURL   string
	ControlToken string
	Client       *http.Client
}

func (c *HTTPClient) EnableEvaluationMode(ctx context.Context, maxPageSize int) error {
	if strings.TrimSpace(c.ControlURL) == "" || strings.TrimSpace(c.ControlToken) == "" {
		return nil
	}
	if maxPageSize <= 0 {
		maxPageSize = 100
	}
	body := map[string]any{"items": []map[string]string{
		{"key": "EVALUATION_MODE_ENABLED", "value": "true"},
		{"key": "EVALUATION_EXPORT_MAX_PAGE_SIZE", "value": fmt.Sprintf("%d", maxPageSize)},
	}}
	return c.doJSON(ctx, http.MethodPatch, endpoint(c.ControlURL, "/control/api/config/evaluation"), c.ControlToken, body, nil)
}

func (c *HTTPClient) CallTool(ctx context.Context, name string, input any, out any) error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("base URL is required")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return fmt.Errorf("API key is required")
	}
	return c.doJSON(ctx, http.MethodPost, endpoint(c.BaseURL, "/api/v1/tools/"+name), c.APIKey, input, out)
}

func (c *HTTPClient) ImportCorpus(ctx context.Context, corpus []CorpusItem) (KnowledgeMapping, error) {
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{}}
	for _, item := range corpus {
		input := map[string]any{
			"content":         item.Content,
			"source":          firstNonEmpty(item.SourceDataset, item.Title, "eval-seed"),
			"idempotency_key": "eval:" + item.SourceDocID,
			"labels":          item.Labels,
			"metadata":        seedMetadata(item),
			"auto_promote":    false,
		}
		if len(item.Claims) > 0 {
			input["claims"] = item.Claims
		}
		var out map[string]any
		if err := c.CallTool(ctx, "remember", input, &out); err != nil {
			return mapping, fmt.Errorf("import %s: %w", item.SourceDocID, err)
		}
		fragmentID := fragmentIDFromRemember(out)
		if fragmentID == "" {
			return mapping, fmt.Errorf("import %s: remember response missing fragment id", item.SourceDocID)
		}
		mapping.BySourceDocID[item.SourceDocID] = Ref{Type: "fragment", ID: fragmentID, SourceDocID: item.SourceDocID}
	}
	return mapping, nil
}

func (c *HTTPClient) ExportFragmentMapping(ctx context.Context, limit int) (KnowledgeMapping, error) {
	if limit <= 0 {
		limit = 100
	}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{}}
	cursor := ""
	for {
		input := map[string]any{"type": "fragment", "limit": limit, "metadata_only": false}
		if cursor != "" {
			input["cursor"] = cursor
		}
		var out struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"next_cursor"`
			HasMore    bool             `json:"has_more"`
		}
		if err := c.CallTool(ctx, "eval_list_knowledge_refs", input, &out); err != nil {
			return mapping, err
		}
		for _, item := range out.Items {
			sourceDocID := nestedString(item, "metadata", "source_doc_id")
			id := stringValue(item["id"])
			if sourceDocID != "" && id != "" {
				mapping.BySourceDocID[sourceDocID] = Ref{Type: "fragment", ID: id, SourceDocID: sourceDocID}
			}
		}
		if !out.HasMore || out.NextCursor == "" {
			break
		}
		cursor = out.NextCursor
	}
	return mapping, nil
}

func (c *HTTPClient) RunRecallCase(ctx context.Context, tc Case) (RecallTrace, error) {
	input := map[string]any{
		"case_id": tc.CaseID,
		"query":   tc.Query,
	}
	if tc.Limit > 0 {
		input["limit"] = tc.Limit
	}
	if tc.ValidAt != "" {
		input["valid_at"] = tc.ValidAt
	}
	if tc.KnownAt != "" {
		input["known_at"] = tc.KnownAt
	}
	var out map[string]any
	if err := c.CallTool(ctx, "eval_run_recall_case", input, &out); err != nil {
		return RecallTrace{}, err
	}
	return traceFromToolOutput(tc, out), nil
}

func (c *HTTPClient) doJSON(ctx context.Context, method, url, bearer string, input any, out any) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s returned %d: %s", method, url, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()
	return decoder.Decode(out)
}

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

func fragmentIDFromRemember(out map[string]any) string {
	fragment, _ := out["fragment"].(map[string]any)
	return firstNonEmpty(stringValue(fragment["id"]), stringValue(fragment["fragment_id"]))
}

func traceFromToolOutput(tc Case, out map[string]any) RecallTrace {
	return RecallTrace{
		CaseID:            tc.CaseID,
		Query:             firstNonEmpty(stringValue(out["query"]), tc.Query),
		RankedRefs:        refsFromAny(out["ranked_refs"]),
		ContextRefs:       refsFromAny(out["context_refs"]),
		LatencyMS:         int64Value(out["latency_ms"]),
		ContextBlockChars: intValue(out["context_block_chars"]),
		Raw:               out,
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
		refs = append(refs, Ref{
			Type: stringValue(m["type"]),
			ID:   stringValue(m["id"]),
			Rank: intValue(m["rank"]),
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
