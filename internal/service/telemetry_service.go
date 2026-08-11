package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const TelemetryRetentionDays = 30

const (
	TelemetryAudienceOperator = "operator"
	TelemetryAudienceUser     = "user"
)

const (
	TelemetrySnapshotReady       = "ready"
	TelemetrySnapshotDegraded    = "degraded"
	TelemetrySnapshotUnavailable = "unavailable"

	TelemetryItemReady       = "ready"
	TelemetryItemInactive    = "inactive"
	TelemetryItemUnavailable = "unavailable"
	TelemetryItemUnsupported = "unsupported"
)

var telemetryWindows = map[string]struct {
	Duration time.Duration
	Step     time.Duration
	Rate     string
}{
	"15m": {Duration: 15 * time.Minute, Step: time.Minute, Rate: "1m"},
	"30m": {Duration: 30 * time.Minute, Step: time.Minute, Rate: "1m"},
	"1h":  {Duration: time.Hour, Step: time.Minute, Rate: "1m"},
	"12h": {Duration: 12 * time.Hour, Step: 5 * time.Minute, Rate: "5m"},
	"1d":  {Duration: 24 * time.Hour, Step: 15 * time.Minute, Rate: "15m"},
	"7d":  {Duration: 7 * 24 * time.Hour, Step: time.Hour, Rate: "1h"},
	"30d": {Duration: 30 * 24 * time.Hour, Step: 6 * time.Hour, Rate: "6h"},
}

type TelemetryReader interface {
	Snapshot(ctx context.Context, filter TelemetryFilter) (*TelemetrySnapshot, error)
}

type TelemetryFilter struct {
	Window    string
	Scope     string
	TeamID    *uuid.UUID
	ProfileID *uuid.UUID
	Audience  string
}

type TelemetrySnapshot struct {
	Available      bool              `json:"available"`
	Status         string            `json:"status"`
	GeneratedAt    string            `json:"generated_at"`
	Message        string            `json:"message,omitempty"`
	Window         TelemetryWindow   `json:"window"`
	Scope          TelemetryScope    `json:"scope"`
	Cards          []TelemetryCard   `json:"cards"`
	WindowedCards  []TelemetryCard   `json:"windowed_cards"`
	CurrentCards   []TelemetryCard   `json:"current_cards"`
	Series         []TelemetrySeries `json:"series"`
	ActivitySeries []TelemetrySeries `json:"activity_series"`
	StateSeries    []TelemetrySeries `json:"state_series"`
}

type TelemetryWindow struct {
	Key           string `json:"key"`
	From          string `json:"from"`
	To            string `json:"to"`
	StepSeconds   int64  `json:"step_seconds"`
	RetentionDays int    `json:"retention_days"`
}

type TelemetryScope struct {
	Type      string     `json:"type"`
	TeamID    *uuid.UUID `json:"team_id,omitempty"`
	ProfileID *uuid.UUID `json:"profile_id,omitempty"`
}

type TelemetryCard struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Unit       string  `json:"unit"`
	Value      float64 `json:"value"`
	Available  bool    `json:"available"`
	Status     string  `json:"status"`
	ReasonCode string  `json:"reason_code,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}

type TelemetrySeries struct {
	ID         string           `json:"id"`
	Label      string           `json:"label"`
	Unit       string           `json:"unit"`
	Points     []TelemetryPoint `json:"points"`
	Status     string           `json:"status"`
	ReasonCode string           `json:"reason_code,omitempty"`
	Reason     string           `json:"reason,omitempty"`
}

type TelemetryPoint struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"value"`
}

type PrometheusTelemetryService struct {
	baseURL       string
	prometheusJob string
	client        *http.Client
	timeout       time.Duration
	now           func() time.Time
	logger        observability.LogProvider
	lifecycle     repository.TelemetryLifecycleReader
	features      TelemetryFeatureResolver
}

type TelemetryFeatureResolver struct {
	RecallFeedbackEnabled func(context.Context) (bool, error)
	DreamingEnabled       func(context.Context, *uuid.UUID) (bool, error)
}

func (s *PrometheusTelemetryService) SetLifecycleReader(reader repository.TelemetryLifecycleReader) {
	if s != nil {
		s.lifecycle = reader
	}
}

func (s *PrometheusTelemetryService) SetFeatureResolver(resolver TelemetryFeatureResolver) {
	if s != nil {
		s.features = resolver
	}
}

func NewPrometheusTelemetryService(baseURL string, timeout time.Duration) *PrometheusTelemetryService {
	return NewPrometheusTelemetryServiceWithLogger(baseURL, timeout, nil)
}

func NewPrometheusTelemetryServiceWithLogger(baseURL string, timeout time.Duration, logger observability.LogProvider) *PrometheusTelemetryService {
	return NewPrometheusTelemetryServiceWithJobAndLogger(baseURL, timeout, "", logger)
}

func NewPrometheusTelemetryServiceWithJobAndLogger(baseURL string, timeout time.Duration, prometheusJob string, logger observability.LogProvider) *PrometheusTelemetryService {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &PrometheusTelemetryService{
		baseURL:       strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		prometheusJob: strings.TrimSpace(prometheusJob),
		client:        &http.Client{Timeout: timeout},
		timeout:       timeout,
		now:           time.Now,
		logger:        logger,
	}
}

func (s *PrometheusTelemetryService) Snapshot(ctx context.Context, filter TelemetryFilter) (*TelemetrySnapshot, error) {
	windowKey := strings.TrimSpace(filter.Window)
	if windowKey == "" {
		windowKey = "1h"
	}
	windowDef, ok := telemetryWindows[windowKey]
	if !ok {
		return nil, httperr.New(httperr.VALIDATION_ERROR, "window must be one of 15m, 30m, 1h, 12h, 1d, 7d, 30d")
	}
	scope, err := normalizeTelemetryScope(filter)
	if err != nil {
		return nil, err
	}
	includeCost := strings.EqualFold(strings.TrimSpace(filter.Audience), TelemetryAudienceOperator)
	includeAssessorTelemetry := includeCost && strings.EqualFold(scope.Type, "system")

	nowFn := time.Now
	if s != nil && s.now != nil {
		nowFn = s.now
	}
	to := nowFn().UTC()
	from := to.Add(-windowDef.Duration)
	var baseLabels map[string]string
	if s != nil {
		baseLabels = s.telemetryBaseLabels()
	}
	selector := telemetrySelector(scope, baseLabels)
	initialWindowedSpecs := telemetryWindowedCardSpecsForAudience(scope, nil, windowKey, includeCost)
	initialCurrentSpecs := telemetryCurrentCardSpecsForAudience(scope, nil, includeCost)
	initialActivitySpecs := telemetryActivitySeriesSpecsForAudience(selector, windowDef.Rate, includeAssessorTelemetry)
	initialStateSpecs := telemetryStateSeriesSpecs(selector)
	initialWindowedCards := telemetryEmptyCardsFromSpecs(initialWindowedSpecs)
	initialCurrentCards := telemetryEmptyCardsFromSpecs(initialCurrentSpecs)
	initialActivitySeries := telemetryEmptySeriesFromSpecs(initialActivitySpecs)
	initialStateSeries := telemetryEmptySeriesFromSpecs(initialStateSpecs)
	snapshot := &TelemetrySnapshot{
		Available:   true,
		Status:      TelemetrySnapshotReady,
		GeneratedAt: to.Format(time.RFC3339Nano),
		Window: TelemetryWindow{
			Key:           windowKey,
			From:          from.Format(time.RFC3339),
			To:            to.Format(time.RFC3339),
			StepSeconds:   int64(windowDef.Step.Seconds()),
			RetentionDays: TelemetryRetentionDays,
		},
		Scope:          scope,
		Cards:          appendTelemetryCards(initialWindowedCards, initialCurrentCards),
		WindowedCards:  initialWindowedCards,
		CurrentCards:   initialCurrentCards,
		Series:         appendTelemetrySeries(initialActivitySeries, initialStateSeries),
		ActivitySeries: initialActivitySeries,
		StateSeries:    initialStateSeries,
	}
	if s == nil || s.baseURL == "" {
		var lifecycle repository.TelemetryLifecycleSnapshot
		var lifecycleErr error
		if s != nil && s.lifecycle != nil {
			lifecycleCtx, cancel := context.WithTimeout(ctx, s.timeout)
			lifecycle, lifecycleErr = s.lifecycle.ReadTelemetryLifecycle(lifecycleCtx, repository.TelemetryLifecycleFilter{TeamID: scope.TeamID, ProfileID: scope.ProfileID}, from, to)
			cancel()
		} else if hasLedgerSpecs(initialWindowedSpecs) || hasLedgerSpecs(initialCurrentSpecs) {
			lifecycleErr = fmt.Errorf("telemetry lifecycle source is unavailable")
		}
		featureStates := map[string]telemetryFeatureState{}
		if s != nil {
			featureStates = s.telemetryFeatureStates(ctx, scope)
		}
		windowedCardsWithInternal := buildTelemetryCards(initialWindowedSpecs, nil, lifecycle, lifecycleErr, featureStates, scope)
		currentCardsWithInternal := buildTelemetryCards(initialCurrentSpecs, nil, lifecycle, lifecycleErr, featureStates, scope)
		activitySeries := buildTelemetrySeries(initialActivitySpecs, nil, featureStates, from, to, windowedCardsWithInternal, scope)
		stateSeries := buildTelemetrySeries(initialStateSpecs, nil, featureStates, from, to, windowedCardsWithInternal, scope)
		windowedCards := visibleTelemetryCards(windowedCardsWithInternal)
		currentCards := visibleTelemetryCards(currentCardsWithInternal)
		markTelemetryUnavailableForNonLedger(windowedCards, initialWindowedSpecs, "source_unconfigured")
		markTelemetryUnavailableForNonLedger(currentCards, initialCurrentSpecs, "source_unconfigured")
		markTelemetryUnavailableSeries(activitySeries, "source_unconfigured")
		markTelemetryUnavailableSeries(stateSeries, "source_unconfigured")
		snapshot.WindowedCards = windowedCards
		snapshot.CurrentCards = currentCards
		snapshot.Cards = appendTelemetryCards(windowedCards, currentCards)
		snapshot.ActivitySeries = activitySeries
		snapshot.StateSeries = stateSeries
		snapshot.Series = appendTelemetrySeries(activitySeries, stateSeries)
		snapshot.Status, snapshot.Available, snapshot.Message = telemetrySnapshotDisposition(snapshot.Cards, snapshot.Series)
		if snapshot.Status != TelemetrySnapshotReady {
			snapshot.Message = "telemetry backend is not configured"
		}
		return snapshot, nil
	}

	queryCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	windowedCardSpecs := telemetryWindowedCardSpecsForAudience(scope, baseLabels, windowKey, includeCost)
	currentCardSpecs := telemetryCurrentCardSpecsForAudience(scope, baseLabels, includeCost)
	activitySpecs := initialActivitySpecs
	stateSpecs := initialStateSpecs
	featureStates := s.telemetryFeatureStates(queryCtx, scope)
	windowedResults := runTelemetryInstantQueries(queryCtx, s, telemetryExecutableSpecs(windowedCardSpecs, scope, featureStates), windowKey, scope)
	currentResults := runTelemetryInstantQueries(queryCtx, s, telemetryExecutableSpecs(currentCardSpecs, scope, featureStates), windowKey, scope)
	activityResults := runTelemetryRangeQueries(queryCtx, s, telemetryExecutableSpecs(activitySpecs, scope, featureStates), from, to, windowDef.Step, windowKey, scope)
	stateResults := runTelemetryRangeQueries(queryCtx, s, telemetryExecutableSpecs(stateSpecs, scope, featureStates), from, to, windowDef.Step, windowKey, scope)

	var lifecycle repository.TelemetryLifecycleSnapshot
	var lifecycleErr error
	if s.lifecycle != nil {
		lifecycleCtx, lifecycleCancel := context.WithTimeout(ctx, s.timeout)
		lifecycle, lifecycleErr = s.lifecycle.ReadTelemetryLifecycle(lifecycleCtx, repository.TelemetryLifecycleFilter{TeamID: scope.TeamID, ProfileID: scope.ProfileID}, from, to)
		lifecycleCancel()
	} else if hasLedgerSpecs(windowedCardSpecs) || hasLedgerSpecs(currentCardSpecs) {
		lifecycleErr = fmt.Errorf("telemetry lifecycle source is unavailable")
	}

	windowedCardsWithInternal := buildTelemetryCards(windowedCardSpecs, windowedResults, lifecycle, lifecycleErr, featureStates, scope)
	currentCardsWithInternal := buildTelemetryCards(currentCardSpecs, currentResults, lifecycle, lifecycleErr, featureStates, scope)
	activitySeries := buildTelemetrySeries(activitySpecs, activityResults, featureStates, from, to, windowedCardsWithInternal, scope)
	stateSeries := buildTelemetrySeries(stateSpecs, stateResults, featureStates, from, to, windowedCardsWithInternal, scope)
	windowedCards := visibleTelemetryCards(windowedCardsWithInternal)
	currentCards := visibleTelemetryCards(currentCardsWithInternal)

	snapshot.WindowedCards = windowedCards
	snapshot.CurrentCards = currentCards
	snapshot.Cards = appendTelemetryCards(windowedCards, currentCards)
	snapshot.ActivitySeries = activitySeries
	snapshot.StateSeries = stateSeries
	snapshot.Series = appendTelemetrySeries(activitySeries, stateSeries)
	snapshot.Status, snapshot.Available, snapshot.Message = telemetrySnapshotDisposition(snapshot.Cards, snapshot.Series)
	return snapshot, nil
}

func (s *PrometheusTelemetryService) logQueryFailure(kind string, spec telemetryQuerySpec, window string, scope TelemetryScope, err error) {
	if s == nil || s.logger == nil {
		return
	}
	attrs := []observability.LogAttr{
		observability.String("query_kind", kind),
		observability.String("query_id", spec.ID),
		observability.String("window", window),
		observability.String("scope", scope.Type),
		observability.String("prometheus_query", spec.Query),
	}
	if scope.TeamID != nil {
		attrs = append(attrs, observability.String("team_id", scope.TeamID.String()))
	}
	if scope.ProfileID != nil {
		attrs = append(attrs, observability.String("profile_id", scope.ProfileID.String()))
	}
	if s.prometheusJob != "" {
		attrs = append(attrs, observability.String("prometheus_job", s.prometheusJob))
	}
	s.logger.Error("telemetry backend query failed", err, attrs...)
}

type telemetryQuerySpec struct {
	ID       string
	Label    string
	Unit     string
	Query    string
	Ledger   bool
	Internal bool
	Catalog  telemetryCatalogEntry
}

func (s *PrometheusTelemetryService) telemetryBaseLabels() map[string]string {
	if s == nil || strings.TrimSpace(s.prometheusJob) == "" {
		return nil
	}
	return map[string]string{"job": strings.TrimSpace(s.prometheusJob)}
}

func telemetryCardSpecs(scope TelemetryScope, baseLabels map[string]string, window string) []telemetryQuerySpec {
	return appendTelemetryQuerySpecs(
		telemetryWindowedCardSpecs(scope, baseLabels, window),
		telemetryCurrentCardSpecs(scope, baseLabels),
	)
}

func telemetryWindowedCardSpecs(scope TelemetryScope, baseLabels map[string]string, window string) []telemetryQuerySpec {
	return telemetryWindowedCardSpecsForAudience(scope, baseLabels, window, false)
}

func telemetryWindowedCardSpecsForAudience(scope TelemetryScope, baseLabels map[string]string, window string, includeCost bool) []telemetryQuerySpec {
	specs := []telemetryQuerySpec{
		{ID: "http_requests", Label: "HTTP requests", Unit: "requests", Query: telemetryIncrease("densemem_http_requests_total", scope, baseLabels, nil, window)},
		{ID: "http_errors", Label: "HTTP errors", Unit: "requests", Query: telemetryIncrease("densemem_http_requests_total", scope, baseLabels, map[string]string{"status_class": "~\"4xx|5xx\""}, window)},
		{ID: "embedding_requests", Label: "Embedding requests", Unit: "requests", Query: telemetrySparseCounterIncrease("densemem_embedding_requests_total", scope, baseLabels, nil, window)},
		{ID: "embedding_errors", Label: "Embedding errors", Unit: "errors", Query: telemetrySparseCounterIncrease("densemem_embedding_errors_total", scope, baseLabels, map[string]string{"code": telemetryEmbeddingErrorCodeMatcher()}, window)},
		{ID: "embedding_tokens", Label: "Embedding tokens", Unit: "tokens", Query: telemetrySparseCounterIncrease("densemem_embedding_tokens_total", scope, baseLabels, map[string]string{"kind": "total"}, window)},
		{ID: "verifier_requests", Label: "Verifier requests", Unit: "requests", Query: telemetrySparseCounterIncrease("densemem_verifier_requests_total", scope, baseLabels, nil, window)},
		{ID: "verifier_tokens", Label: "Verifier tokens", Unit: "tokens", Query: telemetrySparseCounterIncrease("densemem_verifier_tokens_total", scope, baseLabels, map[string]string{"kind": "total"}, window)},
		{ID: "recalls", Label: "Recall requests", Unit: "requests", Query: telemetrySparseCounterIncrease("densemem_recall_requests_total", scope, baseLabels, nil, window)},
		{ID: "avg_recall_results", Label: "Avg recall results", Unit: "results", Query: telemetryHistogramAverage("densemem_recall_results", scope, baseLabels, nil, window, 1)},
		{ID: "p95_recall_latency", Label: "P95 recall latency", Unit: "ms", Query: telemetryHistogramQuantile("densemem_recall_duration_seconds", scope, baseLabels, nil, window, 0.95, 1000)},
		{ID: "llm_recall_used_rate", Label: "LLM recall used", Unit: "percent", Query: telemetryRecallFeedbackRate(scope, baseLabels, map[string]string{"used": "true"}, window)},
		{ID: "llm_recall_answer_supported_rate", Label: "LLM answer supported", Unit: "percent", Query: telemetryRecallFeedbackRate(scope, baseLabels, map[string]string{"answer_supported": "true"}, window)},
		{ID: "llm_recall_quality_score", Label: "LLM recall quality", Unit: "percent", Query: telemetrySparseHistogramAverage("densemem_recall_feedback_quality_score", scope, baseLabels, nil, window, 100)},
		{ID: "llm_recall_missing_context_rate", Label: "LLM missing context", Unit: "percent", Query: telemetryRecallFeedbackRate(scope, baseLabels, map[string]string{"missing_context": "true"}, window)},
		{ID: "llm_recall_irrelevant_rate", Label: "LLM irrelevant recall", Unit: "percent", Query: telemetryRecallFeedbackRate(scope, baseLabels, map[string]string{"irrelevant": "true"}, window)},
		{ID: "dream_feedbacks", Label: "Dream feedback", Unit: "events", Query: telemetrySparseCounterIncrease("densemem_dream_feedback_total", scope, baseLabels, nil, window)},
		{ID: "avg_http_latency", Label: "Avg HTTP latency", Unit: "ms", Query: telemetryAverageLatency("densemem_http_request_duration_seconds", scope, baseLabels, window)},
		{ID: "avg_embedding_latency", Label: "Avg embedding latency", Unit: "ms", Query: telemetrySparseHistogramAverage("densemem_embedding_duration_seconds", scope, baseLabels, nil, window, 1000)},
		{ID: "avg_verifier_latency", Label: "Avg verifier latency", Unit: "ms", Query: telemetrySparseHistogramAverage("densemem_verifier_duration_seconds", scope, baseLabels, nil, window, 1000)},
		{ID: "avg_conflict_review_duration", Label: "Avg conflict review", Unit: "ms", Query: telemetrySparseHistogramAverage("densemem_conflict_review_duration_seconds", scope, baseLabels, nil, window, 1000)},
		{ID: "remember_acknowledgements", Label: "Remember acknowledgements", Unit: "requests", Query: telemetrySparseCounterIncrease("densemem_remember_acknowledgements_total", scope, baseLabels, nil, window)},
		{ID: "avg_remember_acknowledgement_latency", Label: "Avg remember acknowledgement", Unit: "ms", Query: telemetrySparseHistogramAverage("densemem_remember_acknowledgement_duration_seconds", scope, baseLabels, nil, window, 1000)},
		{ID: "p95_remember_acknowledgement_latency", Label: "P95 remember acknowledgement", Unit: "ms", Query: telemetryHistogramQuantile("densemem_remember_acknowledgement_duration_seconds", scope, baseLabels, nil, window, 0.95, 1000)},
		{ID: "remember_first_dispositions", Label: "First remember dispositions", Unit: "requests", Query: telemetrySparseCounterIncrease("densemem_remember_first_disposition_total", scope, baseLabels, nil, window)},
		{ID: "p95_remember_first_disposition_latency", Label: "P95 first disposition", Unit: "ms", Query: telemetryHistogramQuantile("densemem_remember_first_disposition_duration_seconds", scope, baseLabels, nil, window, 0.95, 1000)},
		{ID: telemetryRecallFeedbackActivityID, Query: telemetrySparseCounterIncrease("densemem_recall_feedback_total", scope, baseLabels, nil, window), Internal: true},
	}
	specs = append(specs, telemetryLifecycleWindowedCardSpecs()...)
	if !includeCost {
		return bindTelemetryCatalog(specs)
	}
	includeAssessorTelemetry := strings.EqualFold(scope.Type, "system")
	assessorSpecs := []telemetryQuerySpec{
		{ID: "assessor_requests", Label: "Assessor requests", Unit: "requests", Query: telemetrySparseCounterIncrease("densemem_assessor_requests_total", scope, baseLabels, nil, window)},
		{ID: "assessor_request_failures", Label: "Assessor request failures", Unit: "failures", Query: telemetrySparseCounterIncrease("densemem_assessor_requests_total", scope, baseLabels, map[string]string{"outcome": "~\"provider_error|malformed_exhausted\""}, window)},
		{ID: "assessor_validation_failures", Label: "Assessor validation failures", Unit: "failures", Query: telemetrySparseCounterIncrease("densemem_assessor_validation_failures_total", scope, baseLabels, nil, window)},
		{ID: "assessor_tokens", Label: "Assessor tokens", Unit: "tokens", Query: telemetrySparseCounterIncrease("densemem_assessor_tokens_total", scope, baseLabels, nil, window)},
		{ID: "avg_assessor_duration", Label: "Avg assessor duration", Unit: "ms", Query: telemetrySparseHistogramAverage("densemem_assessor_duration_seconds", scope, baseLabels, nil, window, 1000)},
		{ID: "assessor_terminal_failures", Label: "Assessor terminal failures", Unit: "failures", Query: telemetrySparseCounterIncrease("densemem_assessor_terminal_failures_total", scope, baseLabels, nil, window)},
	}
	if includeAssessorTelemetry {
		specs = append(specs, assessorSpecs...)
	}
	costSpecs := []telemetryQuerySpec{
		{ID: "ai_cost_usd", Label: "AI cost", Unit: "USD", Query: telemetryAICost(scope, baseLabels, "", window)},
		telemetryQuerySpec{ID: "verifier_cost_usd", Label: "Verifier cost", Unit: "USD", Query: telemetryAICost(scope, baseLabels, observability.AIComponentVerifier, window)},
		telemetryQuerySpec{ID: "embedding_cost_usd", Label: "Embedding cost", Unit: "USD", Query: telemetryAICost(scope, baseLabels, observability.AIComponentEmbedding, window)},
	}
	return bindTelemetryCatalog(append(specs, costSpecs...))
}

func telemetryCurrentCardSpecs(scope TelemetryScope, baseLabels map[string]string) []telemetryQuerySpec {
	return telemetryCurrentCardSpecsForAudience(scope, baseLabels, false)
}

func telemetryCurrentCardSpecsForAudience(scope TelemetryScope, baseLabels map[string]string, operator bool) []telemetryQuerySpec {
	specs := telemetryLifecycleCurrentCardSpecs()
	if !operator {
		return bindTelemetryCatalog(specs)
	}
	if strings.EqualFold(scope.Type, "system") {
		specs = append(specs, telemetryQuerySpec{
			ID: "conflict_queue_collection_success", Label: "Conflict queue collection", Unit: "state",
			Query: fmt.Sprintf("min(densemem_conflict_queue_collection_success%s)", telemetrySelector(scope, baseLabels)),
		})
	}
	return bindTelemetryCatalog(specs)
}

func telemetryLifecycleWindowedCardSpecs() []telemetryQuerySpec {
	specs := make([]telemetryQuerySpec, 0, len(domain.RelationshipStatuses())+1)
	for _, status := range domain.RelationshipStatuses() {
		specs = append(specs, telemetryQuerySpec{
			ID:     "relationship_transitions_" + status,
			Label:  "Relationship transitions: " + status,
			Unit:   "transitions",
			Ledger: true,
		})
	}
	return bindTelemetryCatalog(append(specs, telemetryQuerySpec{
		ID:     "relationship_corrections",
		Label:  "Accepted Relationship corrections",
		Unit:   "corrections",
		Ledger: true,
	}))
}

func telemetryLifecycleCurrentCardSpecs() []telemetryQuerySpec {
	specs := make([]telemetryQuerySpec, 0, len(domain.RelationshipStatuses()))
	for _, status := range domain.RelationshipStatuses() {
		specs = append(specs, telemetryQuerySpec{
			ID:     "relationships_" + status,
			Label:  "Relationships: " + status,
			Unit:   "relationships",
			Ledger: true,
		})
	}
	return bindTelemetryCatalog(specs)
}

func telemetrySeriesSpecs(selector string, rateWindow string) []telemetryQuerySpec {
	return appendTelemetryQuerySpecs(
		telemetryActivitySeriesSpecs(selector, rateWindow),
		telemetryStateSeriesSpecs(selector),
	)
}

func telemetryActivitySeriesSpecs(selector string, rateWindow string) []telemetryQuerySpec {
	return telemetryActivitySeriesSpecsForAudience(selector, rateWindow, false)
}

func telemetryActivitySeriesSpecsForAudience(selector string, rateWindow string, includeTerminalFailures bool) []telemetryQuerySpec {
	specs := []telemetryQuerySpec{
		{ID: "http_rps", Label: "HTTP requests", Unit: "rps", Query: telemetryRangeSparseCounterRate("densemem_http_requests_total", selector, rateWindow)},
		{ID: "http_errors_rps", Label: "HTTP errors", Unit: "rps", Query: telemetryRangeSparseCounterRate("densemem_http_requests_total", telemetrySelectorWithRaw(selector, `status_class=~"4xx|5xx"`), rateWindow)},
		{ID: "embedding_requests", Label: "Embedding requests", Unit: "requests/s", Query: telemetryRangeSparseCounterRate("densemem_embedding_requests_total", selector, rateWindow)},
		{ID: "embedding_errors", Label: "Embedding errors", Unit: "errors/s", Query: telemetryRangeSparseCounterRate("densemem_embedding_errors_total", telemetrySelectorWithRaw(selector, "code="+telemetryEmbeddingErrorCodeMatcher()), rateWindow)},
		{ID: "embedding_tokens", Label: "Embedding tokens", Unit: "tokens/s", Query: telemetryRangeSparseCounterRate("densemem_embedding_tokens_total", telemetrySelectorWithRaw(selector, `kind="total"`), rateWindow)},
		{ID: "verifier_requests", Label: "Verifier requests", Unit: "requests/s", Query: telemetryRangeSparseCounterRate("densemem_verifier_requests_total", selector, rateWindow)},
		{ID: "verifier_tokens", Label: "Verifier tokens", Unit: "tokens/s", Query: telemetryRangeSparseCounterRate("densemem_verifier_tokens_total", telemetrySelectorWithRaw(selector, `kind="total"`), rateWindow)},
		{ID: "recalls", Label: "Recall requests", Unit: "requests/s", Query: telemetryRangeSparseCounterRate("densemem_recall_requests_total", selector, rateWindow)},
		{ID: "recall_results", Label: "Recall results", Unit: "results", Query: telemetryRangeSparseHistogramAverage("densemem_recall_results", selector, "", rateWindow, 1)},
		{ID: "recall_p95_latency", Label: "Recall p95 latency", Unit: "ms", Query: telemetryRangeSparseHistogramQuantile("densemem_recall_duration_seconds", selector, "", rateWindow, 0.95, 1000)},
		{ID: "llm_recall_used_rate", Label: "LLM recall used", Unit: "percent", Query: telemetryRangeRecallFeedbackRate(selector, `used="true"`, rateWindow)},
		{ID: "llm_recall_answer_supported_rate", Label: "LLM answer supported", Unit: "percent", Query: telemetryRangeRecallFeedbackRate(selector, `answer_supported="true"`, rateWindow)},
		{ID: "llm_recall_quality_score", Label: "LLM recall quality", Unit: "percent", Query: telemetryRangeSparseHistogramAverage("densemem_recall_feedback_quality_score", selector, "", rateWindow, 100)},
		{ID: "llm_recall_missing_context_rate", Label: "LLM missing context", Unit: "percent", Query: telemetryRangeRecallFeedbackRate(selector, `missing_context="true"`, rateWindow)},
		{ID: "llm_recall_irrelevant_rate", Label: "LLM irrelevant recall", Unit: "percent", Query: telemetryRangeRecallFeedbackRate(selector, `irrelevant="true"`, rateWindow)},
		{ID: "dream_feedbacks", Label: "Dream feedback", Unit: "events/s", Query: telemetryRangeSparseCounterRate("densemem_dream_feedback_total", selector, rateWindow)},
		{ID: "conflict_review_duration", Label: "Conflict review", Unit: "ms", Query: telemetryRangeSparseHistogramAverage("densemem_conflict_review_duration_seconds", selector, "", rateWindow, 1000)},
	}
	if !includeTerminalFailures {
		return bindTelemetryCatalog(specs)
	}
	return bindTelemetryCatalog(append(specs,
		telemetryQuerySpec{ID: "assessor_requests", Label: "Assessor requests", Unit: "requests/s", Query: telemetryRangeSparseCounterRate("densemem_assessor_requests_total", selector, rateWindow)},
		telemetryQuerySpec{ID: "assessor_request_failures", Label: "Assessor request failures", Unit: "failures/s", Query: telemetryRangeSparseCounterRate("densemem_assessor_requests_total", telemetrySelectorWithRaw(selector, `outcome=~"provider_error|malformed_exhausted"`), rateWindow)},
		telemetryQuerySpec{ID: "assessor_validation_failures", Label: "Assessor validation failures", Unit: "failures/s", Query: telemetryRangeSparseCounterRate("densemem_assessor_validation_failures_total", selector, rateWindow)},
		telemetryQuerySpec{ID: "assessor_tokens", Label: "Assessor tokens", Unit: "tokens/s", Query: telemetryRangeSparseCounterRate("densemem_assessor_tokens_total", selector, rateWindow)},
		telemetryQuerySpec{ID: "assessor_duration", Label: "Assessor duration", Unit: "ms", Query: telemetryRangeSparseHistogramAverage("densemem_assessor_duration_seconds", selector, "", rateWindow, 1000)},
		telemetryQuerySpec{ID: "assessor_terminal_failures", Label: "Assessor terminal failures", Unit: "failures/s", Query: telemetryRangeSparseCounterRate("densemem_assessor_terminal_failures_total", selector, rateWindow)},
	))
}

func telemetryEmbeddingErrorCodeMatcher() string {
	codes := domain.EmbeddingFailureCodes()
	codes = append(codes, "stale", "lease_lost")
	return `~"` + strings.Join(codes, "|") + `"`
}

func telemetryStateSeriesSpecs(selector string) []telemetryQuerySpec {
	return nil
}

func telemetryIncrease(metric string, scope TelemetryScope, baseLabels map[string]string, extra map[string]string, window string) string {
	return fmt.Sprintf("sum(increase(%s%s[%s]))", metric, telemetrySelector(scope, mergeTelemetryLabels(baseLabels, extra)), window)
}

func telemetryAverageLatency(metric string, scope TelemetryScope, baseLabels map[string]string, window string) string {
	return telemetryHistogramAverage(metric, scope, baseLabels, nil, window, 1000)
}

func telemetryHistogramAverage(metric string, scope TelemetryScope, baseLabels map[string]string, extra map[string]string, window string, multiplier float64) string {
	return telemetrySparseHistogramAverage(metric, scope, baseLabels, extra, window, multiplier)
}

func telemetryHistogramQuantile(metric string, scope TelemetryScope, baseLabels map[string]string, extra map[string]string, window string, quantile float64, multiplier float64) string {
	selector := telemetrySelector(scope, mergeTelemetryLabels(baseLabels, extra))
	return telemetrySparseHistogramQuantileForSelector(metric, selector, window, quantile, multiplier)
}

func telemetryRecallFeedbackRate(scope TelemetryScope, baseLabels map[string]string, extra map[string]string, window string) string {
	matched := telemetryOrZero(telemetrySparseCounterIncrease("densemem_recall_feedback_total", scope, baseLabels, extra, window))
	all := telemetrySparseCounterIncrease("densemem_recall_feedback_total", scope, baseLabels, nil, window)
	return fmt.Sprintf("100 * (%s) / (%s)", matched, all)
}

func telemetrySparseCounterIncrease(metric string, scope TelemetryScope, baseLabels map[string]string, extra map[string]string, window string) string {
	return telemetrySparseCounterIncreaseForSelector(metric, telemetrySelector(scope, mergeTelemetryLabels(baseLabels, extra)), window)
}

func telemetrySparseCounterIncreaseForSelector(metric string, selector string, window string) string {
	ranged := fmt.Sprintf("increase(%s%s[%s])", metric, selector, window)
	first := fmt.Sprintf("min_over_time(%s%s[%s])", metric, selector, window)
	previous := fmt.Sprintf("last_over_time(%s%s[%s] offset %s)", metric, selector, window, window)
	fallback := fmt.Sprintf("((%s unless ignoring(__name__) %s) or (0 * %s))", first, previous, ranged)
	return fmt.Sprintf("sum(%s + %s)", ranged, fallback)
}

func telemetrySparseCounterIncreaseByLabelForSelector(metric, selector, window, label string) string {
	ranged := fmt.Sprintf("increase(%s%s[%s])", metric, selector, window)
	first := fmt.Sprintf("min_over_time(%s%s[%s])", metric, selector, window)
	previous := fmt.Sprintf("last_over_time(%s%s[%s] offset %s)", metric, selector, window, window)
	fallback := fmt.Sprintf("((%s unless ignoring(__name__) %s) or (0 * %s))", first, previous, ranged)
	return fmt.Sprintf("sum by (%s) (%s + %s)", label, ranged, fallback)
}

func telemetryAICost(scope TelemetryScope, baseLabels map[string]string, component string, window string) string {
	extra := map[string]string(nil)
	if component != "" {
		extra = map[string]string{"component": component}
	}
	cost := telemetrySparseCounterIncrease("densemem_ai_operation_cost_usd_total", scope, baseLabels, extra, window)
	unpriced := telemetryOrZero(telemetrySparseCounterIncrease("densemem_ai_operation_unpriced_total", scope, baseLabels, extra, window))
	unpricedByReason := telemetrySparseCounterIncreaseByLabelForSelector("densemem_ai_operation_unpriced_total", telemetrySelector(scope, mergeTelemetryLabels(baseLabels, extra)), window, "reason")
	activity := telemetryAIRequestActivity(scope, baseLabels, component, window)
	completeCost := fmt.Sprintf("((%s) unless ((%s) > 0))", cost, unpriced)
	zeroWhenInactive := fmt.Sprintf("(vector(0) unless (((%s) + (%s)) > 0))", activity, unpriced)
	reasonWhenUnpriced := fmt.Sprintf("(0 * topk(1, (%s) > 0))", unpricedByReason)
	return fmt.Sprintf("(%s or %s or %s)", completeCost, zeroWhenInactive, reasonWhenUnpriced)
}

func telemetryAIRequestActivity(scope TelemetryScope, baseLabels map[string]string, component string, window string) string {
	verifier := telemetryOrZero(telemetrySparseCounterIncrease("densemem_verifier_requests_total", scope, baseLabels, nil, window))
	embedding := telemetryOrZero(telemetrySparseCounterIncrease("densemem_embedding_requests_total", scope, baseLabels, nil, window))
	switch component {
	case observability.AIComponentVerifier:
		return verifier
	case observability.AIComponentEmbedding:
		return embedding
	default:
		return fmt.Sprintf("((%s) + (%s))", verifier, embedding)
	}
}

func telemetrySparseHistogramAverage(metric string, scope TelemetryScope, baseLabels map[string]string, extra map[string]string, window string, multiplier float64) string {
	sum := telemetrySparseCounterIncrease(metric+"_sum", scope, baseLabels, extra, window)
	count := telemetrySparseCounterIncrease(metric+"_count", scope, baseLabels, extra, window)
	return fmt.Sprintf("%g * (%s) / (%s)", multiplier, sum, count)
}

func telemetryRangeHistogramAverage(metric string, selector string, raw string, rateWindow string, multiplier float64) string {
	return telemetryRangeSparseHistogramAverage(metric, selector, raw, rateWindow, multiplier)
}

func telemetryRangeHistogramQuantile(metric string, selector string, raw string, rateWindow string, quantile float64, multiplier float64) string {
	return telemetryRangeSparseHistogramQuantile(metric, selector, raw, rateWindow, quantile, multiplier)
}

func telemetryRangeSparseCounterRate(metric, selector, rateWindow string) string {
	return fmt.Sprintf("(%s) / %d", telemetrySparseCounterIncreaseForSelector(metric, selector, rateWindow), telemetryWindowSeconds(rateWindow))
}

func telemetryRangeSparseHistogramQuantile(metric string, selector string, raw string, rateWindow string, quantile float64, multiplier float64) string {
	if raw != "" {
		selector = telemetrySelectorWithRaw(selector, raw)
	}
	return telemetrySparseHistogramQuantileForSelector(metric, selector, rateWindow, quantile, multiplier)
}

func telemetrySparseHistogramQuantileForSelector(metric, selector, rateWindow string, quantile, multiplier float64) string {
	bucket := metric + "_bucket"
	ranged := fmt.Sprintf("increase(%s%s[%s])", bucket, selector, rateWindow)
	first := fmt.Sprintf("min_over_time(%s%s[%s])", bucket, selector, rateWindow)
	previous := fmt.Sprintf("last_over_time(%s%s[%s] offset %s)", bucket, selector, rateWindow, rateWindow)
	fallback := fmt.Sprintf("((%s unless ignoring(__name__) %s) or (0 * %s))", first, previous, ranged)
	return fmt.Sprintf("%g * histogram_quantile(%g, sum by (le) ((%s) / %d))", multiplier, quantile, ranged+" + "+fallback, telemetryWindowSeconds(rateWindow))
}

func telemetryWindowSeconds(window string) int64 {
	window = strings.TrimSpace(window)
	if len(window) < 2 {
		return 60
	}
	value, err := strconv.ParseInt(window[:len(window)-1], 10, 64)
	if err != nil || value < 1 {
		return 60
	}
	switch window[len(window)-1] {
	case 's':
		return value
	case 'm':
		return value * 60
	case 'h':
		return value * 60 * 60
	case 'd':
		return value * 24 * 60 * 60
	default:
		return 60
	}
}

func telemetryRangeRecallFeedbackRate(selector string, raw string, rateWindow string) string {
	matchedSelector := telemetrySelectorWithRaw(selector, raw)
	matched := telemetryOrZero(telemetrySparseCounterIncreaseForSelector("densemem_recall_feedback_total", matchedSelector, rateWindow))
	all := telemetrySparseCounterIncreaseForSelector("densemem_recall_feedback_total", selector, rateWindow)
	return fmt.Sprintf("100 * (%s) / (%s)", matched, all)
}

func telemetryRangeSparseHistogramAverage(metric string, selector string, raw string, rateWindow string, multiplier float64) string {
	if raw != "" {
		selector = telemetrySelectorWithRaw(selector, raw)
	}
	sum := telemetrySparseCounterIncreaseForSelector(metric+"_sum", selector, rateWindow)
	count := telemetrySparseCounterIncreaseForSelector(metric+"_count", selector, rateWindow)
	return fmt.Sprintf("%g * (%s) / (%s)", multiplier, sum, count)
}

func telemetryOrZero(query string) string {
	return fmt.Sprintf("((%s) or vector(0))", query)
}

func mergeTelemetryLabels(base map[string]string, extra map[string]string) map[string]string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	labels := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		labels[k] = v
	}
	for k, v := range extra {
		labels[k] = v
	}
	return labels
}

func telemetrySelector(scope TelemetryScope, extra map[string]string) string {
	labels := map[string]string{}
	if scope.TeamID != nil {
		labels["team_id"] = scope.TeamID.String()
	}
	if scope.ProfileID != nil {
		labels["profile_id"] = scope.ProfileID.String()
	}
	for k, v := range extra {
		labels[k] = v
	}
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := labels[k]
		if strings.HasPrefix(v, "~") {
			parts = append(parts, fmt.Sprintf(`%s=~%s`, k, strings.TrimPrefix(v, "~")))
			continue
		}
		parts = append(parts, fmt.Sprintf(`%s=%q`, k, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func telemetrySelectorWithRaw(selector string, raw string) string {
	if selector == "" {
		return "{" + raw + "}"
	}
	return strings.TrimSuffix(selector, "}") + "," + raw + "}"
}

func normalizeTelemetryScope(filter TelemetryFilter) (TelemetryScope, error) {
	scope := strings.TrimSpace(filter.Scope)
	if scope == "" {
		scope = "system"
	}
	switch scope {
	case "system":
		return TelemetryScope{Type: scope}, nil
	case "team":
		if filter.TeamID == nil || *filter.TeamID == uuid.Nil {
			return TelemetryScope{}, httperr.New(httperr.VALIDATION_ERROR, "team_id is required for team telemetry")
		}
		return TelemetryScope{Type: scope, TeamID: filter.TeamID}, nil
	case "profile", "self":
		if filter.TeamID == nil || *filter.TeamID == uuid.Nil {
			return TelemetryScope{}, httperr.New(httperr.VALIDATION_ERROR, "team_id is required for profile telemetry")
		}
		if filter.ProfileID == nil || *filter.ProfileID == uuid.Nil {
			return TelemetryScope{}, httperr.New(httperr.VALIDATION_ERROR, "profile_id is required for profile telemetry")
		}
		return TelemetryScope{Type: scope, TeamID: filter.TeamID, ProfileID: filter.ProfileID}, nil
	default:
		return TelemetryScope{}, httperr.New(httperr.VALIDATION_ERROR, "scope must be one of system, team, profile, self")
	}
}

func (s *PrometheusTelemetryService) queryRange(ctx context.Context, query string, from, to time.Time, step time.Duration) ([]TelemetryPoint, error) {
	endpoint, err := url.Parse(s.baseURL + "/api/v1/query_range")
	if err != nil {
		return nil, err
	}
	params := endpoint.Query()
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(from.Unix(), 10))
	params.Set("end", strconv.FormatInt(to.Unix(), 10))
	params.Set("step", strconv.FormatInt(int64(step.Seconds()), 10))
	endpoint.RawQuery = params.Encode()

	var resp prometheusRangeResponse
	if err := s.get(ctx, endpoint.String(), &resp); err != nil {
		return nil, err
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus query_range failed: %s", resp.Error)
	}
	if len(resp.Data.Result) == 0 {
		return []TelemetryPoint{}, nil
	}
	return decodePrometheusPoints(resp.Data.Result[0].Values)
}

type telemetryScalar struct {
	Value     float64
	Available bool
	Labels    map[string]string
}

func (s *PrometheusTelemetryService) queryInstant(ctx context.Context, query string) (telemetryScalar, error) {
	endpoint, err := url.Parse(s.baseURL + "/api/v1/query")
	if err != nil {
		return telemetryScalar{}, err
	}
	params := endpoint.Query()
	params.Set("query", query)
	endpoint.RawQuery = params.Encode()

	var resp prometheusInstantResponse
	if err := s.get(ctx, endpoint.String(), &resp); err != nil {
		return telemetryScalar{}, err
	}
	if resp.Status != "success" {
		return telemetryScalar{}, fmt.Errorf("prometheus query failed: %s", resp.Error)
	}
	if len(resp.Data.Result) == 0 {
		return telemetryScalar{}, nil
	}
	scalar, err := decodePrometheusValue(resp.Data.Result[0].Value)
	if err != nil {
		return telemetryScalar{}, err
	}
	scalar.Labels = resp.Data.Result[0].Metric
	return scalar, nil
}

func (s *PrometheusTelemetryService) get(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("prometheus returned status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type prometheusRangeResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

type prometheusInstantResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func decodePrometheusPoints(values [][]json.RawMessage) ([]TelemetryPoint, error) {
	points := make([]TelemetryPoint, 0, len(values))
	for _, value := range values {
		if len(value) < 2 {
			continue
		}
		ts, scalar, err := decodePrometheusPair(value)
		if err != nil {
			return nil, err
		}
		if !scalar.Available {
			continue
		}
		points = append(points, TelemetryPoint{Timestamp: ts.Format(time.RFC3339), Value: scalar.Value})
	}
	return points, nil
}

func decodePrometheusValue(value []json.RawMessage) (telemetryScalar, error) {
	if len(value) < 2 {
		return telemetryScalar{}, nil
	}
	_, scalar, err := decodePrometheusPair(value)
	return scalar, err
}

func decodePrometheusPair(value []json.RawMessage) (time.Time, telemetryScalar, error) {
	var unixSeconds float64
	if err := json.Unmarshal(value[0], &unixSeconds); err != nil {
		return time.Time{}, telemetryScalar{}, err
	}
	var raw string
	if err := json.Unmarshal(value[1], &raw); err != nil {
		return time.Time{}, telemetryScalar{}, err
	}
	metricValue, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(metricValue) || math.IsInf(metricValue, 0) {
		secs, frac := math.Modf(unixSeconds)
		return time.Unix(int64(secs), int64(frac*1e9)).UTC(), telemetryScalar{}, nil
	}
	secs, frac := math.Modf(unixSeconds)
	return time.Unix(int64(secs), int64(frac*1e9)).UTC(), telemetryScalar{Value: nilIfNegative(metricValue), Available: true}, nil
}

func nilIfNegative(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func telemetryEmptyCards() []TelemetryCard {
	return appendTelemetryCards(telemetryEmptyWindowedCards(), telemetryEmptyCurrentCards())
}

func telemetryEmptyWindowedCards() []TelemetryCard {
	return telemetryEmptyCardsFromSpecs(telemetryWindowedCardSpecs(TelemetryScope{Type: "system"}, nil, "1h"))
}

func telemetryEmptyCurrentCards() []TelemetryCard {
	return telemetryEmptyCardsFromSpecs(telemetryCurrentCardSpecs(TelemetryScope{Type: "system"}, nil))
}

func telemetryEmptySeries() []TelemetrySeries {
	return telemetryEmptySeriesForAudience(false)
}

func telemetryEmptySeriesForAudience(includeTerminalFailures bool) []TelemetrySeries {
	return appendTelemetrySeries(telemetryEmptyActivitySeriesForAudience(includeTerminalFailures), telemetryEmptyStateSeries())
}

func telemetryEmptyActivitySeriesForAudience(includeTerminalFailures bool) []TelemetrySeries {
	return telemetryEmptySeriesFromSpecs(telemetryActivitySeriesSpecsForAudience("", "1m", includeTerminalFailures))
}

func telemetryEmptyStateSeries() []TelemetrySeries {
	return telemetryEmptySeriesFromSpecs(telemetryStateSeriesSpecs(""))
}

func telemetryEmptyCardsFromSpecs(specs []telemetryQuerySpec) []TelemetryCard {
	cards := make([]TelemetryCard, 0, len(specs))
	for _, spec := range specs {
		if spec.Internal {
			continue
		}
		cards = append(cards, TelemetryCard{ID: spec.ID, Label: spec.Label, Unit: spec.Unit})
	}
	return cards
}

func telemetryEmptySeriesFromSpecs(specs []telemetryQuerySpec) []TelemetrySeries {
	series := make([]TelemetrySeries, 0, len(specs))
	for _, spec := range specs {
		series = append(series, TelemetrySeries{ID: spec.ID, Label: spec.Label, Unit: spec.Unit, Points: []TelemetryPoint{}})
	}
	return series
}

func appendTelemetryCards(groups ...[]TelemetryCard) []TelemetryCard {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	out := make([]TelemetryCard, 0, total)
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func appendTelemetrySeries(groups ...[]TelemetrySeries) []TelemetrySeries {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	out := make([]TelemetrySeries, 0, total)
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func appendTelemetryQuerySpecs(groups ...[]telemetryQuerySpec) []telemetryQuerySpec {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	out := make([]telemetryQuerySpec, 0, total)
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}
