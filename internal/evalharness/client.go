package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		} else if captureErr := c.logServiceTransportFailure(req, exchange, "request", 0, err); captureErr != nil {
			return captureErr
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
		if c.ExchangeCapture == nil {
			if captureErr := c.logServiceTransportFailure(req, exchange, "response_body", response.StatusCode, err); captureErr != nil {
				return captureErr
			}
		}
		if isGatewayTimeoutStatus(response.StatusCode) {
			return fmt.Errorf("HTTP response body interrupted: %w", &HTTPStatusError{StatusCode: response.StatusCode})
		}
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

func (c *HTTPClient) logServiceTransportFailure(req *http.Request, exchange, stage string, status int, cause error) error {
	if c.Capture == nil {
		return nil
	}
	var requestErr *url.Error
	if errors.As(cause, &requestErr) {
		cause = requestErr.Err
	}
	return c.Capture.Emit("runtime_transport_failure", map[string]any{
		"link": "runner_to_dense_mem", "exchange_id": exchange, "stage": stage, "status_code": status,
		"mcp_method": req.Header.Get("Mcp-Method"), "mcp_tool": req.Header.Get("Mcp-Name"), "error": c.Capture.ProtectError(cause),
	})
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
	return c.importCorpus(ctx, items, concurrency, nil)
}

const maxGatewayRetries = 5

type corpusImportJob struct {
	item    CorpusItem
	input   map[string]any
	attempt int
}

func (c *HTTPClient) importCorpus(ctx context.Context, items []CorpusItem, concurrency int, native *Native) (KnowledgeMapping, error) {
	mapping := newKnowledgeMapping()
	if err := validateImportWorkers(concurrency); err != nil {
		return mapping, err
	}
	initialProgress := captureProgress{Stage: "import", Unit: "documents", Total: len(items)}
	if err := c.Capture.emitProgress(initialProgress); err != nil {
		return mapping, err
	}
	work, stop := context.WithCancel(ctx)
	defer stop()
	jobs := make([]corpusImportJob, len(items))
	for index, item := range items {
		jobs[index] = corpusImportJob{item: item, attempt: 1}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	failed := 0
	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				mu.Lock()
				err := c.Capture.emitProgress(captureProgress{Stage: "import", Unit: "documents", Total: len(items), Completed: len(mapping.BySourceDocID), Failed: failed})
				if err != nil {
					if first == nil {
						first = err
					}
					stop()
				}
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()
	finishProgress := func() error {
		close(stopProgress)
		<-progressDone
		return c.Capture.emitProgress(captureProgress{Stage: "import", Unit: "documents", Total: len(items), Completed: len(mapping.BySourceDocID), Failed: failed})
	}
	if _, err := c.ensureContract(work); err != nil {
		progressErr := finishProgress()
		return mapping, errors.Join(err, first, progressErr)
	}
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if len(jobs) == 0 || work.Err() != nil {
					mu.Unlock()
					return
				}
				job := jobs[0]
				jobs[0] = corpusImportJob{}
				jobs = jobs[1:]
				mu.Unlock()
				itemMapping, requeue, err := c.importCorpusItem(work, &job, native)
				mu.Lock()
				switch {
				case requeue:
					jobs = append(jobs, job)
				case err != nil:
					failed++
					if first == nil {
						first = err
					}
					stop()
				default:
					mergeKnowledgeMapping(&mapping, itemMapping)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	progressErr := finishProgress()
	first = errors.Join(first, progressErr)
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
	evidence := map[string]any{"content": item.Content, "source": firstNonEmpty(item.SourceDataset, "public-rag"), "source_type": "document", "metadata": metadata}
	if len(item.Labels) > 0 {
		evidence["labels"] = item.Labels
	}
	return map[string]any{"target_space": "team_shared", "idempotency_key": "eval:" + item.SourceDocID, "evidence": []map[string]any{evidence}}
}

func (c *HTTPClient) importCorpusItem(ctx context.Context, job *corpusImportJob, native *Native) (KnowledgeMapping, bool, error) {
	mapping := newKnowledgeMapping()
	item := job.item
	if job.input == nil {
		job.input = corpusRememberInput(item)
	}
	for {
		started := time.Now()
		var result map[string]any
		err := c.CallTool(ctx, "remember", job.input, &result)
		elapsed := time.Since(started)
		if err == nil && stringValue(result["submission_id"]) == "" {
			err = errors.New("remember response missing submission identity")
		}
		if err == nil {
			mapping, err = c.importTerminalRememberResult(item, result, mapping)
		}
		var status int
		if err != nil || job.attempt > 1 {
			event := map[string]any{"source_id": item.SourceDocID, "attempt": job.attempt, "idempotency_key": job.input["idempotency_key"], "result": result, "elapsed_ms": float64(elapsed) / float64(time.Millisecond)}
			if err != nil {
				event["error"] = err.Error()
				var httpErr *HTTPStatusError
				var toolErr *StructuredToolError
				switch {
				case errors.As(err, &httpErr):
					status = httpErr.StatusCode
					event["http_status"] = status
				case native != nil && ctx.Err() == nil && errors.As(err, &toolErr):
					var checked bool
					status, checked = native.rememberProviderHTTPStatus(ctx, toolErr.Result)
					if status != 0 {
						event["provider_http_status"] = status
					} else if checked {
						event["provider_status"] = "unavailable"
					}
				}
			}
			if captureErr := c.Capture.Emit("import_attempt", event); captureErr != nil {
				return mapping, false, captureErr
			}
		}
		if err == nil {
			return mapping, false, nil
		}
		if ctx.Err() != nil {
			return mapping, false, err
		}
		if isGatewayTimeoutStatus(status) {
			if job.attempt > maxGatewayRetries {
				return mapping, false, err
			}
			job.attempt++
			if err := c.Capture.Emit("intake_retry", map[string]any{"source_id": item.SourceDocID, "attempt": job.attempt, "idempotency_key": job.input["idempotency_key"], "gateway_http_status": status}); err != nil {
				return mapping, false, err
			}
			return mapping, true, nil
		}
		if job.attempt != 1 {
			return mapping, false, err
		}
		retry, original, retryable, terminal := terminalRememberRetryInput("remember", job.input, err)
		if terminal {
			job.input = retry
			if err := c.Capture.Emit("intake_retry", map[string]any{"source_id": item.SourceDocID, "original_attempt_id": original, "original_retryable": retryable, "idempotency_key": job.input["idempotency_key"]}); err != nil {
				return mapping, false, err
			}
		} else if !isTransientCallToolError(err) {
			return mapping, false, err
		}
		select {
		case <-ctx.Done():
			return mapping, false, ctx.Err()
		case <-time.After(750 * time.Millisecond):
		}
		job.attempt++
	}
}

func isGatewayTimeoutStatus(status int) bool {
	return status == http.StatusGatewayTimeout || status == 524
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
