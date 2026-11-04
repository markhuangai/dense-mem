package operations

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestProviderTestGroupsEndpointModelsWithoutMergingCredentialConfigurations(t *testing.T) {
	bindings := []ProviderTestBinding{}
	for _, entry := range []struct{ endpoint, model, credential, role string }{
		{"http://a.test/chat/completions", "model-a", "generative", "Remember"},
		{"http://a.test/chat/completions", "model-a", "generative", "Conflict"},
		{"http://a.test/chat/completions", "model-a", "other", "Other credentials"},
		{"http://b.test/chat/completions", "model-a", "generative", "Different endpoint"},
		{"http://a.test/chat/completions", "model-b", "generative", "Different model"},
	} {
		bindings = append(bindings, ProviderTestBinding{Target: ProviderTestTarget{Endpoint: entry.endpoint, Model: entry.model, CredentialConfiguration: entry.credential, Roles: []string{entry.role}, Operation: "assess", ConfigurationRevision: "env"}})
	}
	service := NewProviderTestService(func(context.Context) ([]ProviderTestBinding, error) { return bindings, nil }, slog.New(slog.NewJSONHandler(io.Discard, nil)), observability.NewCredentialProtector(), "server-a")
	targets, err := service.List(context.Background())
	require.NoError(t, err)
	require.Len(t, targets, 4)
	require.Equal(t, []string{"Conflict", "Remember"}, targets[0].Roles)
	for _, target := range targets {
		require.Len(t, target.ID, 64)
	}
	repeated, err := service.List(context.Background())
	require.NoError(t, err)
	require.Equal(t, targets, repeated)
}

func TestProviderTestClassifiesUnknownTimingsAndActualHTTPObservations(t *testing.T) {
	for _, entry := range []struct {
		phase               string
		status              int
		failure, contextErr error
		reason, state       string
	}{
		{phase: "dns", failure: errors.New("lookup failed"), reason: "dns_failed", state: "no_http_response"},
		{phase: "connect", failure: errors.New("connection refused"), reason: "connect_failed", state: "no_http_response"},
		{phase: "tls", failure: errors.New("untrusted certificate"), reason: "tls_failed", state: "no_http_response"},
		{phase: "response_body", status: 200, failure: errors.New("unexpected EOF"), reason: "response_read_failed", state: "http_response_received"},
		{phase: "response_headers", failure: modelprovider.ErrVerifierTimeout, reason: "timeout", state: "no_http_response"},
		{phase: "response_headers", failure: context.Canceled, reason: "cancelled", state: "no_http_response"},
		{phase: "request", failure: errors.New("unspecified transport failure"), reason: "transport_failed", state: "no_http_response"},
	} {
		t.Run(entry.reason, func(t *testing.T) {
			result := ProviderTestResult{Exchanges: []observability.HTTPDiagnosticObservation{{StatusCode: entry.status, FailurePhase: entry.phase, RequestCaptureState: "captured", ResponseCaptureState: "captured"}}}
			classifyProviderTest(&result, entry.contextErr, entry.failure)
			require.Equal(t, entry.reason, result.Reason)
			require.Equal(t, entry.state, result.NetworkState)
			require.Nil(t, result.Exchanges[0].DNSMS)
		})
	}
}

func TestProviderTestReportsUnavailableProtectedMetadataAndLoggingLevel(t *testing.T) {
	const secret = "provider-test-configured-credential"
	target := ProviderTestTarget{Endpoint: "https://provider.test/" + secret, Model: "model", Roles: []string{"Remember"}, Operation: "assess", Available: true, TimeoutMS: 1000}
	service := NewProviderTestService(func(context.Context) ([]ProviderTestBinding, error) {
		return []ProviderTestBinding{{Target: target, Execute: func(context.Context) (*modelprovider.CallDiagnostics, error) {
			return nil, errors.New("failed " + secret)
		}}}, nil
	}, slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})), observability.NewCredentialProtector(secret), "server")
	targets, err := service.List(context.Background())
	require.NoError(t, err)
	require.NotContains(t, targets[0].Endpoint, secret)
	result, err := service.Test(context.Background(), targets[0].ID, "control_portal:token")
	require.NoError(t, err)
	require.False(t, result.DetailedLogsAvailable)
	require.Equal(t, "not_dispatched", result.Reason)
	require.Contains(t, result.Error, observability.CredentialProtectionRedacted)
	service.protector = nil
	targets, err = service.List(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Diagnostic text unavailable", targets[0].Endpoint)
}
