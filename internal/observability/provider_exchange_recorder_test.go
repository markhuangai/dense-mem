package observability

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

func TestStructuredCaptureSnapshotBoundsRetainedExchanges(t *testing.T) {
	recorder := NewProviderExchangeRecorder(NewCredentialProtector())
	body := bytes.Repeat([]byte{'a'}, modelprovider.MaxProviderDiagnosticBodyBytes)
	for range MaxProviderDiagnosticSnapshotBytes / len(body) {
		recorder.RecordProviderExchange(t.Context(), modelprovider.ProviderExchange{RequestBody: body, Outcome: "captured"})
	}
	ctx := WithAuthenticationSecrets(t.Context(), "actor-canary")
	recorder.RecordProviderExchange(ctx, modelprovider.ProviderExchange{RequestBody: []byte("last admitted body with actor-canary"), Outcome: "captured"})
	exchanges := recorder.Snapshot()
	var retained int
	for _, exchange := range exchanges {
		retained += len(exchange.RequestBody) + len(exchange.ResponseBody)
		require.False(t, strings.Contains(string(exchange.RequestBody), "actor-canary"))
	}
	require.Equal(t, MaxProviderDiagnosticSnapshotBytes, retained)
	require.Equal(t, "truncated", exchanges[len(exchanges)-1].CaptureState)
	require.Empty(t, exchanges[len(exchanges)-1].RequestBody)
}

func TestStructuredExchangeRecorderMarksOversizedResponseUnavailable(t *testing.T) {
	recorder := NewProviderExchangeRecorder(NewCredentialProtector())
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		Component: "assessor", RequestBody: []byte("request"), ResponseBody: []byte(strings.Repeat("x", modelprovider.MaxProviderDiagnosticBodyBytes+1)), Outcome: "captured",
	})
	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 1)
	require.Equal(t, "captured", exchanges[0].Outcome)
	require.Equal(t, "unavailable", exchanges[0].CaptureState)
	require.Equal(t, "credential_protection_13:body_bytes_exceeded:body_bytes=16777216", exchanges[0].CaptureReason)
	require.Equal(t, "request", string(exchanges[0].RequestBody))
	require.Empty(t, exchanges[0].ResponseBody)
}

func TestStructuredExchangeRecorderDerivesOutcomeCaptureStateBeforeExplicitCaptured(t *testing.T) {
	for _, test := range []struct {
		outcome string
		want    string
	}{
		{outcome: "no_response", want: "no_response"},
		{outcome: "response_read_failed", want: "interrupted"},
	} {
		t.Run(test.outcome, func(t *testing.T) {
			recorder := NewProviderExchangeRecorder(NewCredentialProtector())
			recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
				Component: "assessor", RequestBody: []byte(`{"request":true}`), CaptureState: "captured", Outcome: test.outcome,
			})

			exchanges := recorder.Snapshot()
			require.Len(t, exchanges, 1)
			require.Equal(t, test.want, exchanges[0].CaptureState)
		})
	}
}

func TestStructuredDiagnosticCaptureProtectsCredentialsAcrossBodyLimit(t *testing.T) {
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
			body := []byte(strings.Repeat("x", modelprovider.MaxProviderDiagnosticBodyBytes+len(test.encoded)))
			copy(body[modelprovider.MaxProviderDiagnosticBodyBytes-len(prefix):], test.encoded)

			capture := CaptureProviderDiagnosticBody(body, NewCredentialProtector(), test.secret)

			require.Equal(t, "unavailable", capture.State)
			require.Equal(t, "credential_protection_13:body_bytes_exceeded:body_bytes=16777216", capture.Reason)
			require.Empty(t, capture.Body)

			withinBudget := CaptureProviderDiagnosticBody([]byte(test.encoded), NewCredentialProtector(), test.secret)
			require.Equal(t, "captured", withinBudget.State)
			require.Equal(t, CredentialProtectionRedacted, string(withinBudget.Body))
		})
	}
}

func TestStructuredDiagnosticCaptureAllowsJSONExpansionInProtectionBudget(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"message":"<>&"}`),
		[]byte(`<html><body>provider failure & retry</body></html>`),
	} {
		capture := CaptureProviderDiagnosticBody(body, NewCredentialProtector())
		require.Equal(t, "captured", capture.State)
		require.NotEmpty(t, capture.Body)
	}
}

func TestStructuredDiagnosticCaptureAllowsRedactionMarkerExpansion(t *testing.T) {
	body := []byte(strings.Repeat("x", 256))
	capture := CaptureProviderDiagnosticBody(body, NewCredentialProtector(), "x")

	require.Equal(t, "captured", capture.State)
	require.Greater(t, len(capture.Body), len(body)*6)
	require.Contains(t, string(capture.Body), CredentialProtectionRedacted)
}

func TestStructuredDiagnosticCaptureBoundsRedactionAllocation(t *testing.T) {
	body := []byte(strings.Repeat("x", 7<<20))
	protector := NewCredentialProtector("x")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	capture := CaptureProviderDiagnosticBody(body, protector)

	runtime.ReadMemStats(&after)
	require.Equal(t, "unavailable", capture.State)
	require.Equal(t, "credential_protection_11:encoded_bytes_exceeded:encoded_bytes=16777218", capture.Reason)
	require.Empty(t, capture.Body)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(256<<20))
}

func TestStructuredExchangeRecorderFailsClosedWithoutProtector(t *testing.T) {
	recorder := NewProviderExchangeRecorder(nil)
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		Component: "assessor", ResponseBody: []byte(`{"message":"admitted"}`), Outcome: "captured",
	})

	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 1)
	require.Equal(t, "unavailable", exchanges[0].CaptureState)
	require.Equal(t, "credential_protection_5", exchanges[0].CaptureReason)
	require.Empty(t, exchanges[0].ResponseBody)
}

func TestStructuredExchangeSnapshotPreservesUnavailableStateAfterAggregateBudget(t *testing.T) {
	recorder := NewProviderExchangeRecorder(nil)
	body := []byte(strings.Repeat("x", modelprovider.MaxProviderDiagnosticBodyBytes))
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

func TestStructuredExchangeRecorderProjectsProviderExchangeOnce(t *testing.T) {
	recorder := NewProviderExchangeRecorder(NewCredentialProtector())
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

func TestStructuredExchangeRecorderPreservesCompleteProviderBody(t *testing.T) {
	recorder := NewProviderExchangeRecorder(NewCredentialProtector())
	rawResponse := []byte(`{"model":"embedding-model","data":[{"index":0,"embedding":[0.1,0.2]}]}`)
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		Component:              "embedding",
		ResponseBody:           rawResponse,
		ResponseBodyProjection: modelprovider.ProjectEmbeddingProviderResponse(rawResponse, []int{2}),
		Outcome:                "captured",
	})

	exchanges := recorder.Snapshot()
	require.Len(t, exchanges, 1)
	require.JSONEq(t, string(rawResponse), string(exchanges[0].ResponseBody))
	require.Empty(t, exchanges[0].ResponseBodyProjection)

	rawError := []byte(`{"message":"upstream deployment not ready","retry_after":3}`)
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{Component: "embedding", ResponseBody: rawError,
		ResponseBodyProjection: []byte(`{"field_count":2}`), StatusCode: 409, Outcome: "captured"})
	require.JSONEq(t, string(rawError), string(recorder.Snapshot()[1].ResponseBody))
}

func TestStructuredExchangeSnapshotRetainsLaterMetadataAfterAggregateLimit(t *testing.T) {
	recorder := NewProviderExchangeRecorder(nil)
	body := []byte(strings.Repeat("x", modelprovider.MaxProviderDiagnosticBodyBytes))
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
