package decisionprovider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestEvidenceOnlyUnitDecisionIncompleteHTTPBodyRemainsAVisibleFailure(t *testing.T) {
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		connection, writer, err := w.(http.Hijacker).Hijack()
		require.NoError(t, err)
		defer connection.Close()
		_, err = fmt.Fprint(writer, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\nContent-Type: application/json\r\n\r\n{}")
		require.NoError(t, err)
		require.NoError(t, writer.Flush())
	}))
	defer server.Close()
	result, err := fixtureProvider(t, "openai", server.URL, &logs).Decide(context.Background(), fixtureRequest())
	var provider *modelprovider.ProviderError
	require.ErrorAs(t, err, &provider)
	require.Equal(t, "transport", provider.FailureClass)
	require.Empty(t, result.Answers)
	require.Contains(t, logs.String(), "response_body")
	require.Contains(t, logs.String(), "decision.exchange.completed")
	require.NotContains(t, logs.String(), "private-decision-fixture-key")
}

func TestEvidenceOnlyUnitDecisionFailuresPreserveCompleteExchanges(t *testing.T) {
	for _, test := range []struct {
		name                  string
		status                int
		retryAfter, requestID string
		reportedOverflow      bool
	}{
		{name: "client rejection", status: 400},
		{name: "negative retry after", status: 429, retryAfter: "-5"},
		{name: "bounded retry after", status: 429, retryAfter: "301"},
		{name: "invalid request identity", status: 200, requestID: "unsafe request identity"},
		{name: "oversized request identity", status: 200, requestID: strings.Repeat("x", 129)},
		{name: "reported input overflow", status: 200, reportedOverflow: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Retry-After", test.retryAfter)
				w.Header().Set("x-request-id", test.requestID)
				w.WriteHeader(test.status)
				body := openAIAnswers
				if test.reportedOverflow {
					body = strings.Replace(body, `"input_tokens":100`, `"input_tokens":40000`, 1)
					body = strings.Replace(body, `"total_tokens":100`, `"total_tokens":40000`, 1)
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			p := fixtureProvider(t, "openai", server.URL, &logs)
			result, err := p.Decide(context.Background(), fixtureRequest())
			require.Equal(t, 1, calls)
			require.NotContains(t, logs.String(), p.config.APIKey)
			require.Contains(t, logs.String(), "decision.exchange.completed")
			if test.status != 200 || test.reportedOverflow {
				require.Error(t, err)
				require.Empty(t, result.Answers)
				if test.status == 429 {
					var rate *modelprovider.RateLimitError
					require.ErrorAs(t, err, &rate)
					want := 0
					if test.retryAfter == "301" {
						want = 300
					}
					require.Equal(t, want, rate.RetryAfter)
				}
				if test.reportedOverflow {
					require.Contains(t, logs.String(), "reported_context_limit")
				}
			} else {
				require.NoError(t, err)
				require.Equal(t, "unavailable", result.Receipts[0].ProviderRequestID)
			}
		})
	}
}

func TestEvidenceOnlyUnitDecisionAdmissionAndRedirectNeverLeakCredentials(t *testing.T) {
	var logs bytes.Buffer
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls++ }))
	defer target.Close()
	sourceCalls := 0
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls++
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	p := fixtureProvider(t, "openai", source.URL, &logs)
	_, err := p.Decide(context.Background(), fixtureRequest())
	require.Error(t, err)
	require.Equal(t, 1, sourceCalls)
	require.Zero(t, targetCalls)
	admissionErr := errors.New("admission denied")
	ctx := modelprovider.WithAdmissionCallback(context.Background(), func(context.Context) error { return admissionErr })
	_, err = p.Decide(ctx, fixtureRequest())
	require.ErrorIs(t, err, admissionErr)
	require.Equal(t, 1, sourceCalls)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = modelprovider.WithAdmissionCallback(ctx, func(context.Context) error { cancel(); return nil })
	_, err = p.Decide(ctx, fixtureRequest())
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, sourceCalls)
	require.NotContains(t, logs.String(), p.config.APIKey)
}

func TestEvidenceOnlyUnitDecisionConstructorRejectsUnusableDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	p := fixtureProvider(t, "openai", "http://fixture.invalid", &logs)
	_, err := New(config.DecisionProviderConfig{}, "cl100k_base", nil, p.logger, p.protector, nil)
	require.Error(t, err)
	_, err = New(p.config, "invalid-tokenizer", nil, p.logger, p.protector, nil)
	require.ErrorContains(t, err, "decision tokenizer")
	for _, missing := range []string{"logger", "protector"} {
		var logger observability.LogProvider = p.logger
		var protector observability.DiagnosticProtector = p.protector
		if missing == "logger" {
			logger = nil
		} else {
			protector = nil
		}
		_, err = New(p.config, "cl100k_base", nil, logger, protector, nil)
		require.ErrorContains(t, err, "logging and credential protection")
	}
}
