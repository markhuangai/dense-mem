package dream

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestDreamDiagnosticExchangeRecorderProtectsCredentialsAndMarksBodyTruncation(t *testing.T) {
	recorder := newDreamDiagnosticExchangeRecorder(observability.NewCredentialProtector("provider-secret"))
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		Component: "dream", Model: "fixture", RequestBody: []byte(`{"token":"provider-secret"}`),
		ResponseBody: []byte(`{"ok":true}`), Outcome: "completed",
	})
	payload := string(recorder.Payload())
	require.NotContains(t, payload, "provider-secret")
	require.Contains(t, payload, observability.CredentialProtectionRedacted)
	state, _ := recorder.State()
	require.Equal(t, "captured", state)

	truncated := newDreamDiagnosticExchangeRecorder(observability.NewCredentialProtector())
	truncated.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		Component: "dream", Model: "fixture", RequestBody: []byte(strings.Repeat("x", dreamDiagnosticProviderBodyLimit+1)), Outcome: "completed",
	})
	state, reason := truncated.State()
	require.Equal(t, "truncated", state)
	require.Equal(t, "provider_body_budget_exceeded", reason)
}

func TestDreamDiagnosticExchangeRecorderHandlesUnavailableAndRunBudgets(t *testing.T) {
	var nilRecorder *dreamDiagnosticExchangeRecorder
	nilRecorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{})
	require.Equal(t, []byte(nil), nilRecorder.Payload())
	state, reason := nilRecorder.State()
	require.Equal(t, "not_captured", state)
	require.Equal(t, "provider_not_called", reason)
	var nilContext context.Context
	require.Nil(t, dreamDiagnosticRecorderFromContext(nilContext))
	recorder := newDreamDiagnosticExchangeRecorder(nil)
	recorder.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		RequestBody: []byte(`{"token":"secret"}`), ResponseBody: []byte(`{"ok":true}`),
	})
	state, reason = recorder.State()
	require.Equal(t, "unavailable", state)
	require.Equal(t, "credential_protection_unavailable", reason)
	require.NotContains(t, string(recorder.Payload()), "secret")

	projected := newDreamDiagnosticExchangeRecorder(observability.NewCredentialProtector())
	projected.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{
		ResponseBody: []byte(`{"private":"raw"}`), ResponseBodyProjection: []byte(`{"status":"safe"}`),
	})
	payload := string(projected.Payload())
	require.Contains(t, payload, "safe")
	require.NotContains(t, payload, "raw")

	tooLarge := newDreamDiagnosticExchangeRecorder(observability.NewCredentialProtector())
	tooLarge.bytes = dreamDiagnosticRunPayloadLimit
	tooLarge.RecordProviderExchange(context.Background(), modelprovider.ProviderExchange{ResponseBody: []byte(`{"ok":true}`)})
	require.Empty(t, tooLarge.items)
	state, reason = tooLarge.State()
	require.Equal(t, "not_captured", state)
	require.Equal(t, "provider_not_called", reason)

	empty := newDreamDiagnosticExchangeRecorder(observability.NewCredentialProtector())
	require.Equal(t, []byte(`{}`), empty.Payload())
	require.Equal(t, "not_captured", func() string { state, _ := empty.State(); return state }())
}
