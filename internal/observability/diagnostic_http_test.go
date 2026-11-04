package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

func TestDiagnosticHTTPClientPreservesCompleteExchanges(t *testing.T) {
	const secret = "known-operational-credential"
	requestBody := `{"secret":"admitted user fact","messages":"` + strings.Repeat("α", 5000) + secret + `"}`
	responseBody := ` {"error":"unknown deployment ` + secret + `"} {malformed second object}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, requestBody, string(body))
		require.Equal(t, "Bearer "+secret, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, responseBody)
	}))
	defer server.Close()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := DiagnosticHTTPClient(server.Client(), logger, NewCredentialProtector(secret))
	ctx := WithExchangeScope(context.Background(), ExchangeScope{TeamID: "team", RunID: "run", BatchID: "batch"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(requestBody))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+secret)
	response, err := client.Do(req)
	require.NoError(t, err)
	got, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusConflict, response.StatusCode)
	require.Equal(t, responseBody, string(got))
	require.NotContains(t, output.String(), secret)
	require.NotContains(t, output.String(), "Authorization")
	bodies := map[string]string{}
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var record struct {
			Direction string              `json:"direction"`
			TeamID    string              `json:"team_id"`
			Chunk     DiagnosticBodyChunk `json:"payload_chunk"`
		}
		require.NoError(t, json.Unmarshal(line, &record))
		require.Equal(t, "team", record.TeamID)
		require.Equal(t, "captured", record.Chunk.State)
		bodies[record.Direction] += record.Chunk.Body
	}
	require.Equal(t, strings.ReplaceAll(requestBody, secret, CredentialProtectionRedacted), bodies["request"])
	require.Equal(t, strings.ReplaceAll(responseBody, secret, CredentialProtectionRedacted), bodies["response"])
}

func TestDiagnosticHTTPClientPreservesTransportFailuresAndDebugFiltering(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo} {
		var output bytes.Buffer
		failure := errors.New("upstream unavailable")
		transport := diagnosticTestTransport(func(*http.Request) (*http.Response, error) { return nil, failure })
		client := DiagnosticHTTPClient(&http.Client{Transport: transport}, slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level})), NewCredentialProtector())
		req, err := http.NewRequest(http.MethodPost, "http://provider.invalid", strings.NewReader(`{"request":"kept"}`))
		require.NoError(t, err)
		_, err = client.Do(req)
		require.ErrorIs(t, err, failure)
		if level == slog.LevelDebug {
			require.Contains(t, output.String(), "no_response")
		} else {
			require.Empty(t, output.String())
		}
	}
}

func TestDiagnosticHTTPClientMarksInterruptedAndOversizedBodies(t *testing.T) {
	for _, mode := range []string{"interrupted", "oversized", "invalid_utf8"} {
		var output bytes.Buffer
		body := "partial"
		if mode == "oversized" {
			body = strings.Repeat("x", modelprovider.MaxProviderDiagnosticBodyBytes+1)
		}
		if mode == "invalid_utf8" {
			body = string([]byte{0xff})
		}
		transport := diagnosticTestTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
		})
		client := DiagnosticHTTPClient(&http.Client{Transport: transport}, slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})), NewCredentialProtector())
		req, err := http.NewRequest(http.MethodPost, "http://provider.invalid", nil)
		require.NoError(t, err)
		response, err := client.Do(req)
		require.NoError(t, err)
		if mode != "interrupted" {
			var received []byte
			received, err = io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, body, string(received))
		} else {
			_, err = response.Body.Read(make([]byte, 3))
			require.NoError(t, err)
		}
		require.NoError(t, response.Body.Close())
		if mode != "interrupted" {
			require.Contains(t, output.String(), `"state":"unavailable"`)
			if mode == "invalid_utf8" {
				require.Contains(t, output.String(), `"reason":6`)
			}
		} else {
			require.Contains(t, output.String(), `"transport_capture_state":"interrupted"`)
		}
	}
}

func TestDiagnosticResponseReadAndCloseCanRunConcurrently(t *testing.T) {
	for range 32 {
		reader, writer := io.Pipe()
		var captures atomic.Int32
		body := &diagnosticResponseBody{ReadCloser: reader, expected: -1, done: func([]byte, string) { captures.Add(1) }}
		var workers sync.WaitGroup
		workers.Add(2)
		go func() { defer workers.Done(); _, _ = io.Copy(io.Discard, body) }()
		go func() {
			defer workers.Done()
			_, _ = writer.Write([]byte("observed provider bytes"))
			_ = writer.Close()
		}()
		_ = body.Close()
		workers.Wait()
		require.EqualValues(t, 1, captures.Load())
	}
}

func TestDiagnosticHTTPClientReadsResponseBeforeBlockedPayloadLogging(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"result":"complete"}`)
	}))
	defer server.Close()
	output := &blockedDiagnosticWriter{started: make(chan struct{}), release: make(chan struct{})}
	base := server.Client()
	base.Timeout = 2 * time.Second
	client := DiagnosticHTTPClient(base, slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelDebug})), NewCredentialProtector())
	type readResult struct {
		body string
		err  error
	}
	read := make(chan readResult, 1)
	closed := make(chan error, 1)
	go func() {
		response, err := client.Post(server.URL, "application/json", strings.NewReader(`{"request":"kept"}`))
		if err != nil {
			read <- readResult{err: err}
			closed <- err
			return
		}
		body, err := io.ReadAll(response.Body)
		read <- readResult{body: string(body), err: err}
		closed <- response.Body.Close()
	}()
	defer func() {
		close(output.release)
		select {
		case err := <-closed:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("provider response did not close after logging was released")
		}
	}()
	select {
	case <-output.started:
	case <-time.After(5 * time.Second):
		t.Fatal("diagnostic writer was not reached")
	}
	select {
	case result := <-read:
		require.NoError(t, result.err)
		require.Equal(t, `{"result":"complete"}`, result.body)
	default:
		t.Fatal("payload logging blocked the provider request or response read")
	}
}

func TestDiagnosticHTTPClientDoesNotMarkReadErrorsCompleteOnClose(t *testing.T) {
	var output bytes.Buffer
	transport := diagnosticTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF)), Header: http.Header{}, ContentLength: 0}, nil
	})
	client := DiagnosticHTTPClient(&http.Client{Transport: transport}, slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})), NewCredentialProtector())
	response, err := client.Get("http://provider.invalid")
	require.NoError(t, err)
	_, err = io.ReadAll(response.Body)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.NoError(t, response.Body.Close())
	require.Contains(t, output.String(), `"transport_capture_state":"interrupted"`)
}

type blockedDiagnosticWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockedDiagnosticWriter) Write(body []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(body), nil
}

type diagnosticTestTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
