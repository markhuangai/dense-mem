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
	BaseURL      string
	APIKey       string
	ControlURL   string
	ControlToken string
	Client       *http.Client
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
	return c.doJSON(ctx, http.MethodPost, endpoint(c.BaseURL, "/api/v1/tools/"+name), c.APIKey, input, out)
}

func (c *HTTPClient) ImportCorpus(ctx context.Context, corpus []CorpusItem) (KnowledgeMapping, error) {
	return c.ImportCorpusWithConcurrency(ctx, corpus, 1)
}

func (c *HTTPClient) ImportCorpusFileWithConcurrency(ctx context.Context, path string, concurrency int, keepSourceDocIDs map[string]struct{}) (KnowledgeMapping, int, error) {
	if concurrency <= 1 {
		return c.importCorpusFileSequential(ctx, path, keepSourceDocIDs)
	}
	return c.importCorpusFileConcurrent(ctx, path, concurrency, keepSourceDocIDs)
}

func (c *HTTPClient) importCorpusFileSequential(ctx context.Context, path string, keepSourceDocIDs map[string]struct{}) (KnowledgeMapping, int, error) {
	mapping := newKnowledgeMapping()
	count := 0
	err := scanCorpusFile(path, func(item CorpusItem) error {
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

func (c *HTTPClient) importCorpusFileConcurrent(ctx context.Context, path string, concurrency int, keepSourceDocIDs map[string]struct{}) (KnowledgeMapping, int, error) {
	mapping := newKnowledgeMapping()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type importResult struct {
		mapping KnowledgeMapping
		err     error
	}

	jobs := make(chan CorpusItem)
	results := make(chan importResult)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				itemMapping, err := c.importCorpusItem(ctx, item)
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
			case jobs <- item:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if scanErr != nil {
			cancel()
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	count := 0
	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				cancel()
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

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

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
			case <-ctx.Done():
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
				cancel()
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
		if line == "" {
			continue
		}
		var item CorpusItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
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
	if len(item.Claims) > 0 {
		if err := c.importMemoryCorpusItem(ctx, item, &mapping); err != nil {
			return mapping, err
		}
		return mapping, nil
	}
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
	if fragmentID == "" {
		return mapping, fmt.Errorf("import %s: remember response missing fragment id", item.SourceDocID)
	}
	if ingestID := stringValue(out["ingest_id"]); ingestID != "" {
		if err := c.WaitForMemoryPlacement(ctx, ingestID, 2*time.Minute); err != nil {
			return mapping, fmt.Errorf("import %s: %w", item.SourceDocID, err)
		}
	}
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: fragmentID, SourceDocID: item.SourceDocID}, true)
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

func (c *HTTPClient) importMemoryCorpusItem(ctx context.Context, item CorpusItem, mapping *KnowledgeMapping) error {
	input := map[string]any{
		"summary":         item.Content,
		"source":          firstNonEmpty(item.SourceDataset, item.Title, "eval-seed"),
		"idempotency_key": "eval:" + item.SourceDocID,
		"labels":          item.Labels,
		"metadata":        seedMetadata(item),
		"claims":          seedTypedClaims(item),
	}
	if item.AutoPromote {
		input["auto_promote"] = true
	}
	var out map[string]any
	if err := c.callToolWithRetry(ctx, "import_memories", input, &out); err != nil {
		return fmt.Errorf("import %s: %w", item.SourceDocID, err)
	}
	fragmentID := fragmentIDFromRemember(out)
	if fragmentID == "" {
		return fmt.Errorf("import %s: import_memories response missing fragment id", item.SourceDocID)
	}
	addSourceMapping(mapping, Ref{Type: "fragment", ID: fragmentID, SourceDocID: item.SourceDocID}, true)
	claims, _ := out["claims"].([]any)
	if len(item.Claims) > 0 && len(claims) != len(item.Claims) {
		return fmt.Errorf("import %s: import_memories returned %d claim outcomes for %d typed claims", item.SourceDocID, len(claims), len(item.Claims))
	}
	for i, raw := range claims {
		claim, _ := raw.(map[string]any)
		if errText := stringValue(claim["error"]); errText != "" {
			return fmt.Errorf("import %s: claim import failed: %s", item.SourceDocID, errText)
		}
		claimID := stringValue(claim["claim_id"])
		if claimID != "" {
			addSourceMapping(mapping, Ref{Type: "claim", ID: claimID, SourceDocID: item.SourceDocID}, false)
			addSourceMapping(mapping, Ref{Type: "claim", ID: claimID, SourceDocID: typedClaimSourceDocID(item.SourceDocID, i+1)}, false)
		}
		fact, _ := claim["fact"].(map[string]any)
		factID := firstNonEmpty(stringValue(fact["fact_id"]), stringValue(fact["id"]))
		if factID != "" {
			addSourceMapping(mapping, Ref{Type: "fact", ID: factID, SourceDocID: item.SourceDocID}, false)
			addSourceMapping(mapping, Ref{Type: "fact", ID: factID, SourceDocID: typedFactSourceDocID(item.SourceDocID, i+1)}, false)
		}
	}
	return nil
}

func (c *HTTPClient) WaitForMemoryPlacement(ctx context.Context, ingestID string, timeout time.Duration) error {
	if strings.TrimSpace(ingestID) == "" {
		return nil
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
				return fmt.Errorf("memory placement %s did not complete within %s", ingestID, timeout)
			}
			return err
		}
		status := nestedString(out, "placement", "status")
		if status == "completed" || status == "failed" {
			if status == "failed" {
				return fmt.Errorf("memory placement %s failed", ingestID)
			}
			return nil
		}
		select {
		case <-waitCtx.Done():
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("memory placement %s did not complete within %s", ingestID, timeout)
			}
			return waitCtx.Err()
		case <-time.After(time.Second):
		}
	}
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
	claimSourceDocIDs := map[string]string{}
	claimFactSourceDocIDs := map[string]string{}
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
				sourceDocIDs := sourceDocIDsFromKnowledgeItem(kind, item, claimSourceDocIDs, claimFactSourceDocIDs)
				for _, sourceDocID := range sourceDocIDs {
					addSourceMapping(&mapping, Ref{Type: kind, ID: id, SourceDocID: sourceDocID}, kind == "fragment")
				}
				if kind == "claim" {
					if len(sourceDocIDs) > 0 {
						claimSourceDocIDs[id] = sourceDocIDs[0]
					}
					if factSourceDocID := factSourceDocIDForClaimSourceDocIDs(sourceDocIDs); factSourceDocID != "" {
						claimFactSourceDocIDs[id] = factSourceDocID
					}
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

func seedTypedClaims(item CorpusItem) []map[string]any {
	claims := make([]map[string]any, 0, len(item.Claims))
	metadata := seedMetadata(item)
	for i, claim := range item.Claims {
		classification := map[string]any{}
		for key, value := range claim.Classification {
			classification[key] = value
		}
		for key, value := range metadata {
			classification[key] = value
		}
		classification["eval_claim_source_doc_id"] = typedClaimSourceDocID(item.SourceDocID, i+1)
		classification["eval_fact_source_doc_id"] = typedFactSourceDocID(item.SourceDocID, i+1)
		claimInput := map[string]any{
			"subject":         claim.Subject,
			"predicate":       claim.Predicate,
			"object":          claim.Object,
			"extract_conf":    claim.ExtractConf,
			"resolution_conf": claim.ResolutionConf,
			"classification":  classification,
		}
		if claim.Modality != "" {
			claimInput["modality"] = claim.Modality
		}
		if claim.Polarity != "" {
			claimInput["polarity"] = claim.Polarity
		}
		if claim.Speaker != "" {
			claimInput["speaker"] = claim.Speaker
		}
		if claim.IdempotencyKey != "" {
			claimInput["idempotency_key"] = claim.IdempotencyKey
		} else {
			claimInput["idempotency_key"] = fmt.Sprintf("eval:%s:claim:%d", item.SourceDocID, i+1)
		}
		if claim.ValidFrom != nil {
			claimInput["valid_from"] = claim.ValidFrom.UTC().Format(time.RFC3339Nano)
		}
		if claim.ValidTo != nil {
			claimInput["valid_to"] = claim.ValidTo.UTC().Format(time.RFC3339Nano)
		}
		if len(claim.SupportedBy) > 0 {
			claimInput["supported_by"] = claim.SupportedBy
		}
		if claim.ExtractionModel != "" {
			claimInput["extraction_model"] = claim.ExtractionModel
		}
		if claim.ExtractionVersion != "" {
			claimInput["extraction_version"] = claim.ExtractionVersion
		}
		if claim.PipelineRunID != "" {
			claimInput["pipeline_run_id"] = claim.PipelineRunID
		} else {
			claimInput["pipeline_run_id"] = "eval:" + item.SourceDocID
		}
		claims = append(claims, claimInput)
	}
	return claims
}

func sourceDocIDsFromKnowledgeItem(kind string, item map[string]any, claimSourceDocIDs, claimFactSourceDocIDs map[string]string) []string {
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
		sourceDocIDs = append(sourceDocIDs,
			nestedString(item, "classification", "eval_claim_source_doc_id"),
			typedClaimSourceDocIDFromIdempotencyKey(stringValue(item["idempotency_key"])),
		)
	case "fact":
		if sourceDocID == "" {
			sourceDocIDs = append(sourceDocIDs, claimSourceDocIDs[stringValue(item["promoted_from_claim_id"])])
		}
		sourceDocIDs = append(sourceDocIDs,
			nestedString(item, "classification", "eval_fact_source_doc_id"),
			claimFactSourceDocIDs[stringValue(item["promoted_from_claim_id"])],
		)
	}
	return uniqueNonEmpty(sourceDocIDs)
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

func factSourceDocIDForClaimSourceDocIDs(sourceDocIDs []string) string {
	for _, sourceDocID := range sourceDocIDs {
		if factSourceDocID := typedFactSourceDocIDFromClaimSourceDocID(sourceDocID); factSourceDocID != "" {
			return factSourceDocID
		}
	}
	return ""
}

func typedClaimSourceDocID(sourceDocID string, index int) string {
	return fmt.Sprintf("%s:claim:%d", sourceDocID, index)
}

func typedFactSourceDocID(sourceDocID string, index int) string {
	return fmt.Sprintf("%s:fact:%d", sourceDocID, index)
}

func typedFactSourceDocIDFromClaimSourceDocID(sourceDocID string) string {
	const marker = ":claim:"
	index := strings.LastIndex(sourceDocID, marker)
	if index == -1 {
		return ""
	}
	return sourceDocID[:index] + ":fact:" + sourceDocID[index+len(marker):]
}

func typedClaimSourceDocIDFromIdempotencyKey(key string) string {
	const prefix = "eval:"
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(key, prefix)
	const marker = ":claim:"
	index := strings.LastIndex(rest, marker)
	if index <= 0 || index+len(marker) >= len(rest) {
		return ""
	}
	ordinal := rest[index+len(marker):]
	for _, r := range ordinal {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return rest[:index] + marker + ordinal
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
