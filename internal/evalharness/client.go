package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type HTTPClient struct {
	BaseURL         string
	APIKey          string
	Client          *http.Client
	Capture         *Capture
	ExchangeCapture *Capture
	contractMu      sync.Mutex
	contractMode    contractMode
}

type HTTPStatusError struct{ StatusCode int }

var errMalformedHTTPJSON = errors.New("invalid HTTP JSON response")

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP request failed with status %d", e.StatusCode)
}

type StructuredToolError struct {
	Tool   string
	Result map[string]any
}

func (e *StructuredToolError) Error() string {
	return "MCP " + e.Tool + ": " + submissionErrorMessage(e.Result)
}

type mcpToolError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func endpoint(base, path string) string { return strings.TrimRight(base, "/") + path }

func (c *HTTPClient) doJSON(ctx context.Context, method, url, bearer string, input, out any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	exchange := uuid.NewString()
	scope := observability.ExchangeScopeFromContext(ctx)
	if c.ExchangeCapture != nil {
		if err := c.ExchangeCapture.Emit("answer_wire_request", map[string]any{"exchange_id": exchange, "operation_id": scope.OperationID, "attempt": scope.Attempt, "body": json.RawMessage(payload)}); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid request configuration")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	if rpc, ok := input.(map[string]any); ok && rpc["jsonrpc"] == "2.0" {
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", stringValue(rpc["method"]))
		if params, ok := rpc["params"].(map[string]any); ok {
			req.Header.Set("Mcp-Name", stringValue(params["name"]))
		}
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	response, err := client.Do(req)
	if err != nil {
		if c.ExchangeCapture != nil {
			if captureErr := c.ExchangeCapture.Emit("answer_wire_unavailable", map[string]any{"exchange_id": exchange, "operation_id": scope.OperationID, "attempt": scope.Attempt, "state": "no_response", "error": c.ExchangeCapture.ProtectError(err)}); captureErr != nil {
				return captureErr
			}
		}
		return errors.New("HTTP transport failed or cancelled: " + safeContextError(ctx))
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if c.ExchangeCapture != nil {
		state := "captured"
		if err != nil {
			state = "interrupted"
		}
		if captureErr := c.ExchangeCapture.Emit("answer_wire_response", map[string]any{"exchange_id": exchange, "operation_id": scope.OperationID, "attempt": scope.Attempt, "state": state, "status_code": response.StatusCode, "body": string(data)}); captureErr != nil {
			return captureErr
		}
	}
	if err != nil {
		return errors.New("HTTP response body interrupted")
	}
	if len(data) > 16<<20 {
		return errors.New("HTTP response exceeds capture bound")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPStatusError{StatusCode: response.StatusCode}
	}
	if out == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return errMalformedHTTPJSON
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errMalformedHTTPJSON
	}
	return nil
}

func safeContextError(ctx context.Context) string {
	if err := ctx.Err(); err != nil {
		return err.Error()
	}
	return "dependency unavailable"
}

func (c *HTTPClient) CallTool(ctx context.Context, name string, input, out any) error {
	if c.BaseURL == "" || c.APIKey == "" {
		return errors.New("MCP configuration missing")
	}
	var rpc struct {
		Error  *mcpToolError `json:"error"`
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
			Content           []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	body := map[string]any{"jsonrpc": "2.0", "id": uuid.NewString(), "method": "tools/call", "params": map[string]any{"name": name, "arguments": input, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}}
	err := c.doJSON(ctx, http.MethodPost, endpoint(c.BaseURL, "/mcp"), c.APIKey, body, &rpc)
	if err != nil {
		return err
	}
	if rpc.Error != nil {
		return fmt.Errorf("MCP protocol error %d", rpc.Error.Code)
	}
	result := rpc.Result.StructuredContent
	if result == nil {
		for _, item := range rpc.Result.Content {
			if item.Type == "text" {
				decoder := json.NewDecoder(strings.NewReader(item.Text))
				decoder.UseNumber()
				if err = decoder.Decode(&result); err != nil {
					return errors.New("invalid MCP content")
				}
				break
			}
		}
	}
	if result == nil {
		return errors.New("MCP response has no structured result")
	}
	if out != nil {
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if target, ok := out.(*map[string]any); ok {
			*target = nil
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		if err = decoder.Decode(out); err != nil {
			return err
		}
	}
	if rpc.Result.IsError {
		return &StructuredToolError{Tool: name, Result: result}
	}
	return nil
}

func (c *HTTPClient) ImportCorpus(ctx context.Context, items []CorpusItem) (KnowledgeMapping, error) {
	return c.ImportCorpusWithConcurrency(ctx, items, 1)
}

func (c *HTTPClient) ImportCorpusWithConcurrency(ctx context.Context, items []CorpusItem, concurrency int) (KnowledgeMapping, error) {
	mapping := newKnowledgeMapping()
	if concurrency < 1 || concurrency > 10 {
		return mapping, errors.New("invalid import concurrency")
	}
	if _, err := c.ensureContract(ctx); err != nil {
		return mapping, err
	}
	work, stop := context.WithCancel(ctx)
	defer stop()
	jobs := make(chan CorpusItem)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				itemMapping, err := c.importCorpusItem(work, item)
				mu.Lock()
				if err != nil {
					if first == nil {
						first = err
					}
					stop()
				} else {
					mergeKnowledgeMapping(&mapping, itemMapping)
				}
				mu.Unlock()
			}
		}()
	}
schedule:
	for _, item := range items {
		select {
		case jobs <- item:
		case <-work.Done():
			break schedule
		}
	}
	close(jobs)
	wg.Wait()
	if first != nil {
		return mapping, first
	}
	if err := ctx.Err(); err != nil {
		return mapping, err
	}
	return mapping, nil
}

func corpusRememberInput(item CorpusItem) map[string]any {
	metadata := map[string]any{}
	for k, v := range item.Metadata {
		metadata[k] = v
	}
	metadata["source_doc_id"], metadata["source_dataset"], metadata["eval_seed"] = item.SourceDocID, item.SourceDataset, true
	evidence := map[string]any{"content": item.Content, "source": firstNonEmpty(item.SourceDataset, "public-rag"), "source_key": item.SourceDocID, "source_revision": "1", "source_type": "document", "metadata": metadata}
	if len(item.Labels) > 0 {
		evidence["labels"] = item.Labels
	}
	return map[string]any{"target_space": "team_shared", "idempotency_key": "eval:" + item.SourceDocID, "evidence": []map[string]any{evidence}}
}

func (c *HTTPClient) importCorpusItem(ctx context.Context, item CorpusItem) (KnowledgeMapping, error) {
	mapping := newKnowledgeMapping()
	input := corpusRememberInput(item)
	for attempt := 1; attempt <= 2; attempt++ {
		started := time.Now()
		var result map[string]any
		err := c.CallTool(ctx, "remember", input, &result)
		if err == nil && stringValue(result["submission_id"]) == "" {
			err = errors.New("remember response missing submission identity")
		}
		if err == nil {
			mapping, err = c.importTerminalRememberResult(item, result, mapping)
		}
		event := map[string]any{"source_id": item.SourceDocID, "attempt": attempt, "idempotency_key": input["idempotency_key"], "result": result, "elapsed_ms": float64(time.Since(started)) / float64(time.Millisecond)}
		if err == nil {
			event["admitted_input"] = input
		} else {
			event["error"] = err.Error()
		}
		if captureErr := c.Capture.Emit("import_attempt", event); captureErr != nil {
			return mapping, captureErr
		}
		if err == nil {
			return mapping, nil
		}
		if attempt == 2 || ctx.Err() != nil {
			return mapping, err
		}
		retry, original, retryable, terminal := terminalRememberRetryInput("remember", input, err)
		if terminal {
			input = retry
			if err := c.Capture.Emit("intake_retry", map[string]any{"source_id": item.SourceDocID, "original_attempt_id": original, "original_retryable": retryable, "idempotency_key": input["idempotency_key"]}); err != nil {
				return mapping, err
			}
		} else if !isTransientCallToolError(err) {
			return mapping, err
		}
		select {
		case <-ctx.Done():
			return mapping, ctx.Err()
		case <-time.After(750 * time.Millisecond):
		}
	}
	return mapping, errors.New("intake attempts exhausted")
}

func isTransientHTTPError(err error) bool {
	var status *HTTPStatusError
	if !errors.As(err, &status) {
		return false
	}
	return status.StatusCode == 429 || status.StatusCode >= 500
}

func terminalRememberRetryInput(name string, input any, cause error) (map[string]any, string, bool, bool) {
	var failure *StructuredToolError
	if name != "remember" || !errors.As(cause, &failure) || failure.Tool != "remember" || submissionProcessingState(failure.Result) != "failed" {
		return nil, "", false, false
	}
	id := stringValue(failure.Result["submission_id"])
	if _, err := uuid.Parse(id); err != nil {
		return nil, "", false, false
	}
	original, ok := input.(map[string]any)
	if !ok {
		return nil, "", false, false
	}
	key := stringValue(original["idempotency_key"])
	if !strings.HasPrefix(key, "eval:") || strings.HasSuffix(key, ":resubmit1") {
		return nil, "", false, false
	}
	retryable := structuredToolErrorRetryable(failure.Result)
	if !retryable {
		key += ":resubmit1"
	}
	if len(key) > 128 {
		return nil, "", false, false
	}
	retry := make(map[string]any, len(original))
	for name, value := range original {
		retry[name] = value
	}
	retry["idempotency_key"] = key
	return retry, id, retryable, true
}
