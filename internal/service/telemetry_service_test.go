package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestPrometheusTelemetryService_UnconfiguredReturnsUnavailableSnapshot(t *testing.T) {
	svc := NewPrometheusTelemetryService("", time.Second)
	svc.now = func() time.Time { return time.Date(2026, 5, 2, 13, 0, 0, 0, time.UTC) }

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "30m", Scope: "system"})

	require.NoError(t, err)
	require.False(t, snapshot.Available)
	require.Equal(t, TelemetrySnapshotUnavailable, snapshot.Status)
	require.NotEmpty(t, snapshot.GeneratedAt)
	require.Equal(t, "30m", snapshot.Window.Key)
	require.Equal(t, "telemetry backend is not configured", snapshot.Message)
	require.NotEmpty(t, snapshot.Cards)
	require.NotEmpty(t, snapshot.WindowedCards)
	require.NotEmpty(t, snapshot.CurrentCards)
	require.NotEmpty(t, snapshot.Series)
	require.NotEmpty(t, snapshot.ActivitySeries)
	require.Empty(t, snapshot.StateSeries)
	require.False(t, snapshot.Cards[0].Available)

	payload, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.Contains(t, string(payload), `"points":[]`)
	require.NotContains(t, string(payload), `"points":null`)
	require.Contains(t, string(payload), `"windowed_cards"`)
	require.Contains(t, string(payload), `"current_cards"`)
	require.Contains(t, string(payload), `"activity_series"`)
	require.Contains(t, string(payload), `"state_series"`)
}

func TestPrometheusTelemetryService_UnconfiguredPrometheusKeepsLifecycleCards(t *testing.T) {
	active := "active"
	lifecycle := &telemetryLifecycleReaderStub{snapshot: repository.TelemetryLifecycleSnapshot{
		Transitions: map[string]float64{active: 2},
		Corrections: 1,
		Current:     map[string]float64{active: 3},
	}}
	svc := NewPrometheusTelemetryService("", time.Second)
	svc.now = func() time.Time { return time.Date(2026, 5, 2, 13, 0, 0, 0, time.UTC) }
	svc.SetLifecycleReader(lifecycle)

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m", Scope: "system", Audience: TelemetryAudienceOperator})

	require.NoError(t, err)
	require.True(t, snapshot.Available)
	require.Equal(t, TelemetrySnapshotDegraded, snapshot.Status)
	require.Equal(t, "telemetry backend is not configured", snapshot.Message)
	transition := telemetrySpecByID(snapshot.WindowedCards, "relationship_transitions_active")
	require.NotNil(t, transition)
	require.Equal(t, TelemetryItemReady, transition.Status)
	require.Equal(t, 2.0, transition.Value)
	current := telemetrySpecByID(snapshot.CurrentCards, "relationships_active")
	require.NotNil(t, current)
	require.Equal(t, TelemetryItemReady, current.Status)
	require.Equal(t, 3.0, current.Value)
	httpRequests := telemetrySpecByID(snapshot.WindowedCards, "http_requests")
	require.NotNil(t, httpRequests)
	require.Equal(t, TelemetryItemUnavailable, httpRequests.Status)
	require.Equal(t, "source_unconfigured", httpRequests.ReasonCode)
	require.Equal(t, 1, lifecycle.calls)
}

func TestTelemetryCostCardsAreOperatorOnly(t *testing.T) {
	scope := TelemetryScope{Type: "system"}
	operatorIDs := telemetryQuerySpecIDs(telemetryWindowedCardSpecsForAudience(scope, nil, "1h", true))
	userIDs := telemetryQuerySpecIDs(telemetryWindowedCardSpecsForAudience(scope, nil, "1h", false))

	require.Subset(t, operatorIDs, []string{
		"ai_cost_usd",
		"verifier_cost_usd",
		"embedding_cost_usd",
	})
	for _, id := range []string{"ai_cost_usd", "verifier_cost_usd", "embedding_cost_usd"} {
		require.NotContains(t, userIDs, id)
	}
	require.Contains(t, operatorIDs, "assessor_terminal_failures")
	require.NotContains(t, userIDs, "assessor_terminal_failures")
	for _, id := range []string{"recall_embedding_cost_usd", "background_embedding_cost_usd", "ai_unpriced_operations"} {
		require.NotContains(t, operatorIDs, id)
		require.NotContains(t, userIDs, id)
	}

	operator := telemetryWindowedCardSpecsForAudience(scope, nil, "1h", true)
	verifierCost := telemetryQuerySpecByID(operator, "verifier_cost_usd")
	require.NotNil(t, verifierCost)
	require.Equal(t, "USD", verifierCost.Unit)
	require.Contains(t, verifierCost.Query, `component="verifier"`)
	require.Contains(t, verifierCost.Query, "densemem_ai_operation_cost_usd_total")
	require.Contains(t, verifierCost.Query, "densemem_verifier_requests_total")
	require.Contains(t, verifierCost.Query, "densemem_ai_operation_unpriced_total")
	require.Contains(t, verifierCost.Query, "vector(0) unless")

	embeddingCost := telemetryQuerySpecByID(operator, "embedding_cost_usd")
	require.NotNil(t, embeddingCost)
	require.Equal(t, "Embedding cost", embeddingCost.Label)
	require.Equal(t, "USD", embeddingCost.Unit)
	require.Contains(t, embeddingCost.Query, `component="embedding"`)
	require.Contains(t, embeddingCost.Query, "densemem_ai_operation_cost_usd_total")
	require.Contains(t, embeddingCost.Query, "densemem_embedding_requests_total")
	require.Contains(t, embeddingCost.Query, "densemem_ai_operation_unpriced_total")
	require.Contains(t, embeddingCost.Query, "vector(0) unless")
	require.NotContains(t, embeddingCost.Query, "operation=")

	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	profileID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	profileEmbeddingCost := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(TelemetryScope{
		Type:      "profile",
		TeamID:    &teamID,
		ProfileID: &profileID,
	}, nil, "1h", true), "embedding_cost_usd")
	require.NotNil(t, profileEmbeddingCost)
	require.Contains(t, profileEmbeddingCost.Query, `team_id="11111111-1111-4111-8111-111111111111"`)
	require.Contains(t, profileEmbeddingCost.Query, `profile_id="22222222-2222-4222-8222-222222222222"`)
	require.NotContains(t, profileEmbeddingCost.Query, `operation="background_embedding"`)
}

func TestTelemetryTerminalFailureSeriesIsOperatorSystemOnly(t *testing.T) {
	system := TelemetryScope{Type: "system"}
	operatorCard := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(system, nil, "1h", true), "assessor_terminal_failures")
	require.NotNil(t, operatorCard)
	require.Contains(t, operatorCard.Query, "densemem_assessor_terminal_failures_total")
	require.NotContains(t, operatorCard.Query, "team_id=")

	operatorSeries := telemetryQuerySpecByID(telemetryActivitySeriesSpecsForAudience(`{job="dense-mem"}`, "1m", true), "assessor_terminal_failures")
	require.NotNil(t, operatorSeries)
	require.Equal(t, "failures/s", operatorSeries.Unit)
	require.Contains(t, operatorSeries.Query, `densemem_assessor_terminal_failures_total{job="dense-mem"}`)

	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	team := TelemetryScope{Type: "team", TeamID: &teamID}
	teamCard := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(team, nil, "1h", true), "assessor_terminal_failures")
	require.Nil(t, teamCard)

	profileID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	profile := TelemetryScope{Type: "profile", TeamID: &teamID, ProfileID: &profileID}
	profileCard := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(profile, nil, "1h", true), "assessor_terminal_failures")
	require.Nil(t, profileCard)

	teamCost := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(team, nil, "1h", true), "verifier_cost_usd")
	require.NotNil(t, teamCost)
	profileCost := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(profile, nil, "1h", true), "verifier_cost_usd")
	require.NotNil(t, profileCost)
}

func TestPrometheusTelemetryService_QueriesTypedScope(t *testing.T) {
	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	profileID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	var queries []string

	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("query"))
		switch r.URL.Path {
		case "/api/v1/query":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"value":[1770000000,"42"]}]}}`))
		case "/api/v1/query_range":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"values":[[1770000000,"0.5"],[1770000060,"1.25"]]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prom.Close()

	svc := NewPrometheusTelemetryServiceWithJobAndLogger(prom.URL, time.Second, "dense-mem-demo", nil)
	svc.now = func() time.Time { return time.Unix(1770000060, 0).UTC() }

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{
		Window:    "1h",
		Scope:     "profile",
		TeamID:    &teamID,
		ProfileID: &profileID,
		Audience:  TelemetryAudienceOperator,
	})

	require.NoError(t, err)
	require.True(t, snapshot.Available)
	require.Equal(t, 42.0, snapshot.Cards[0].Value)
	require.True(t, snapshot.Cards[0].Available)
	require.Equal(t, 2, len(snapshot.Series[0].Points))
	require.Len(t, snapshot.Cards, len(snapshot.WindowedCards)+len(snapshot.CurrentCards))
	require.Len(t, snapshot.Series, len(snapshot.ActivitySeries)+len(snapshot.StateSeries))
	require.NotNil(t, telemetrySpecByID(snapshot.WindowedCards, "http_requests"))
	require.NotNil(t, telemetrySpecByID(snapshot.WindowedCards, "avg_conflict_review_duration"))
	require.NotEmpty(t, snapshot.CurrentCards)
	require.NotNil(t, telemetrySeriesByID(snapshot.ActivitySeries, "conflict_review_duration"))
	require.Empty(t, snapshot.StateSeries)
	require.Condition(t, func() bool {
		for _, query := range queries {
			if strings.Contains(query, `team_id="`+teamID.String()+`"`) && strings.Contains(query, `profile_id="`+profileID.String()+`"`) {
				return true
			}
		}
		return false
	})
	require.Condition(t, func() bool {
		for _, query := range queries {
			if !strings.Contains(query, `job="dense-mem-demo"`) {
				return false
			}
		}
		return len(queries) > 0
	})
	var httpErrorCardQuery string
	for _, query := range queries {
		if strings.Contains(query, `sum(increase(densemem_http_requests_total`) && strings.Contains(query, "status_class") {
			httpErrorCardQuery = query
			break
		}
	}
	require.NotEmpty(t, httpErrorCardQuery)
	require.Contains(t, httpErrorCardQuery, `status_class=~"4xx|5xx"`)
	require.NotContains(t, httpErrorCardQuery, `status_class="4xx|5xx"`)
	require.Condition(t, func() bool {
		for _, query := range queries {
			if strings.Contains(query, `densemem_recall_feedback_total`) &&
				strings.Contains(query, `used="true"`) {
				return true
			}
		}
		return false
	})
	require.Condition(t, func() bool {
		for _, query := range queries {
			if strings.Contains(query, `densemem_dream_feedback_total`) {
				return true
			}
		}
		return false
	})
	require.Condition(t, func() bool {
		for _, query := range queries {
			if strings.Contains(query, `histogram_quantile(0.95`) &&
				strings.Contains(query, `densemem_recall_duration_seconds_bucket`) {
				return true
			}
		}
		return false
	})
}

func TestPrometheusTelemetryService_RejectsInvalidWindow(t *testing.T) {
	svc := NewPrometheusTelemetryService("", time.Second)

	_, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "2h", Scope: "system"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "window must be one of")
}

func TestPrometheusTelemetryService_SnapshotTimeoutBoundsSequentialQueries(t *testing.T) {
	var requests atomic.Int32
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		time.Sleep(25 * time.Millisecond)
		switch r.URL.Path {
		case "/api/v1/query":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"value":[1770000000,"42"]}]}}`))
		case "/api/v1/query_range":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"values":[[1770000000,"0.5"]]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prom.Close()

	svc := NewPrometheusTelemetryService(prom.URL, 60*time.Millisecond)
	svc.now = func() time.Time { return time.Unix(1770000060, 0).UTC() }

	start := time.Now()
	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m", Scope: "system"})
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.True(t, snapshot.Available)
	require.Equal(t, TelemetrySnapshotDegraded, snapshot.Status)
	require.Equal(t, "some telemetry sources are unavailable", snapshot.Message)
	require.Less(t, elapsed, 250*time.Millisecond)
	require.LessOrEqual(t, requests.Load(), int32(len(telemetryCardSpecs(TelemetryScope{Type: "system"}, nil, "15m"))))
}

func TestPrometheusTelemetryService_ValidationAndDecodeBranches(t *testing.T) {
	svc := NewPrometheusTelemetryService(" https://prom.example.test/ ", 0)
	require.Equal(t, "https://prom.example.test", svc.baseURL)
	require.Equal(t, 5*time.Second, svc.client.Timeout)
	require.Equal(t, 5*time.Second, svc.timeout)

	snapshot, err := (*PrometheusTelemetryService)(nil).Snapshot(context.Background(), TelemetryFilter{})
	require.NoError(t, err)
	require.False(t, snapshot.Available)
	require.Equal(t, "1h", snapshot.Window.Key)
	require.Equal(t, "system", snapshot.Scope.Type)

	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	profileID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	scope, err := normalizeTelemetryScope(TelemetryFilter{Scope: "team", TeamID: &teamID})
	require.NoError(t, err)
	require.Equal(t, `{"team_id":"11111111-1111-4111-8111-111111111111"}`, mustMarshalStringMap(t, scopeLabels(scope)))
	require.Equal(t, `{kind="total"}`, telemetrySelectorWithRaw("", `kind="total"`))
	require.Equal(t, `{status_class=~"4xx|5xx"}`, telemetrySelector(TelemetryScope{}, map[string]string{"status_class": `~"4xx|5xx"`}))
	require.Equal(t, `{job="dense-mem-demo",status_class=~"4xx|5xx"}`, telemetrySelector(TelemetryScope{}, mergeTelemetryLabels(map[string]string{"job": "dense-mem-demo"}, map[string]string{"status_class": `~"4xx|5xx"`})))
	feedbackRateQuery := telemetryRecallFeedbackRate(TelemetryScope{}, nil, map[string]string{"used": "true"}, "1h")
	require.Contains(t, feedbackRateQuery, `min_over_time(densemem_recall_feedback_total{used="true"}[1h]) unless ignoring(__name__) last_over_time(densemem_recall_feedback_total{used="true"}[1h] offset 1h)`)
	require.Contains(t, feedbackRateQuery, `0 * increase(densemem_recall_feedback_total{used="true"}[1h])`)
	require.NotContains(t, feedbackRateQuery, `count_over_time(densemem_recall_feedback_total{used="true"}[1h]) < bool on(job, instance) group_left() count_over_time(up[1h])`)
	require.Contains(t, feedbackRateQuery, `min_over_time(densemem_recall_feedback_total[1h]) unless ignoring(__name__) last_over_time(densemem_recall_feedback_total[1h] offset 1h)`)
	require.Contains(t, feedbackRateQuery, `0 * increase(densemem_recall_feedback_total[1h])`)
	qualityQuery := telemetrySparseHistogramAverage("densemem_recall_feedback_quality_score", TelemetryScope{}, nil, nil, "1h", 100)
	require.Contains(t, qualityQuery, `min_over_time(densemem_recall_feedback_quality_score_sum[1h]) unless ignoring(__name__) last_over_time(densemem_recall_feedback_quality_score_sum[1h] offset 1h)`)
	require.Contains(t, qualityQuery, `min_over_time(densemem_recall_feedback_quality_score_count[1h]) unless ignoring(__name__) last_over_time(densemem_recall_feedback_quality_score_count[1h] offset 1h)`)
	require.Contains(t, feedbackRateQuery, `or vector(0)`)
	rangeFeedbackQuery := telemetryRangeRecallFeedbackRate(`{job="dense-mem"}`, `used="true"`, "1m")
	require.Contains(t, rangeFeedbackQuery, `min_over_time(densemem_recall_feedback_total{job="dense-mem",used="true"}[1m]) unless ignoring(__name__) last_over_time(densemem_recall_feedback_total{job="dense-mem",used="true"}[1m] offset 1m)`)
	require.NotContains(t, rangeFeedbackQuery, `count_over_time(densemem_recall_feedback_total{job="dense-mem",used="true"}[1m]) < bool on(job, instance) group_left() count_over_time(up[1m])`)
	require.Condition(t, func() bool {
		for _, spec := range telemetryCardSpecs(TelemetryScope{}, nil, "1h") {
			if spec.ID == "dream_feedbacks" {
				return strings.Contains(spec.Query, `min_over_time(densemem_dream_feedback_total[1h])`)
			}
		}
		return false
	})
	require.Condition(t, func() bool {
		for _, spec := range telemetrySeriesSpecs(`{job="dense-mem"}`, "1m") {
			if spec.ID == "dream_feedbacks" {
				return strings.Contains(spec.Query, "densemem_dream_feedback_total")
			}
		}
		return false
	})
	require.Contains(t, telemetryRangeHistogramQuantile("densemem_recall_duration_seconds", "", "", "1m", 0.95, 1000), "histogram_quantile(0.95")
	quantileQuery := telemetryRangeHistogramQuantile("densemem_recall_duration_seconds", "", "", "1m", 0.95, 1000)
	require.Contains(t, quantileQuery, "min_over_time")
	require.NotContains(t, quantileQuery, "count_over_time")
	windowedIDs := telemetryQuerySpecIDs(telemetryWindowedCardSpecs(TelemetryScope{}, nil, "1h"))
	require.Subset(t, windowedIDs, []string{
		"embedding_requests",
		"embedding_errors",
		"verifier_requests",
		"recalls",
		"avg_conflict_review_duration",
	})
	activityIDs := telemetryQuerySpecIDs(telemetryActivitySeriesSpecs("", "1m"))
	require.Subset(t, activityIDs, []string{
		"embedding_requests",
		"embedding_errors",
		"verifier_requests",
		"recalls",
		"conflict_review_duration",
	})
	for _, retiredID := range []string{"fragment_creates", "claim_creates", "promotions", "retractions", "facts_requeued", "community_runs", "promote_lock_wait"} {
		require.NotContains(t, windowedIDs, retiredID)
		require.NotContains(t, activityIDs, retiredID)
	}
	require.NotEmpty(t, telemetryCurrentCardSpecs(TelemetryScope{}, nil))
	operatorCards := telemetryCurrentCardSpecsForAudience(TelemetryScope{Type: "system"}, nil, true)
	require.Equal(t, "conflict_queue_collection_success", operatorCards[len(operatorCards)-1].ID)
	require.Contains(t, operatorCards[len(operatorCards)-1].Query, "min(densemem_conflict_queue_collection_success")
	userCards := telemetryCurrentCardSpecsForAudience(TelemetryScope{Type: "system"}, nil, false)
	require.NotEmpty(t, userCards)
	require.Len(t, operatorCards, len(userCards)+1)
	require.NotContains(t, telemetryQuerySpecIDs(userCards), "conflict_queue_collection_success")
	require.Empty(t, telemetryStateSeriesSpecs(""))

	scope, err = normalizeTelemetryScope(TelemetryFilter{Scope: "self", TeamID: &teamID, ProfileID: &profileID})
	require.NoError(t, err)
	require.Equal(t, "self", scope.Type)
	require.Equal(t, profileID, *scope.ProfileID)
	require.Equal(t, `{profile_id="22222222-2222-4222-8222-222222222222",team_id="11111111-1111-4111-8111-111111111111"}`, telemetrySelector(scope, nil))

	_, err = normalizeTelemetryScope(TelemetryFilter{Scope: "team"})
	require.ErrorContains(t, err, "team_id is required")
	_, err = normalizeTelemetryScope(TelemetryFilter{Scope: "profile", ProfileID: &profileID})
	require.ErrorContains(t, err, "team_id is required")
	_, err = normalizeTelemetryScope(TelemetryFilter{Scope: "profile"})
	require.ErrorContains(t, err, "team_id is required")
	nilTeamID := uuid.Nil
	_, err = normalizeTelemetryScope(TelemetryFilter{Scope: "profile", TeamID: &nilTeamID, ProfileID: &profileID})
	require.ErrorContains(t, err, "team_id is required")
	_, err = normalizeTelemetryScope(TelemetryFilter{Scope: "other"})
	require.ErrorContains(t, err, "scope must be one of")

	points, err := decodePrometheusPoints([][]json.RawMessage{
		{json.RawMessage(`1770000000.5`), json.RawMessage(`"2.5"`)},
		{json.RawMessage(`1770000001`)},
		{json.RawMessage(`1770000002`), json.RawMessage(`"-1"`)},
		{json.RawMessage(`1770000003`), json.RawMessage(`"NaN"`)},
	})
	require.NoError(t, err)
	require.Len(t, points, 2)
	require.Equal(t, 2.5, points[0].Value)
	require.Equal(t, 0.0, points[1].Value)

	value, err := decodePrometheusValue(nil)
	require.NoError(t, err)
	require.False(t, value.Available)
	require.Equal(t, 0.0, value.Value)

	_, _, err = decodePrometheusPair([]json.RawMessage{json.RawMessage(`"bad"`), json.RawMessage(`"1"`)})
	require.Error(t, err)
	_, _, err = decodePrometheusPair([]json.RawMessage{json.RawMessage(`1`), json.RawMessage(`1`)})
	require.Error(t, err)
	_, value, err = decodePrometheusPair([]json.RawMessage{json.RawMessage(`1`), json.RawMessage(`"NaN"`)})
	require.NoError(t, err)
	require.False(t, value.Available)
	require.Equal(t, 0.0, value.Value)
}

func TestPrometheusTelemetryService_QueryFailureBranches(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		switch query {
		case "status-error":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "bad-json":
			_, _ = w.Write([]byte(`{`))
		case "instant-error":
			_, _ = w.Write([]byte(`{"status":"error","error":"bad instant"}`))
		case "instant-empty":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
		case "range-error":
			_, _ = w.Write([]byte(`{"status":"error","error":"bad range"}`))
		case "range-empty":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer prom.Close()

	svc := NewPrometheusTelemetryService(prom.URL, time.Second)
	now := time.Unix(1770000000, 0).UTC()

	value, err := svc.queryInstant(context.Background(), "instant-empty")
	require.NoError(t, err)
	require.False(t, value.Available)
	require.Equal(t, 0.0, value.Value)
	_, err = svc.queryInstant(context.Background(), "instant-error")
	require.ErrorContains(t, err, "bad instant")
	_, err = svc.queryInstant(context.Background(), "bad-json")
	require.Error(t, err)
	_, err = svc.queryInstant(context.Background(), "status-error")
	require.ErrorContains(t, err, "status 503")

	points, err := svc.queryRange(context.Background(), "range-empty", now.Add(-time.Minute), now, time.Minute)
	require.NoError(t, err)
	require.Empty(t, points)
	_, err = svc.queryRange(context.Background(), "range-error", now.Add(-time.Minute), now, time.Minute)
	require.ErrorContains(t, err, "bad range")

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m"})
	require.NoError(t, err)
	require.False(t, snapshot.Available)
	require.Equal(t, TelemetrySnapshotUnavailable, snapshot.Status)
	require.Equal(t, "telemetry sources are unavailable", snapshot.Message)
}

func TestPrometheusTelemetryService_LogsQueryFailure(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","error":"bad instant"}`))
	}))
	defer prom.Close()

	logger := &captureTelemetryLogger{}
	svc := NewPrometheusTelemetryServiceWithLogger(prom.URL, time.Second, logger)

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m", Scope: "system", Audience: TelemetryAudienceOperator})

	require.NoError(t, err)
	require.False(t, snapshot.Available)
	require.Equal(t, TelemetrySnapshotUnavailable, snapshot.Status)
	require.Equal(t, "telemetry sources are unavailable", snapshot.Message)
	require.Equal(t, "telemetry backend query failed", logger.message)
	require.ErrorContains(t, logger.err, "bad instant")
	require.Condition(t, func() bool {
		return containsTelemetryAttr(logger.attrs, "query_kind=instant") || containsTelemetryAttr(logger.attrs, "query_kind=range")
	})
	require.Contains(t, logger.attrs, "window=15m")
	require.Contains(t, logger.attrs, "scope=system")
	require.Condition(t, func() bool { return hasTelemetryAttrPrefix(logger.attrs, "prometheus_query=") })
}

func TestPrometheusTelemetryServiceKeepsSuccessfulItemsWhenOneQueryFails(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), "status_class") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/v1/query_range" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"values":[[1770000000,"1"],[1770000060,"1"]]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"value":[1770000000,"1"]}]}}`))
	}))
	defer prom.Close()

	svc := NewPrometheusTelemetryService(prom.URL, time.Second)
	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m", Scope: "system", Audience: TelemetryAudienceOperator})
	require.NoError(t, err)
	require.Equal(t, TelemetrySnapshotDegraded, snapshot.Status)
	require.True(t, snapshot.Available)
	require.Equal(t, TelemetryItemReady, telemetrySpecByID(snapshot.WindowedCards, "http_requests").Status)
	errorCard := telemetrySpecByID(snapshot.WindowedCards, "http_errors")
	require.NotNil(t, errorCard)
	require.Equal(t, TelemetryItemUnavailable, errorCard.Status)
	require.Equal(t, "query_failed", errorCard.ReasonCode)
	errorSeries := telemetrySeriesByID(snapshot.ActivitySeries, "http_errors_rps")
	require.NotNil(t, errorSeries)
	require.Equal(t, TelemetryItemUnavailable, errorSeries.Status)
	require.Equal(t, "query_failed", errorSeries.ReasonCode)
	require.Empty(t, errorSeries.Points)
	payload, marshalErr := json.Marshal(snapshot)
	require.NoError(t, marshalErr)
	require.NotContains(t, string(payload), `"points":null`)
}

func TestPrometheusTelemetryServiceKeepsLifecycleItemsWhenPrometheusTimesOut(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer prom.Close()

	lifecycle := &telemetryLifecycleReaderStub{snapshot: repository.TelemetryLifecycleSnapshot{
		Transitions: map[string]float64{"active": 1},
		Current:     map[string]float64{"active": 1},
	}}
	svc := NewPrometheusTelemetryService(prom.URL, 20*time.Millisecond)
	svc.SetLifecycleReader(lifecycle)

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m", Scope: "system", Audience: TelemetryAudienceOperator})
	require.NoError(t, err)
	require.Equal(t, TelemetrySnapshotDegraded, snapshot.Status)
	require.True(t, snapshot.Available)
	active := telemetrySpecByID(snapshot.CurrentCards, "relationships_active")
	require.NotNil(t, active)
	require.Equal(t, TelemetryItemReady, active.Status)
	require.Equal(t, 1.0, active.Value)
	require.Equal(t, 1, lifecycle.calls)
}

func TestPrometheusTelemetryServicePreservesSparseFirstObservations(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if r.URL.Path == "/api/v1/query_range" {
			if strings.Contains(query, "densemem_http_requests_total") {
				_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"values":[[1770000000,"0"],[1770000060,"0.5"]]}]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
			return
		}
		if strings.Contains(query, "densemem_http_requests_total") {
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"value":[1770000060,"1"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer prom.Close()

	svc := NewPrometheusTelemetryService(prom.URL, time.Second)
	svc.now = func() time.Time { return time.Unix(1770000060, 0).UTC() }

	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m", Scope: "system"})

	require.NoError(t, err)
	httpCard := telemetrySpecByID(snapshot.WindowedCards, "http_requests")
	require.NotNil(t, httpCard)
	require.Equal(t, TelemetryItemReady, httpCard.Status)
	require.Equal(t, 1.0, httpCard.Value)
	httpSeries := telemetrySeriesByID(snapshot.ActivitySeries, "http_rps")
	require.NotNil(t, httpSeries)
	require.Equal(t, TelemetryItemReady, httpSeries.Status)
	require.Len(t, httpSeries.Points, 2)
	require.Equal(t, 0.5, httpSeries.Points[1].Value)
}

func TestPrometheusTelemetryServiceLimitsQueryConcurrency(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		if r.URL.Path == "/api/v1/query_range" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer prom.Close()

	svc := NewPrometheusTelemetryService(prom.URL, time.Second)
	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m", Scope: "system"})

	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.LessOrEqual(t, maximum.Load(), int32(telemetryQueryConcurrency))
	if runtime.GOMAXPROCS(0) > 1 {
		require.Greater(t, maximum.Load(), int32(1))
	}
}

func TestPrometheusTelemetryServiceReportsFeatureAndScopeStates(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/query_range" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"values":[[1770000000,"1"]]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"value":[1770000000,"1"]}]}}`))
	}))
	defer prom.Close()

	teamID := uuid.New()
	profileID := uuid.New()
	svc := NewPrometheusTelemetryService(prom.URL, time.Second)
	svc.SetFeatureResolver(TelemetryFeatureResolver{
		RecallFeedbackEnabled: func(context.Context) (bool, error) { return false, nil },
		DreamingEnabled:       func(context.Context, *uuid.UUID) (bool, error) { return false, nil },
	})
	snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{Window: "15m", Scope: "profile", TeamID: &teamID, ProfileID: &profileID, Audience: TelemetryAudienceOperator})
	require.NoError(t, err)
	require.Equal(t, TelemetryItemInactive, telemetrySpecByID(snapshot.WindowedCards, "llm_recall_used_rate").Status)
	require.Equal(t, "feature_disabled", telemetrySpecByID(snapshot.WindowedCards, "dream_feedbacks").ReasonCode)
	require.Equal(t, TelemetryItemUnsupported, telemetrySpecByID(snapshot.WindowedCards, "embedding_requests").Status)
	require.Equal(t, TelemetryItemUnsupported, telemetrySpecByID(snapshot.WindowedCards, "embedding_cost_usd").Status)
	require.Nil(t, telemetrySpecByID(snapshot.WindowedCards, "assessor_requests"))
	require.Equal(t, TelemetryItemReady, telemetrySpecByID(snapshot.WindowedCards, "verifier_cost_usd").Status)
}

func TestTelemetryCostCardsFailClosedWhenActivityIsUnpriced(t *testing.T) {
	scope := TelemetryScope{Type: "system"}
	specs := telemetryWindowedCardSpecsForAudience(scope, nil, "1h", true)
	results := map[string]telemetryInstantResult{
		"verifier_requests":  {Scalar: telemetryScalar{Value: 1, Available: true}},
		"embedding_requests": {Scalar: telemetryScalar{Value: 0, Available: true}},
		"verifier_cost_usd":  {Scalar: telemetryScalar{}},
		"embedding_cost_usd": {Scalar: telemetryScalar{}},
	}
	cards := buildTelemetryCards(specs, results, repository.TelemetryLifecycleSnapshot{
		Transitions: map[string]float64{},
		Current:     map[string]float64{},
	}, nil, nil, scope)

	verifier := telemetrySpecByID(cards, "verifier_cost_usd")
	require.NotNil(t, verifier)
	require.Equal(t, TelemetryItemUnavailable, verifier.Status)
	require.Equal(t, "pricing_missing", verifier.ReasonCode)
	embedding := telemetrySpecByID(cards, "embedding_cost_usd")
	require.NotNil(t, embedding)
	require.Equal(t, TelemetryItemReady, embedding.Status)
	require.Equal(t, 0.0, embedding.Value)
}

func TestTelemetryAggregateCostIncludesUnpricedEmbeddingActivity(t *testing.T) {
	scope := TelemetryScope{Type: "system"}
	specs := telemetryWindowedCardSpecsForAudience(scope, nil, "1h", true)
	results := map[string]telemetryInstantResult{
		"verifier_requests":  {Scalar: telemetryScalar{Value: 0, Available: true}},
		"embedding_requests": {Scalar: telemetryScalar{Value: 2, Available: true}},
		"ai_cost_usd":        {Scalar: telemetryScalar{}},
		"embedding_cost_usd": {Scalar: telemetryScalar{}},
	}
	cards := buildTelemetryCards(specs, results, repository.TelemetryLifecycleSnapshot{
		Transitions: map[string]float64{},
		Current:     map[string]float64{},
	}, nil, nil, scope)

	aggregate := telemetrySpecByID(cards, "ai_cost_usd")
	require.NotNil(t, aggregate)
	require.Equal(t, TelemetryItemUnavailable, aggregate.Status)
	require.Equal(t, "pricing_missing", aggregate.ReasonCode)
}

func TestTelemetryCostCardsPreserveUnpricedUsageReasons(t *testing.T) {
	scope := TelemetryScope{Type: "system"}
	specs := telemetryWindowedCardSpecsForAudience(scope, nil, "1h", true)
	baseResults := map[string]telemetryInstantResult{
		"verifier_requests":  {Scalar: telemetryScalar{Value: 1, Available: true}},
		"embedding_requests": {Scalar: telemetryScalar{Value: 0, Available: true}},
	}
	for _, tc := range []struct {
		name   string
		reason string
		code   string
	}{
		{name: "missing usage", reason: "missing_usage", code: "provider_usage_missing"},
		{name: "invalid usage", reason: "invalid_usage", code: "usage_invalid"},
		{name: "tokenizer failure", reason: "tokenizer_error", code: "usage_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results := make(map[string]telemetryInstantResult, len(baseResults)+1)
			for id, result := range baseResults {
				results[id] = result
			}
			results["verifier_cost_usd"] = telemetryInstantResult{Scalar: telemetryScalar{
				Labels: map[string]string{"reason": tc.reason},
			}}
			cards := buildTelemetryCards(specs, results, repository.TelemetryLifecycleSnapshot{
				Transitions: map[string]float64{},
				Current:     map[string]float64{},
			}, nil, nil, scope)
			card := telemetrySpecByID(cards, "verifier_cost_usd")
			require.NotNil(t, card)
			require.Equal(t, TelemetryItemUnavailable, card.Status)
			require.Equal(t, tc.code, card.ReasonCode)
		})
	}
}

func TestTelemetryConflictQueueFailureSentinelIsUnavailable(t *testing.T) {
	scope := TelemetryScope{Type: "system"}
	specs := telemetryCurrentCardSpecsForAudience(scope, nil, true)
	cards := buildTelemetryCards(specs, map[string]telemetryInstantResult{
		"conflict_queue_collection_success": {Scalar: telemetryScalar{Value: 0, Available: true}},
	}, repository.TelemetryLifecycleSnapshot{
		Transitions: map[string]float64{},
		Current:     map[string]float64{},
	}, nil, nil, scope)
	card := telemetrySpecByID(cards, "conflict_queue_collection_success")
	require.NotNil(t, card)
	require.Equal(t, TelemetryItemUnavailable, card.Status)
	require.Equal(t, "query_failed", card.ReasonCode)
}

func TestTelemetryRecallFeedbackParentUsesEventCount(t *testing.T) {
	scope := TelemetryScope{Type: "system"}
	specs := telemetryWindowedCardSpecsForAudience(scope, nil, "1h", false)
	cards := buildTelemetryCards(specs, map[string]telemetryInstantResult{
		telemetryRecallFeedbackActivityID: {Scalar: telemetryScalar{Value: 3, Available: true}},
		"llm_recall_used_rate":            {Scalar: telemetryScalar{Value: 0, Available: true}},
	}, repository.TelemetryLifecycleSnapshot{
		Transitions: map[string]float64{},
		Current:     map[string]float64{},
	}, nil, nil, scope)
	quality := telemetrySpecByID(cards, "llm_recall_quality_score")
	require.NotNil(t, quality)
	require.Equal(t, TelemetryItemUnavailable, quality.Status)
	require.Equal(t, "query_failed", quality.ReasonCode)
	parent := telemetrySpecByID(cards, telemetryRecallFeedbackActivityID)
	require.NotNil(t, parent)
	require.Equal(t, TelemetryItemReady, parent.Status)
	require.Len(t, visibleTelemetryCards(cards), len(cards)-1)
}

func TestTelemetryParentActivityMakesMissingDerivedItemsUnavailable(t *testing.T) {
	scope := TelemetryScope{Type: "system"}
	cardSpecs := telemetryWindowedCardSpecsForAudience(scope, nil, "1h", false)
	cards := buildTelemetryCards(cardSpecs, map[string]telemetryInstantResult{
		"recalls":            {Scalar: telemetryScalar{Value: 2, Available: true}},
		"avg_recall_results": {Scalar: telemetryScalar{}},
	}, repository.TelemetryLifecycleSnapshot{Transitions: map[string]float64{}, Current: map[string]float64{}}, nil, nil, scope)
	card := telemetrySpecByID(cards, "avg_recall_results")
	require.NotNil(t, card)
	require.Equal(t, TelemetryItemUnavailable, card.Status)
	require.Equal(t, "query_failed", card.ReasonCode)

	seriesSpec := telemetryQuerySpecByID(telemetryActivitySeriesSpecsForAudience("", "1m", false), "recall_results")
	require.NotNil(t, seriesSpec)
	series := buildTelemetrySeries([]telemetryQuerySpec{*seriesSpec}, map[string]telemetryRangeResult{
		"recall_results": {Points: []TelemetryPoint{}},
	}, nil, time.Unix(1770000000, 0).UTC(), time.Unix(1770000060, 0).UTC(), cards, scope)
	require.Len(t, series, 1)
	require.Equal(t, TelemetryItemUnavailable, series[0].Status)
	require.Equal(t, "query_failed", series[0].ReasonCode)

	parentCards := []TelemetryCard{
		{ID: "http_requests", Status: TelemetryItemReady, Value: 3},
		{ID: "http_errors", Status: TelemetryItemInactive},
	}
	parent := telemetryParentCard("http_errors_rps", parentCards)
	require.NotNil(t, parent)
	require.Equal(t, "http_requests", parent.ID)

	zeroUsageCards := buildTelemetryCards(cardSpecs, map[string]telemetryInstantResult{
		"verifier_requests": {Scalar: telemetryScalar{Value: 0, Available: true}},
		"verifier_tokens":   {Scalar: telemetryScalar{}},
	}, repository.TelemetryLifecycleSnapshot{Transitions: map[string]float64{}, Current: map[string]float64{}}, nil, nil, scope)
	zeroUsage := telemetrySpecByID(zeroUsageCards, "verifier_tokens")
	require.NotNil(t, zeroUsage)
	require.Equal(t, TelemetryItemInactive, zeroUsage.Status)
	require.Equal(t, "no_activity", zeroUsage.ReasonCode)
}

func TestAssessorFailureTelemetryUsesReadyZeroWhenRequestsAreHealthy(t *testing.T) {
	scope := TelemetryScope{Type: "system"}
	windowedSpecs := telemetryWindowedCardSpecsForAudience(scope, nil, "1h", true)
	windowedResults := map[string]telemetryInstantResult{
		"assessor_requests":            {Scalar: telemetryScalar{Value: 2, Available: true}},
		"assessor_request_failures":    {Scalar: telemetryScalar{Value: 0, Available: false}},
		"assessor_validation_failures": {Scalar: telemetryScalar{Value: 0, Available: false}},
		"assessor_terminal_failures":   {Scalar: telemetryScalar{Value: 0, Available: false}},
	}
	cards := buildTelemetryCards(windowedSpecs, windowedResults, repository.TelemetryLifecycleSnapshot{}, nil, nil, scope)
	for _, id := range []string{"assessor_request_failures", "assessor_validation_failures", "assessor_terminal_failures"} {
		card := telemetrySpecByID(cards, id)
		require.NotNil(t, card, id)
		require.Equal(t, TelemetryItemReady, card.Status, id)
		require.Equal(t, 0.0, card.Value, id)
	}

	seriesSpecs := telemetryActivitySeriesSpecsForAudience("", "1m", true)
	series := buildTelemetrySeries(
		[]telemetryQuerySpec{
			*telemetryQuerySpecByID(seriesSpecs, "assessor_request_failures"),
			*telemetryQuerySpecByID(seriesSpecs, "assessor_validation_failures"),
			*telemetryQuerySpecByID(seriesSpecs, "assessor_terminal_failures"),
		},
		map[string]telemetryRangeResult{
			"assessor_request_failures":    {Points: []TelemetryPoint{}},
			"assessor_validation_failures": {Points: []TelemetryPoint{}},
			"assessor_terminal_failures":   {Points: []TelemetryPoint{}},
		},
		nil,
		time.Unix(1770000000, 0).UTC(),
		time.Unix(1770000060, 0).UTC(),
		cards,
		scope,
	)
	require.Len(t, series, 3)
	for _, item := range series {
		require.Equal(t, TelemetryItemReady, item.Status, item.ID)
		require.Len(t, item.Points, 2, item.ID)
		require.Equal(t, 0.0, item.Points[0].Value, item.ID)
		require.Equal(t, 0.0, item.Points[1].Value, item.ID)
	}

	noParent := buildTelemetryCards(windowedSpecs, map[string]telemetryInstantResult{}, repository.TelemetryLifecycleSnapshot{}, nil, nil, scope)
	missingParentFailure := telemetrySpecByID(noParent, "assessor_request_failures")
	require.NotNil(t, missingParentFailure)
	require.Equal(t, TelemetryItemUnavailable, missingParentFailure.Status)
	require.Equal(t, "query_failed", missingParentFailure.ReasonCode)
}

func TestTelemetrySnapshotDispositionIgnoresUnsupportedItems(t *testing.T) {
	status, available, message := telemetrySnapshotDisposition(
		[]TelemetryCard{{ID: "unsupported", Status: TelemetryItemUnsupported}},
		[]TelemetrySeries{{ID: "failed", Status: TelemetryItemUnavailable}},
	)
	require.Equal(t, TelemetrySnapshotUnavailable, status)
	require.False(t, available)
	require.Equal(t, "telemetry sources are unavailable", message)

	status, available, message = telemetrySnapshotDisposition(
		[]TelemetryCard{{ID: "unsupported", Status: TelemetryItemUnsupported}},
		[]TelemetrySeries{{ID: "ready", Status: TelemetryItemReady}},
	)
	require.NotEqual(t, TelemetrySnapshotUnavailable, status)
	require.True(t, available)
	require.Empty(t, message)
}

type captureTelemetryLogger struct {
	message string
	err     error
	attrs   []string
}

type telemetryLifecycleReaderStub struct {
	snapshot repository.TelemetryLifecycleSnapshot
	err      error
	calls    int
}

func (s *telemetryLifecycleReaderStub) ReadTelemetryLifecycle(context.Context, repository.TelemetryLifecycleFilter, time.Time, time.Time) (repository.TelemetryLifecycleSnapshot, error) {
	s.calls++
	return s.snapshot, s.err
}

func containsTelemetryAttr(attrs []string, want string) bool {
	for _, attr := range attrs {
		if attr == want {
			return true
		}
	}
	return false
}

func hasTelemetryAttrPrefix(attrs []string, prefix string) bool {
	for _, attr := range attrs {
		if strings.HasPrefix(attr, prefix) {
			return true
		}
	}
	return false
}

func (l *captureTelemetryLogger) Info(string, ...observability.LogAttr) {}

func (l *captureTelemetryLogger) Error(msg string, err error, attrs ...observability.LogAttr) {
	l.message = msg
	l.err = err
	l.attrs = logAttrStrings(attrs)
}

func (l *captureTelemetryLogger) Warn(string, ...observability.LogAttr)  {}
func (l *captureTelemetryLogger) Debug(string, ...observability.LogAttr) {}

func (l *captureTelemetryLogger) With(...observability.LogAttr) observability.LogProvider {
	return l
}

func logAttrStrings(attrs []observability.LogAttr) []string {
	out := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		out = append(out, attr.Key+"="+toLogString(attr.Value))
	}
	return out
}

func toLogString(value any) string {
	if err, ok := value.(error); ok {
		return err.Error()
	}
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func scopeLabels(scope TelemetryScope) map[string]string {
	labels := map[string]string{}
	if scope.TeamID != nil {
		labels["team_id"] = scope.TeamID.String()
	}
	if scope.ProfileID != nil {
		labels["profile_id"] = scope.ProfileID.String()
	}
	return labels
}

func telemetrySpecByID(cards []TelemetryCard, id string) *TelemetryCard {
	for i := range cards {
		if cards[i].ID == id {
			return &cards[i]
		}
	}
	return nil
}

func telemetrySeriesByID(series []TelemetrySeries, id string) *TelemetrySeries {
	for i := range series {
		if series[i].ID == id {
			return &series[i]
		}
	}
	return nil
}

func telemetryQuerySpecIDs(specs []telemetryQuerySpec) []string {
	ids := make([]string, 0, len(specs))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	return ids
}

func telemetryQuerySpecByID(specs []telemetryQuerySpec, id string) *telemetryQuerySpec {
	for i := range specs {
		if specs[i].ID == id {
			return &specs[i]
		}
	}
	return nil
}

func mustMarshalStringMap(t *testing.T, value map[string]string) string {
	t.Helper()

	out, err := json.Marshal(value)
	require.NoError(t, err)
	return string(out)
}
