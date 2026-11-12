package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/sse"
	"github.com/markhuangai/dense-mem/internal/storage/inmem"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
)

type progressFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Token    json.RawMessage `json:"progressToken"`
		Progress int             `json:"progress"`
		Message  string          `json:"message"`
		Total    *int            `json:"total"`
	} `json:"params"`
	Result json.RawMessage `json:"result"`
}

var progressStages = []rememberapp.ProgressStage{rememberapp.ProgressPreparingIntake, rememberapp.ProgressCheckingDuplicates,
	rememberapp.ProgressAssessingEvidence, rememberapp.ProgressPreparingSearch, rememberapp.ProgressCommitting}

func TestMCPHandlerRememberProgressTokensAndTerminalCompatibility(t *testing.T) {
	server := newProgressHTTPServer(t, nil, func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
		for _, stage := range progressStages {
			rememberapp.ReportProgress(ctx, stage)
			rememberapp.ReportProgress(ctx, stage)
		}
		rememberapp.ReportProgress(ctx, "unrecognized")
		return map[string]any{"completed": true}, nil
	})
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, token := range []string{"", `"request-token"`, "0", "9007199254740993", `"0"`, `""`, "null", "true", "{}", "[]", "1.5"} {
			for _, mode := range []string{"text/event-stream", "application/json"} {
				t.Run(version+"/"+token+"/"+mode, func(t *testing.T) {
					response := progressHTTPCall(t, server.URL, version, mode, token, "remember", "1")
					body, err := io.ReadAll(response.Body)
					require.NoError(t, err)
					require.NoError(t, response.Body.Close())
					valid := token == `"request-token"` || token == "0" || token == "9007199254740993" || token == `"0"` || token == `""`
					if mode == "application/json" {
						require.NotContains(t, string(body), "notifications/progress")
						var frame progressFrame
						require.NoError(t, json.Unmarshal(body, &frame))
						require.NotEmpty(t, frame.Result)
						return
					}
					frames := decodeProgressFrames(t, bytes.NewReader(body))
					expected := 1
					if valid {
						expected = 6
					}
					require.Len(t, frames, expected)
					for index, frame := range frames[:len(frames)-1] {
						require.Equal(t, "notifications/progress", frame.Method)
						require.JSONEq(t, token, string(frame.Params.Token))
						require.Equal(t, index+1, frame.Params.Progress)
						require.NotEmpty(t, frame.Params.Message)
						require.Less(t, len(frame.Params.Message), 64)
						require.Nil(t, frame.Params.Total)
						require.Empty(t, frame.Result)
					}
					require.JSONEq(t, "1", string(frames[len(frames)-1].ID))
					require.NotEmpty(t, frames[len(frames)-1].Result)
				})
			}
		}
	}
}

func TestMCPHandlerRememberProgressArrivesBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	server := newProgressHTTPServer(t, nil, func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
		rememberapp.ReportProgress(ctx, rememberapp.ProgressAssessingEvidence)
		<-release
		rememberapp.ReportProgress(ctx, rememberapp.ProgressCommitting)
		return map[string]any{"completed": true}, nil
	})
	response := progressHTTPCall(t, server.URL, "2026-07-28", "text/event-stream", "0", "remember", "7")
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "event: message\n", line)
	line, err = reader.ReadString('\n')
	require.NoError(t, err)
	require.Contains(t, line, "notifications/progress")
	require.Contains(t, line, "Assessing Evidence")
	require.NotContains(t, line, "completed")
	unblock()
	_, err = io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
}

type progressWriteDeadline struct {
	deadline  time.Time
	appliedAt time.Time
}

type progressDeadlineWriter struct {
	http.ResponseWriter
	deadlines chan<- progressWriteDeadline
}

func (w progressDeadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w progressDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines <- progressWriteDeadline{deadline: deadline, appliedAt: time.Now()}
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
}

func TestMCPHandlerRememberProgressCapsAndRestoresWriteDeadline(t *testing.T) {
	for _, earlier := range []time.Duration{0, 75 * time.Millisecond} {
		t.Run(earlier.String(), func(t *testing.T) {
			deadlines := make(chan progressWriteDeadline, 4)
			parentDeadlines := make(chan time.Time, 1)
			server := newProgressHTTPServer(t, nil, func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
				rememberapp.ReportProgress(ctx, rememberapp.ProgressAssessingEvidence)
				if earlier == 0 {
					select {
					case <-time.After(150 * time.Millisecond):
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return map[string]any{"completed": true}, nil
			}, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var deadline time.Time
					if earlier != 0 {
						deadline = time.Now().Add(earlier)
						ctx, cancel := context.WithDeadline(r.Context(), deadline)
						defer cancel()
						r = r.WithContext(ctx)
					}
					parentDeadlines <- deadline
					next.ServeHTTP(progressDeadlineWriter{ResponseWriter: w, deadlines: deadlines}, r)
				})
			})
			response := progressHTTPCall(t, server.URL, "2026-07-28", "text/event-stream", "0", "remember", "1")
			frames := decodeProgressFrames(t, response.Body)
			require.NoError(t, response.Body.Close())
			require.Len(t, frames, 2)
			require.Contains(t, string(frames[1].Result), `"completed":true`)
			require.Len(t, deadlines, 2, "progress must apply and restore an HTTP write deadline")
			first, restored := <-deadlines, <-deadlines
			require.True(t, restored.deadline.IsZero())
			parentDeadline := <-parentDeadlines
			if earlier != 0 {
				require.Equal(t, parentDeadline, first.deadline)
			} else {
				require.Positive(t, first.deadline.Sub(first.appliedAt))
				require.LessOrEqual(t, first.deadline.Sub(first.appliedAt), 100*time.Millisecond)
				require.True(t, time.Now().After(first.deadline), "terminal delivery must succeed after the optional deadline")
			}
		})
	}
}

func TestMCPHandlerRememberProgressIsRequestLocalAndOtherToolsRemainTerminalOnly(t *testing.T) {
	server := newProgressHTTPServer(t, nil, func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
		count := int(input["count"].(float64))
		for _, stage := range progressStages[:count] {
			rememberapp.ReportProgress(ctx, stage)
		}
		return map[string]any{"count": count}, nil
	})
	var workers sync.WaitGroup
	for actor := 1; actor <= 3; actor++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			response := progressHTTPCall(t, server.URL, "2026-07-28", "text/event-stream", `"same-token"`, "remember", fmt.Sprint(actor))
			defer response.Body.Close()
			frames := decodeProgressFrames(t, response.Body)
			require.Len(t, frames, actor+1)
			require.JSONEq(t, fmt.Sprint(actor), string(frames[len(frames)-1].ID))
		}()
	}
	workers.Wait()
	response := progressHTTPCall(t, server.URL, "2025-11-25", "text/event-stream", "0", "ingest_session", "1")
	defer response.Body.Close()
	require.Len(t, decodeProgressFrames(t, response.Body), 1)
}

type progressLogBuffer struct {
	mu   sync.Mutex
	body bytes.Buffer
}

func (b *progressLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.body.Write(p)
}

func (b *progressLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.body.String()
}

func TestMCPHandlerRememberProgressUsesProtectedLeveledLogging(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo} {
		var logs progressLogBuffer
		logger := observability.NewWithHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: level}))
		server := newProgressHTTPServer(t, logger, func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
			rememberapp.ReportProgress(ctx, rememberapp.ProgressAssessingEvidence)
			return nil, registry.NewToolResultError(map[string]any{"code": "provider_unavailable"})
		})
		response := progressHTTPCall(t, server.URL, "2025-11-25", "text/event-stream", `"admitted password token secret actual-key"`, "remember", "1")
		_, err := io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		server.Close()
		body := logs.String()
		require.NotContains(t, body, "actual-key")
		require.Contains(t, body, "mcp_tool_outcome")
		require.Contains(t, body, "tool_error")
		if level == slog.LevelDebug {
			require.Contains(t, body, "mcp_progress_delivered")
			require.Contains(t, body, "admitted password token secret [REDACTED]")
			require.Contains(t, body, "write_budget_ms")
		} else {
			require.NotContains(t, body, "mcp_progress_")
		}
	}
}

func TestMCPHandlerRememberProgressStalledReaderIsBounded(t *testing.T) {
	done := make(chan struct{})
	server := newProgressHTTPServer(t, nil, func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
		for _, stage := range progressStages {
			rememberapp.ReportProgress(ctx, stage)
		}
		close(done)
		return map[string]any{"completed": true}, nil
	})
	server.Close()
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	listener := &progressPipeListener{connection: serverConn, closed: make(chan struct{})}
	httpServer := &http.Server{Handler: server.Config.Handler}
	defer httpServer.Close()
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = httpServer.Serve(listener) }()
	request := progressRequest(t, "http://pipe/mcp", "2026-07-28", "text/event-stream", "0", "remember", "1")
	require.NoError(t, request.Write(clientConn))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("an unread progress response blocked required work")
	}
	require.NoError(t, clientConn.Close())
	require.NoError(t, httpServer.Close())
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("HTTP serving leaked after the stalled reader disconnected")
	}
	var releases []func()
	for range 3 {
		release, err := server.limiter.Acquire(t.Context(), server.teamID.String())
		require.NoError(t, err, "stalled progress must release its stream slot")
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
	}
}

type progressPipeListener struct {
	connection net.Conn
	closed     chan struct{}
	once       sync.Once
}

func (l *progressPipeListener) Accept() (net.Conn, error) {
	if l.connection != nil {
		conn := l.connection
		l.connection = nil
		return conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *progressPipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *progressPipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

type progressHTTPServer struct {
	*httptest.Server
	limiter *inmem.InMemoryConcurrencyLimiter
	teamID  uuid.UUID
}

func newProgressHTTPServer(t *testing.T, logger *observability.Logger, invoke registry.ToolInvoker, wrappers ...func(http.Handler) http.Handler) *progressHTTPServer {
	t.Helper()
	reg := registry.New()
	for _, name := range []string{"remember", "ingest_session"} {
		require.NoError(t, reg.Register(registry.Tool{Name: name, InputSchema: map[string]any{"type": "object"}, Invoke: invoke,
			Available: func(context.Context) bool { return true }}))
	}
	limiter := inmem.NewInMemoryConcurrencyLimiter(3, time.Hour)
	lifecycle := sse.NewStreamLifecycle(limiter)
	h := NewMCPHandlerWithLifecycle(reg, nil, lifecycle)
	if logger != nil {
		h = NewMCPHandlerWithLifecycle(reg, NewMCPLogger(progressContextLogger{mcpTestLogger{delegate: logger}}), lifecycle)
	}
	e := echo.New()
	e.POST("/mcp", h.HandlePost)
	teamID, ownerID := uuid.New(), uuid.New()
	var root http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := mcpTestContext(r.Context(), teamID, []string{"read", "write"})
		ctx = requestctx.WithActor(ctx, requestctx.Actor{TeamID: teamID, OwnerID: ownerID, Grants: []string{"read", "write"},
			AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.New(), Kind: domain.MemorySpaceTeamShared, Generation: 1}}})
		ctx = correlation.WithID(ctx, "progress-"+r.Header.Get("X-Test-Actor"))
		ctx = observability.WithAuthenticationSecrets(ctx, "actual-key")
		ctx = middleware.SetResolvedTeamIDForTest(ctx, teamID)
		e.ServeHTTP(w, r.WithContext(ctx))
	})
	for _, wrap := range wrappers {
		root = wrap(root)
	}
	server := httptest.NewServer(root)
	t.Cleanup(server.Close)
	return &progressHTTPServer{Server: server, limiter: limiter, teamID: teamID}
}

type progressContextLogger struct{ mcpTestLogger }

func (l progressContextLogger) DebugContext(ctx context.Context, msg string, attrs ...httpcontract.LogAttr) {
	l.delegate.(observability.ContextLogProvider).DebugContext(ctx, msg, mcpObservabilityAttrs(attrs)...)
}
func (l progressContextLogger) InfoContext(ctx context.Context, msg string, attrs ...httpcontract.LogAttr) {
	l.delegate.(observability.ContextLogProvider).InfoContext(ctx, msg, mcpObservabilityAttrs(attrs)...)
}
func (l progressContextLogger) ErrorContext(ctx context.Context, msg string, err error, attrs ...httpcontract.LogAttr) {
	l.delegate.(observability.ContextLogProvider).ErrorContext(ctx, msg, err, mcpObservabilityAttrs(attrs)...)
}

func TestMCPHandlerRememberProgressStopsBeforeTerminalResponse(t *testing.T) {
	var logs progressLogBuffer
	logger := observability.NewWithHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var retained context.Context
	server := newProgressHTTPServer(t, logger, func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
		retained = ctx
		rememberapp.ReportProgress(ctx, rememberapp.ProgressAssessingEvidence)
		return map[string]any{"completed": true}, nil
	})
	response := progressHTTPCall(t, server.URL, "2025-11-25", "text/event-stream", "0", "remember", "1")
	require.Len(t, decodeProgressFrames(t, response.Body), 2)
	require.NoError(t, response.Body.Close())
	server.Close()
	before := logs.String()
	rememberapp.ReportProgress(retained, rememberapp.ProgressCommitting)
	require.Equal(t, before, logs.String(), "a previously unreached stage must not be delivered after completion")
}

func TestMCPHandlerRememberProgressUnsupportedWriterKeepsRequiredFailure(t *testing.T) {
	var logs progressLogBuffer
	logger := observability.NewWithHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{Name: "remember", InputSchema: map[string]any{"type": "object"},
		Invoke: func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
			for _, stage := range progressStages {
				rememberapp.ReportProgress(ctx, stage)
			}
			return nil, registry.NewToolResultError(map[string]any{"code": "provider_unavailable"})
		}}))
	h := NewMCPHandler(reg, NewMCPLogger(progressContextLogger{mcpTestLogger{delegate: logger}}))
	request := progressRequest(t, "http://example/mcp", "2025-11-25", "text/event-stream", "0", "remember", "1")
	teamID := uuid.New()
	ctx := mcpTestContext(request.Context(), teamID, []string{"read", "write"})
	ctx = requestctx.WithActor(ctx, requestctx.Actor{TeamID: teamID, OwnerID: uuid.New(), Grants: []string{"read", "write"},
		AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.New(), Kind: domain.MemorySpaceTeamShared, Generation: 1}}})
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	require.NoError(t, h.HandlePost(echo.New().NewContext(request, response)))
	frames := decodeProgressFrames(t, response.Body)
	require.Len(t, frames, 1)
	require.Contains(t, string(frames[0].Result), "provider_unavailable")
	require.Contains(t, logs.String(), "write_deadline_unsupported")
	require.Contains(t, logs.String(), `"level":"WARN"`)
	require.Equal(t, 1, strings.Count(logs.String(), "mcp_progress_delivery_failed"))
}

func progressRequest(t *testing.T, url, version, accept, token, name, id string) *http.Request {
	t.Helper()
	meta := map[string]any{"io.modelcontextprotocol/protocolVersion": version, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
	if token != "" {
		meta["progressToken"] = json.RawMessage(token)
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": map[string]any{"count": json.RawMessage(id)}, "_meta": meta}})
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", accept)
	request.Header.Set("MCP-Protocol-Version", version)
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", name)
	request.Header.Set("X-Test-Actor", id)
	return request
}

func progressHTTPCall(t *testing.T, url, version, accept, token, name, id string) *http.Response {
	t.Helper()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(progressRequest(t, url+"/mcp", version, accept, token, name, id))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	return response
}

func decodeProgressFrames(t *testing.T, body io.Reader) []progressFrame {
	t.Helper()
	var frames []progressFrame
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
			var frame progressFrame
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame))
			frames = append(frames, frame)
		}
	}
	require.NoError(t, scanner.Err())
	return frames
}
