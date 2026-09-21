package http

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestTransportRequestAttrsSeparatesApplicationAndDeliveryOutcomes(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	ctx, _ := domain.WithMCPToolMetrics(req.Context())
	domain.RecordMCPToolCall(ctx)
	domain.RecordMCPToolFailure(ctx)
	req = req.WithContext(ctx)
	recorder := httptest.NewRecorder()
	e := echo.New()
	echoContext := e.NewContext(req, recorder)
	echoContext.Response().WriteHeader(http.StatusOK)
	values := middleware.RequestLoggerValues{Method: http.MethodPost, RoutePath: "/mcp", Status: http.StatusOK, Latency: 12 * time.Millisecond}

	attrs := transportRequestAttrs(echoContext, values)
	got := make(map[string]any, len(attrs))
	for _, attr := range attrs {
		got[attr.Key] = attr.Value
	}
	require.Equal(t, int(1), got["mcp_tool_calls"])
	require.Equal(t, int(1), got["mcp_tool_failures"])
	require.Equal(t, "tool_error", got["application_outcome"])
	require.Equal(t, "write_observed", got["delivery_stage"])
	require.Equal(t, "unknown", got["caller_receipt"])
}

func TestTransportRequestAttrsIncludesPublishedRememberInvocationID(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	ctx := requestctx.WithRememberInvocationIDSink(req.Context())
	requestctx.SetRememberInvocationID(ctx, "invocation-1")
	req = req.WithContext(ctx)
	e := echo.New()
	echoContext := e.NewContext(req, httptest.NewRecorder())
	attrs := transportRequestAttrs(echoContext, middleware.RequestLoggerValues{Method: http.MethodPost, RoutePath: "/mcp", Status: http.StatusOK})
	got := make(map[string]any, len(attrs))
	for _, attr := range attrs {
		got[attr.Key] = attr.Value
	}
	require.Equal(t, "invocation-1", got["invocation_id"])
}

func TestRequestDeliveryStageReportsCancellationBeforeWrite(t *testing.T) {
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(requestContext)
	e := echo.New()
	ctx := e.NewContext(req, httptest.NewRecorder())
	stage := requestDeliveryStage(ctx, middleware.RequestLoggerValues{Status: http.StatusOK}, nil)
	require.Equal(t, "disconnect_observed", stage)
}

func TestTransportLogContextDetachesCancellationAndPreservesValues(t *testing.T) {
	type contextKey struct{}
	requestContext, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "preserved"))
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(requestContext)
	e := echo.New()
	ctx := e.NewContext(req, httptest.NewRecorder())

	logContext := TransportLogContext(ctx)
	require.NoError(t, logContext.Err())
	require.Equal(t, "preserved", logContext.Value(contextKey{}))
}

func TestRequestTransportStatusAndCommittedResponseStages(t *testing.T) {
	require.Equal(t, "error", requestTransportStatus(middleware.RequestLoggerValues{
		Status: http.StatusOK,
		Error:  errors.New("handler failed"),
	}))

	e := echo.New()
	committed := httptest.NewRecorder()
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), committed)
	ctx.Response().WriteHeader(http.StatusOK)
	require.Equal(t, "write_observed", requestDeliveryStage(ctx, middleware.RequestLoggerValues{}, nil))
	require.Equal(t, "write_error_observed", requestDeliveryStage(ctx, middleware.RequestLoggerValues{Error: errors.New("write failed")}, nil))
}

type partialResponseWriter struct {
	header http.Header
}

func (w *partialResponseWriter) Header() http.Header { return w.header }
func (w *partialResponseWriter) WriteHeader(int)     {}
func (w *partialResponseWriter) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	return len(value) - 1, errors.New("connection reset")
}
func (w *partialResponseWriter) FlushError() error { return errors.New("flush failed") }

type plainResponseWriter struct {
	header http.Header
	body   bytes.Buffer
}

func (w *plainResponseWriter) Header() http.Header { return w.header }
func (w *plainResponseWriter) WriteHeader(int)     {}
func (w *plainResponseWriter) Write(value []byte) (int, error) {
	return w.body.Write(value)
}

type failingReaderFromWriter struct {
	header http.Header
}

func (w *failingReaderFromWriter) Header() http.Header { return w.header }
func (w *failingReaderFromWriter) WriteHeader(int)     {}
func (w *failingReaderFromWriter) Write(value []byte) (int, error) {
	return len(value), nil
}
func (w *failingReaderFromWriter) ReadFrom(io.Reader) (int64, error) {
	return 2, errors.New("reader reset")
}

func TestDeliveryObserverCapturesPartialAndFlushFailures(t *testing.T) {
	underlying := &partialResponseWriter{header: make(http.Header)}
	wrapped, observation := NewDeliveryResponseWriter(underlying)
	if _, ok := wrapped.(interface{ FlushError() error }); !ok {
		t.Fatal("flush capability was not preserved")
	}
	if _, ok := wrapped.(http.Hijacker); ok {
		t.Fatal("unsupported hijack capability was advertised")
	}
	_, err := wrapped.Write([]byte("body"))
	require.Error(t, err)
	flushErr := wrapped.(interface{ FlushError() error }).FlushError()
	require.Error(t, flushErr)
	require.Equal(t, "flush_error_observed", observation.Stage(context.Background(), nil))
	require.Equal(t, int64(3), observation.WriteBytes())
}

func TestDeliveryObserverDoesNotBufferWrites(t *testing.T) {
	underlying := httptest.NewRecorder()
	wrapped, observation := NewDeliveryResponseWriter(underlying)
	_, err := wrapped.Write(bytes.Repeat([]byte("x"), 3))
	require.NoError(t, err)
	require.Equal(t, "xxx", underlying.Body.String())
	require.Equal(t, int64(3), observation.WriteBytes())
}

type allDeliveryCapabilitiesWriter struct {
	header   http.Header
	body     bytes.Buffer
	flushErr error
}

func (w *allDeliveryCapabilitiesWriter) Header() http.Header { return w.header }
func (w *allDeliveryCapabilitiesWriter) WriteHeader(int)     {}
func (w *allDeliveryCapabilitiesWriter) Write(value []byte) (int, error) {
	return w.body.Write(value)
}
func (w *allDeliveryCapabilitiesWriter) Flush()            {}
func (w *allDeliveryCapabilitiesWriter) FlushError() error { return w.flushErr }
func (w *allDeliveryCapabilitiesWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	left, right := net.Pipe()
	right.Close()
	return left, bufio.NewReadWriter(bufio.NewReader(left), bufio.NewWriter(left)), nil
}
func (w *allDeliveryCapabilitiesWriter) Push(string, *http.PushOptions) error { return nil }
func (w *allDeliveryCapabilitiesWriter) ReadFrom(reader io.Reader) (int64, error) {
	value, err := io.ReadAll(reader)
	if err != nil {
		return 0, err
	}
	n, err := w.body.Write(value)
	return int64(n), err
}

func TestDeliveryObserverPreservesEveryCapabilityCombination(t *testing.T) {
	for capabilities := 0; capabilities <= 15; capabilities++ {
		t.Run(strconv.Itoa(capabilities), func(t *testing.T) {
			underlying := &allDeliveryCapabilitiesWriter{header: make(http.Header)}
			observation := &DeliveryObservation{response: underlying}
			wrapped := deliveryCapabilityWriter(observation, capabilities)
			require.NotNil(t, wrapped.Header())
			wrapped.WriteHeader(http.StatusCreated)
			wrapped.WriteHeader(http.StatusAccepted)
			_, err := wrapped.Write([]byte("body"))
			require.NoError(t, err)
			require.Equal(t, "body", underlying.body.String()[:4])
			require.Same(t, underlying, wrapped.(interface{ Unwrap() http.ResponseWriter }).Unwrap())
			if flusher, ok := wrapped.(http.Flusher); ok {
				flusher.Flush()
			}
			if flusher, ok := wrapped.(interface{ FlushError() error }); ok {
				require.NoError(t, flusher.FlushError())
			}
			if hijacker, ok := wrapped.(http.Hijacker); ok {
				connection, _, err := hijacker.Hijack()
				require.NoError(t, err)
				connection.Close()
			}
			if pusher, ok := wrapped.(http.Pusher); ok {
				require.NoError(t, pusher.Push("/asset", nil))
			}
			if reader, ok := wrapped.(io.ReaderFrom); ok {
				_, err := reader.ReadFrom(strings.NewReader("read"))
				require.NoError(t, err)
			}
		})
	}
}

func TestNewDeliveryResponseWriterPreservesUnderlyingCapabilities(t *testing.T) {
	underlying := &allDeliveryCapabilitiesWriter{header: make(http.Header)}
	wrapped, observation := NewDeliveryResponseWriter(underlying)

	require.Same(t, underlying, observation.Unwrap())
	if _, ok := wrapped.(interface{ FlushError() error }); !ok {
		t.Fatal("FlushError capability was not preserved")
	}
	if _, ok := wrapped.(http.Hijacker); !ok {
		t.Fatal("Hijacker capability was not preserved")
	}
	if _, ok := wrapped.(http.Pusher); !ok {
		t.Fatal("Pusher capability was not preserved")
	}
	if _, ok := wrapped.(io.ReaderFrom); !ok {
		t.Fatal("ReaderFrom capability was not preserved")
	}
}

func TestDeliveryObserverHandlesUnsupportedCapabilitiesAndReaderErrors(t *testing.T) {
	plain := &plainResponseWriter{header: make(http.Header)}
	observation := &DeliveryObservation{response: plain}

	require.ErrorIs(t, observation.flush(), http.ErrNotSupported)
	_, _, err := observation.hijack()
	require.ErrorIs(t, err, http.ErrNotSupported)
	require.ErrorIs(t, observation.push("/asset", nil), http.ErrNotSupported)
	count, err := observation.readFrom(strings.NewReader("fallback"))
	require.NoError(t, err)
	require.Equal(t, int64(len("fallback")), count)
	require.Equal(t, "fallback", plain.body.String())
	require.Same(t, plain, observation.Unwrap())

	failing := &failingReaderFromWriter{header: make(http.Header)}
	failingObservation := &DeliveryObservation{response: failing}
	count, err = failingObservation.readFrom(strings.NewReader("ignored"))
	require.Error(t, err)
	require.Equal(t, int64(2), count)
	require.Equal(t, int64(2), failingObservation.WriteBytes())
	require.Equal(t, "partial_write_observed", failingObservation.Stage(context.Background(), nil))
}

func TestDeliveryObserverUsesStandardFlusherAndHandlesMissingContext(t *testing.T) {
	wrapped, observation := NewDeliveryResponseWriter(httptest.NewRecorder())
	flusher, ok := wrapped.(http.Flusher)
	require.True(t, ok)
	flusher.Flush()
	require.Equal(t, http.StatusOK, observation.Status())

	require.Nil(t, deliveryObserverFromContext(nil))
	e := echo.New()
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	require.Nil(t, deliveryObserverFromContext(ctx))
}

func TestDeliveryObserverStageClassifiesTerminalOutcomes(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, deadlineCancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer deadlineCancel()
	deadlineCancel()
	cases := []struct {
		name        string
		observation DeliveryObservation
		ctx         context.Context
		handlerErr  error
		want        string
	}{
		{name: "prepared", observation: DeliveryObservation{response: httptest.NewRecorder()}, want: "prepared"},
		{name: "status", observation: DeliveryObservation{response: httptest.NewRecorder(), status: http.StatusOK}, want: "write_observed"},
		{name: "write", observation: DeliveryObservation{response: httptest.NewRecorder(), writeStarted: true}, want: "write_observed"},
		{name: "partial", observation: DeliveryObservation{response: httptest.NewRecorder(), writeErr: errors.New("reset"), writeBytes: 1}, want: "partial_write_observed"},
		{name: "write error", observation: DeliveryObservation{response: httptest.NewRecorder(), writeErr: errors.New("reset")}, want: "write_error_observed"},
		{name: "flush error", observation: DeliveryObservation{response: httptest.NewRecorder(), flushErr: errors.New("flush")}, want: "flush_error_observed"},
		{name: "context canceled", observation: DeliveryObservation{response: httptest.NewRecorder()}, ctx: cancelled, want: "disconnect_observed"},
		{name: "context deadline", observation: DeliveryObservation{response: httptest.NewRecorder()}, ctx: deadline, want: "disconnect_observed"},
		{name: "handler canceled", observation: DeliveryObservation{response: httptest.NewRecorder()}, handlerErr: context.Canceled, want: "disconnect_observed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.observation.Stage(tc.ctx, tc.handlerErr))
		})
	}
}
