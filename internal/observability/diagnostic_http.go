package observability

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type exchangeScopeKey struct{}

type ExchangeScope struct{ TeamID, RunID, BatchID string }

func WithExchangeScope(ctx context.Context, scope ExchangeScope) context.Context {
	return context.WithValue(ctx, exchangeScopeKey{}, scope)
}

// DiagnosticHTTPClient preserves transport behavior and only observes bodies
// already admitted by the caller; headers never enter payload diagnostics.
func DiagnosticHTTPClient(client *http.Client, logger *slog.Logger, protector DiagnosticProtector) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copyClient.Transport = &diagnosticTransport{base: base, logger: logger, protector: protector}
	return &copyClient
}

type diagnosticTransport struct {
	base      http.RoundTripper
	logger    *slog.Logger
	protector DiagnosticProtector
}

func (t *diagnosticTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	observer, _ := ctx.Value(httpDiagnosticObserverKey{}).(func(HTTPDiagnosticObservation))
	if observer == nil && (t.logger == nil || !t.logger.Enabled(ctx, slog.LevelDebug)) {
		return t.base.RoundTrip(request)
	}
	exchangeID := uuid.NewString()
	observation, tracedCtx := newHTTPObservation(ctx, exchangeID)
	if observation != nil {
		request = request.WithContext(tracedCtx)
	}
	attrs := []slog.Attr{slog.String("exchange_id", exchangeID), slog.String("invocation_id", requestctx.RememberInvocationIDFromContext(ctx))}
	if scope, ok := ctx.Value(exchangeScopeKey{}).(ExchangeScope); ok {
		attrs = append(attrs, slog.String("team_id", scope.TeamID), slog.String("run_id", scope.RunID), slog.String("batch_id", scope.BatchID))
	}
	var body []byte
	state := "captured"
	if request.Body != nil && request.GetBody != nil {
		copyBody, err := request.GetBody()
		if err == nil {
			body, err = io.ReadAll(io.LimitReader(copyBody, modelprovider.MaxProviderDiagnosticBodyBytes+1))
			_ = copyBody.Close()
		}
		if err != nil {
			state = "unavailable"
			body = nil
		}
	} else if request.Body != nil {
		state = "unavailable"
	}
	started := time.Now()
	attrs = append(attrs, slog.Time("started_at", started.UTC()))
	response, err := t.base.RoundTrip(request)
	if err != nil {
		attrs = append(attrs, slog.Int64("elapsed_ms", time.Since(started).Milliseconds()))
		requestState, reason := t.log(ctx, "request", body, state, attrs)
		t.log(ctx, "response", nil, "no_response", attrs)
		t.log(ctx, "transport_error", []byte(err.Error()), "captured", attrs)
		observation.finish(ctx, t.protector, 0, err, requestState, "no_response", reason)
		return response, err
	}
	attrs = append(attrs, slog.Int("status_code", response.StatusCode))
	if response.Body == nil {
		attrs = append(attrs, slog.Int64("elapsed_ms", time.Since(started).Milliseconds()))
		requestState, reason := t.log(ctx, "request", body, state, attrs)
		t.log(ctx, "response", nil, "no_response", attrs)
		observation.finish(ctx, t.protector, response.StatusCode, nil, requestState, "no_response", reason)
		return response, nil
	}
	response.Body = &diagnosticResponseBody{ReadCloser: response.Body, expected: response.ContentLength, done: func(observed []byte, responseState string, cause error) {
		attrs = append(attrs, slog.Int64("elapsed_ms", time.Since(started).Milliseconds()))
		requestState, requestReason := t.log(ctx, "request", body, state, attrs)
		responseState, responseReason := t.log(ctx, "response", observed, responseState, attrs)
		observation.finish(ctx, t.protector, response.StatusCode, cause, requestState, responseState, strings.Trim(strings.Join([]string{requestReason, responseReason}, ";"), ";"))
	}}
	return response, nil
}

func (t *diagnosticTransport) log(ctx context.Context, direction string, body []byte, state string, attrs []slog.Attr) (string, string) {
	attrs = append(append([]slog.Attr{}, attrs...), slog.String("direction", direction), slog.String("transport_capture_state", state))
	chunks := CaptureDiagnosticBody(ctx, t.protector, body, modelprovider.MaxProviderDiagnosticBodyBytes)
	logger := t.logger
	if scoped, ok := ctx.Value(httpDiagnosticLoggerKey{}).(*slog.Logger); ok {
		logger = scoped
	}
	if logger != nil && logger.Enabled(ctx, slog.LevelDebug) {
		for _, chunk := range chunks {
			fields := append(append([]slog.Attr{}, attrs...), slog.Any("payload_chunk", chunk))
			logger.LogAttrs(WithSinkSuppressed(ctx), slog.LevelDebug, "ai_exchange_payload", fields...)
		}
	}
	if chunks[0].State != "captured" {
		return chunks[0].State, "credential_protection_" + strconv.Itoa(chunks[0].Reason)
	}
	return state, ""
}

type diagnosticResponseBody struct {
	io.ReadCloser
	body     []byte
	observed int64
	expected int64
	readEOF  bool
	readErr  bool
	cause    error
	mu       sync.Mutex
	once     sync.Once
	done     func([]byte, string, error)
}

func (b *diagnosticResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observed += int64(n)
	if remaining := modelprovider.MaxProviderDiagnosticBodyBytes + 1 - len(b.body); remaining > 0 {
		b.body = append(b.body, p[:min(n, remaining)]...)
	}
	b.readEOF = b.readEOF || err == io.EOF
	b.readErr = b.readErr || (err != nil && err != io.EOF)
	if err != nil && err != io.EOF {
		b.cause = err
	}
	return n, err
}

func (b *diagnosticResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	state := "interrupted"
	if !b.readErr && (b.readEOF || (err == nil && b.expected >= 0 && b.observed == b.expected)) {
		state = "captured"
	}
	// EOF must reach the HTTP client before diagnostic work can consume its timeout.
	if b.cause == nil {
		b.cause = err
	}
	b.once.Do(func() { b.done(b.body, state, b.cause) })
	return err
}
