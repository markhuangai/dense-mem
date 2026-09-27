package observability

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const (
	usageCredential             = "credential"
	usageAuthenticatedNoKey     = "authenticated_no_credential"
	usageTeamBackground         = "team_background"
	usageUnattributed           = "unattributed"
	maxCredentialMetricNameSize = 128
)

type usageMetricIdentity struct {
	teamID, profileID, credentialID, attribution, credentialName string
}

func usageIdentityFromContext(ctx context.Context) usageMetricIdentity {
	identity := usageMetricIdentity{teamID: unknownMetricLabel, profileID: unknownMetricLabel, attribution: usageUnattributed}
	if ctx == nil {
		return identity
	}
	worker, workerScoped := metricIdentityFromContext(ctx)
	if workerScoped {
		identity.teamID, identity.profileID = worker.teamID, worker.profileID
		identity.attribution = usageTeamBackground
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return identity
	}
	actorTeam, actorProfile := actor.TeamID.String(), actor.OwnerID.String()
	if workerScoped && worker.profileID == unknownMetricLabel {
		return identity
	}
	if identity.teamID != unknownMetricLabel && identity.teamID != actorTeam {
		return usageMetricIdentity{teamID: unknownMetricLabel, profileID: unknownMetricLabel, attribution: usageUnattributed}
	}
	if identity.profileID != unknownMetricLabel && identity.profileID != actorProfile {
		return usageMetricIdentity{teamID: unknownMetricLabel, profileID: unknownMetricLabel, attribution: usageUnattributed}
	}
	if identity.teamID == unknownMetricLabel {
		identity.teamID = actorTeam
	}
	if identity.profileID == unknownMetricLabel {
		identity.profileID = actorProfile
	}
	identity.attribution = usageAuthenticatedNoKey
	if actor.CredentialID != nil && *actor.CredentialID != uuid.Nil {
		identity.credentialID = actor.CredentialID.String()
		identity.credentialName = actor.CredentialName
		identity.attribution = usageCredential
	}
	return identity
}

func (identity usageMetricIdentity) labels() []string {
	return []string{identity.teamID, identity.profileID, identity.credentialID, identity.attribution}
}

type credentialUsagePrometheusMetrics struct {
	httpRequests        *prometheus.CounterVec
	httpDuration        *prometheus.HistogramVec
	mcpResults          *prometheus.CounterVec
	aiAttempts          *prometheus.CounterVec
	aiDuration          *prometheus.HistogramVec
	aiTokens            *prometheus.CounterVec
	aiCosts             *prometheus.CounterVec
	aiUnpriced          *prometheus.CounterVec
	credentialInfo      *prometheus.GaugeVec
	metadataUnavailable prometheus.Counter
	protector           *CredentialProtector
	nameMu              sync.Mutex
	previousNames       map[string]string
}

func newCredentialUsagePrometheusMetrics() *credentialUsagePrometheusMetrics {
	base := []string{"team_id", "profile_id", "credential_id", "attribution"}
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }
	return &credentialUsagePrometheusMetrics{
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_usage_http_requests_total", Help: "HTTP requests by authenticated credential or explicit unattributed category.",
		}, with("route", "method", "status_class")),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "densemem_usage_http_request_duration_seconds", Help: "HTTP request duration by authenticated credential or explicit unattributed category.", Buckets: requestDurationBuckets(),
		}, with("route", "method", "status_class")),
		mcpResults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_usage_mcp_tool_results_total", Help: "Logical MCP outcomes by authenticated credential or explicit unattributed category.",
		}, with("outcome")),
		aiAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_usage_ai_provider_attempts_total", Help: "AI provider attempts by feature and attribution.",
		}, with("operation", "component", "model", "outcome")),
		aiDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "densemem_usage_ai_provider_duration_seconds", Help: "AI provider attempt duration by feature and attribution.", Buckets: requestDurationBuckets(),
		}, with("operation", "component", "model", "outcome")),
		aiTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_usage_ai_operation_tokens_total", Help: "AI operation input and output tokens by feature and attribution.",
		}, with("operation", "component", "model", "kind", "source")),
		aiCosts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_usage_ai_operation_cost_usd_total", Help: "Estimated AI operation cost by feature and attribution.",
		}, with("operation", "component", "model", "source")),
		aiUnpriced: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_usage_ai_operation_unpriced_total", Help: "AI operation usage without complete pricing by feature and attribution.",
		}, with("operation", "component", "model", "reason")),
		credentialInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "densemem_usage_credential_last_observed_timestamp_seconds", Help: "Last observation time for a protected credential display name.",
		}, []string{"team_id", "profile_id", "credential_id", "credential_name"}),
		metadataUnavailable: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "densemem_usage_credential_metadata_unavailable_total", Help: "Credential names omitted because they could not be protected or bounded.",
		}),
		protector:     NewCredentialProtector(),
		previousNames: map[string]string{},
	}
}

func (m *credentialUsagePrometheusMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{m.httpRequests, m.httpDuration, m.mcpResults,
		m.aiAttempts, m.aiDuration, m.aiTokens, m.aiCosts, m.aiUnpriced,
		m.credentialInfo, m.metadataUnavailable}
}

func (m *PrometheusMetrics) SetCredentialMetadataProtector(protector *CredentialProtector) {
	if m == nil || m.credentialUsage == nil || protector == nil {
		return
	}
	m.credentialUsage.protector = protector
}

func (m *credentialUsagePrometheusMetrics) observeMetadata(ctx context.Context, identity usageMetricIdentity) {
	if identity.attribution != usageCredential || identity.credentialID == "" {
		return
	}
	name := strings.TrimSpace(identity.credentialName)
	if name == "" || len(name) > maxCredentialMetricNameSize {
		m.clearCredentialMetadata(identity)
		m.metadataUnavailable.Inc()
		return
	}
	protected := m.protector.Snapshot(name, maxCredentialMetricNameSize+16, requestctx.AuthenticationSecretsFromContext(ctx)...)
	protectedName, ok := protected.Value.(string)
	if protected.UnavailableReason != CredentialProtectionAvailable || !ok || protectedName != name {
		m.clearCredentialMetadata(identity)
		m.metadataUnavailable.Inc()
		return
	}
	key := identity.teamID + ":" + identity.credentialID
	m.nameMu.Lock()
	defer m.nameMu.Unlock()
	if previous, ok := m.previousNames[key]; ok && previous != name {
		m.credentialInfo.DeleteLabelValues(identity.teamID, identity.profileID, identity.credentialID, previous)
	}
	m.previousNames[key] = name
	m.credentialInfo.WithLabelValues(identity.teamID, identity.profileID, identity.credentialID, name).Set(float64(time.Now().Unix()))
}

func (m *credentialUsagePrometheusMetrics) clearCredentialMetadata(identity usageMetricIdentity) {
	key := identity.teamID + ":" + identity.credentialID
	m.nameMu.Lock()
	defer m.nameMu.Unlock()
	if previous, ok := m.previousNames[key]; ok {
		m.credentialInfo.DeleteLabelValues(identity.teamID, identity.profileID, identity.credentialID, previous)
		delete(m.previousNames, key)
	}
}

func (m *credentialUsagePrometheusMetrics) observeHTTPRequest(ctx context.Context, route, method, class string, duration time.Duration) {
	identity := usageIdentityFromContext(ctx)
	labels := append(identity.labels(), route, method, class)
	m.httpRequests.WithLabelValues(labels...).Inc()
	m.httpDuration.WithLabelValues(labels...).Observe(duration.Seconds())
	m.observeMetadata(ctx, identity)
}

func (m *PrometheusMetrics) ObserveCredentialMCPToolResult(ctx context.Context, outcome string, count int64) {
	if m == nil || m.credentialUsage == nil || count < 1 {
		return
	}
	identity := usageIdentityFromContext(ctx)
	outcome = boundedMetricLabel(outcome, []string{"success", "cancelled", "rpc_error", "tool_error", "missing_result", "other"})
	m.credentialUsage.mcpResults.WithLabelValues(append(identity.labels(), outcome)...).Add(float64(count))
	m.credentialUsage.observeMetadata(ctx, identity)
}

func (m *credentialUsagePrometheusMetrics) observeAIAttempt(ctx context.Context, operation, component, model, outcome string, durationSeconds float64) {
	identity := usageIdentityFromContext(ctx)
	labels := append(identity.labels(), operation, component, model, outcome)
	m.aiAttempts.WithLabelValues(labels...).Inc()
	m.aiDuration.WithLabelValues(labels...).Observe(durationSeconds)
	m.observeMetadata(ctx, identity)
}

func (m *PrometheusMetrics) ObserveEmbeddingProviderAttempt(ctx context.Context, model string, durationSeconds float64, outcome string) {
	if m == nil || m.credentialUsage == nil || durationSeconds < 0 {
		return
	}
	operation, ok := aiOperationFromContext(ctx)
	if !ok {
		return
	}
	m.credentialUsage.observeAIAttempt(ctx, operation.operation, AIComponentEmbedding, normalizeLabel(model), normalizeAIProviderAttemptOutcome(outcome), durationSeconds)
}

func RecordEmbeddingProviderAttempt(ctx context.Context, metrics DiscoverabilityMetrics, model string, durationSeconds float64, outcome string) {
	if recorder, ok := metrics.(interface {
		ObserveEmbeddingProviderAttempt(context.Context, string, float64, string)
	}); ok {
		recorder.ObserveEmbeddingProviderAttempt(ctx, model, durationSeconds, outcome)
	}
}

func normalizeAIProviderAttemptOutcome(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ok":
		return "ok"
	case "cancelled", "canceled":
		return "cancelled"
	case "timeout", "provider_timeout":
		return "timeout"
	default:
		return "error"
	}
}

func (m *credentialUsagePrometheusMetrics) addAITokens(ctx context.Context, operation, component, model, kind, source string, count int64) {
	if count <= 0 {
		return
	}
	identity := usageIdentityFromContext(ctx)
	m.aiTokens.WithLabelValues(append(identity.labels(), operation, component, model, kind, source)...).Add(float64(count))
	m.observeMetadata(ctx, identity)
}

func (m *credentialUsagePrometheusMetrics) addAICost(ctx context.Context, operation, component, model, source string, cost float64) {
	identity := usageIdentityFromContext(ctx)
	m.aiCosts.WithLabelValues(append(identity.labels(), operation, component, model, source)...).Add(cost)
	m.observeMetadata(ctx, identity)
}

func (m *credentialUsagePrometheusMetrics) addAIUnpriced(ctx context.Context, operation, component, model, reason string) {
	identity := usageIdentityFromContext(ctx)
	m.aiUnpriced.WithLabelValues(append(identity.labels(), operation, component, model, reason)...).Inc()
	m.observeMetadata(ctx, identity)
}
