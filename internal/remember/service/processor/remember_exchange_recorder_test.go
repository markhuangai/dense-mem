package processor

import (
	"context"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestRememberExchangeRecorderMarksOversizedResponseUnavailable(t *testing.T) {
	recorder := &rememberExchangeRecorder{protector: observability.NewCredentialProtector()}
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		Component: "assessor", RequestBody: []byte("request"), ResponseBody: []byte(strings.Repeat("x", rememberDiagnosticMaxBodyBytes+1)), Outcome: "captured",
	})
	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 1)
	require.Equal(t, "captured", exchanges[0].Outcome)
	require.Equal(t, "unavailable", exchanges[0].CaptureState)
	require.Equal(t, "credential_protection_2", exchanges[0].CaptureReason)
	require.Equal(t, "request", string(exchanges[0].RequestBody))
	require.Empty(t, exchanges[0].ResponseBody)
}

func TestRememberExchangeRecorderDerivesOutcomeCaptureStateBeforeExplicitCaptured(t *testing.T) {
	for _, test := range []struct {
		outcome string
		want    string
	}{
		{outcome: "no_response", want: "no_response"},
		{outcome: "response_read_failed", want: "interrupted"},
	} {
		t.Run(test.outcome, func(t *testing.T) {
			recorder := &rememberExchangeRecorder{protector: observability.NewCredentialProtector()}
			recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
				Component: "assessor", RequestBody: []byte(`{"request":true}`), CaptureState: "captured", Outcome: test.outcome,
			})

			exchanges := recorder.Snapshot()
			require.Len(t, exchanges, 1)
			require.Equal(t, test.want, exchanges[0].CaptureState)
		})
	}
}

func TestRememberDiagnosticCaptureProtectsCredentialsAcrossBodyLimit(t *testing.T) {
	secret := "credential-boundary-probe-with-three-layers-12345678"
	encoded := secret
	for range 3 {
		var escaped strings.Builder
		for _, char := range encoded {
			fmt.Fprintf(&escaped, "\\U%08X", char)
		}
		encoded = url.QueryEscape(escaped.String())
	}
	for _, test := range []struct {
		name, secret, encoded string
	}{
		{name: "plain", secret: strings.Repeat("s", 64), encoded: strings.Repeat("s", 64)},
		{name: "mixed encoding", secret: secret, encoded: encoded},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := test.encoded[:min(len(test.encoded)/2, 128)]
			body := []byte(strings.Repeat("x", rememberDiagnosticMaxBodyBytes+len(test.encoded)))
			copy(body[rememberDiagnosticMaxBodyBytes-len(prefix):], test.encoded)

			capture := captureRememberDiagnosticBody(body, observability.NewCredentialProtector(), test.secret)

			require.Equal(t, "unavailable", capture.state)
			require.Equal(t, "credential_protection_2", capture.reason)
			require.Empty(t, capture.body)

			withinBudget := captureRememberDiagnosticBody([]byte(test.encoded), observability.NewCredentialProtector(), test.secret)
			require.Equal(t, "captured", withinBudget.state)
			require.Equal(t, observability.CredentialProtectionRedacted, string(withinBudget.body))
		})
	}
}

func TestRememberDiagnosticCaptureAllowsJSONExpansionInProtectionBudget(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"message":"<>&"}`),
		[]byte(`<html><body>provider failure & retry</body></html>`),
	} {
		capture := captureRememberDiagnosticBody(body, observability.NewCredentialProtector())
		require.Equal(t, "captured", capture.state)
		require.NotEmpty(t, capture.body)
	}
}

func TestRememberDiagnosticCaptureAllowsRedactionMarkerExpansion(t *testing.T) {
	body := []byte(strings.Repeat("x", 256))
	capture := captureRememberDiagnosticBody(body, observability.NewCredentialProtector(), "x")

	require.Equal(t, "captured", capture.state)
	require.Greater(t, len(capture.body), len(body)*6)
	require.Contains(t, string(capture.body), observability.CredentialProtectionRedacted)
}

func TestRememberDiagnosticCaptureBoundsRedactionAllocation(t *testing.T) {
	body := []byte(strings.Repeat("x", 7<<20))
	protector := observability.NewCredentialProtector("x")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	capture := captureRememberDiagnosticBody(body, protector)

	runtime.ReadMemStats(&after)
	require.Equal(t, "unavailable", capture.state)
	require.Equal(t, "credential_protection_2", capture.reason)
	require.Empty(t, capture.body)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(256<<20))
}

func TestRememberExchangeRecorderFailsClosedWithoutProtector(t *testing.T) {
	recorder := &rememberExchangeRecorder{}
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		Component: "assessor", ResponseBody: []byte(`{"message":"admitted"}`), Outcome: "captured",
	})

	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 1)
	require.Equal(t, "unavailable", exchanges[0].CaptureState)
	require.Equal(t, "credential_protection_5", exchanges[0].CaptureReason)
	require.Empty(t, exchanges[0].ResponseBody)
}

func TestBoundRememberDiagnosticItemsStopsAfterAggregateBudget(t *testing.T) {
	body := []byte(strings.Repeat("x", rememberDiagnosticMaxBodyBytes))
	items := make([]knowledgecontract.RememberAttemptDiagnosticInput, 5)
	for index := range items {
		items[index] = knowledgecontract.RememberAttemptDiagnosticInput{
			RequestBody:  body,
			CaptureState: "captured",
		}
	}

	boundRememberDiagnosticItems(items)
	require.Len(t, items[0].RequestBody, rememberDiagnosticMaxBodyBytes)
	require.Empty(t, items[4].RequestBody)
	require.Equal(t, "truncated", items[4].CaptureState)
}

func TestRememberExchangeSnapshotPreservesUnavailableStateAfterAggregateBudget(t *testing.T) {
	recorder := &rememberExchangeRecorder{}
	body := []byte(strings.Repeat("x", rememberDiagnosticMaxBodyBytes))
	for range 4 {
		recorder.exchanges = append(recorder.exchanges, modelprovider.ProviderExchange{
			Component: "assessor", ResponseBody: body, Outcome: "captured", CaptureState: "captured",
		})
	}
	recorder.exchanges = append(recorder.exchanges, modelprovider.ProviderExchange{
		Component: "assessor", Outcome: "captured", CaptureState: "unavailable", CaptureReason: "credential_protection_2",
	})
	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 5)
	require.Equal(t, "unavailable", exchanges[4].CaptureState)
	require.Equal(t, "credential_protection_2", exchanges[4].CaptureReason)
	require.Empty(t, exchanges[4].ResponseBody)
}

func TestRememberExchangeRecorderProjectsProviderExchangeOnce(t *testing.T) {
	recorder := &rememberExchangeRecorder{protector: observability.NewCredentialProtector()}
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		Component:    "embedding",
		RequestBody:  []byte(`{"model":"embedding-model","input":["private evidence"],"dimensions":2}`),
		ResponseBody: []byte(`{"model":"embedding-model","data":[{"index":0,"embedding":[0.1,0.2]}]}`),
		Outcome:      "captured",
	})
	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 1)
	require.Contains(t, string(exchanges[0].RequestBody), "private evidence")
	require.Contains(t, string(exchanges[0].ResponseBody), "embedding")
}

func TestRememberExchangeRecorderUsesPrecomputedProviderProjection(t *testing.T) {
	recorder := &rememberExchangeRecorder{protector: observability.NewCredentialProtector()}
	rawResponse := []byte(`{"model":"embedding-model","data":[{"index":0,"embedding":[0.1,0.2]}]}`)
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		Component:              "embedding",
		ResponseBody:           rawResponse,
		ResponseBodyProjection: modelprovider.ProjectEmbeddingProviderResponse(rawResponse, []int{2}),
		Outcome:                "captured",
	})

	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 1)
	require.Contains(t, string(exchanges[0].ResponseBody), `"embedding_dimensions":2`)
	require.NotContains(t, string(exchanges[0].ResponseBody), "0.1")
}

func TestRememberExchangeSnapshotRetainsLaterMetadataAfterAggregateLimit(t *testing.T) {
	recorder := &rememberExchangeRecorder{}
	body := []byte(strings.Repeat("x", rememberDiagnosticMaxBodyBytes))
	for index := 0; index < 3; index++ {
		recorder.exchanges = append(recorder.exchanges, modelprovider.ProviderExchange{
			Component: fmt.Sprintf("provider-%d", index), Model: "test-model", RequestBody: body,
			ResponseBody: body, StatusCode: 500 + index, Outcome: "captured", CaptureState: "captured",
		})
	}
	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 3)
	require.Equal(t, "provider-2", exchanges[2].Component)
	require.Equal(t, 502, exchanges[2].StatusCode)
	require.Equal(t, "captured", exchanges[2].Outcome)
	require.Equal(t, "truncated", exchanges[2].CaptureState)
	require.Empty(t, exchanges[2].RequestBody)
	require.Empty(t, exchanges[2].ResponseBody)
}
