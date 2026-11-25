package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const RepairPolicyRevision = "structured-complete-v1"

const omittedAssistant = "The prior assistant response exceeded the repair history budget. Use the unchanged original request and validation errors to return one complete response."

// Task freezes the provider envelope before its first dispatch. A validator
// returns a ValidationError for repairable content and any other error for a
// terminal capability failure.
type Task[T any] struct {
	ID, Capability, Tokenizer, Correction string
	Request                               modelprovider.StructuredRequest
	InitialInputLimit                     int
	RepairTokenReserve                    int
	ResponsesConsumed                     int
	Validate                              func(context.Context, []byte) (T, error)
	BeforeRepair                          func(context.Context, Attempt) error
	BeforeAttempt                         func(context.Context, int, int) (context.Context, error)
	AfterTransport                        func(context.Context, *Attempt, error) error
	Observe                               func(context.Context, Attempt)
}

type Attempt struct {
	Number, InputTokens, OutputTokens int
	ExchangeID                        string
	Provider                          modelprovider.StructuredResult
	Validation                        *jsonstrict.ValidationError
	Elapsed, ValidationElapsed        time.Duration
	Err                               error
}

func (a Attempt) EffectiveInputTokens() int {
	if a.Provider.PromptTokens > 0 {
		return a.Provider.PromptTokens
	}
	return a.InputTokens
}

type Result[T any] struct {
	Value    T
	Attempts []Attempt
}

type Service[T any] struct {
	Transport modelprovider.StructuredTransport
	Logger    *slog.Logger
}

type correction struct {
	Errors      []jsonstrict.Issue `json:"validation_errors"`
	Instruction string             `json:"instruction"`
}

// Run returns only a completely validated replacement. Each call has at most
// three provider responses, including any previously consumed allowance.
func (s Service[T]) Run(ctx context.Context, task Task[T]) (final Result[T], terminal error) {
	started := time.Now()
	terminalCtx := ctx
	defer func() {
		level, event, outcome := slog.LevelInfo, "structured_ai.completed", "validated"
		if terminal != nil {
			level, event, outcome = slog.LevelError, "structured_ai.failed", "failed"
		}
		if s.Logger == nil || !s.Logger.Enabled(terminalCtx, level) {
			return
		}
		fields := []slog.Attr{slog.String("outcome", outcome), slog.Int("attempt_count", len(final.Attempts)),
			slog.Int("responses_previously_consumed", task.ResponsesConsumed), slog.Int64("elapsed_ms", time.Since(started).Milliseconds())}
		summaries := make([]map[string]any, 0, len(final.Attempts))
		for _, attempt := range final.Attempts {
			summary := map[string]any{
				"attempt": attempt.Number, "exchange_id": attempt.ExchangeID, "elapsed_ms": attempt.Elapsed.Milliseconds(),
				"estimated_input_tokens": attempt.InputTokens, "estimated_output_tokens": attempt.OutputTokens,
				"usage_available": attempt.Provider.UsageAvailable, "reported_input_tokens": attempt.Provider.PromptTokens,
				"reported_output_tokens": attempt.Provider.CompletionTokens, "reported_total_tokens": attempt.Provider.TotalTokens,
				"http_status": attempt.Provider.StatusCode, "transport": attempt.Provider.Diagnostics.LogFields(),
			}
			switch {
			case attempt.Validation != nil:
				summary["outcome"] = "invalid_response"
				summary["validation_stage"] = attempt.Validation.Stage
				summary["issue_count"] = len(attempt.Validation.Errors)
			case attempt.Err != nil:
				summary["outcome"] = "failed"
			default:
				summary["outcome"] = "validated"
			}
			summaries = append(summaries, summary)
		}
		fields = append(fields, slog.Any("attempts", summaries))
		if terminal != nil {
			fields = failureFields(terminal, fields)
		}
		s.log(terminalCtx, level, event, task, fields...)
	}()
	if s.Transport == nil || task.Validate == nil || task.ResponsesConsumed < 0 || task.ResponsesConsumed >= modelprovider.StructuredResponseLimit {
		return final, &modelprovider.ProviderError{Provider: task.Capability, Message: "invalid structured generation task", FailureClass: modelprovider.ProviderFailureClassRequestInvalid}
	}
	request := task.Request
	schema, err := json.Marshal(request.Schema)
	if err != nil {
		return final, invalidEnvelope(task.Capability, err)
	}
	request.Messages = slices.Clone(request.Messages)
	if request.Temperature != nil {
		value := *request.Temperature
		request.Temperature = &value
	}
	for turn := task.ResponsesConsumed + 1; turn <= modelprovider.StructuredResponseLimit; turn++ {
		if err := ctx.Err(); err != nil {
			return final, err
		}
		// Re-decode the sealed schema for the call so transport hooks cannot
		// mutate retained nested schema values used by a later repair.
		request.Schema = nil
		decoder := json.NewDecoder(bytes.NewReader(schema))
		decoder.UseNumber()
		if err := decoder.Decode(&request.Schema); err != nil {
			return final, invalidEnvelope(task.Capability, err)
		}
		inputLimit := request.MaxInputTokens - (modelprovider.StructuredResponseLimit-turn)*task.RepairTokenReserve
		if turn == task.ResponsesConsumed+1 && task.InitialInputLimit > 0 {
			inputLimit = min(inputLimit, task.InitialInputLimit)
		}
		inputTokens, err := fitHistory(&request, task.Tokenizer, inputLimit, len(task.Request.Messages))
		if err != nil {
			return final, invalidEnvelope(task.Capability, err)
		}
		if inputTokens > inputLimit {
			return final, inputOverrun(task.Capability, turn-1, inputTokens, inputLimit)
		}
		attempt := Attempt{Number: turn, InputTokens: inputTokens, ExchangeID: uuid.NewString()}
		scope := observability.ExchangeScopeFromContext(ctx)
		scope.AssessmentID, scope.Attempt, scope.ExchangeID = task.ID, turn, attempt.ExchangeID
		attemptCtx := observability.WithExchangeScope(ctx, scope)
		terminalCtx = attemptCtx
		if task.BeforeAttempt != nil {
			attemptCtx, err = task.BeforeAttempt(attemptCtx, turn, inputTokens)
			if err != nil {
				return final, err
			}
			terminalCtx = attemptCtx
		}
		call := request
		call.Messages = slices.Clone(request.Messages)
		if call.Temperature != nil {
			value := *call.Temperature
			call.Temperature = &value
		}
		attemptStarted := time.Now()
		reply, err := s.Transport.Complete(attemptCtx, call)
		attempt.Provider = reply
		attempt.Provider.Content = ""
		if task.AfterTransport != nil {
			err = task.AfterTransport(attemptCtx, &attempt, err)
		}
		fields := []slog.Attr{
			slog.Int("estimated_input_tokens", inputTokens),
			slog.Int("reported_input_tokens", reply.PromptTokens),
			slog.Int("reported_output_tokens", reply.CompletionTokens),
			slog.Int("reported_total_tokens", reply.TotalTokens),
			slog.Bool("usage_available", reply.UsageAvailable),
			slog.Int("http_status", reply.StatusCode),
			slog.Int64("elapsed_ms", time.Since(attemptStarted).Milliseconds()),
			slog.Any("transport", attempt.Provider.Diagnostics.LogFields()),
		}
		if err != nil {
			s.log(attemptCtx, slog.LevelError, "structured_ai.attempt.completed", task, failureFields(err, fields)...)
		} else {
			s.log(attemptCtx, observability.LevelTrace, "structured_ai.attempt.completed", task, fields...)
		}
		validationStarted := time.Now()
		var value T
		if err == nil {
			switch {
			case attemptCtx.Err() != nil:
				err = attemptCtx.Err()
			case reply.PromptTokens > request.MaxInputTokens:
				err = inputOverrun(task.Capability, turn, reply.PromptTokens, request.MaxInputTokens)
			default:
				attempt.OutputTokens, err = modelprovider.CountTokens(reply.Content, task.Tokenizer)
				if err == nil {
					value, err = task.Validate(attemptCtx, []byte(reply.Content))
					var rejected *jsonstrict.ValidationError
					if observed := max(attempt.OutputTokens, reply.CompletionTokens); observed > request.MaxOutputTokens && (err == nil || errors.As(err, &rejected)) {
						err = &jsonstrict.ValidationError{Stage: "response_output_tokens", Errors: []jsonstrict.Issue{{
							Field: "output_tokens", Code: "output_limit", Message: fmt.Sprintf("at most %d output tokens allowed; received %d", request.MaxOutputTokens, observed),
						}}}
					}
				}
			}
			if contextErr := attemptCtx.Err(); contextErr != nil {
				err = contextErr
			}
			attempt.ValidationElapsed = time.Since(validationStarted)
			errors.As(err, &attempt.Validation)
		}
		attempt.Err, attempt.Elapsed = err, time.Since(attemptStarted)
		final.Attempts = append(final.Attempts, attempt)
		if task.Observe != nil {
			task.Observe(attemptCtx, attempt)
		}
		if err == nil {
			final.Value = value
			return final, nil
		}
		if attempt.Validation == nil {
			return final, err
		}
		issues := jsonstrict.BoundIssues(attempt.Validation.Errors)
		diagnosticIssues := make([]map[string]string, 0, len(issues))
		for _, issue := range issues {
			diagnosticIssues = append(diagnosticIssues, map[string]string{"field": issue.Field, "message": issue.Message, "code": issue.Code})
		}
		validationFields := append(fields, slog.Int("estimated_output_tokens", attempt.OutputTokens),
			slog.Int64("validation_ms", attempt.ValidationElapsed.Milliseconds()), slog.String("validation_stage", attempt.Validation.Stage),
			slog.Any("issues", diagnosticIssues), slog.Int("issue_count", len(attempt.Validation.Errors)))
		if turn == modelprovider.StructuredResponseLimit {
			s.log(attemptCtx, slog.LevelError, "structured_ai.validation.exhausted", task, validationFields...)
			return final, &modelprovider.MalformedResponseError{
				Provider: task.Capability, Message: "structured response remained invalid after complete regeneration",
				FailureClass: "malformed_exhausted", Attempts: turn, ValidationStage: attempt.Validation.Stage,
			}
		}
		if task.BeforeRepair != nil {
			if err := task.BeforeRepair(ctx, attempt); err != nil {
				s.log(attemptCtx, slog.LevelError, "structured_ai.repair.blocked", task, failureFields(err, validationFields)...)
				return final, err
			}
		}
		s.log(attemptCtx, slog.LevelWarn, "structured_ai.repair.requested", task,
			append(validationFields, slog.String("next_action", "request_complete_replacement"))...)
		feedback, err := json.Marshal(correction{Errors: issues, Instruction: task.Correction})
		if err != nil {
			return final, invalidEnvelope(task.Capability, err)
		}
		previous := reply.Content
		if attempt.OutputTokens > request.MaxOutputTokens || len(previous) > modelprovider.MaxProviderDiagnosticBodyBytes {
			previous = omittedAssistant
		}
		request.Messages = append(request.Messages,
			modelprovider.Message{Role: "assistant", Content: previous},
			modelprovider.Message{Role: "user", Content: string(feedback)},
		)
	}
	panic("unreachable structured generation state")
}

func fitHistory(request *modelprovider.StructuredRequest, tokenizer string, limit, initialMessages int) (int, error) {
	tokens, err := modelprovider.CountStructuredRequestTokens(*request, tokenizer)
	for i := len(request.Messages) - 2; err == nil && tokens > limit && i >= initialMessages; i-- {
		item := &request.Messages[i]
		if item.Role != "assistant" || item.Content == omittedAssistant {
			continue
		}
		original := item.Content
		item.Content = omittedAssistant
		smaller, countErr := modelprovider.CountStructuredRequestTokens(*request, tokenizer)
		if countErr != nil {
			return 0, countErr
		}
		if smaller < tokens {
			tokens = smaller
		} else {
			item.Content = original
		}
	}
	return tokens, err
}

func invalidEnvelope(provider string, cause error) error {
	return &modelprovider.ProviderError{Provider: provider, Message: "could not serialize or measure structured request", Cause: cause, FailureClass: modelprovider.ProviderFailureClassRequestInvalid}
}

func inputOverrun(provider string, count, observed, limit int) error {
	return &modelprovider.MalformedResponseError{
		Provider: provider, Message: "structured request exceeds input token limit", FailureClass: "input_budget",
		Attempts: count, ValidationStage: "conversation_input_tokens", ValidationFieldFamilies: []string{"input_tokens"},
		Measurement: &modelprovider.FailureMeasurement{Unit: "tokens", Observed: observed, Limit: limit},
	}
}

func failureFields(err error, fields []slog.Attr) []slog.Attr {
	failure := modelprovider.ProviderFailureDetails(err)
	var invalid *modelprovider.MalformedResponseError
	if errors.As(err, &invalid) {
		failure.Class = invalid.FailureClass
		if invalid.Measurement != nil {
			fields = append(fields, slog.Int("observed_tokens", invalid.Measurement.Observed), slog.Int("token_limit", invalid.Measurement.Limit))
		}
	} else if errors.Is(err, context.Canceled) {
		failure.Class = "cancelled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		failure.Class = "timeout"
	}
	return append(fields, slog.String("failure_class", failure.Class), slog.Int64("retry_after_ms", failure.RetryAfter.Milliseconds()), slog.String("error", err.Error()))
}

func (s Service[T]) log(ctx context.Context, level slog.Level, event string, task Task[T], fields ...slog.Attr) {
	if s.Logger == nil || !s.Logger.Enabled(ctx, level) {
		return
	}
	scope := observability.ExchangeScopeFromContext(ctx)
	attrs := []slog.Attr{
		slog.String("assessment_id", task.ID), slog.String("capability", task.Capability),
		slog.String("schema", task.Request.SchemaName), slog.String("model", task.Request.Model),
		slog.String("repair_policy", RepairPolicyRevision), slog.String("tokenizer", task.Tokenizer),
		slog.Int("max_input_tokens", task.Request.MaxInputTokens), slog.Int("max_output_tokens", task.Request.MaxOutputTokens),
		slog.Int("attempt", scope.Attempt), slog.String("exchange_id", scope.ExchangeID),
	}
	if id := requestctx.RememberInvocationIDFromContext(ctx); id != "" {
		attrs = append(attrs, slog.String("invocation_id", id))
	}
	if scope.TeamID != "" {
		attrs = append(attrs, slog.String("team_id", scope.TeamID))
	}
	for key, value := range map[string]string{"run_id": scope.RunID, "batch_id": scope.BatchID, "operation_id": scope.OperationID} {
		if value != "" {
			attrs = append(attrs, slog.String(key, value))
		}
	}
	s.Logger.LogAttrs(ctx, level, event, append(attrs, fields...)...)
}
