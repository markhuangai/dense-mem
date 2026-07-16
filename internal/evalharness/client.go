package evalharness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type HTTPClient struct {
	BaseURL          string
	APIKey           string
	ControlURL       string
	ControlToken     string
	PlacementTimeout time.Duration
	ToolTransport    string
	Client           *http.Client
}

type HTTPStatusError struct {
	Method     string
	URL        string
	StatusCode int
	Body       string
}

type DreamCycleSeed struct {
	Hypothesis string `json:"hypothesis"`
	SourceRefs []Ref  `json:"source_refs"`
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.URL, e.StatusCode, e.Body)
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
	if strings.EqualFold(strings.TrimSpace(c.ToolTransport), "mcp") {
		return c.callMCPTool(ctx, name, input, out)
	}
	return c.doJSON(ctx, http.MethodPost, endpoint(c.BaseURL, "/api/v1/tools/"+name), c.APIKey, input, out)
}

func (c *HTTPClient) callMCPTool(ctx context.Context, name string, input any, out any) error {
	arguments, ok := input.(map[string]any)
	if !ok {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(payload, &arguments); err != nil {
			return fmt.Errorf("mcp tool arguments must be an object: %w", err)
		}
	}
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      time.Now().UnixNano(),
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}
	var rpc mcpToolResponse
	if err := c.doJSON(ctx, http.MethodPost, endpoint(c.BaseURL, "/mcp"), c.APIKey, body, &rpc); err != nil {
		return err
	}
	if rpc.Error != nil {
		return fmt.Errorf("mcp tools/call %s returned %d: %s", name, rpc.Error.Code, rpc.Error.Message)
	}
	if out == nil {
		return nil
	}
	if len(rpc.Result.Content) == 0 {
		return fmt.Errorf("mcp tools/call %s returned no content", name)
	}
	text := strings.TrimSpace(rpc.Result.Content[0].Text)
	if text == "" {
		return fmt.Errorf("mcp tools/call %s returned empty text content", name)
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	return decoder.Decode(out)
}

type mcpToolResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	Result  mcpToolResult `json:"result"`
	Error   *mcpToolError `json:"error,omitempty"`
}

type mcpToolResult struct {
	Content []mcpToolContent `json:"content"`
}

type mcpToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *HTTPClient) ImportCorpus(ctx context.Context, corpus []CorpusItem) (KnowledgeMapping, error) {
	return c.ImportCorpusWithConcurrency(ctx, corpus, 1)
}

func (c *HTTPClient) ImportCorpusFileWithConcurrency(ctx context.Context, path string, concurrency int, keepSourceDocIDs, skipSourceDocIDs map[string]struct{}) (KnowledgeMapping, int, error) {
	groups, itemCount, err := loadCorpusImportGroups(path, skipSourceDocIDs)
	if err != nil {
		return newKnowledgeMapping(), 0, err
	}
	if concurrency <= 1 || len(groups) <= 1 {
		mapping, err := c.importCorpusGroupsSequential(ctx, groups, keepSourceDocIDs)
		return mapping, itemCount, err
	}
	return c.importCorpusGroupsConcurrent(ctx, groups, concurrency, keepSourceDocIDs, itemCount)
}

func loadCorpusImportGroups(path string, skipSourceDocIDs map[string]struct{}) ([][]CorpusItem, int, error) {
	items := []CorpusItem{}
	itemCount := 0
	err := scanCorpusFile(path, func(item CorpusItem) error {
		itemCount++
		if shouldSkipCorpusItem(item, skipSourceDocIDs) {
			return nil
		}
		items = append(items, item)
		return nil
	})
	if err != nil {
		return nil, itemCount, err
	}
	return corpusImportGroups(items), itemCount, nil
}

func (c *HTTPClient) importCorpusGroupsSequential(ctx context.Context, groups [][]CorpusItem, keepSourceDocIDs map[string]struct{}) (KnowledgeMapping, error) {
	mapping := newKnowledgeMapping()
	for _, group := range groups {
		itemMapping, err := c.importCorpusGroup(ctx, group)
		if err != nil {
			return mapping, err
		}
		mergeFilteredKnowledgeMapping(&mapping, itemMapping, keepSourceDocIDs)
	}
	return mapping, nil
}

func (c *HTTPClient) importCorpusGroupsConcurrent(ctx context.Context, groups [][]CorpusItem, concurrency int, keepSourceDocIDs map[string]struct{}, itemCount int) (KnowledgeMapping, int, error) {
	mapping := newKnowledgeMapping()
	if concurrency > len(groups) {
		concurrency = len(groups)
	}
	scheduleCtx, stopScheduling := context.WithCancel(ctx)
	defer stopScheduling()

	type importResult struct {
		mapping KnowledgeMapping
		err     error
	}

	jobs := make(chan []CorpusItem)
	results := make(chan importResult)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for group := range jobs {
				itemMapping, err := c.importCorpusGroup(ctx, group)
				select {
				case results <- importResult{mapping: itemMapping, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, group := range groups {
			select {
			case jobs <- group:
			case <-scheduleCtx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				stopScheduling()
			}
			continue
		}
		mergeFilteredKnowledgeMapping(&mapping, result.mapping, keepSourceDocIDs)
	}
	if firstErr != nil {
		return mapping, itemCount, firstErr
	}
	if err := ctx.Err(); err != nil {
		return mapping, itemCount, err
	}
	return mapping, itemCount, nil
}

func shouldSkipCorpusItem(item CorpusItem, skipSourceDocIDs map[string]struct{}) bool {
	if len(skipSourceDocIDs) == 0 {
		return false
	}
	_, ok := skipSourceDocIDs[strings.TrimSpace(item.SourceDocID)]
	return ok
}

func (c *HTTPClient) ImportCorpusWithConcurrency(ctx context.Context, corpus []CorpusItem, concurrency int) (KnowledgeMapping, error) {
	mapping := newKnowledgeMapping()
	groups := corpusImportGroups(corpus)
	if concurrency <= 1 || len(groups) <= 1 {
		for _, group := range groups {
			groupMapping, err := c.importCorpusGroup(ctx, group)
			if err != nil {
				return mapping, err
			}
			mergeKnowledgeMapping(&mapping, groupMapping)
		}
		return mapping, nil
	}
	if concurrency > len(groups) {
		concurrency = len(groups)
	}

	scheduleCtx, stopScheduling := context.WithCancel(ctx)
	defer stopScheduling()

	type importResult struct {
		mapping KnowledgeMapping
		err     error
	}

	jobs := make(chan []CorpusItem)
	results := make(chan importResult)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for group := range jobs {
				itemMapping, err := c.importCorpusGroup(ctx, group)
				select {
				case results <- importResult{mapping: itemMapping, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, group := range groups {
			select {
			case jobs <- group:
			case <-scheduleCtx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				stopScheduling()
			}
			continue
		}
		mergeKnowledgeMapping(&mapping, result.mapping)
	}
	if firstErr != nil {
		return mapping, firstErr
	}
	if err := ctx.Err(); err != nil {
		return mapping, err
	}
	return mapping, nil
}

func scanCorpusFile(path string, fn func(CorpusItem) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		item, err := decodeCorpusItem([]byte(line))
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		if strings.TrimSpace(item.SourceDocID) == "" {
			return fmt.Errorf("%s:%d: missing source_doc_id", path, lineNo)
		}
		if strings.TrimSpace(item.Content) == "" {
			return fmt.Errorf("%s:%d: missing content", path, lineNo)
		}
		if err := fn(item); err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func decodeCorpusItem(line []byte) (CorpusItem, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return CorpusItem{}, err
	}
	for _, field := range []string{"claims", "auto_promote"} {
		if _, ok := fields[field]; ok {
			return CorpusItem{}, fmt.Errorf("legacy corpus field %q is not supported; regenerate the seed for remember-only ingestion", field)
		}
	}
	var item CorpusItem
	if err := json.Unmarshal(line, &item); err != nil {
		return CorpusItem{}, err
	}
	return item, nil
}

func (c *HTTPClient) importCorpusGroup(ctx context.Context, corpus []CorpusItem) (KnowledgeMapping, error) {
	mapping := newKnowledgeMapping()
	for len(corpus) > 0 {
		batch := corpus
		if len(batch) > maxRememberEvidenceItems {
			batch = corpus[:maxRememberEvidenceItems]
		}
		itemMapping, err := c.importCorpusBatch(ctx, batch)
		if err != nil {
			return mapping, err
		}
		mergeKnowledgeMapping(&mapping, itemMapping)
		corpus = corpus[len(batch):]
	}
	return mapping, nil
}

func (c *HTTPClient) importCorpusItem(ctx context.Context, item CorpusItem) (KnowledgeMapping, error) {
	return c.importCorpusBatch(ctx, []CorpusItem{item})
}

const maxRememberEvidenceItems = 20

func (c *HTTPClient) importCorpusBatch(ctx context.Context, items []CorpusItem) (KnowledgeMapping, error) {
	mapping := newKnowledgeMapping()
	if len(items) == 0 {
		return mapping, nil
	}
	if len(items) > maxRememberEvidenceItems {
		return mapping, fmt.Errorf("remember batch has %d evidence items; max is %d", len(items), maxRememberEvidenceItems)
	}
	evidence := make([]map[string]any, 0, len(items))
	for _, item := range items {
		evidence = append(evidence, map[string]any{
			"content":         item.Content,
			"source":          firstNonEmpty(item.SourceDataset, item.Title, "eval-seed"),
			"idempotency_key": "eval:" + item.SourceDocID,
			"labels":          item.Labels,
			"metadata":        seedMetadata(item),
		})
	}
	input := map[string]any{"evidence": evidence}
	var out map[string]any
	if err := c.callToolWithRetry(ctx, "remember", input, &out); err != nil {
		return mapping, fmt.Errorf("import %s: %w", sourceDocIDRange(items), err)
	}
	var placement map[string]any
	if ingestID := stringValue(out["ingest_id"]); ingestID != "" {
		var err error
		placement, err = c.WaitForMemoryPlacementResult(ctx, ingestID, c.placementTimeout())
		if err != nil {
			return mapping, fmt.Errorf("import %s: %w", sourceDocIDRange(items), err)
		}
	}
	refsBySourceDocID := refsFromPlacementBatch(placement, items)
	if len(refsBySourceDocID) == 0 {
		refsBySourceDocID = refsFromRememberBatch(out, items)
	}
	if len(refsBySourceDocID) == 0 {
		return mapping, fmt.Errorf("import %s: remember placement produced no relationship or evidence id", sourceDocIDRange(items))
	}
	for _, item := range items {
		refs := refsBySourceDocID[item.SourceDocID]
		if len(refs) == 0 {
			return mapping, fmt.Errorf("import %s: remember placement produced no relationship or evidence id", item.SourceDocID)
		}
		for i, ref := range refs {
			addSourceMapping(&mapping, ref, i == 0)
		}
	}
	return mapping, nil
}

func corpusImportGroups(corpus []CorpusItem) [][]CorpusItem {
	groups := make([][]CorpusItem, 0, len(corpus))
	for _, item := range corpus {
		groups = append(groups, []CorpusItem{item})
	}
	return groups
}

func (c *HTTPClient) WaitForMemoryPlacement(ctx context.Context, ingestID string, timeout time.Duration) error {
	_, err := c.WaitForMemoryPlacementResult(ctx, ingestID, timeout)
	return err
}

func (c *HTTPClient) WaitForMemoryPlacementResult(ctx context.Context, ingestID string, timeout time.Duration) (map[string]any, error) {
	if strings.TrimSpace(ingestID) == "" {
		return nil, nil
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		var out map[string]any
		if err := c.callToolWithRetry(waitCtx, "get_memory_placement", map[string]any{"ingest_id": ingestID}, &out); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, fmt.Errorf("memory placement %s did not complete within %s", ingestID, timeout)
			}
			return nil, err
		}
		status := nestedString(out, "placement", "status")
		if status == "completed" || status == "failed" {
			if status == "failed" {
				if cause := nestedString(out, "placement", "error"); cause != "" {
					return nil, fmt.Errorf("memory placement %s failed: %s", ingestID, cause)
				}
				return nil, fmt.Errorf("memory placement %s failed", ingestID)
			}
			return out, nil
		}
		select {
		case <-waitCtx.Done():
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("memory placement %s did not complete within %s", ingestID, timeout)
			}
			return nil, waitCtx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (c *HTTPClient) placementTimeout() time.Duration {
	if c.PlacementTimeout > 0 {
		return c.PlacementTimeout
	}
	return 2 * time.Minute
}

func (c *HTTPClient) ExportFragmentMapping(ctx context.Context, limit int) (KnowledgeMapping, error) {
	return c.exportKnowledgeMapping(ctx, limit, []string{"fragment"})
}

func (c *HTTPClient) ExportKnowledgeMapping(ctx context.Context, limit int) (KnowledgeMapping, error) {
	return c.exportKnowledgeMapping(ctx, limit, []string{"fragment", "claim", "fact"})
}

func (c *HTTPClient) ExportDreamMapping(ctx context.Context, limit int) (KnowledgeMapping, error) {
	return c.exportKnowledgeMapping(ctx, limit, []string{"dream"})
}

func (c *HTTPClient) exportKnowledgeMapping(ctx context.Context, limit int, kinds []string) (KnowledgeMapping, error) {
	if limit <= 0 {
		limit = 100
	}
	mapping := newKnowledgeMapping()
	fragmentSourceDocIDs := map[string][]string{}
	claimSourceDocIDs := map[string][]string{}
	for _, kind := range kinds {
		cursor := ""
		for {
			input := map[string]any{"type": kind, "limit": limit, "metadata_only": false}
			if cursor != "" {
				input["cursor"] = cursor
			}
			var out struct {
				Items      []map[string]any `json:"items"`
				NextCursor string           `json:"next_cursor"`
				HasMore    bool             `json:"has_more"`
			}
			if err := c.CallTool(ctx, "eval_list_knowledge_refs", input, &out); err != nil {
				if isToolUnavailableError(err) {
					break
				}
				return mapping, err
			}
			for _, item := range out.Items {
				id := knowledgeItemID(kind, item)
				if id == "" {
					continue
				}
				if kind == "dream" {
					addDreamSourceRefs(&mapping, id, dreamSourceRefsFromKnowledgeItem(item))
				}
				sourceDocIDs := sourceDocIDsFromKnowledgeItem(kind, item, fragmentSourceDocIDs, claimSourceDocIDs)
				for _, sourceDocID := range sourceDocIDs {
					addSourceMapping(&mapping, Ref{Type: kind, ID: id, SourceDocID: sourceDocID}, kind == "fragment")
				}
				switch kind {
				case "fragment":
					fragmentSourceDocIDs[id] = sourceDocIDs
				case "claim":
					claimSourceDocIDs[id] = sourceDocIDs
				}
			}
			if !out.HasMore || out.NextCursor == "" {
				break
			}
			cursor = out.NextCursor
		}
	}
	return mapping, nil
}

func isToolUnavailableError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "tool not available")
}

func (c *HTTPClient) RunDreamCycle(ctx context.Context, expectedDreams int, seeds ...DreamCycleSeed) error {
	if expectedDreams <= 0 {
		return nil
	}
	input := map[string]any{
		"manual":             true,
		"reflect_enabled":    false,
		"reevaluate_enabled": false,
		"dream_enabled":      true,
		"max_outputs":        expectedDreams,
	}
	if len(seeds) > 0 {
		input["seed_dreams"] = seeds
	}
	var out map[string]any
	return c.CallTool(ctx, "eval_run_dream_cycle", input, &out)
}

func (c *HTTPClient) RunRecallCase(ctx context.Context, tc Case) (RecallTrace, error) {
	input := map[string]any{
		"query": tc.Query,
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
	var response map[string]any
	started := time.Now()
	if err := c.callToolWithRetry(ctx, "recall_memory", input, &response); err != nil {
		return RecallTrace{}, err
	}
	return traceFromRecallMemoryResponse(tc, response, time.Since(started).Milliseconds()), nil
}

func (c *HTTPClient) callToolWithRetry(ctx context.Context, name string, input any, out any) error {
	const maxAttempts = 6
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := c.CallTool(ctx, name, input, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientHTTPError(err) || attempt == maxAttempts {
			return err
		}
		delay := time.Duration(attempt) * 750 * time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return lastErr
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
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &HTTPStatusError{
			Method:     method,
			URL:        url,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(payload)),
		}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()
	return decoder.Decode(out)
}

func isTransientHTTPError(err error) bool {
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	switch statusErr.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}
