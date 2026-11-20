package evalharness

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const maxAnswerTransportRetries = 2

type answerRateLimitKey struct{}

type answerAdmission struct {
	mu    sync.Mutex
	until time.Time
}

func (a *answerAdmission) hold(until time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if until.After(a.until) {
		a.until = until
	}
}

func (a *answerAdmission) wait(ctx context.Context, retryAt time.Time) error {
	for {
		a.mu.Lock()
		readyAt := a.until
		a.mu.Unlock()
		if retryAt.After(readyAt) {
			readyAt = retryAt
		}
		if !time.Now().Before(readyAt) {
			return ctx.Err()
		}
		if err := waitUntil(ctx, readyAt); err != nil {
			return err
		}
	}
}

func waitUntil(ctx context.Context, readyAt time.Time) error {
	if deadline, ok := ctx.Deadline(); ok && !readyAt.Before(deadline) {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(time.Until(readyAt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func answerRetryAfter(value string, receivedAt time.Time) time.Time {
	value = strings.TrimSpace(value)
	seconds, err := strconv.ParseUint(value, 10, 64)
	const maximum = time.Duration(1<<63 - 1)
	if errors.Is(err, strconv.ErrRange) || (err == nil && seconds > uint64(maximum/time.Second)) {
		return receivedAt.Add(maximum)
	}
	if err == nil {
		return receivedAt.Add(time.Duration(seconds) * time.Second)
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		return retryAt
	}
	return time.Time{}
}

type answerExchange struct {
	client     *HTTPClient
	transport  modelprovider.StructuredTransport
	runID      string
	questionID string
	condition  string
}

func (e answerExchange) complete(ctx context.Context, request modelprovider.StructuredRequest, result *HostResult, regeneration int, retries *int) (modelprovider.StructuredResult, error) {
	var retryAt time.Time
	var response modelprovider.StructuredResult
	var callErr error
	operation := e.condition + ":" + result.Policy + ":" + e.questionID
	capture := e.client.Capture
	for {
		if err := e.client.answerAdmission.wait(ctx, retryAt); err != nil {
			return response, errors.Join(callErr, err)
		}
		number := len(result.Attempts) + 1
		event := map[string]any{"question_id": e.questionID, "condition": e.condition, "policy": result.Policy, "turn": len(result.Calls), "regeneration": regeneration}
		event["attempt"], event["request"] = number, request
		if err := capture.Emit("answer_request", event); err != nil {
			return response, err
		}
		called := time.Now()
		callCtx := observability.WithExchangeScope(ctx, observability.ExchangeScope{RunID: e.runID, OperationID: operation, Attempt: number})
		backoff := time.Duration(min(*retries+1, maxAnswerTransportRetries)) * time.Second
		callCtx = context.WithValue(callCtx, answerRateLimitKey{}, func(retryAt time.Time) time.Time {
			return e.holdRateLimit(retryAt, backoff)
		})
		response, callErr = e.transport.Complete(callCtx, request)
		attempt := ProviderAttempt{Number: number, ElapsedMS: float64(time.Since(called)) / float64(time.Millisecond), UsageState: "unavailable"}
		if response.UsageAvailable {
			attempt.Input, attempt.Output, attempt.Total = response.PromptTokens, response.CompletionTokens, response.TotalTokens
			attempt.UsageState = "observed"
			if attempt.Input < 0 || attempt.Output < 0 || attempt.Total < 0 || attempt.Input+attempt.Output != attempt.Total {
				attempt.UsageState = "unreconciled"
			}
		}
		if callErr != nil {
			attempt.Error = capture.ProtectError(callErr)
		}
		next, status, network := e.retryTime(callErr, backoff)
		retryAt = next
		retry := !retryAt.IsZero() && *retries < maxAnswerTransportRetries && ctx.Err() == nil
		if deadline, ok := ctx.Deadline(); ok && !retryAt.Before(deadline) {
			retry = false
		}
		if !retryAt.IsZero() && capture != nil && capture.Logger != nil {
			capture.Logger.Warn("evaluation answer transport failure",
				"run_id", e.runID, "operation_id", operation, "attempt", number, "http_status", status,
				"network_failure", network, "wait_ms", max(0, time.Until(retryAt).Milliseconds()), "retrying", retry)
		}
		result.Attempts = append(result.Attempts, attempt)
		delete(event, "request")
		event["attempt"], event["response"] = attempt, response
		if err := capture.Emit("answer_response", event); err != nil {
			return response, err
		}
		if !retry {
			return response, callErr
		}
		*retries = *retries + 1
	}
}

func (e answerExchange) holdRateLimit(retryAt time.Time, backoff time.Duration) time.Time {
	ready := time.Now().Add(backoff)
	if retryAt.After(ready) {
		ready = retryAt
	}
	e.client.answerAdmission.hold(ready)
	return ready
}

func (e answerExchange) retryTime(err error, backoff time.Duration) (time.Time, int, string) {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		if status.StatusCode == http.StatusTooManyRequests {
			retryAt := status.retryAt
			if !status.held {
				retryAt = e.holdRateLimit(retryAt, backoff)
			}
			return retryAt, status.StatusCode, ""
		}
		if isRetryableGatewayStatus(status.StatusCode) {
			return time.Now().Add(backoff), status.StatusCode, ""
		}
	}
	var network *httpNetworkError
	if errors.As(err, &network) && modelprovider.IsTransientNetworkFailure(network.kind) {
		return time.Now().Add(backoff), 0, network.kind
	}
	return time.Time{}, 0, ""
}
