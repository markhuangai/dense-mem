package observability

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestCredentialUsageSeparatesCredentialsFromSharedOwner(t *testing.T) {
	metrics := NewPrometheusMetrics()
	teamID := uuid.New()
	ownerID := uuid.New()
	firstID := uuid.New()
	secondID := uuid.New()
	for _, credential := range []struct {
		id   uuid.UUID
		name string
	}{
		{firstID, "research-key"},
		{secondID, "batch-key"},
	} {
		ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
			TeamID: teamID, OwnerID: ownerID, CredentialID: &credential.id, CredentialName: credential.name,
		})
		ctx = WithAIOperation(ctx, AIOperationSemanticAssessment, 1)
		metrics.ObserveHTTPRequest(ctx, "/mcp", "POST", 200, time.Millisecond)
		metrics.ObserveCredentialMCPToolResult(ctx, "success", 1)
		metrics.ObserveVerifierLatencyFor(ctx, "verifier-model", 10, "ok")
		metrics.ObserveAIOperationUsage(ctx, AIOperationUsage{
			Component: AIComponentVerifier, Model: "verifier-model", InputTokens: 10, OutputTokens: 2, Source: AITokenSourceProvider,
		})
	}

	background := WithAIOperation(WithMetricIdentity(context.Background(), teamID.String(), ""), AIOperationDreamGeneration, 1)
	metrics.ObserveVerifierLatencyFor(background, "verifier-model", 10, "ok")
	body := scrapePrometheusMetrics(t, metrics)
	for _, credential := range []struct {
		id   uuid.UUID
		name string
	}{
		{firstID, "research-key"},
		{secondID, "batch-key"},
	} {
		var namedSeries string
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "densemem_usage_credential_last_observed_timestamp_seconds{") && strings.Contains(line, `credential_id="`+credential.id.String()+`"`) {
				namedSeries = line
				break
			}
		}
		require.NotEmpty(t, namedSeries)
		require.Contains(t, namedSeries, `credential_name="`+credential.name+`"`)
	}
	require.Contains(t, body, `attribution="team_background"`)
	require.Contains(t, body, `densemem_usage_ai_provider_attempts_total{`)
	require.Contains(t, body, `densemem_usage_ai_operation_tokens_total{`)
	require.NotContains(t, body, `credential_id="`+ownerID.String()+`"`)
}

func TestCredentialUsageDoesNotExportAuthenticationMaterialAsName(t *testing.T) {
	metrics := NewPrometheusMetrics()
	secret := "private-token-string"
	credentialID := uuid.New()
	ctx := requestctx.WithAuthenticationSecrets(context.Background(), secret)
	ctx = requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: uuid.New(), OwnerID: uuid.New(), CredentialID: &credentialID,
		CredentialName: "agent " + secret,
	})
	metrics.ObserveHTTPRequest(ctx, "/mcp", "POST", 200, time.Millisecond)
	body := scrapePrometheusMetrics(t, metrics)
	require.False(t, strings.Contains(body, secret))
	require.Contains(t, body, `credential_id="`+credentialID.String()+`"`)
	require.Contains(t, body, "densemem_usage_credential_metadata_unavailable_total 1")
	require.NotContains(t, body, `credential_name="agent`)
}

func TestCredentialUsageRejectsConflictingWorkerAndActorIdentity(t *testing.T) {
	metrics := NewPrometheusMetrics()
	actorTeam := uuid.New()
	workerTeam := uuid.New()
	ownerID := uuid.New()
	credentialID := uuid.New()
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: actorTeam, OwnerID: ownerID, CredentialID: &credentialID, CredentialName: "actor-key",
	})
	ctx = WithMetricIdentity(ctx, workerTeam.String(), ownerID.String())
	metrics.ObserveHTTPRequest(ctx, "/mcp", "POST", 200, time.Millisecond)
	body := scrapePrometheusMetrics(t, metrics)
	var usageLine string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "densemem_usage_http_requests_total{") {
			usageLine = line
			break
		}
	}
	require.NotEmpty(t, usageLine)
	require.Contains(t, usageLine, `attribution="unattributed"`)
	require.NotContains(t, usageLine, credentialID.String())
	require.NotContains(t, usageLine, workerTeam.String())
}

func TestCredentialAIProviderAttemptsClassifyEmbeddingFailures(t *testing.T) {
	metrics := NewPrometheusMetrics()
	ctx := WithAIOperation(context.Background(), AIOperationRecallEmbedding, 1)
	metrics.ObserveEmbeddingProviderAttempt(ctx, "embed-model", 0.015, "provider_timeout")
	metrics.ObserveEmbeddingProviderAttempt(ctx, "embed-model", 0.020, "provider_network_error")
	body := scrapePrometheusMetrics(t, metrics)
	var attempts []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "densemem_usage_ai_provider_attempts_total{") {
			attempts = append(attempts, line)
		}
	}
	require.Len(t, attempts, 2)
	require.Contains(t, strings.Join(attempts, "\n"), `outcome="timeout"`)
	require.Contains(t, strings.Join(attempts, "\n"), `outcome="error"`)
	require.NotContains(t, strings.Join(attempts, "\n"), `outcome="unknown"`)
}
