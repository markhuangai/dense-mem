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
	switch strings.ToLower(strings.TrimSpace(c.ToolTransport)) {
	case "", "rest":
		return c.doJSON(ctx, http.MethodPost, endpoint(c.BaseURL, "/api/v1/tools/"+name), c.APIKey, input, out)
	case "mcp":
		return c.callMCPTool(ctx, name, input, out)
	default:
		return fmt.Errorf("unsupported tool transport %q", c.ToolTransport)
	}
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
	for _, content := range rpc.Result.Content {
		if content.Type != "text" || strings.TrimSpace(content.Text) == "" {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(content.Text))
		decoder.UseNumber()
		if err := decoder.Decode(out); err != nil {
			return fmt.Errorf("decode mcp tools/call %s result: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("mcp tools/call %s returned no JSON text content", name)
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
	if concurrency <= 1 {
		return c.importCorpusFileSequential(ctx, path, keepSourceDocIDs, skipSourceDocIDs)
	}
	return c.importCorpusFileConcurrent(ctx, path, concurrency, keepSourceDocIDs, skipSourceDocIDs)
}

func (c *HTTPClient) importCorpusFileSequential(ctx context.Context, path string, keepSourceDocIDs, skipSourceDocIDs map[string]struct{}) (KnowledgeMapping, int, error) {
	mapping := newKnowledgeMapping()
	count := 0
	err := scanCorpusFile(path, func(item CorpusItem) error {
		if shouldSkipCorpusItem(item, skipSourceDocIDs) {
			count++
			return nil
		}
		itemMapping, err := c.importCorpusItem(ctx, item)
		if err != nil {
			return err
		}
		mergeFilteredKnowledgeMapping(&mapping, itemMapping, keepSourceDocIDs)
		count++
		return nil
	})
	return mapping, count, err
}

func (c *HTTPClient) importCorpusFileConcurrent(ctx context.Context, path string, concurrency int, keepSourceDocIDs, skipSourceDocIDs map[string]struct{}) (KnowledgeMapping, int, error) {
	mapping := newKnowledgeMapping()
	scheduleCtx, stopScheduling := context.WithCancel(ctx)
	defer stopScheduling()

	type importResult struct {
		mapping KnowledgeMapping
		skipped bool
		err     error
	}
	type importJob struct {
		item    CorpusItem
		skipped bool
	}

	jobs := make(chan importJob)
	results := make(chan importResult)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if job.skipped {
					select {
					case results <- importResult{skipped: true}:
					case <-ctx.Done():
						return
					}
					continue
				}
				itemMapping, err := c.importCorpusItem(ctx, job.item)
				select {
				case results <- importResult{mapping: itemMapping, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	var scanErr error
	go func() {
		defer close(jobs)
		scanErr = scanCorpusFile(path, func(item CorpusItem) error {
			select {
			case jobs <- importJob{item: item, skipped: shouldSkipCorpusItem(item, skipSourceDocIDs)}:
				return nil
			case <-scheduleCtx.Done():
				return scheduleCtx.Err()
			}
		})
		if scanErr != nil {
			stopScheduling()
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	count := 0
	var firstErr error
	for result := range results {
		if result.skipped {
			count++
			continue
		}
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				stopScheduling()
			}
			continue
		}
		mergeFilteredKnowledgeMapping(&mapping, result.mapping, keepSourceDocIDs)
		count++
	}
	if firstErr != nil {
		return mapping, count, firstErr
	}
	if scanErr != nil {
		return mapping, count, scanErr
	}
	if err := ctx.Err(); err != nil {
		return mapping, count, err
	}
	return mapping, count, nil
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
		groupMapping, err := c.importCorpusGroup(ctx, corpus)
		if err != nil {
			return mapping, err
		}
		mergeKnowledgeMapping(&mapping, groupMapping)
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
	for _, item := range corpus {
		itemMapping, err := c.importCorpusItem(ctx, item)
		if err != nil {
			return mapping, err
		}
		mergeKnowledgeMapping(&mapping, itemMapping)
	}
	return mapping, nil
}
func (c *HTTPClient) importCorpusItem(ctx context.Context, item CorpusItem) (KnowledgeMapping, error) {
	mapping := newKnowledgeMapping()
	evidence := map[string]any{
		"content":         item.Content,
		"source":          firstNonEmpty(item.SourceDataset, item.Title, "eval-seed"),
		"idempotency_key": "eval:" + item.SourceDocID,
		"labels":          item.Labels,
		"metadata":        seedMetadata(item),
	}
	input := map[string]any{"evidence": []map[string]any{evidence}}
	var out map[string]any
	if err := c.callToolWithRetry(ctx, "remember", input, &out); err != nil {
		return mapping, fmt.Errorf("import %s: %w", item.SourceDocID, err)
	}
	fragmentID := fragmentIDFromRemember(out)
	evidenceID := evidenceIDFromRemember(out)
	if fragmentID == "" && evidenceID != "" && !isV2RememberOutput(out) {
		fragmentID = evidenceID
	}
	if ingestID := stringValue(out["ingest_id"]); ingestID != "" {
		placement, err := c.WaitForMemoryPlacementResult(ctx, ingestID, c.placementTimeout())
		if err != nil {
			return mapping, fmt.Errorf("import %s: %w", item.SourceDocID, err)
		}
		if evidenceID == "" {
			evidenceID = evidenceIDFromPlacement(placement)
		}
	}
	if fragmentID == "" && evidenceID == "" {
		return mapping, fmt.Errorf("import %s: remember response missing fragment or evidence id", item.SourceDocID)
	}
	if fragmentID != "" {
		addSourceMapping(&mapping, Ref{Type: "fragment", ID: fragmentID, SourceDocID: item.SourceDocID}, true)
	}
	if evidenceID != "" {
		addSourceMapping(&mapping, Ref{Type: "evidence", ID: evidenceID, SourceDocID: item.SourceDocID}, fragmentID == "")
	}
	return mapping, nil
}

func corpusImportGroups(corpus []CorpusItem) [][]CorpusItem {
	groups := make([][]CorpusItem, 0, len(corpus))
	groupIndex := map[string]int{}
	for _, item := range corpus {
		key := corpusImportGroupKey(item)
		index, ok := groupIndex[key]
		if !ok {
			index = len(groups)
			groupIndex[key] = index
			groups = append(groups, nil)
		}
		groups[index] = append(groups[index], item)
	}
	return groups
}

func corpusImportGroupKey(item CorpusItem) string {
	if item.Metadata != nil {
		if caseID, ok := item.Metadata["case_id"].(string); ok && strings.TrimSpace(caseID) != "" {
			return "case:" + strings.TrimSpace(caseID)
		}
	}
	sourceDocID := strings.TrimSpace(item.SourceDocID)
	if sourceDocID == "" {
		return "item:" + item.Content
	}
	return "item:" + sourceDocID
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
		status := placementProcessingState(out)
		switch status {
		case "completed":
			searchState := placementSearchState(out)
			switch searchState {
			case "", "current", "not_required":
				return out, nil
			case "pending":
			case "failed":
				if cause := placementErrorMessage(out); cause != "" {
					return nil, fmt.Errorf("memory placement %s search_state failed: %s", ingestID, cause)
				}
				return nil, fmt.Errorf("memory placement %s search_state failed", ingestID)
			default:
				return nil, fmt.Errorf("memory placement %s returned unknown search_state %q", ingestID, searchState)
			}
		case "failed", "guarded", "quarantined", "awaiting_review":
			if cause := placementErrorMessage(out); cause != "" {
				return nil, fmt.Errorf("memory placement %s %s: %s", ingestID, status, cause)
			}
			return nil, fmt.Errorf("memory placement %s %s", ingestID, status)
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

func (c *HTTPClient) ExportV2KnowledgeMapping(ctx context.Context, limit int) (KnowledgeMapping, error) {
	return c.exportKnowledgeMapping(ctx, limit, []string{"evidence", "entity", "value", "relationship", "hypothesis"})
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
				case "evidence":
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
	if tc.IncludeDreams {
		input["include_dreams"] = true
	}
	if tc.UseCommunities {
		input["use_communities"] = true
	}
	var out map[string]any
	if err := c.callToolWithRetry(ctx, "eval_run_recall_case", input, &out); err != nil {
		return RecallTrace{}, err
	}
	return traceFromToolOutput(tc, out), nil
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
		nestedString(item, "payload", "source_doc_id"),
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
	case "hypothesis":
		for _, ref := range sourceRefsFromAny(item["source_refs"]) {
			sourceDocIDs = append(sourceDocIDs, fragmentSourceDocIDs[ref.ID]...)
		}
		if payloadRefs, ok := nestedValue(item, "payload", "source_refs"); ok {
			for _, ref := range sourceRefsFromAny(payloadRefs) {
				sourceDocIDs = append(sourceDocIDs, fragmentSourceDocIDs[ref.ID]...)
			}
		}
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
	return sourceRefsFromAny(item["source_refs"])
}

func sourceRefsFromAny(value any) []Ref {
	raw, ok := value.([]any)
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
	return firstNonEmpty(stringValue(fragment["id"]), stringValue(fragment["fragment_id"]))
}

func evidenceIDFromRemember(out map[string]any) string {
	if id := stringValue(out["evidence_id"]); id != "" {
		return id
	}
	evidence, _ := out["evidence"].([]any)
	if len(evidence) == 0 {
		return ""
	}
	first, _ := evidence[0].(map[string]any)
	return firstNonEmpty(stringValue(first["evidence_id"]), stringValue(first["id"]), stringValue(first["fragment_id"]))
}

func isV2RememberOutput(out map[string]any) bool {
	return stringValue(out["processing_state"]) != "" ||
		stringValue(out["status_tool"]) != "" ||
		stringValue(out["correlation_id"]) != ""
}

func evidenceIDFromPlacement(out map[string]any) string {
	items, _ := out["items"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if id := firstNonEmpty(stringValue(item["evidence_id"]), stringValue(item["id"]), stringValue(item["fragment_id"])); id != "" {
			return id
		}
	}
	return evidenceIDFromRemember(out)
}

func traceFromToolOutput(tc Case, out map[string]any) RecallTrace {
	return RecallTrace{
		CaseID:              tc.CaseID,
		Query:               firstNonEmpty(stringValue(out["query"]), tc.Query),
		RankedRefs:          refsFromAny(out["ranked_refs"]),
		ContextRefs:         refsFromAny(out["context_refs"]),
		ContextEvidenceRefs: refsFromAny(out["context_evidence_refs"]),
		DreamRefs:           refsFromAny(out["dream_refs"]),
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
	nested, _ := nestedValue(m, objectKey, valueKey)
	return stringValue(nested)
}

func nestedValue(m map[string]any, objectKey, valueKey string) (any, bool) {
	nested, _ := m[objectKey].(map[string]any)
	if nested == nil {
		return nil, false
	}
	value, ok := nested[valueKey]
	return value, ok
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
