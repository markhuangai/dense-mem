package decisionprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const completeAttempts = 2

type Provider struct {
	config    config.DecisionProviderConfig
	client    *http.Client
	gate      modelprovider.ConcurrencyGate
	tokenizer string
	logger    observability.LogProvider
	protector observability.DiagnosticProtector
	metrics   observability.DiscoverabilityMetrics
	endpoint  string
}

var _ modelprovider.DecisionProvider = (*Provider)(nil)

func New(cfg config.DecisionProviderConfig, tokenizer string, client *http.Client, logger observability.LogProvider, protector observability.DiagnosticProtector, metrics observability.DiscoverabilityMetrics) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if _, err := assessor.CountTokens("", tokenizer); err != nil {
		return nil, fmt.Errorf("decision tokenizer: %w", err)
	}
	if logger == nil || protector == nil {
		return nil, fmt.Errorf("decision logging and credential protection are required")
	}
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	// Redirects must not replay a private credential or an admitted evidence body.
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	path := "/decisions"
	if cfg.Provider == "typesafe" {
		path = "/systemone"
	}
	return &Provider{config: cfg, client: &copyClient, gate: modelprovider.NewConcurrencyGate(cfg.MaxConcurrency), tokenizer: tokenizer, logger: logger, protector: protector, metrics: metrics, endpoint: strings.TrimRight(cfg.APIURL, "/") + path}, nil
}

type batch struct {
	body      []byte
	questions []modelprovider.DecisionQuestion
	tokens    int
}

func (p *Provider) Decide(ctx context.Context, request modelprovider.DecisionRequest) (result modelprovider.DecisionResult, err error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.config.TimeoutSeconds)*time.Second)
	defer cancel()
	attrs := p.attributes(request)
	p.log(ctx, slog.LevelInfo, "decision.task.started", append(attrs, attr("action", "evaluate"))...)
	defer func() {
		level, action := slog.LevelInfo, "return_complete_answers"
		if err != nil {
			level, action = slog.LevelError, "fail_task"
		}
		fields := append(append([]observability.LogAttr{}, attrs...), attr("action", action), attr("total_ms", time.Since(started).Milliseconds()), attr("receipt_count", len(result.Receipts)))
		if err != nil {
			fields = append(fields, attr("error", err.Error()), attr("reason_code", failureReason(err)))
		}
		p.log(ctx, level, "decision.task.completed", fields...)
	}()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = modelprovider.ValidateDecisionRequest(request); err != nil {
		return result, p.failure("invalid decision request", "request_invalid", 0, err)
	}
	for _, group := range request.Groups {
		for _, question := range group.Questions {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			p.log(ctx, slog.LevelDebug, "decision.question.prepared", append(append([]observability.LogAttr{}, attrs...), attr("group_id", group.ID), attr("candidate_reference", group.Reference), attr("question", observability.DecisionLogValue(question)))...)
		}
	}
	batches, err := p.pack(ctx, request.Groups)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		p.log(ctx, slog.LevelWarn, "decision.context.rejected", append(attrs, attr("action", "reject_before_dispatch"), attr("reason_code", "configured_context_limit"), attr("error", err.Error()))...)
		return result, p.failure("decision request exceeds configured limits or cannot be encoded", "request_invalid", 0, err)
	}
	for attempt := 1; attempt <= completeAttempts; attempt++ {
		answers := make(map[string]modelprovider.DecisionAnswer)
		var attemptErr error
		for index, batch := range batches {
			var partial modelprovider.DecisionResult
			var receipt modelprovider.DecisionReceipt
			partial, receipt, attemptErr = p.call(ctx, batch, attrs, index+1, attempt)
			result.Receipts = append(result.Receipts, receipt)
			if attemptErr != nil {
				break
			}
			for name, answer := range partial.Answers {
				answers[name] = answer
			}
		}
		if attemptErr == nil {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			result.Answers = answers
			return result, nil
		}
		if !errors.Is(attemptErr, modelprovider.ErrVerifierMalformedResponse) || attempt == completeAttempts || ctx.Err() != nil {
			return result, attemptErr
		}
		p.log(ctx, slog.LevelWarn, "decision.validation.regenerate", append(attrs, attr("attempt", attempt), attr("action", "regenerate_complete_task"), attr("reason_code", "invalid_complete_response"), attr("error", attemptErr.Error()))...)
	}
	return result, p.failure("decision attempts exhausted", "provider_protocol", 0, nil)
}

func (p *Provider) pack(ctx context.Context, groups []modelprovider.DecisionGroup) ([]batch, error) {
	var batches []batch
	for len(groups) > 0 {
		encoded, err := p.encodeBatch(ctx, groups[:1])
		if err != nil {
			return nil, err
		}
		count := 1
		for low, high := 2, len(groups); low <= high; {
			middle := low + (high-low)/2
			next, err := p.encodeBatch(ctx, groups[:middle])
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil {
				high = middle - 1
				continue
			}
			encoded, count = next, middle
			low = middle + 1
		}
		batches = append(batches, encoded)
		groups = groups[count:]
	}
	return batches, nil
}

func (p *Provider) encodeBatch(ctx context.Context, groups []modelprovider.DecisionGroup) (batch, error) {
	if err := ctx.Err(); err != nil {
		return batch{}, err
	}
	body, questions, err := encodeRequest(p.config.Provider, p.config.Model, groups)
	if err != nil {
		return batch{}, err
	}
	if len(body) > modelprovider.MaxProviderDiagnosticBodyBytes || len(questions) > modelprovider.DecisionMaxBatchQuestions {
		return batch{}, fmt.Errorf("decision batch exceeds body or question limit")
	}
	tokens, err := p.inputTokens(ctx, body)
	if err != nil {
		return batch{}, err
	}
	if tokens > p.config.MaxInputTokens {
		return batch{}, fmt.Errorf("estimated request tokens %d exceed %d", tokens, p.config.MaxInputTokens)
	}
	for _, group := range groups {
		for _, question := range group.Questions {
			if err := ctx.Err(); err != nil {
				return batch{}, err
			}
			one := make([]modelprovider.DecisionGroup, len(groups))
			copy(one, groups)
			for i := range one {
				one[i].Questions = nil
				if one[i].ID == group.ID {
					one[i].Questions = []modelprovider.DecisionQuestion{question}
				}
			}
			single, _, encodeErr := encodeRequest(p.config.Provider, p.config.Model, one)
			if encodeErr != nil {
				return batch{}, encodeErr
			}
			count, countErr := p.inputTokens(ctx, single)
			if countErr != nil {
				return batch{}, countErr
			}
			if count > p.config.MaxQuestionInputTokens {
				return batch{}, fmt.Errorf("estimated shared state plus question tokens %d exceed %d", count, p.config.MaxQuestionInputTokens)
			}
		}
	}
	return batch{body: body, questions: questions, tokens: tokens}, nil
}

func (p *Provider) inputTokens(ctx context.Context, body []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	count, err := assessor.CountTokens(string(body), p.tokenizer)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	return count, err
}

func (p *Provider) call(ctx context.Context, batch batch, common []observability.LogAttr, index, attempt int) (result modelprovider.DecisionResult, receipt modelprovider.DecisionReceipt, err error) {
	started := time.Now()
	diagnostics := &modelprovider.CallDiagnostics{}
	receipt = modelprovider.DecisionReceipt{Provider: p.config.Provider, RequestedModel: p.config.Model, RequestHash: digest(batch.body), Batch: index, Attempt: attempt, QuestionCount: len(batch.questions), EstimatedInputTokens: batch.tokens, Tokenizer: p.tokenizer, Diagnostics: diagnostics}
	fields := append(append([]observability.LogAttr{}, common...), attr("batch", index), attr("attempt", attempt), attr("question_count", len(batch.questions)), attr("estimated_input_tokens", batch.tokens), attr("tokenizer", p.tokenizer), attr("token_count_kind", "estimate"))
	exchange := modelprovider.ProviderExchange{Component: "decision", Model: p.config.Model, RequestBody: batch.body, RequestContentType: "application/json", StartedAt: started, CaptureState: "captured"}
	defer func() {
		exchange.CompletedAt = time.Now()
		exchange.Outcome = "ok"
		level := slog.LevelInfo
		if err != nil {
			exchange.Outcome = "error"
			level = slog.LevelError
		}
		if exchange.StatusCode == http.StatusOK {
			if receipt.Usage == nil {
				p.log(ctx, slog.LevelWarn, "decision.usage.unavailable", append(fields, attr("action", "record_unpriced"), attr("reason_code", "missing_usage"))...)
				observability.RecordAIOperationUnpriced(ctx, p.metrics, observability.AIComponentDecision, receipt.Model, "missing_usage")
			} else {
				observability.RecordAIOperationUsage(ctx, p.metrics, observability.AIOperationUsage{Component: observability.AIComponentDecision, Model: receipt.Model, InputTokens: int64(receipt.Usage.InputTokens), OutputTokens: int64(receipt.Usage.OutputTokens), Source: observability.AITokenSourceProvider})
			}
		}
		receipt.ResponseHash = digest(exchange.ResponseBody)
		if recorder := modelprovider.ExchangeRecorderFromContext(ctx); recorder != nil {
			recorder.RecordProviderExchange(ctx, exchange)
		}
		p.payload(ctx, "request", exchange.RequestBody, fields)
		p.payload(ctx, "response", exchange.ResponseBody, fields)
		finished := append(append([]observability.LogAttr{}, fields...), attr("action", "complete_exchange"), attr("status", exchange.StatusCode), attr("resolved_model", receipt.Model), attr("provider_request_id", receipt.ProviderRequestID), attr("usage", observability.DecisionLogValue(receipt.Usage)), attr("diagnostics", observability.DecisionLogValue(diagnostics)), attr("total_ms", time.Since(started).Milliseconds()), attr("capture_state", exchange.CaptureState))
		if err != nil {
			finished = append(finished, attr("error", err.Error()), attr("reason_code", failureReason(err)))
		}
		p.log(ctx, level, "decision.exchange.completed", finished...)
	}()
	diagnostics.FailurePhase = "queue"
	if err = modelprovider.AcquireConcurrency(ctx, p.gate); err != nil {
		return result, receipt, err
	}
	defer modelprovider.ReleaseConcurrency(p.gate)
	queueMS := time.Since(started).Milliseconds()
	diagnostics.QueueMS = &queueMS
	diagnostics.FailurePhase = "prepare"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(batch.body))
	if err != nil {
		return result, receipt, p.failure("decision request construction failed", "request_invalid", 0, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	if p.config.LongContextEnabled {
		request.Header.Set("TypeSafe-Danger-Zone-Long-Context", "true")
	}
	diagnostics.FailurePhase = "admission"
	admission := time.Now()
	if err = modelprovider.NotifyAdmission(ctx); err != nil {
		return result, receipt, err
	}
	admissionMS := time.Since(admission).Milliseconds()
	diagnostics.AdmissionMS = &admissionMS
	if err = ctx.Err(); err != nil {
		return result, receipt, err
	}
	diagnostics.FailurePhase = "request"
	httpStarted := time.Now()
	response, err := p.client.Do(request)
	httpMS := time.Since(httpStarted).Milliseconds()
	diagnostics.HTTPMS = &httpMS
	if err != nil {
		if ctx.Err() != nil {
			return result, receipt, ctx.Err()
		}
		return result, receipt, p.failure("decision HTTP transport failed", "transport", 0, err)
	}
	defer response.Body.Close()
	exchange.StatusCode, exchange.ResponseContentType = response.StatusCode, response.Header.Get("Content-Type")
	receipt.ProviderRequestID = boundedIdentity(response.Header.Get("x-request-id"))
	diagnostics.FailurePhase = "response_body"
	body, readErr := io.ReadAll(io.LimitReader(response.Body, modelprovider.MaxProviderDiagnosticBodyBytes+1))
	exchange.ResponseBodySize = len(body)
	if len(body) > modelprovider.MaxProviderDiagnosticBodyBytes {
		exchange.CaptureState, exchange.CaptureReason = "unavailable", "body_limit"
		return result, receipt, p.malformed("response exceeds transport limit", attempt)
	}
	exchange.ResponseBody = body
	if readErr != nil {
		if ctx.Err() != nil {
			return result, receipt, ctx.Err()
		}
		return result, receipt, p.failure("decision response read failed", "transport", response.StatusCode, readErr)
	}
	if err = ctx.Err(); err != nil {
		return result, receipt, err
	}
	diagnostics.FailurePhase = "response_status"
	if response.StatusCode != http.StatusOK {
		return result, receipt, p.statusError(ctx, response, fields)
	}
	diagnostics.FailurePhase = "response_decode"
	result, receipt.Model, receipt.Usage, err = decodeResponse(p.config.Provider, body, batch.questions)
	if err != nil {
		p.log(ctx, slog.LevelDebug, "decision.validation.discrepancy", append(fields, attr("error", err.Error()), attr("action", "reject_complete_response"))...)
		return modelprovider.DecisionResult{}, receipt, p.malformed(err.Error(), attempt)
	}
	diagnostics.FailurePhase = "validation"
	if receipt.Usage != nil && receipt.Usage.InputTokens > p.config.MaxInputTokens {
		p.log(ctx, slog.LevelWarn, "decision.context.rejected", append(fields, attr("reported_input_tokens", receipt.Usage.InputTokens), attr("reason_code", "reported_context_limit"), attr("action", "fail_task"))...)
		return modelprovider.DecisionResult{}, receipt, p.failure("reported decision input exceeds configured limit", "request_invalid", 200, nil)
	}
	for name, answer := range result.Answers {
		p.log(ctx, slog.LevelDebug, "decision.answer.validated", append(fields, attr("question_id", name), attr("answer", observability.DecisionLogValue(answer)))...)
		if answer.Type == modelprovider.DecisionRefusal {
			p.log(ctx, slog.LevelWarn, "decision.refused", append(fields, attr("action", "fail_task"), attr("reason_code", "refusal"))...)
			return modelprovider.DecisionResult{}, receipt, p.failure("decision provider refused a required answer", "refusal", 200, nil)
		}
	}
	diagnostics.FailurePhase = ""
	return result, receipt, nil
}

func (p *Provider) statusError(ctx context.Context, response *http.Response, fields []observability.LogAttr) error {
	status := response.StatusCode
	reason, class := "unexpected_status", modelprovider.ProviderFailureClassHTTPUnexpected
	switch {
	case status == 401 || status == 403:
		reason, class = "authentication", modelprovider.ProviderFailureClassHTTPClient
	case status == 429:
		reason, class = "rate_limited", modelprovider.ProviderFailureClassRateLimited
	case status == 529 || status == 503:
		reason, class = "overloaded", modelprovider.ProviderFailureClassHTTPServer
	case status == 413 || status == 422:
		reason, class = "context_rejected", modelprovider.ProviderFailureClassHTTPClient
	case status >= 500:
		reason, class = "provider_server", modelprovider.ProviderFailureClassHTTPServer
	case status >= 400:
		reason, class = "provider_request", modelprovider.ProviderFailureClassHTTPClient
	}
	if status == 429 || status == 529 || status == 503 || status == 413 || status == 422 {
		p.log(ctx, slog.LevelWarn, "decision.provider.rejected", append(fields, attr("status", status), attr("reason_code", reason), attr("action", "fail_task"))...)
	}
	if status == 429 {
		retryAfter, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		if retryAfter < 0 {
			retryAfter = 0
		}
		if retryAfter > 300 {
			retryAfter = 300
		}
		return &modelprovider.RateLimitError{Provider: p.config.Provider, Message: "decision request rate limited", RetryAfter: retryAfter}
	}
	return p.failure("decision provider rejected request: "+reason, class, status, nil)
}

func (p *Provider) failure(message, class string, status int, cause error) error {
	return &modelprovider.ProviderError{Provider: p.config.Provider, Message: message, FailureClass: class, StatusCode: status, Cause: cause}
}

func (p *Provider) malformed(message string, attempts int) error {
	return &modelprovider.MalformedResponseError{Provider: p.config.Provider, Message: message, Attempts: attempts, FailureClass: modelprovider.ProviderFailureClassProtocol, ValidationStage: "decision_complete_response"}
}

func digest(body []byte) string { return fmt.Sprintf("%x", sha256.Sum256(body)) }
func attr(key string, value any) observability.LogAttr {
	return observability.LogAttr{Key: key, Value: value}
}

func boundedIdentity(value string) string {
	if len(value) > 128 {
		return "unavailable"
	}
	for _, r := range value {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.:/", r) {
			return "unavailable"
		}
	}
	return value
}

func failureReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, modelprovider.ErrVerifierMalformedResponse) {
		return "malformed_response"
	}
	return modelprovider.ProviderFailureDetails(err).Class
}

func (p *Provider) attributes(request modelprovider.DecisionRequest) []observability.LogAttr {
	endpoint, _ := url.Parse(p.endpoint)
	return []observability.LogAttr{attr("workload", request.Workload), attr("operation_id", request.OperationID), attr("settings_revision", request.SettingsVersion), attr("provider", p.config.Provider), attr("configured_model", p.config.Model), attr("endpoint", endpoint.Scheme+"://"+endpoint.Host+endpoint.Path), attr("group_count", len(request.Groups)), attr("max_input_tokens", p.config.MaxInputTokens), attr("max_question_input_tokens", p.config.MaxQuestionInputTokens), attr("timeout_seconds", p.config.TimeoutSeconds), attr("max_concurrency", p.config.MaxConcurrency), attr("long_context_enabled", p.config.LongContextEnabled)}
}

func (p *Provider) log(ctx context.Context, level slog.Level, event string, fields ...observability.LogAttr) {
	fields = append(fields, attr("event", event))
	if logger, ok := p.logger.(observability.ContextLogProvider); ok {
		logger.LogContext(ctx, level, event, fields...)
		return
	}
	switch level {
	case slog.LevelDebug:
		p.logger.Debug(event, fields...)
	case slog.LevelWarn:
		p.logger.Warn(event, fields...)
	case slog.LevelError:
		p.logger.Error(event, nil, fields...)
	default:
		p.logger.Info(event, fields...)
	}
}

func (p *Provider) payload(ctx context.Context, direction string, body []byte, common []observability.LogAttr) {
	const logBodyLimit = 8192
	protected, reason := p.protector.ProtectDiagnosticBytes(body, logBodyLimit, observability.AuthenticationSecretsFromContext(ctx)...)
	state := "captured"
	if reason != observability.CredentialProtectionAvailable {
		state = "unavailable"
	}
	fields := append(append([]observability.LogAttr{}, common...), attr("direction", direction), attr("capture_state", state), attr("capture_reason", int(reason)), attr("payload_bytes", len(body)), attr("payload_limit_bytes", logBodyLimit), attr("truncated", len(body) > logBodyLimit))
	if protected != nil {
		fields = append(fields, attr("payload", string(protected)))
	}
	p.log(ctx, slog.LevelDebug, "decision.exchange.payload", fields...)
}
