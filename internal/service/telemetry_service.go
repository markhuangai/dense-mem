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

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const TelemetryRetentionDays = 30

const (
	TelemetryAudienceOperator = "operator"
	TelemetryAudienceUser     = "user"
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
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Unit      string  `json:"unit"`
	Value     float64 `json:"value"`
	Available bool    `json:"available"`
}

type TelemetrySeries struct {
	ID     string           `json:"id"`
	Label  string           `json:"label"`
	Unit   string           `json:"unit"`
	Points []TelemetryPoint `json:"points"`
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
	includeTerminalFailures := includeCost && strings.EqualFold(scope.Type, "system")

	nowFn := time.Now
	if s != nil && s.now != nil {
		nowFn = s.now
	}
	to := nowFn().UTC()
	from := to.Add(-windowDef.Duration)
	initialWindowedCards := telemetryEmptyCardsFromSpecs(telemetryWindowedCardSpecsForAudience(scope, nil, windowKey, includeCost))
	initialCurrentCards := telemetryEmptyCardsFromSpecs(telemetryCurrentCardSpecsForAudience(scope, nil, includeCost))
	snapshot := &TelemetrySnapshot{
		Available: false,
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
		Series:         telemetryEmptySeriesForAudience(includeTerminalFailures),
		ActivitySeries: telemetryEmptyActivitySeriesForAudience(includeTerminalFailures),
		StateSeries:    telemetryEmptyStateSeries(),
	}
	if s == nil || s.baseURL == "" {
		snapshot.Message = "telemetry backend is not configured"
		return snapshot, nil
	}

	queryCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	baseLabels := s.telemetryBaseLabels()
	selector := telemetrySelector(scope, baseLabels)
	windowedCardSpecs := telemetryWindowedCardSpecsForAudience(scope, baseLabels, windowKey, includeCost)
	windowedCards := make([]TelemetryCard, 0, len(windowedCardSpecs))
	for _, spec := range windowedCardSpecs {
		scalar, queryErr := s.queryInstant(queryCtx, spec.Query)
		if queryErr != nil {
			s.logQueryFailure("instant", spec, windowKey, scope, queryErr)
			snapshot.Message = "telemetry backend query failed"
			return snapshot, nil
		}
		windowedCards = append(windowedCards, TelemetryCard{ID: spec.ID, Label: spec.Label, Unit: spec.Unit, Value: scalar.Value, Available: scalar.Available})
	}

	currentCardSpecs := telemetryCurrentCardSpecsForAudience(scope, baseLabels, includeCost)
	currentCards := make([]TelemetryCard, 0, len(currentCardSpecs))
	for _, spec := range currentCardSpecs {
		scalar, queryErr := s.queryInstant(queryCtx, spec.Query)
		if queryErr != nil {
			s.logQueryFailure("instant", spec, windowKey, scope, queryErr)
			snapshot.Message = "telemetry backend query failed"
			return snapshot, nil
		}
		currentCards = append(currentCards, TelemetryCard{ID: spec.ID, Label: spec.Label, Unit: spec.Unit, Value: scalar.Value, Available: scalar.Available})
	}

	activitySpecs := telemetryActivitySeriesSpecsForAudience(selector, windowDef.Rate, includeTerminalFailures)
	activitySeries := make([]TelemetrySeries, 0, len(activitySpecs))
	for _, spec := range activitySpecs {
		points, queryErr := s.queryRange(queryCtx, spec.Query, from, to, windowDef.Step)
		if queryErr != nil {
			s.logQueryFailure("range", spec, windowKey, scope, queryErr)
			snapshot.Message = "telemetry backend query failed"
			return snapshot, nil
		}
		activitySeries = append(activitySeries, TelemetrySeries{ID: spec.ID, Label: spec.Label, Unit: spec.Unit, Points: points})
	}

	stateSpecs := telemetryStateSeriesSpecs(selector)
	stateSeries := make([]TelemetrySeries, 0, len(stateSpecs))
	for _, spec := range stateSpecs {
		points, queryErr := s.queryRange(queryCtx, spec.Query, from, to, windowDef.Step)
		if queryErr != nil {
			s.logQueryFailure("range", spec, windowKey, scope, queryErr)
			snapshot.Message = "telemetry backend query failed"
			return snapshot, nil
		}
		stateSeries = append(stateSeries, TelemetrySeries{ID: spec.ID, Label: spec.Label, Unit: spec.Unit, Points: points})
	}

	snapshot.Available = true
	snapshot.Message = ""
	snapshot.WindowedCards = windowedCards
	snapshot.CurrentCards = currentCards
	snapshot.Cards = appendTelemetryCards(windowedCards, currentCards)
	snapshot.ActivitySeries = activitySeries
	snapshot.StateSeries = stateSeries
	snapshot.Series = appendTelemetrySeries(activitySeries, stateSeries)
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
	ID    string
	Label string
	Unit  string
	Query string
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
		{ID: "embedding_errors", Label: "Embedding errors", Unit: "errors", Query: telemetrySparseCounterIncrease("densemem_embedding_errors_total", scope, baseLabels, nil, window)},
		{ID: "embedding_tokens", Label: "Embedding tokens", Unit: "tokens", Query: telemetrySparseCounterIncrease("densemem_embedding_tokens_total", scope, baseLabels, map[string]string{"kind": "total"}, window)},
		{ID: "verifier_requests", Label: "Verifier requests", Unit: "requests", Query: telemetrySparseCounterIncrease("densemem_verifier_requests_total", scope, baseLabels, nil, window)},
		{ID: "verifier_tokens", Label: "Verifier tokens", Unit: "tokens", Query: telemetrySparseCounterIncrease("densemem_verifier_tokens_total", scope, baseLabels, map[string]string{"kind": "total"}, window)},
		{ID: "verify_verdicts", Label: "Verify verdicts", Unit: "verdicts", Query: telemetrySparseCounterIncrease("densemem_verify_verdict_total", scope, baseLabels, nil, window)},
		{ID: "recalls", Label: "Recall requests", Unit: "requests", Query: telemetrySparseCounterIncrease("densemem_recall_requests_total", scope, baseLabels, nil, window)},
		{ID: "avg_recall_results", Label: "Avg recall results", Unit: "results", Query: telemetryHistogramAverage("densemem_recall_results", scope, baseLabels, nil, window, 1)},
		{ID: "p95_recall_latency", Label: "P95 recall latency", Unit: "ms", Query: telemetryHistogramQuantile("densemem_recall_duration_seconds", scope, baseLabels, nil, window, 0.95, 1000)},
		{ID: "llm_recall_used_rate", Label: "LLM recall used", Unit: "percent", Query: telemetryRecallFeedbackRate(scope, baseLabels, map[string]string{"used": "true"}, window)},
		{ID: "llm_recall_answer_supported_rate", Label: "LLM answer supported", Unit: "percent", Query: telemetryRecallFeedbackRate(scope, baseLabels, map[string]string{"answer_supported": "true"}, window)},
		{ID: "llm_recall_quality_score", Label: "LLM recall quality", Unit: "percent", Query: telemetrySparseHistogramAverage("densemem_recall_feedback_quality_score", scope, baseLabels, nil, window, 100)},
		{ID: "llm_recall_missing_context_rate", Label: "LLM missing context", Unit: "percent", Query: telemetryRecallFeedbackRate(scope, baseLabels, map[string]string{"missing_context": "true"}, window)},
		{ID: "llm_recall_irrelevant_rate", Label: "LLM irrelevant recall", Unit: "percent", Query: telemetryRecallFeedbackRate(scope, baseLabels, map[string]string{"irrelevant": "true"}, window)},
		{ID: "dream_feedbacks", Label: "Dream feedback", Unit: "events", Query: telemetrySparseCounterIncrease("densemem_dream_feedback_total", scope, baseLabels, nil, window)},
		{ID: "dream_promote_candidates", Label: "Dream promote candidates", Unit: "events", Query: telemetrySparseCounterIncrease("densemem_dream_feedback_total", scope, baseLabels, map[string]string{"decision": "promote_candidate", "outcome": "ok"}, window)},
		{ID: "avg_http_latency", Label: "Avg HTTP latency", Unit: "ms", Query: telemetryAverageLatency("densemem_http_request_duration_seconds", scope, baseLabels, window)},
		{ID: "avg_embedding_latency", Label: "Avg embedding latency", Unit: "ms", Query: telemetrySparseHistogramAverage("densemem_embedding_duration_seconds", scope, baseLabels, nil, window, 1000)},
		{ID: "avg_verifier_latency", Label: "Avg verifier latency", Unit: "ms", Query: telemetrySparseHistogramAverage("densemem_verifier_duration_seconds", scope, baseLabels, nil, window, 1000)},
		{ID: "avg_conflict_review_duration", Label: "Avg conflict review", Unit: "ms", Query: telemetrySparseHistogramAverage("densemem_conflict_review_duration_seconds", scope, baseLabels, nil, window, 1000)},
		{ID: "remember_acknowledgements", Label: "Remember acknowledgements", Unit: "requests", Query: telemetrySparseCounterIncrease("densemem_remember_acknowledgements_total", scope, baseLabels, nil, window)},
		{ID: "avg_remember_acknowledgement_latency", Label: "Avg remember acknowledgement", Unit: "ms", Query: telemetrySparseHistogramAverage("densemem_remember_acknowledgement_duration_seconds", scope, baseLabels, nil, window, 1000)},
		{ID: "p95_remember_acknowledgement_latency", Label: "P95 remember acknowledgement", Unit: "ms", Query: telemetryHistogramQuantile("densemem_remember_acknowledgement_duration_seconds", scope, baseLabels, nil, window, 0.95, 1000)},
		{ID: "remember_first_dispositions", Label: "First remember dispositions", Unit: "requests", Query: telemetrySparseCounterIncrease("densemem_remember_first_disposition_total", scope, baseLabels, nil, window)},
		{ID: "p95_remember_first_disposition_latency", Label: "P95 first disposition", Unit: "ms", Query: telemetryHistogramQuantile("densemem_remember_first_disposition_duration_seconds", scope, baseLabels, nil, window, 0.95, 1000)},
	}
	if !includeCost {
		return specs
	}
	costSpecs := []telemetryQuerySpec{
		{ID: "ai_cost_usd", Label: "AI cost", Unit: "USD", Query: telemetryAICost(scope, baseLabels, "", window)},
		telemetryQuerySpec{ID: "verifier_cost_usd", Label: "Verifier cost", Unit: "USD", Query: telemetryAICost(scope, baseLabels, observability.AIComponentVerifier, window)},
		telemetryQuerySpec{ID: "embedding_cost_usd", Label: "Embedding cost", Unit: "USD", Query: telemetryAICost(scope, baseLabels, observability.AIComponentEmbedding, window)},
	}
	if !strings.EqualFold(scope.Type, "system") {
		return append(specs, costSpecs...)
	}
	return append(append(specs, telemetryQuerySpec{
		ID: "assessor_terminal_failures", Label: "Assessor terminal failures", Unit: "failures", Query: telemetrySparseCounterIncrease("densemem_assessor_terminal_failures_total", scope, baseLabels, nil, window),
	}), costSpecs...)
}

func telemetryCurrentCardSpecs(scope TelemetryScope, baseLabels map[string]string) []telemetryQuerySpec {
	return telemetryCurrentCardSpecsForAudience(scope, baseLabels, false)
}

func telemetryCurrentCardSpecsForAudience(scope TelemetryScope, baseLabels map[string]string, operator bool) []telemetryQuerySpec {
	if !operator || !strings.EqualFold(scope.Type, "system") {
		return nil
	}
	return []telemetryQuerySpec{{
		ID: "conflict_queue_collection_success", Label: "Conflict queue collection", Unit: "state",
		Query: fmt.Sprintf("min(densemem_conflict_queue_collection_success%s)", telemetrySelector(scope, baseLabels)),
	}}
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
		{ID: "http_rps", Label: "HTTP requests", Unit: "rps", Query: fmt.Sprintf("sum(rate(densemem_http_requests_total%s[%s]))", selector, rateWindow)},
		{ID: "http_errors_rps", Label: "HTTP errors", Unit: "rps", Query: fmt.Sprintf("sum(rate(densemem_http_requests_total%s[%s]))", telemetrySelectorWithRaw(selector, `status_class=~"4xx|5xx"`), rateWindow)},
		{ID: "embedding_requests", Label: "Embedding requests", Unit: "requests/s", Query: fmt.Sprintf("sum(rate(densemem_embedding_requests_total%s[%s]))", selector, rateWindow)},
		{ID: "embedding_errors", Label: "Embedding errors", Unit: "errors/s", Query: fmt.Sprintf("sum(rate(densemem_embedding_errors_total%s[%s]))", selector, rateWindow)},
		{ID: "embedding_tokens", Label: "Embedding tokens", Unit: "tokens/s", Query: fmt.Sprintf("sum(rate(densemem_embedding_tokens_total%s[%s]))", telemetrySelectorWithRaw(selector, `kind="total"`), rateWindow)},
		{ID: "verifier_requests", Label: "Verifier requests", Unit: "requests/s", Query: fmt.Sprintf("sum(rate(densemem_verifier_requests_total%s[%s]))", selector, rateWindow)},
		{ID: "verifier_tokens", Label: "Verifier tokens", Unit: "tokens/s", Query: fmt.Sprintf("sum(rate(densemem_verifier_tokens_total%s[%s]))", telemetrySelectorWithRaw(selector, `kind="total"`), rateWindow)},
		{ID: "verify_verdicts", Label: "Verify verdicts", Unit: "verdicts/s", Query: fmt.Sprintf("sum(rate(densemem_verify_verdict_total%s[%s]))", selector, rateWindow)},
		{ID: "recalls", Label: "Recall requests", Unit: "requests/s", Query: fmt.Sprintf("sum(rate(densemem_recall_requests_total%s[%s]))", selector, rateWindow)},
		{ID: "recall_results", Label: "Recall results", Unit: "results", Query: telemetryRangeHistogramAverage("densemem_recall_results", selector, "", rateWindow, 1)},
		{ID: "recall_p95_latency", Label: "Recall p95 latency", Unit: "ms", Query: telemetryRangeHistogramQuantile("densemem_recall_duration_seconds", selector, "", rateWindow, 0.95, 1000)},
		{ID: "llm_recall_used_rate", Label: "LLM recall used", Unit: "percent", Query: telemetryRangeRecallFeedbackRate(selector, `used="true"`, rateWindow)},
		{ID: "llm_recall_answer_supported_rate", Label: "LLM answer supported", Unit: "percent", Query: telemetryRangeRecallFeedbackRate(selector, `answer_supported="true"`, rateWindow)},
		{ID: "llm_recall_quality_score", Label: "LLM recall quality", Unit: "percent", Query: telemetryRangeSparseHistogramAverage("densemem_recall_feedback_quality_score", selector, "", rateWindow, 100)},
		{ID: "llm_recall_missing_context_rate", Label: "LLM missing context", Unit: "percent", Query: telemetryRangeRecallFeedbackRate(selector, `missing_context="true"`, rateWindow)},
		{ID: "llm_recall_irrelevant_rate", Label: "LLM irrelevant recall", Unit: "percent", Query: telemetryRangeRecallFeedbackRate(selector, `irrelevant="true"`, rateWindow)},
		{ID: "dream_feedbacks", Label: "Dream feedback", Unit: "events/s", Query: fmt.Sprintf("sum(rate(densemem_dream_feedback_total%s[%s]))", selector, rateWindow)},
		{ID: "dream_promote_candidates", Label: "Dream promote candidates", Unit: "events/s", Query: fmt.Sprintf("sum(rate(densemem_dream_feedback_total%s[%s]))", telemetrySelectorWithRaw(selector, `decision="promote_candidate",outcome="ok"`), rateWindow)},
		{ID: "conflict_review_duration", Label: "Conflict review", Unit: "ms", Query: telemetryRangeHistogramAverage("densemem_conflict_review_duration_seconds", selector, "", rateWindow, 1000)},
	}
	if !includeTerminalFailures {
		return specs
	}
	return append(specs, telemetryQuerySpec{
		ID:    "assessor_terminal_failures",
		Label: "Assessor terminal failures",
		Unit:  "failures/s",
		Query: fmt.Sprintf("sum(rate(densemem_assessor_terminal_failures_total%s[%s]))", selector, rateWindow),
	})
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
	selector := telemetrySelector(scope, mergeTelemetryLabels(baseLabels, extra))
	return fmt.Sprintf("%g * sum(increase(%s_sum%s[%s])) / sum(increase(%s_count%s[%s]))", multiplier, metric, selector, window, metric, selector, window)
}

func telemetryHistogramQuantile(metric string, scope TelemetryScope, baseLabels map[string]string, extra map[string]string, window string, quantile float64, multiplier float64) string {
	selector := telemetrySelector(scope, mergeTelemetryLabels(baseLabels, extra))
	return fmt.Sprintf("%g * histogram_quantile(%g, sum(rate(%s_bucket%s[%s])) by (le))", multiplier, quantile, metric, selector, window)
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
	offset := fmt.Sprintf("%s%s offset %s", metric, selector, window)
	current := fmt.Sprintf("%s%s", metric, selector)
	sampleCount := fmt.Sprintf("count_over_time(%s%s[%s])", metric, selector, window)
	targetScrapeCount := fmt.Sprintf("count_over_time(up[%s])", window)
	fallback := fmt.Sprintf("((%s unless %s) or (%s * (%s >= bool 0) * (%s < bool on(job, instance) group_left() %s)) or (0 * %s))", first, offset, first, current, sampleCount, targetScrapeCount, ranged)
	return fmt.Sprintf("sum(%s + %s)", ranged, fallback)
}

func telemetryAICost(scope TelemetryScope, baseLabels map[string]string, component string, window string) string {
	extra := map[string]string(nil)
	if component != "" {
		extra = map[string]string{"component": component}
	}
	cost := telemetrySparseCounterIncrease("densemem_ai_operation_cost_usd_total", scope, baseLabels, extra, window)
	unpriced := telemetryOrZero(telemetrySparseCounterIncrease("densemem_ai_operation_unpriced_total", scope, baseLabels, extra, window))
	activity := telemetryAIRequestActivity(scope, baseLabels, component, window)
	completeCost := fmt.Sprintf("((%s) unless ((%s) > 0))", cost, unpriced)
	zeroWhenInactive := fmt.Sprintf("(vector(0) unless (((%s) + (%s)) > 0))", activity, unpriced)
	return fmt.Sprintf("(%s or %s)", completeCost, zeroWhenInactive)
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
	if raw != "" {
		selector = telemetrySelectorWithRaw(selector, raw)
	}
	return fmt.Sprintf("%g * sum(rate(%s_sum%s[%s])) / sum(rate(%s_count%s[%s]))", multiplier, metric, selector, rateWindow, metric, selector, rateWindow)
}

func telemetryRangeHistogramQuantile(metric string, selector string, raw string, rateWindow string, quantile float64, multiplier float64) string {
	if raw != "" {
		selector = telemetrySelectorWithRaw(selector, raw)
	}
	return fmt.Sprintf("%g * histogram_quantile(%g, sum(rate(%s_bucket%s[%s])) by (le))", multiplier, quantile, metric, selector, rateWindow)
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
	return decodePrometheusValue(resp.Data.Result[0].Value)
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
			Value []json.RawMessage `json:"value"`
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
