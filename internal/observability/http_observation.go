package observability

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http/httptrace"
	"sync"
	"time"
)

type HTTPDiagnosticObservation struct {
	ExchangeID           string `json:"exchange_id"`
	StatusCode           int    `json:"status_code"`
	DurationMS           int64  `json:"duration_ms"`
	DNSMS                *int64 `json:"dns_ms"`
	ConnectMS            *int64 `json:"connect_ms"`
	TLSMS                *int64 `json:"tls_ms"`
	FirstByteMS          *int64 `json:"first_byte_ms"`
	ConnectionReused     *bool  `json:"connection_reused"`
	FailurePhase         string `json:"failure_phase,omitempty"`
	Error                string `json:"error,omitempty"`
	RequestCaptureState  string `json:"request_capture_state"`
	ResponseCaptureState string `json:"response_capture_state"`
	CaptureReason        string `json:"capture_reason,omitempty"`
}

type httpDiagnosticObserverKey struct{}
type httpDiagnosticLoggerKey struct{}

func WithHTTPDiagnosticObserver(ctx context.Context, observer func(HTTPDiagnosticObservation), loggers ...*slog.Logger) context.Context {
	if len(loggers) > 0 && loggers[0] != nil {
		ctx = context.WithValue(ctx, httpDiagnosticLoggerKey{}, loggers[0])
	}
	return context.WithValue(ctx, httpDiagnosticObserverKey{}, observer)
}

type httpObservation struct {
	mu        sync.Mutex
	value     HTTPDiagnosticObservation
	started   time.Time
	phase     string
	dns       time.Time
	tls       time.Time
	connect   map[string]time.Time
	connected bool
	observe   func(HTTPDiagnosticObservation)
}

func newHTTPObservation(ctx context.Context, exchangeID string) (*httpObservation, context.Context) {
	observer, _ := ctx.Value(httpDiagnosticObserverKey{}).(func(HTTPDiagnosticObservation))
	if observer == nil {
		return nil, ctx
	}
	o := &httpObservation{value: HTTPDiagnosticObservation{ExchangeID: exchangeID}, started: time.Now(), phase: "connect", connect: map[string]time.Time{}, observe: observer}
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.dns, o.phase = time.Now(), "dns"
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.value.DNSMS = elapsedMS(o.dns)
		},
		ConnectStart: func(network, address string) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.connect[network+address], o.phase = time.Now(), "connect"
		},
		ConnectDone: func(network, address string, err error) {
			o.mu.Lock()
			defer o.mu.Unlock()
			if err == nil || !o.connected {
				o.value.ConnectMS = elapsedMS(o.connect[network+address])
			}
			o.connected = o.connected || err == nil
		},
		TLSHandshakeStart: func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.tls, o.phase = time.Now(), "tls"
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.value.TLSMS = elapsedMS(o.tls)
		},
		GotConn: func(info httptrace.GotConnInfo) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.value.ConnectionReused, o.phase = &info.Reused, "request"
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.phase = "response_headers"
			if info.Err != nil {
				o.phase = "request"
			}
		},
		GotFirstResponseByte: func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.value.FirstByteMS = elapsedMS(o.started)
		},
	}
	return o, httptrace.WithClientTrace(ctx, trace)
}

func elapsedMS(started time.Time) *int64 {
	if started.IsZero() {
		return nil
	}
	value := time.Since(started).Milliseconds()
	return &value
}

func (o *httpObservation) finish(ctx context.Context, protector DiagnosticProtector, status int, cause error, requestState, responseState, captureReason string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	value := o.value
	value.DurationMS = time.Since(o.started).Milliseconds()
	value.StatusCode = status
	value.RequestCaptureState, value.ResponseCaptureState, value.CaptureReason = requestState, responseState, captureReason
	if cause != nil {
		value.FailurePhase = o.phase
		if status != 0 {
			value.FailurePhase = "response_body"
		}
		protected, reason := ProtectDiagnosticText(ctx, protector, []byte(cause.Error()), MaxOperationMetadataBytes)
		if reason == CredentialProtectionAvailable {
			value.Error = string(protected)
		} else {
			value.Error, value.CaptureReason = "Diagnostic error unavailable", "credential_protection_unavailable"
		}
	}
	o.mu.Unlock()
	o.observe(value)
}
