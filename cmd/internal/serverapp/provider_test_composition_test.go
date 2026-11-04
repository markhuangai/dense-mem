package serverapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/operations"
	"github.com/stretchr/testify/require"
)

func TestProviderTestReusesProductionOperationsAndProtectsCompleteLogs(t *testing.T) {
	for _, vendor := range []string{"openai", "typesafe"} {
		t.Run(vendor, func(t *testing.T) {
			fixture := newProviderTestFixture(t, vendor)
			ctx := correlation.WithID(context.Background(), "provider-test-correlation")
			ctx = observability.WithAuthenticationSecrets(ctx, "control-test-authentication")
			targets, err := fixture.service.List(ctx)
			require.NoError(t, err)
			require.Len(t, targets, 3)
			require.Zero(t, fixture.calls.Load(), "discovery must never call a provider")
			expected := map[string]bool{}
			for _, target := range targets {
				result, err := fixture.service.Test(ctx, target.ID, "control_portal:token")
				require.NoError(t, err)
				require.Equal(t, "succeeded", result.Outcome, "%+v", result)
				require.Equal(t, "http_response_received", result.NetworkState)
				require.Equal(t, 200, *result.HTTPStatus)
				require.Equal(t, "captured", result.CaptureState)
				require.Equal(t, "provider-test-correlation", result.CorrelationID)
				require.NotEmpty(t, result.ServerInstance)
				require.Len(t, result.Exchanges, 1)
				expected[result.Exchanges[0].ExchangeID] = true
				if target.Operation == "assess" {
					require.Len(t, target.Roles, 4)
					require.NotNil(t, result.Diagnostics.QueueMS)
				}
			}
			require.EqualValues(t, 3, fixture.calls.Load())
			require.NotContains(t, fixture.logs.String(), providerTestSecret)
			require.NotContains(t, fixture.logs.String(), "control-test-authentication")
			bodies := map[string]map[string][]observability.DiagnosticBodyChunk{}
			for _, line := range bytes.Split(bytes.TrimSpace(fixture.logs.Bytes()), []byte("\n")) {
				var record struct {
					Message   string                            `json:"msg"`
					RunID     string                            `json:"run_id"`
					Direction string                            `json:"direction"`
					Exchange  string                            `json:"exchange_id"`
					Chunk     observability.DiagnosticBodyChunk `json:"payload_chunk"`
				}
				require.NoError(t, json.Unmarshal(line, &record))
				if record.Message != "ai_exchange_payload" {
					continue
				}
				require.NotEmpty(t, record.RunID)
				require.True(t, expected[record.Exchange], "every payload must match an observed exchange")
				require.Equal(t, "captured", record.Chunk.State)
				if bodies[record.Exchange] == nil {
					bodies[record.Exchange] = map[string][]observability.DiagnosticBodyChunk{}
				}
				bodies[record.Exchange][record.Direction] = append(bodies[record.Exchange][record.Direction], record.Chunk)
			}
			require.Len(t, bodies, 3, "every assessor, embedding, and decision exchange must reach the console sink")
			for id := range expected {
				for _, direction := range []string{"request", "response"} {
					chunks := bodies[id][direction]
					require.NotEmpty(t, chunks, "missing %s for %s", direction, id)
					require.Len(t, chunks, chunks[0].Count)
					var body string
					for index, chunk := range chunks {
						require.Equal(t, index, chunk.Index)
						require.Equal(t, chunks[0].Count, chunk.Count)
						require.Equal(t, chunks[0].SHA256, chunk.SHA256)
						body += chunk.Body
					}
					require.Equal(t, chunks[0].SHA256, fmt.Sprintf("%x", sha256.Sum256([]byte(body))))
					require.True(t, json.Valid([]byte(body)))
				}
			}
			require.Contains(t, fixture.logs.String(), "decision.exchange.payload")
			require.Contains(t, fixture.logs.String(), "provider_test.completed")
		})
	}
}

func TestProviderTestDiscoversActiveAndPendingOntologyModels(t *testing.T) {
	fixture := newProviderTestFixture(t, "disabled")
	fixture.policy.active = domain.OntologyMaintenanceConfig{Model: "active-window-model", SettingsVersion: "active"}
	fixture.policy.value = domain.OntologyMaintenanceConfig{Model: "pending-window-model", SettingsVersion: "pending"}
	targets, err := fixture.service.List(context.Background())
	require.NoError(t, err)
	roles := map[string][]string{}
	for _, target := range targets {
		roles[target.Model] = target.Roles
	}
	require.Equal(t, []string{"Ontology maintenance"}, roles["active-window-model"])
	require.Equal(t, []string{"Ontology maintenance (next window)"}, roles["pending-window-model"])
	require.Zero(t, fixture.calls.Load())
}

func TestProviderTestEmbeddingQueueCancellationAndDeadlineReleaseCapacity(t *testing.T) {
	fixture := newProviderTestFixture(t, "disabled")
	targets, err := fixture.service.List(context.Background())
	require.NoError(t, err)
	var target operations.ProviderTestTarget
	for _, candidate := range targets {
		if candidate.Operation == "embed" {
			target = candidate
		}
	}
	fixture.hold.Store(true)
	active, cancelActive := context.WithCancel(context.Background())
	defer cancelActive()
	done := make(chan operations.ProviderTestResult, 1)
	go func() {
		result, err := fixture.service.Test(active, target.ID, "control_portal:token")
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	select {
	case <-fixture.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("embedding sample never reached the provider")
	}
	for _, cancelled := range []bool{true, false} {
		ctx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
		if cancelled {
			stop()
		}
		result, err := fixture.service.Test(ctx, target.ID, "control_portal:token")
		stop()
		require.NoError(t, err)
		require.Equal(t, "queue", result.FailurePhase)
		require.Equal(t, "not_dispatched", result.NetworkState)
		require.Empty(t, result.Exchanges)
		require.NotNil(t, result.Diagnostics.QueueMS)
		if cancelled {
			require.Equal(t, "cancelled", result.Outcome)
		} else {
			require.Equal(t, "timeout", result.Reason)
		}
	}
	require.EqualValues(t, 1, fixture.calls.Load())
	cancelActive()
	select {
	case result := <-done:
		require.Equal(t, "cancelled", result.Outcome)
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled embedding sample did not release capacity")
	}
	fixture.hold.Store(false)
	result, err := fixture.service.Test(context.Background(), target.ID, "control_portal:token")
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.Outcome)
}

func TestProviderTestDistinguishesProviderFailuresFromNetworkFailures(t *testing.T) {
	fixture := newProviderTestFixture(t, "openai")
	targets, err := fixture.service.List(context.Background())
	require.NoError(t, err)
	for _, target := range targets {
		for status, reason := range map[int]string{302: "provider_http_error", 401: "authentication_failed", 429: "rate_limited", 503: "provider_http_error"} {
			fixture.status.Store(int32(status))
			result, err := fixture.service.Test(context.Background(), target.ID, "control_portal:token")
			require.NoError(t, err)
			require.Equal(t, "failed", result.Outcome)
			require.Equal(t, reason, result.Reason)
			require.Equal(t, "http_response_received", result.NetworkState)
			require.Equal(t, status, *result.HTTPStatus)
			require.NotContains(t, result.Error, providerTestSecret)
		}
	}
	fixture.status.Store(0)
	fixture.malformed.Store(true)
	for _, target := range targets {
		result, err := fixture.service.Test(context.Background(), target.ID, "control_portal:token")
		require.NoError(t, err)
		require.Equal(t, "invalid_response", result.Reason)
		require.Equal(t, 200, *result.HTTPStatus)
	}
	fixture.malformed.Store(false)
	fixture.disconnect.Store(true)
	result, err := fixture.service.Test(context.Background(), targets[0].ID, "control_portal:token")
	require.NoError(t, err)
	require.Nil(t, result.HTTPStatus)
	require.Equal(t, "no_http_response", result.NetworkState)
	require.NotEqual(t, "succeeded", result.Outcome)
}

func TestProviderTestRefreshesModelGroupingAndRejectsStaleTargets(t *testing.T) {
	fixture := newProviderTestFixture(t, "disabled")
	initial, err := fixture.service.List(context.Background())
	require.NoError(t, err)
	var oldModel operations.ProviderTestTarget
	for _, target := range initial {
		if target.Operation == "assess" {
			oldModel = target
		}
		if target.Operation == "decide" {
			require.False(t, target.Available)
		}
	}
	fixture.policy.value.Model, fixture.policy.value.SettingsVersion = "ontology-model-override", "ontology-2"
	refreshed, err := fixture.service.List(context.Background())
	require.NoError(t, err)
	require.Len(t, refreshed, 4)
	_, err = fixture.service.Test(context.Background(), oldModel.ID, "control_portal:token")
	require.ErrorIs(t, err, operations.ErrProviderTestTargetChanged)
	for _, target := range refreshed {
		if target.Model == "ontology-model-override" {
			result, err := fixture.service.Test(context.Background(), target.ID, "control_portal:token")
			require.NoError(t, err)
			require.Equal(t, "succeeded", result.Outcome)
		}
		if !target.Available {
			_, err := fixture.service.Test(context.Background(), target.ID, "control_portal:token")
			require.ErrorIs(t, err, operations.ErrProviderTestUnavailable)
		}
	}
	fixture.policy.err = errors.New("configuration database unavailable")
	_, err = fixture.service.List(context.Background())
	require.ErrorIs(t, err, operations.ErrProviderTestUnavailable)
}

func TestProviderTestCancellationReleasesProductionGate(t *testing.T) {
	fixture := newProviderTestFixture(t, "disabled")
	targets, err := fixture.service.List(context.Background())
	require.NoError(t, err)
	var target operations.ProviderTestTarget
	for _, candidate := range targets {
		if candidate.Operation == "assess" {
			target = candidate
		}
	}
	fixture.deps.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := fixture.service.Test(ctx, target.ID, "control_portal:token")
	require.NoError(t, err)
	require.Equal(t, "cancelled", result.Outcome)
	require.Equal(t, "not_dispatched", result.NetworkState)
	require.Zero(t, fixture.calls.Load())
	<-fixture.deps.gate
	result, err = fixture.service.Test(context.Background(), target.ID, "control_portal:token")
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.Outcome)

	fixture.deps.gate <- struct{}{}
	deadline, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	result, err = fixture.service.Test(deadline, target.ID, "control_portal:token")
	require.NoError(t, err)
	require.Equal(t, "timeout", result.Reason)
	require.Equal(t, "queue", result.FailurePhase)
	<-fixture.deps.gate
	require.NotContains(t, result.Guidance, "DNS")

	fixture.hold.Store(true)
	active, cancelActive := context.WithCancel(context.Background())
	defer cancelActive()
	done := make(chan operations.ProviderTestResult, 1)
	go func() {
		result, err := fixture.service.Test(active, target.ID, "control_portal:token")
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	select {
	case <-fixture.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("sample never reached the provider")
	}
	cancelActive()
	select {
	case result = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled sample did not release its request")
	}
	require.Equal(t, "cancelled", result.Outcome)
	fixture.hold.Store(false)
	result, err = fixture.service.Test(context.Background(), target.ID, "control_portal:token")
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.Outcome)
}
