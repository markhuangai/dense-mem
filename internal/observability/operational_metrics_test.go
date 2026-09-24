package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

type operationalTelemetryReaderFunc func(context.Context) (operationscontract.OperationalTelemetrySnapshot, error)

func (f operationalTelemetryReaderFunc) ReadOperationalTelemetry(ctx context.Context) (operationscontract.OperationalTelemetrySnapshot, error) {
	return f(ctx)
}

func TestOperationalMetricsBoundLabelsAndCoverLongDurations(t *testing.T) {
	metrics := NewPrometheusMetrics()
	operationCtx := WithAIOperation(context.Background(), AIOperationSemanticAssessment, 1)
	metrics.ObserveLogicalOperation("private-request-8e1c", "query=customer secret", "provider error raw", 181*time.Second)
	metrics.ObserveMCPToolResult("private-request-8e1c", 1)
	metrics.ObserveVerifierTokens(operationCtx, "private-model-name", 11, 7, 18)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	require.NotContains(t, body, "private-request-8e1c")
	require.NotContains(t, body, "customer secret")
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "densemem_operation_provider_tokens_total") {
			require.NotContains(t, line, "private-model-name")
		}
	}
	require.Contains(t, body, `densemem_mcp_tool_results_total{outcome="unknown"} 1`)
	require.Contains(t, body, `densemem_operation_provider_tokens_total{component="verifier",kind="input",operation="semantic_assessment",source="provider"} 11`)
	require.Contains(t, body, `densemem_operation_provider_tokens_total{component="verifier",kind="output",operation="semantic_assessment",source="provider"} 7`)
	require.Contains(t, body, `densemem_operation_provider_tokens_total{component="verifier",kind="total",operation="semantic_assessment",source="provider"} 18`)
	require.Contains(t, body, `densemem_verifier_tokens_total{kind="prompt",model="private-model-name"`)
	require.Contains(t, body, `densemem_logical_operation_duration_seconds_bucket{operation="unknown",outcome="unknown",le="180"} 0`)
	require.Contains(t, body, `densemem_logical_operation_duration_seconds_bucket{operation="unknown",outcome="unknown",le="240"} 1`)
}

func TestProviderUsageAggregateCardinalityDoesNotFollowModelNames(t *testing.T) {
	metrics := NewPrometheusMetrics()
	ctx := WithAIOperation(context.Background(), AIOperationSemanticAssessment, 1)
	for index := 0; index < 100; index++ {
		RecordVerifierTokens(ctx, metrics, fmt.Sprintf("model-%d", index), 1, 0, 1)
	}
	body := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(body, httptest.NewRequest("GET", "/metrics", nil))
	var aggregateLines []string
	for _, line := range strings.Split(body.Body.String(), "\n") {
		if strings.HasPrefix(line, "densemem_operation_provider_tokens_total{") {
			aggregateLines = append(aggregateLines, line)
			require.NotContains(t, line, "model-")
		}
	}
	require.Equal(t, 216, len(aggregateLines))
	inputTokens := prometheusCounterValue(t, body.Body.String(), "densemem_operation_provider_tokens_total",
		`component="verifier"`, `kind="input"`, `operation="semantic_assessment"`, `source="provider"`)
	require.Equal(t, float64(100), inputTokens)
}

func TestOperationalTelemetryCollectorEmitsDurableSnapshotAndFixedLabelSets(t *testing.T) {
	metrics := NewPrometheusMetrics()
	reader := operationalTelemetryReaderFunc(func(context.Context) (operationscontract.OperationalTelemetrySnapshot, error) {
		return operationscontract.OperationalTelemetrySnapshot{
			DreamRuns: []operationscontract.DreamRunTelemetry{
				{Window: "1h", Lane: "graph", Status: "completed", Runs: 2, Attempts: 3, InputTargets: 8, ProviderProposals: 4, CreatedHypotheses: 2},
				{Window: "1h", Lane: "graph", Status: "completed", Runs: 1, Attempts: 2, InputTargets: 5, ProviderProposals: 2, CreatedHypotheses: 1},
				{Window: "private-query", Lane: "raw-lane", Status: "raw-status", Runs: 99},
			},
			Hypotheses: []operationscontract.HypothesisTelemetry{
				{Lane: "graph", Status: "proposed", Count: 3, BacklogCount: 3, OldestBacklogAge: 40},
				{Lane: "graph", Status: "proposed", Count: 2, BacklogCount: 2, OldestBacklogAge: 70},
			},
			RelationshipsCurrent: []operationscontract.NamedTelemetryCount{
				{Kind: "active", Count: 4}, {Kind: "active", Count: 2}, {Kind: "raw-status", Count: 99},
			},
		}, nil
	})
	require.NoError(t, metrics.RegisterOperationalTelemetryCollector(reader))

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	require.Contains(t, body, `densemem_operational_ledger_collection_success 1`)
	require.Contains(t, body, `densemem_operational_dream_runs{lane="graph",status="completed",window="1h"} 3`)
	require.Contains(t, body, `densemem_operational_dream_run_attempts{lane="graph",status="completed",window="1h"} 5`)
	require.Contains(t, body, `densemem_operational_hypothesis_backlog{lane="graph"} 5`)
	require.Contains(t, body, `densemem_operational_hypothesis_oldest_backlog_age_seconds{lane="graph"} 70`)
	require.Contains(t, body, `densemem_operational_relationships_current{status="active"} 6`)
	require.NotContains(t, body, "private-query")
	require.NotContains(t, body, "raw-lane")
	require.NotContains(t, body, "raw-status")
}

func TestOperationalTelemetryCollectorDoesNotPublishFalseZerosOnPartialFailure(t *testing.T) {
	metrics := NewPrometheusMetrics()
	reader := operationalTelemetryReaderFunc(func(context.Context) (operationscontract.OperationalTelemetrySnapshot, error) {
		return operationscontract.OperationalTelemetrySnapshot{RelationshipsCurrent: []operationscontract.NamedTelemetryCount{{Kind: "active", Count: 12}}}, errors.New("relationship correction query failed")
	})
	require.NoError(t, metrics.RegisterOperationalTelemetryCollector(reader))

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	require.Contains(t, body, `densemem_operational_ledger_collection_success 0`)
	require.NotContains(t, body, "densemem_operational_relationships_current")
	require.NotContains(t, body, "relationship correction query failed")
}

func TestOperationalTelemetryCollectorCanReadDurableStateAfterRecreation(t *testing.T) {
	reader := operationalTelemetryReaderFunc(func(context.Context) (operationscontract.OperationalTelemetrySnapshot, error) {
		return operationscontract.OperationalTelemetrySnapshot{
			RelationshipsCurrent: []operationscontract.NamedTelemetryCount{{Kind: "active", Count: 9}},
		}, nil
	})
	read := func() string {
		metrics := NewPrometheusMetrics()
		require.NoError(t, metrics.RegisterOperationalTelemetryCollector(reader))
		recorder := httptest.NewRecorder()
		metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
		return recorder.Body.String()
	}
	require.Contains(t, read(), `densemem_operational_relationships_current{status="active"} 9`)
	require.Contains(t, read(), `densemem_operational_relationships_current{status="active"} 9`)
}

func TestOperationalTelemetryCollectorRegistersWithRealPrometheusRegistry(t *testing.T) {
	registry := prometheus.NewRegistry()
	collector := NewOperationalTelemetryCollector(operationalTelemetryReaderFunc(func(context.Context) (operationscontract.OperationalTelemetrySnapshot, error) {
		return operationscontract.OperationalTelemetrySnapshot{}, nil
	}))
	require.NoError(t, registry.Register(collector))
	families, err := registry.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, families)
	for _, family := range families {
		if strings.HasPrefix(family.GetName(), "densemem_operational_") {
			require.NotEmpty(t, family.Metric)
		}
	}
}
