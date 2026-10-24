package assessorprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

func TestStructuredProviderDiagnosticsSeparateQueueAdmissionAndHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(25 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	provider := NewOpenAIAssessorWithAssessmentLimitsAndConcurrencyGate(&config.Config{AIVerifierAPIURL: server.URL, AIVerifierAPIKey: "synthetic-key"}, server.Client(), assessor.DefaultSemanticAssessmentLimits(), gate)
	ctx := modelprovider.WithAdmissionCallback(context.Background(), func(context.Context) error {
		time.Sleep(25 * time.Millisecond)
		return nil
	})
	type completed struct {
		result modelprovider.StructuredResult
		err    error
	}
	done := make(chan completed, 1)
	go func() {
		result, err := provider.Complete(ctx, modelprovider.StructuredRequest{Model: "fixture", Schema: map[string]any{}})
		done <- completed{result, err}
	}()
	time.Sleep(25 * time.Millisecond)
	<-gate
	result := <-done
	require.NoError(t, result.err)
	d := result.result.Diagnostics
	require.NotNil(t, d)
	require.True(t, d.Valid())
	require.GreaterOrEqual(t, *d.QueueMS, int64(15))
	require.GreaterOrEqual(t, *d.AdmissionMS, int64(15))
	require.GreaterOrEqual(t, *d.HTTPMS, int64(15))
	require.Nil(t, d.ReconciliationMS)
	require.Empty(t, d.FailurePhase)
	require.Equal(t, 5, result.result.TotalTokens)
	require.Empty(t, gate)
	encoded, err := json.Marshal(d)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "synthetic-key")
}

func TestStructuredProviderDiagnosticsRetainResponseBodyTimeoutAndCancellation(t *testing.T) {
	for _, mode := range []string{"client_deadline", "cancellation", "protocol"} {
		t.Run(mode, func(t *testing.T) {
			bodyRead := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "1000")
				_, _ = w.Write([]byte(`{"choices":`))
				w.(http.Flusher).Flush()
				if mode != "protocol" {
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			client := server.Client()
			client.Timeout = 2 * time.Second
			if mode == "cancellation" {
				transport := client.Transport
				client.Transport = assessorRoundTripper(func(request *http.Request) (*http.Response, error) {
					response, err := transport.RoundTrip(request)
					if err == nil {
						response.Body = &diagnosticBodyReadSignal{ReadCloser: response.Body, started: bodyRead}
					}
					return response, err
				})
			}
			provider := NewOpenAIAssessor(&config.Config{AIVerifierAPIURL: server.URL}, client)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancellation" {
				go func() {
					select {
					case <-bodyRead:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			result, err := provider.Complete(ctx, modelprovider.StructuredRequest{Model: "fixture", Schema: map[string]any{}})
			require.Error(t, err)
			if mode == "protocol" {
				require.Equal(t, modelprovider.ProviderFailureClassProtocol, modelprovider.ProviderFailureDetails(err).Class)
			} else {
				require.ErrorIs(t, err, modelprovider.ErrVerifierTimeout)
			}
			require.True(t, result.Diagnostics.Valid())
			require.Equal(t, "response_body", result.Diagnostics.FailurePhase)
			require.NotNil(t, result.Diagnostics.HTTPMS)
			require.EqualValues(t, 2000, *result.Diagnostics.HTTPTimeoutMS)
			require.Zero(t, result.TotalTokens)
			require.Empty(t, result.Content)
			require.Empty(t, provider.sem)
		})
	}
}

func TestStructuredProviderDiagnosticsDistinguishUndispatchedFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	for _, phase := range []string{"queue", "admission", "prepare"} {
		t.Run(phase, func(t *testing.T) {
			provider := NewOpenAIAssessor(&config.Config{AIVerifierAPIURL: server.URL}, server.Client())
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			request := modelprovider.StructuredRequest{Model: "fixture", Schema: map[string]any{}}
			switch phase {
			case "queue":
				provider.sem = make(chan struct{}, 1)
				provider.sem <- struct{}{}
			case "admission":
				ctx = modelprovider.WithAdmissionCallback(ctx, func(context.Context) error { return errors.New("synthetic admission failure") })
			case "prepare":
				request.Schema["invalid"] = func() {}
			}
			result, err := provider.Complete(ctx, request)
			require.Error(t, err)
			require.Equal(t, phase, result.Diagnostics.FailurePhase)
			require.True(t, result.Diagnostics.Valid())
			require.NotNil(t, result.Diagnostics.QueueMS)
			require.Nil(t, result.Diagnostics.HTTPMS)
			require.Nil(t, result.Diagnostics.ReconciliationMS)
			if phase == "admission" {
				require.NotNil(t, result.Diagnostics.AdmissionMS)
			} else {
				require.Nil(t, result.Diagnostics.AdmissionMS)
			}
			if phase == "queue" {
				<-provider.sem
			}
		})
	}
	require.Zero(t, calls.Load())
}

type diagnosticBodyReadSignal struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (r *diagnosticBodyReadSignal) Read(body []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.ReadCloser.Read(body)
}
