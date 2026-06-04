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
}

type TelemetrySnapshot struct {
	Available bool              `json:"available"`
	Message   string            `json:"message,omitempty"`
	Window    TelemetryWindow   `json:"window"`
	Scope     TelemetryScope    `json:"scope"`
	Cards     []TelemetryCard   `json:"cards"`
	Series    []TelemetrySeries `json:"series"`
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
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
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
	baseURL string
	client  *http.Client
	timeout time.Duration
	now     func() time.Time
	logger  observability.LogProvider
}

func NewPrometheusTelemetryService(baseURL string, timeout time.Duration) *PrometheusTelemetryService {
	return NewPrometheusTelemetryServiceWithLogger(baseURL, timeout, nil)
}

func NewPrometheusTelemetryServiceWithLogger(baseURL string, timeout time.Duration, logger observability.LogProvider) *PrometheusTelemetryService {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &PrometheusTelemetryService{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:  &http.Client{Timeout: timeout},
		timeout: timeout,
		now:     time.Now,
		logger:  logger,
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

	nowFn := time.Now
	if s != nil && s.now != nil {
		nowFn = s.now
	}
	to := nowFn().UTC()
	from := to.Add(-windowDef.Duration)
	snapshot := &TelemetrySnapshot{
		Available: false,
		Window: TelemetryWindow{
			Key:           windowKey,
			From:          from.Format(time.RFC3339),
			To:            to.Format(time.RFC3339),
			StepSeconds:   int64(windowDef.Step.Seconds()),
			RetentionDays: TelemetryRetentionDays,
		},
		Scope:  scope,
		Cards:  telemetryEmptyCards(),
		Series: telemetryEmptySeries(),
	}
	if s == nil || s.baseURL == "" {
		snapshot.Message = "telemetry backend is not configured"
		return snapshot, nil
	}

	queryCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	selector := telemetrySelector(scope, nil)
	cardSpecs := telemetryCardSpecs(scope, windowKey)
	cards := make([]TelemetryCard, 0, len(cardSpecs))
	for _, spec := range cardSpecs {
		value, queryErr := s.queryInstant(queryCtx, spec.Query)
		if queryErr != nil {
			s.logQueryFailure("instant", spec, windowKey, scope, queryErr)
			snapshot.Message = "telemetry backend query failed"
			return snapshot, nil
		}
		cards = append(cards, TelemetryCard{ID: spec.ID, Label: spec.Label, Unit: spec.Unit, Value: value})
	}

	seriesSpecs := telemetrySeriesSpecs(selector, windowDef.Rate)
	series := make([]TelemetrySeries, 0, len(seriesSpecs))
	for _, spec := range seriesSpecs {
		points, queryErr := s.queryRange(queryCtx, spec.Query, from, to, windowDef.Step)
		if queryErr != nil {
			s.logQueryFailure("range", spec, windowKey, scope, queryErr)
			snapshot.Message = "telemetry backend query failed"
			return snapshot, nil
		}
		series = append(series, TelemetrySeries{ID: spec.ID, Label: spec.Label, Unit: spec.Unit, Points: points})
	}

	snapshot.Available = true
	snapshot.Message = ""
	snapshot.Cards = cards
	snapshot.Series = series
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
	s.logger.Error("telemetry backend query failed", err, attrs...)
}

type telemetryQuerySpec struct {
	ID    string
	Label string
	Unit  string
	Query string
}

func telemetryCardSpecs(scope TelemetryScope, window string) []telemetryQuerySpec {
	return []telemetryQuerySpec{
		{ID: "http_requests", Label: "HTTP requests", Unit: "requests", Query: telemetryIncrease("densemem_http_requests_total", scope, nil, window)},
		{ID: "http_errors", Label: "HTTP errors", Unit: "requests", Query: telemetryIncrease("densemem_http_requests_total", scope, map[string]string{"status_class": "~\"4xx|5xx\""}, window)},
		{ID: "verifier_tokens", Label: "Verifier tokens", Unit: "tokens", Query: telemetryIncrease("densemem_verifier_tokens_total", scope, map[string]string{"kind": "total"}, window)},
		{ID: "embedding_tokens", Label: "Embedding tokens", Unit: "tokens", Query: telemetryIncrease("densemem_embedding_tokens_total", scope, map[string]string{"kind": "total"}, window)},
		{ID: "recalls", Label: "Recall requests", Unit: "requests", Query: telemetryIncrease("densemem_recall_requests_total", scope, nil, window)},
		{ID: "avg_recall_results", Label: "Avg recall results", Unit: "results", Query: telemetryHistogramAverage("densemem_recall_results", scope, nil, window, 1)},
		{ID: "promotions", Label: "Promotions", Unit: "promotions", Query: telemetryIncrease("densemem_promotion_outcome_total", scope, map[string]string{"outcome": "promoted"}, window)},
		{ID: "promotion_rate", Label: "Promotion rate", Unit: "percent", Query: telemetryPromotionRate(scope, window)},
		{ID: "avg_http_latency", Label: "Avg HTTP latency", Unit: "ms", Query: telemetryAverageLatency("densemem_http_request_duration_seconds", scope, window)},
		{ID: "avg_embedding_latency", Label: "Avg embedding latency", Unit: "ms", Query: telemetryAverageLatency("densemem_embedding_duration_seconds", scope, window)},
		{ID: "avg_verifier_latency", Label: "Avg verifier latency", Unit: "ms", Query: telemetryAverageLatency("densemem_verifier_duration_seconds", scope, window)},
		{ID: "avg_claim_verify_latency", Label: "Avg claim-to-verify", Unit: "ms", Query: telemetryHistogramAverage("densemem_memory_funnel_latency_seconds", scope, map[string]string{"stage": "claim_to_verify"}, window, 1000)},
		{ID: "avg_claim_promotion_latency", Label: "Avg claim-to-promote", Unit: "ms", Query: telemetryHistogramAverage("densemem_memory_funnel_latency_seconds", scope, map[string]string{"stage": "claim_to_promotion"}, window, 1000)},
		{ID: "avg_verify_promotion_latency", Label: "Avg verify-to-promote", Unit: "ms", Query: telemetryHistogramAverage("densemem_memory_funnel_latency_seconds", scope, map[string]string{"stage": "verify_to_promotion"}, window, 1000)},
		{ID: "pending_claims", Label: "Pending claims", Unit: "claims", Query: telemetryGaugeSum("densemem_knowledge_backlog_items", scope, map[string]string{"item": "claim_candidate"})},
		{ID: "validated_claims", Label: "Validated claims", Unit: "claims", Query: telemetryGaugeSum("densemem_knowledge_backlog_items", scope, map[string]string{"item": "claim_validated"})},
		{ID: "disputed_claims", Label: "Disputed claims", Unit: "claims", Query: telemetryGaugeSum("densemem_knowledge_backlog_items", scope, map[string]string{"item": "claim_disputed"})},
		{ID: "revalidation_backlog", Label: "Revalidation backlog", Unit: "facts", Query: telemetryGaugeSum("densemem_knowledge_backlog_items", scope, map[string]string{"item": "fact_needs_revalidation"})},
	}
}

func telemetrySeriesSpecs(selector string, rateWindow string) []telemetryQuerySpec {
	return []telemetryQuerySpec{
		{ID: "http_rps", Label: "HTTP requests", Unit: "rps", Query: fmt.Sprintf("sum(rate(densemem_http_requests_total%s[%s]))", selector, rateWindow)},
		{ID: "http_errors_rps", Label: "HTTP errors", Unit: "rps", Query: fmt.Sprintf("sum(rate(densemem_http_requests_total%s[%s]))", telemetrySelectorWithRaw(selector, `status_class=~"4xx|5xx"`), rateWindow)},
		{ID: "embedding_tokens", Label: "Embedding tokens", Unit: "tokens/s", Query: fmt.Sprintf("sum(rate(densemem_embedding_tokens_total%s[%s]))", telemetrySelectorWithRaw(selector, `kind="total"`), rateWindow)},
		{ID: "verifier_tokens", Label: "Verifier tokens", Unit: "tokens/s", Query: fmt.Sprintf("sum(rate(densemem_verifier_tokens_total%s[%s]))", telemetrySelectorWithRaw(selector, `kind="total"`), rateWindow)},
		{ID: "recalls", Label: "Recall requests", Unit: "requests/s", Query: fmt.Sprintf("sum(rate(densemem_recall_requests_total%s[%s]))", selector, rateWindow)},
		{ID: "promotions", Label: "Promotions", Unit: "promotions/s", Query: fmt.Sprintf("sum(rate(densemem_promotion_outcome_total%s[%s]))", telemetrySelectorWithRaw(selector, `outcome="promoted"`), rateWindow)},
		{ID: "recall_results", Label: "Recall results", Unit: "results", Query: telemetryRangeHistogramAverage("densemem_recall_results", selector, "", rateWindow, 1)},
		{ID: "claim_verify_latency", Label: "Claim-to-verify", Unit: "ms", Query: telemetryRangeHistogramAverage("densemem_memory_funnel_latency_seconds", selector, `stage="claim_to_verify"`, rateWindow, 1000)},
		{ID: "claim_promotion_latency", Label: "Claim-to-promote", Unit: "ms", Query: telemetryRangeHistogramAverage("densemem_memory_funnel_latency_seconds", selector, `stage="claim_to_promotion"`, rateWindow, 1000)},
		{ID: "verify_promotion_latency", Label: "Verify-to-promote", Unit: "ms", Query: telemetryRangeHistogramAverage("densemem_memory_funnel_latency_seconds", selector, `stage="verify_to_promotion"`, rateWindow, 1000)},
		{ID: "pending_claims", Label: "Pending claims", Unit: "claims", Query: telemetryRangeGauge("densemem_knowledge_backlog_items", selector, `item="claim_candidate"`)},
		{ID: "validated_claims", Label: "Validated claims", Unit: "claims", Query: telemetryRangeGauge("densemem_knowledge_backlog_items", selector, `item="claim_validated"`)},
		{ID: "disputed_claims", Label: "Disputed claims", Unit: "claims", Query: telemetryRangeGauge("densemem_knowledge_backlog_items", selector, `item="claim_disputed"`)},
		{ID: "revalidation_backlog", Label: "Revalidation backlog", Unit: "facts", Query: telemetryRangeGauge("densemem_knowledge_backlog_items", selector, `item="fact_needs_revalidation"`)},
	}
}

func telemetryIncrease(metric string, scope TelemetryScope, extra map[string]string, window string) string {
	return fmt.Sprintf("sum(increase(%s%s[%s]))", metric, telemetrySelector(scope, extra), window)
}

func telemetryAverageLatency(metric string, scope TelemetryScope, window string) string {
	return telemetryHistogramAverage(metric, scope, nil, window, 1000)
}

func telemetryHistogramAverage(metric string, scope TelemetryScope, extra map[string]string, window string, multiplier float64) string {
	selector := telemetrySelector(scope, extra)
	return fmt.Sprintf("%g * sum(increase(%s_sum%s[%s])) / sum(increase(%s_count%s[%s]))", multiplier, metric, selector, window, metric, selector, window)
}

func telemetryGaugeSum(metric string, scope TelemetryScope, extra map[string]string) string {
	return fmt.Sprintf("sum(%s%s)", metric, telemetrySelector(scope, extra))
}

func telemetryRangeHistogramAverage(metric string, selector string, raw string, rateWindow string, multiplier float64) string {
	if raw != "" {
		selector = telemetrySelectorWithRaw(selector, raw)
	}
	return fmt.Sprintf("%g * sum(rate(%s_sum%s[%s])) / sum(rate(%s_count%s[%s]))", multiplier, metric, selector, rateWindow, metric, selector, rateWindow)
}

func telemetryRangeGauge(metric string, selector string, raw string) string {
	return fmt.Sprintf("sum(%s%s)", metric, telemetrySelectorWithRaw(selector, raw))
}

func telemetryPromotionRate(scope TelemetryScope, window string) string {
	promoted := telemetryIncrease("densemem_promotion_outcome_total", scope, map[string]string{"outcome": "promoted"}, window)
	all := telemetryIncrease("densemem_promotion_outcome_total", scope, nil, window)
	return fmt.Sprintf("100 * (%s) / (%s)", promoted, all)
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

func (s *PrometheusTelemetryService) queryInstant(ctx context.Context, query string) (float64, error) {
	endpoint, err := url.Parse(s.baseURL + "/api/v1/query")
	if err != nil {
		return 0, err
	}
	params := endpoint.Query()
	params.Set("query", query)
	endpoint.RawQuery = params.Encode()

	var resp prometheusInstantResponse
	if err := s.get(ctx, endpoint.String(), &resp); err != nil {
		return 0, err
	}
	if resp.Status != "success" {
		return 0, fmt.Errorf("prometheus query failed: %s", resp.Error)
	}
	if len(resp.Data.Result) == 0 {
		return 0, nil
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
		ts, metricValue, err := decodePrometheusPair(value)
		if err != nil {
			return nil, err
		}
		points = append(points, TelemetryPoint{Timestamp: ts.Format(time.RFC3339), Value: metricValue})
	}
	return points, nil
}

func decodePrometheusValue(value []json.RawMessage) (float64, error) {
	if len(value) < 2 {
		return 0, nil
	}
	_, metricValue, err := decodePrometheusPair(value)
	return metricValue, err
}

func decodePrometheusPair(value []json.RawMessage) (time.Time, float64, error) {
	var unixSeconds float64
	if err := json.Unmarshal(value[0], &unixSeconds); err != nil {
		return time.Time{}, 0, err
	}
	var raw string
	if err := json.Unmarshal(value[1], &raw); err != nil {
		return time.Time{}, 0, err
	}
	metricValue, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(metricValue) || math.IsInf(metricValue, 0) {
		metricValue = 0
	}
	secs, frac := math.Modf(unixSeconds)
	return time.Unix(int64(secs), int64(frac*1e9)).UTC(), nilIfNegative(metricValue), nil
}

func nilIfNegative(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func telemetryEmptyCards() []TelemetryCard {
	return []TelemetryCard{
		{ID: "http_requests", Label: "HTTP requests", Unit: "requests"},
		{ID: "http_errors", Label: "HTTP errors", Unit: "requests"},
		{ID: "verifier_tokens", Label: "Verifier tokens", Unit: "tokens"},
		{ID: "embedding_tokens", Label: "Embedding tokens", Unit: "tokens"},
		{ID: "recalls", Label: "Recall requests", Unit: "requests"},
		{ID: "avg_recall_results", Label: "Avg recall results", Unit: "results"},
		{ID: "promotions", Label: "Promotions", Unit: "promotions"},
		{ID: "promotion_rate", Label: "Promotion rate", Unit: "percent"},
		{ID: "avg_http_latency", Label: "Avg HTTP latency", Unit: "ms"},
		{ID: "avg_embedding_latency", Label: "Avg embedding latency", Unit: "ms"},
		{ID: "avg_verifier_latency", Label: "Avg verifier latency", Unit: "ms"},
		{ID: "avg_claim_verify_latency", Label: "Avg claim-to-verify", Unit: "ms"},
		{ID: "avg_claim_promotion_latency", Label: "Avg claim-to-promote", Unit: "ms"},
		{ID: "avg_verify_promotion_latency", Label: "Avg verify-to-promote", Unit: "ms"},
		{ID: "pending_claims", Label: "Pending claims", Unit: "claims"},
		{ID: "validated_claims", Label: "Validated claims", Unit: "claims"},
		{ID: "disputed_claims", Label: "Disputed claims", Unit: "claims"},
		{ID: "revalidation_backlog", Label: "Revalidation backlog", Unit: "facts"},
	}
}

func telemetryEmptySeries() []TelemetrySeries {
	emptyPoints := []TelemetryPoint{}
	return []TelemetrySeries{
		{ID: "http_rps", Label: "HTTP requests", Unit: "rps", Points: emptyPoints},
		{ID: "http_errors_rps", Label: "HTTP errors", Unit: "rps", Points: emptyPoints},
		{ID: "embedding_tokens", Label: "Embedding tokens", Unit: "tokens/s", Points: emptyPoints},
		{ID: "verifier_tokens", Label: "Verifier tokens", Unit: "tokens/s", Points: emptyPoints},
		{ID: "recalls", Label: "Recall requests", Unit: "requests/s", Points: emptyPoints},
		{ID: "promotions", Label: "Promotions", Unit: "promotions/s", Points: emptyPoints},
		{ID: "recall_results", Label: "Recall results", Unit: "results", Points: emptyPoints},
		{ID: "claim_verify_latency", Label: "Claim-to-verify", Unit: "ms", Points: emptyPoints},
		{ID: "claim_promotion_latency", Label: "Claim-to-promote", Unit: "ms", Points: emptyPoints},
		{ID: "verify_promotion_latency", Label: "Verify-to-promote", Unit: "ms", Points: emptyPoints},
		{ID: "pending_claims", Label: "Pending claims", Unit: "claims", Points: emptyPoints},
		{ID: "validated_claims", Label: "Validated claims", Unit: "claims", Points: emptyPoints},
		{ID: "disputed_claims", Label: "Disputed claims", Unit: "claims", Points: emptyPoints},
		{ID: "revalidation_backlog", Label: "Revalidation backlog", Unit: "facts", Points: emptyPoints},
	}
}
