package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
)

const telemetryQueryConcurrency = 6

// telemetryCatalogEntry is the source-of-truth metadata for a rendered
// telemetry item. Query construction remains scope- and window-specific, but
// the item contract is kept in one ordered catalog.
type telemetryCatalogEntry struct {
	ID                   string
	Source               string
	SourceKind           string
	Audience             string
	SupportedScopes      []string
	RuntimePrerequisite  string
	ParentActivitySource string
	ZeroPolicy           string
	Presentations        []string
}

const (
	telemetrySourceCollector   = "collector"
	telemetrySourceLedger      = "ledger"
	telemetryAudienceUser      = "user"
	telemetryAudienceOperator  = "operator"
	telemetryParentNone        = "none"
	telemetryZeroValid         = "valid_zero"
	telemetryZeroParent        = "parent_activity_zero"
	telemetryZeroUnavailable   = "unavailable_without_source"
	telemetryZeroNotApplicable = "not_applicable"
)

var telemetryAllScopes = []string{"system", "team", "profile"}

// orderedTelemetryCatalog is intentionally explicit and ordered. Lifecycle
// status items are appended by telemetryCatalogEntryFor because their IDs are
// derived from the active V2 status registry.
var orderedTelemetryCatalog = []telemetryCatalogEntry{
	{ID: "http_requests", Source: "densemem_http_requests_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "prometheus_scrape", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroValid, Presentations: []string{"card", "chart"}},
	{ID: "http_errors", Source: "densemem_http_requests_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "prometheus_scrape", ParentActivitySource: "densemem_http_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card"}},
	{ID: "http_rps", Source: "densemem_http_requests_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "prometheus_scrape", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroValid, Presentations: []string{"chart"}},
	{ID: "http_errors_rps", Source: "densemem_http_requests_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "prometheus_scrape", ParentActivitySource: "densemem_http_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"chart"}},
	{ID: "embedding_requests", Source: "densemem_embedding_requests_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "embedding_instrumentation", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroValid, Presentations: []string{"card", "chart"}},
	{ID: "embedding_errors", Source: "densemem_embedding_errors_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "embedding_instrumentation", ParentActivitySource: "densemem_embedding_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card", "chart"}},
	{ID: "embedding_tokens", Source: "densemem_embedding_tokens_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "provider_usage", ParentActivitySource: "densemem_embedding_requests_total", ZeroPolicy: telemetryZeroUnavailable, Presentations: []string{"card", "chart"}},
	{ID: "verifier_requests", Source: "densemem_verifier_requests_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "verifier_instrumentation", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroValid, Presentations: []string{"card", "chart"}},
	{ID: "verifier_tokens", Source: "densemem_verifier_tokens_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "provider_usage", ParentActivitySource: "densemem_verifier_requests_total", ZeroPolicy: telemetryZeroUnavailable, Presentations: []string{"card", "chart"}},
	{ID: "recalls", Source: "densemem_recall_requests_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "recall_instrumentation", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroValid, Presentations: []string{"card", "chart"}},
	{ID: "avg_recall_results", Source: "densemem_recall_results_sum/count", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "recall_instrumentation", ParentActivitySource: "densemem_recall_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card"}},
	{ID: "p95_recall_latency", Source: "densemem_recall_duration_seconds_bucket", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "recall_instrumentation", ParentActivitySource: "densemem_recall_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card"}},
	{ID: "recall_results", Source: "densemem_recall_results_sum/count", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "recall_instrumentation", ParentActivitySource: "densemem_recall_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"chart"}},
	{ID: "recall_p95_latency", Source: "densemem_recall_duration_seconds_bucket", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "recall_instrumentation", ParentActivitySource: "densemem_recall_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"chart"}},
	{ID: "llm_recall_used_rate", Source: "densemem_recall_feedback_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "recall_feedback_enabled", ParentActivitySource: "densemem_recall_feedback_total", ZeroPolicy: telemetryZeroUnavailable, Presentations: []string{"card", "chart"}},
	{ID: "llm_recall_answer_supported_rate", Source: "densemem_recall_feedback_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "recall_feedback_enabled", ParentActivitySource: "densemem_recall_feedback_total", ZeroPolicy: telemetryZeroUnavailable, Presentations: []string{"card", "chart"}},
	{ID: "llm_recall_quality_score", Source: "densemem_recall_feedback_quality_score", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "recall_feedback_enabled", ParentActivitySource: "densemem_recall_feedback_total", ZeroPolicy: telemetryZeroUnavailable, Presentations: []string{"card", "chart"}},
	{ID: "llm_recall_missing_context_rate", Source: "densemem_recall_feedback_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "recall_feedback_enabled", ParentActivitySource: "densemem_recall_feedback_total", ZeroPolicy: telemetryZeroUnavailable, Presentations: []string{"card", "chart"}},
	{ID: "llm_recall_irrelevant_rate", Source: "densemem_recall_feedback_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "recall_feedback_enabled", ParentActivitySource: "densemem_recall_feedback_total", ZeroPolicy: telemetryZeroUnavailable, Presentations: []string{"card", "chart"}},
	{ID: "dream_feedbacks", Source: "densemem_dream_feedback_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "dreaming_enabled", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroValid, Presentations: []string{"card", "chart"}},
	{ID: "avg_http_latency", Source: "densemem_http_request_duration_seconds_sum/count", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "http_instrumentation", ParentActivitySource: "densemem_http_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card"}},
	{ID: "avg_embedding_latency", Source: "densemem_embedding_duration_seconds_sum/count", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "embedding_instrumentation", ParentActivitySource: "densemem_embedding_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card"}},
	{ID: "avg_verifier_latency", Source: "densemem_verifier_duration_seconds_sum/count", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "verifier_instrumentation", ParentActivitySource: "densemem_verifier_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card"}},
	{ID: "avg_conflict_review_duration", Source: "densemem_conflict_review_duration_seconds_sum/count", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: []string{"system", "team"}, RuntimePrerequisite: "conflict_review_instrumentation", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroParent, Presentations: []string{"card"}},
	{ID: "conflict_review_duration", Source: "densemem_conflict_review_duration_seconds_sum/count", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: []string{"system", "team"}, RuntimePrerequisite: "conflict_review_instrumentation", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroParent, Presentations: []string{"chart"}},
	{ID: "remember_acknowledgements", Source: "densemem_remember_acknowledgements_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "remember_instrumentation", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroValid, Presentations: []string{"card", "chart"}},
	{ID: "avg_remember_acknowledgement_latency", Source: "densemem_remember_acknowledgement_duration_seconds_sum/count", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "remember_instrumentation", ParentActivitySource: "densemem_remember_acknowledgements_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card"}},
	{ID: "p95_remember_acknowledgement_latency", Source: "densemem_remember_acknowledgement_duration_seconds_bucket", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "remember_instrumentation", ParentActivitySource: "densemem_remember_acknowledgements_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card"}},
	{ID: "remember_first_dispositions", Source: "densemem_remember_first_disposition_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "remember_instrumentation", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroValid, Presentations: []string{"card", "chart"}},
	{ID: "p95_remember_first_disposition_latency", Source: "densemem_remember_first_disposition_duration_seconds_bucket", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "remember_instrumentation", ParentActivitySource: "densemem_remember_first_disposition_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card"}},
	{ID: "assessor_requests", Source: "densemem_assessor_requests_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceOperator, SupportedScopes: []string{"system"}, RuntimePrerequisite: "active_assessor_instrumentation", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroValid, Presentations: []string{"card", "chart"}},
	{ID: "assessor_request_failures", Source: "densemem_assessor_requests_total{outcome=provider_error|malformed_exhausted}", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceOperator, SupportedScopes: []string{"system"}, RuntimePrerequisite: "active_assessor_instrumentation", ParentActivitySource: "densemem_assessor_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card", "chart"}},
	{ID: "assessor_validation_failures", Source: "densemem_assessor_validation_failures_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceOperator, SupportedScopes: []string{"system"}, RuntimePrerequisite: "active_assessor_instrumentation", ParentActivitySource: "densemem_assessor_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card", "chart"}},
	{ID: "assessor_tokens", Source: "densemem_assessor_tokens_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceOperator, SupportedScopes: []string{"system"}, RuntimePrerequisite: "provider_usage", ParentActivitySource: "densemem_assessor_requests_total", ZeroPolicy: telemetryZeroUnavailable, Presentations: []string{"card", "chart"}},
	{ID: "avg_assessor_duration", Source: "densemem_assessor_duration_seconds_sum/count", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceOperator, SupportedScopes: []string{"system"}, RuntimePrerequisite: "active_assessor_instrumentation", ParentActivitySource: "densemem_assessor_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card"}},
	{ID: "assessor_duration", Source: "densemem_assessor_duration_seconds_sum/count", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceOperator, SupportedScopes: []string{"system"}, RuntimePrerequisite: "active_assessor_instrumentation", ParentActivitySource: "densemem_assessor_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"chart"}},
	{ID: "assessor_terminal_failures", Source: "densemem_assessor_terminal_failures_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceOperator, SupportedScopes: []string{"system"}, RuntimePrerequisite: "active_assessor_instrumentation", ParentActivitySource: "densemem_assessor_requests_total", ZeroPolicy: telemetryZeroParent, Presentations: []string{"card", "chart"}},
	{ID: "ai_cost_usd", Source: "densemem_ai_operation_cost_usd_total", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceOperator, SupportedScopes: []string{"system", "team"}, RuntimePrerequisite: "pricing_and_provider_usage", ParentActivitySource: "densemem_verifier_requests_total+densemem_embedding_requests_total", ZeroPolicy: telemetryZeroUnavailable, Presentations: []string{"card"}},
	{ID: "verifier_cost_usd", Source: "densemem_ai_operation_cost_usd_total{component=verifier}", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceOperator, SupportedScopes: telemetryAllScopes, RuntimePrerequisite: "pricing_and_provider_usage", ParentActivitySource: "densemem_verifier_requests_total", ZeroPolicy: telemetryZeroUnavailable, Presentations: []string{"card"}},
	{ID: "embedding_cost_usd", Source: "densemem_ai_operation_cost_usd_total{component=embedding}", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceOperator, SupportedScopes: []string{"system", "team"}, RuntimePrerequisite: "pricing_and_provider_usage", ParentActivitySource: "densemem_embedding_requests_total", ZeroPolicy: telemetryZeroUnavailable, Presentations: []string{"card"}},
	{ID: "conflict_queue_collection_success", Source: "densemem_conflict_queue_collection_success", SourceKind: telemetrySourceCollector, Audience: telemetryAudienceOperator, SupportedScopes: []string{"system"}, RuntimePrerequisite: "conflict_queue_instrumentation", ParentActivitySource: telemetryParentNone, ZeroPolicy: telemetryZeroNotApplicable, Presentations: []string{"card"}},
}

func telemetryCatalogEntryFor(id string) (telemetryCatalogEntry, bool) {
	for _, entry := range orderedTelemetryCatalog {
		if entry.ID == id {
			return entry, true
		}
	}
	if strings.HasPrefix(id, "relationship_transitions_") {
		return telemetryCatalogEntry{
			ID: id, Source: "relationship_transition_events", SourceKind: telemetrySourceLedger,
			Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes,
			RuntimePrerequisite: "postgresql_lifecycle_reader", ParentActivitySource: telemetryParentNone,
			ZeroPolicy: telemetryZeroValid, Presentations: []string{"card"},
		}, true
	}
	if strings.HasPrefix(id, "relationships_") {
		return telemetryCatalogEntry{
			ID: id, Source: "relationship_records", SourceKind: telemetrySourceLedger,
			Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes,
			RuntimePrerequisite: "postgresql_lifecycle_reader", ParentActivitySource: telemetryParentNone,
			ZeroPolicy: telemetryZeroValid, Presentations: []string{"card"},
		}, true
	}
	if id == "relationship_corrections" {
		return telemetryCatalogEntry{
			ID: id, Source: "relationship_correction_events", SourceKind: telemetrySourceLedger,
			Audience: telemetryAudienceUser, SupportedScopes: telemetryAllScopes,
			RuntimePrerequisite: "postgresql_lifecycle_reader", ParentActivitySource: telemetryParentNone,
			ZeroPolicy: telemetryZeroValid, Presentations: []string{"card"},
		}, true
	}
	return telemetryCatalogEntry{}, false
}

func bindTelemetryCatalog(specs []telemetryQuerySpec) []telemetryQuerySpec {
	for index := range specs {
		if entry, ok := telemetryCatalogEntryFor(specs[index].ID); ok {
			specs[index].Catalog = entry
		}
	}
	return specs
}

type telemetryDisposition struct {
	Status     string
	ReasonCode string
	Reason     string
}

type telemetryFeatureState struct {
	Disposition telemetryDisposition
	Set         bool
}

type telemetryInstantResult struct {
	Scalar telemetryScalar
	Err    error
}

type telemetryRangeResult struct {
	Points []TelemetryPoint
	Err    error
}

func telemetryExecutableSpecs(specs []telemetryQuerySpec, scope TelemetryScope, features map[string]telemetryFeatureState) []telemetryQuerySpec {
	result := make([]telemetryQuerySpec, 0, len(specs))
	for _, spec := range specs {
		if spec.Ledger || strings.TrimSpace(spec.Query) == "" || telemetryScopeUnsupported(spec.ID, scope) {
			continue
		}
		if feature, ok := telemetryFeatureForSpec(spec.ID, features); ok && feature.Set {
			continue
		}
		result = append(result, spec)
	}
	return result
}

func runTelemetryInstantQueries(ctx context.Context, service *PrometheusTelemetryService, specs []telemetryQuerySpec, window string, scope TelemetryScope) map[string]telemetryInstantResult {
	results := make(map[string]telemetryInstantResult, len(specs))
	var mu sync.Mutex
	var group sync.WaitGroup
	semaphore := make(chan struct{}, telemetryQueryConcurrency)
	for _, spec := range specs {
		spec := spec
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				results[spec.ID] = telemetryInstantResult{Err: ctx.Err()}
				mu.Unlock()
				return
			}
			defer func() { <-semaphore }()
			scalar, err := service.queryInstant(ctx, spec.Query)
			if err != nil {
				service.logQueryFailure("instant", spec, window, scope, err)
			}
			mu.Lock()
			results[spec.ID] = telemetryInstantResult{Scalar: scalar, Err: err}
			mu.Unlock()
		}()
	}
	group.Wait()
	return results
}

func runTelemetryRangeQueries(ctx context.Context, service *PrometheusTelemetryService, specs []telemetryQuerySpec, from, to time.Time, step time.Duration, window string, scope TelemetryScope) map[string]telemetryRangeResult {
	results := make(map[string]telemetryRangeResult, len(specs))
	var mu sync.Mutex
	var group sync.WaitGroup
	semaphore := make(chan struct{}, telemetryQueryConcurrency)
	for _, spec := range specs {
		spec := spec
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				results[spec.ID] = telemetryRangeResult{Err: ctx.Err()}
				mu.Unlock()
				return
			}
			defer func() { <-semaphore }()
			points, err := service.queryRange(ctx, spec.Query, from, to, step)
			if err != nil {
				service.logQueryFailure("range", spec, window, scope, err)
			}
			mu.Lock()
			results[spec.ID] = telemetryRangeResult{Points: points, Err: err}
			mu.Unlock()
		}()
	}
	group.Wait()
	return results
}

func (s *PrometheusTelemetryService) telemetryFeatureStates(ctx context.Context, scope TelemetryScope) map[string]telemetryFeatureState {
	states := make(map[string]telemetryFeatureState)
	if s == nil || (s.features.RecallFeedbackEnabled == nil && s.features.DreamingEnabled == nil) {
		return states
	}
	if s.features.RecallFeedbackEnabled != nil {
		enabled, err := s.features.RecallFeedbackEnabled(ctx)
		if err != nil {
			states["recall"] = telemetryFeatureState{Set: true, Disposition: telemetryUnavailable("source_unconfigured")}
		} else if !enabled {
			states["recall"] = telemetryFeatureState{Set: true, Disposition: telemetryInactive("feature_disabled")}
		}
	}
	if s.features.DreamingEnabled != nil {
		var teamID *uuid.UUID
		if scope.Type != "system" {
			teamID = scope.TeamID
		}
		enabled, err := s.features.DreamingEnabled(ctx, teamID)
		if err != nil {
			states["dream"] = telemetryFeatureState{Set: true, Disposition: telemetryUnavailable("source_unconfigured")}
		} else if !enabled {
			states["dream"] = telemetryFeatureState{Set: true, Disposition: telemetryInactive("feature_disabled")}
		}
	}
	return states
}

func buildTelemetryCards(specs []telemetryQuerySpec, results map[string]telemetryInstantResult, lifecycle repository.TelemetryLifecycleSnapshot, lifecycleErr error, features map[string]telemetryFeatureState, scope TelemetryScope) []TelemetryCard {
	cards := make([]TelemetryCard, 0, len(specs))
	for _, spec := range specs {
		disposition := telemetryDisposition{}
		value := float64(0)
		if unsupported := telemetryScopeUnsupported(spec.ID, scope); unsupported {
			disposition = telemetryUnsupported()
		} else if feature, ok := telemetryFeatureForSpec(spec.ID, features); ok && feature.Set {
			disposition = feature.Disposition
		} else if spec.Ledger {
			if lifecycleErr != nil {
				disposition = telemetryUnavailable("ledger_unavailable")
			} else {
				disposition = telemetryReady()
				switch {
				case strings.HasPrefix(spec.ID, "relationship_transitions_"):
					value = lifecycle.Transitions[strings.TrimPrefix(spec.ID, "relationship_transitions_")]
				case spec.ID == "relationship_corrections":
					value = lifecycle.Corrections
				case strings.HasPrefix(spec.ID, "relationships_"):
					value = lifecycle.Current[strings.TrimPrefix(spec.ID, "relationships_")]
				}
			}
		} else if result, ok := results[spec.ID]; !ok || result.Err != nil {
			disposition = telemetryUnavailable("query_failed")
		} else if result.Scalar.Available {
			value = result.Scalar.Value
			disposition = telemetryReady()
		} else {
			disposition = telemetryInactive("no_activity")
		}

		cards = append(cards, TelemetryCard{
			ID:         spec.ID,
			Label:      spec.Label,
			Unit:       spec.Unit,
			Value:      value,
			Available:  disposition.Status == TelemetryItemReady,
			Status:     disposition.Status,
			ReasonCode: disposition.ReasonCode,
			Reason:     disposition.Reason,
		})
	}
	applyTelemetryCardParentPolicies(cards)
	return cards
}

func buildTelemetrySeries(specs []telemetryQuerySpec, results map[string]telemetryRangeResult, features map[string]telemetryFeatureState, from, to time.Time, cards []TelemetryCard, scope TelemetryScope) []TelemetrySeries {
	series := make([]TelemetrySeries, 0, len(specs))
	for _, spec := range specs {
		disposition := telemetryDisposition{}
		points := []TelemetryPoint{}
		if telemetryScopeUnsupported(spec.ID, scope) {
			disposition = telemetryUnsupported()
		} else if feature, ok := telemetryFeatureForSpec(spec.ID, features); ok && feature.Set {
			disposition = feature.Disposition
		} else if result, ok := results[spec.ID]; !ok || result.Err != nil {
			disposition = telemetryUnavailable("query_failed")
		} else if len(result.Points) == 0 {
			disposition = telemetryInactive("no_activity")
		} else {
			points = result.Points
			disposition = telemetryReady()
		}
		if len(points) == 0 && disposition.Status == TelemetryItemInactive && telemetrySeriesCanProveZero(spec.ID, cards) {
			points = []TelemetryPoint{{Timestamp: from.Format(time.RFC3339), Value: 0}, {Timestamp: to.Format(time.RFC3339), Value: 0}}
			disposition = telemetryReady()
		}
		if len(points) == 0 && disposition.Status == TelemetryItemInactive && telemetrySeriesParentActivityMissing(spec.ID, cards) {
			disposition = telemetryUnavailable("query_failed")
		}
		series = append(series, TelemetrySeries{
			ID:         spec.ID,
			Label:      spec.Label,
			Unit:       spec.Unit,
			Points:     points,
			Status:     disposition.Status,
			ReasonCode: disposition.ReasonCode,
			Reason:     disposition.Reason,
		})
	}
	return series
}

func applyTelemetryCardParentPolicies(cards []TelemetryCard) {
	byID := make(map[string]*TelemetryCard, len(cards))
	for index := range cards {
		byID[cards[index].ID] = &cards[index]
	}
	for _, childID := range []string{"http_errors", "embedding_errors"} {
		child := byID[childID]
		if child == nil || child.Status != TelemetryItemInactive {
			continue
		}
		parentID := "http_requests"
		if childID == "embedding_errors" {
			parentID = "embedding_requests"
		}
		if parent := byID[parentID]; parent != nil && parent.Status == TelemetryItemReady {
			child.Status = TelemetryItemReady
			child.Available = true
			child.ReasonCode = ""
			child.Reason = ""
		}
	}
	for _, childID := range []string{"embedding_tokens", "verifier_tokens"} {
		child := byID[childID]
		if child == nil || child.Status != TelemetryItemInactive {
			continue
		}
		parentID := strings.TrimSuffix(childID, "_tokens") + "_requests"
		if parent := byID[parentID]; parent != nil && parent.Status == TelemetryItemReady {
			child.Status = TelemetryItemUnavailable
			child.Available = false
			child.ReasonCode = "provider_usage_missing"
			child.Reason = telemetryReason("provider_usage_missing")
		}
	}
	for index := range cards {
		if !strings.HasSuffix(cards[index].ID, "_cost_usd") || cards[index].Status != TelemetryItemInactive {
			continue
		}
		parents := make([]*TelemetryCard, 0, 2)
		switch cards[index].ID {
		case "ai_cost_usd":
			parents = append(parents, byID["verifier_requests"], byID["embedding_requests"])
		case "embedding_cost_usd":
			parents = append(parents, byID["embedding_requests"])
		default:
			parents = append(parents, byID["verifier_requests"])
		}
		activeParent := false
		allParentsIdle := true
		for _, parent := range parents {
			if parent == nil {
				allParentsIdle = false
				continue
			}
			if parent.Status == TelemetryItemReady && parent.Value > 0 {
				activeParent = true
			}
			if parent.Status != TelemetryItemInactive && !(parent.Status == TelemetryItemReady && parent.Value == 0) {
				allParentsIdle = false
			}
		}
		if activeParent {
			cards[index].Status = TelemetryItemUnavailable
			cards[index].Available = false
			cards[index].ReasonCode = "pricing_missing"
			cards[index].Reason = telemetryReason("pricing_missing")
			continue
		}
		if allParentsIdle {
			cards[index].Status = TelemetryItemReady
			cards[index].Available = true
			cards[index].ReasonCode = ""
			cards[index].Reason = ""
		}
	}
	for index := range cards {
		if cards[index].Status != TelemetryItemInactive {
			continue
		}
		if parent := telemetryParentCard(cards[index].ID, byID); parent != nil && parent.Status == TelemetryItemReady && parent.Value > 0 {
			cards[index].Status = TelemetryItemUnavailable
			cards[index].Available = false
			cards[index].ReasonCode = "query_failed"
			cards[index].Reason = telemetryReason("query_failed")
		}
	}
}

func telemetrySeriesCanProveZero(id string, cards []TelemetryCard) bool {
	parent := ""
	switch id {
	case "http_errors_rps":
		parent = "http_requests"
	case "embedding_errors":
		parent = "embedding_requests"
	}
	if parent == "" {
		return false
	}
	for _, card := range cards {
		if card.ID == parent {
			return card.Status == TelemetryItemReady
		}
	}
	return false
}

func telemetrySeriesParentActivityMissing(id string, cards []TelemetryCard) bool {
	byID := make(map[string]*TelemetryCard, len(cards))
	for index := range cards {
		byID[cards[index].ID] = &cards[index]
	}
	parent := telemetryParentCard(id, byID)
	return parent != nil && parent.Status == TelemetryItemReady && parent.Value > 0
}

func telemetryParentCard(id string, cards map[string]*TelemetryCard) *TelemetryCard {
	entry, ok := telemetryCatalogEntryFor(id)
	if !ok || entry.ParentActivitySource == telemetryParentNone {
		return nil
	}
	for cardID, card := range cards {
		parentEntry, ok := telemetryCatalogEntryFor(cardID)
		if ok && parentEntry.Source == entry.ParentActivitySource {
			return card
		}
	}
	return nil
}

func telemetrySnapshotDisposition(cards []TelemetryCard, series []TelemetrySeries) (string, bool, string) {
	hasUnavailable := false
	hasUsable := false
	for _, card := range cards {
		hasUnavailable = hasUnavailable || card.Status == TelemetryItemUnavailable
		hasUsable = hasUsable || card.Status == TelemetryItemReady || card.Status == TelemetryItemInactive
	}
	for _, item := range series {
		hasUnavailable = hasUnavailable || item.Status == TelemetryItemUnavailable
		hasUsable = hasUsable || item.Status == TelemetryItemReady || item.Status == TelemetryItemInactive
	}
	if hasUnavailable && !hasUsable {
		return TelemetrySnapshotUnavailable, false, "telemetry sources are unavailable"
	}
	if hasUnavailable {
		return TelemetrySnapshotDegraded, true, "some telemetry sources are unavailable"
	}
	return TelemetrySnapshotReady, true, ""
}

func telemetryScopeUnsupported(id string, scope TelemetryScope) bool {
	entry, ok := telemetryCatalogEntryFor(id)
	if !ok {
		return false
	}
	scopeType := scope.Type
	if scopeType == "self" {
		scopeType = "profile"
	}
	for _, supported := range entry.SupportedScopes {
		if supported == scopeType {
			return false
		}
	}
	return true
}

func telemetryFeatureForSpec(id string, states map[string]telemetryFeatureState) (telemetryFeatureState, bool) {
	if strings.HasPrefix(id, "llm_recall_") {
		state, ok := states["recall"]
		return state, ok
	}
	if strings.HasPrefix(id, "dream_") {
		state, ok := states["dream"]
		return state, ok
	}
	return telemetryFeatureState{}, false
}

func hasLedgerSpecs(specs []telemetryQuerySpec) bool {
	for _, spec := range specs {
		if spec.Ledger {
			return true
		}
	}
	return false
}

func telemetryReady() telemetryDisposition {
	return telemetryDisposition{Status: TelemetryItemReady}
}

func telemetryInactive(code string) telemetryDisposition {
	return telemetryDisposition{Status: TelemetryItemInactive, ReasonCode: code, Reason: telemetryReason(code)}
}

func telemetryUnavailable(code string) telemetryDisposition {
	return telemetryDisposition{Status: TelemetryItemUnavailable, ReasonCode: code, Reason: telemetryReason(code)}
}

func telemetryUnsupported() telemetryDisposition {
	return telemetryDisposition{Status: TelemetryItemUnsupported, ReasonCode: "scope_unsupported", Reason: telemetryReason("scope_unsupported")}
}

func telemetryReason(code string) string {
	switch code {
	case "no_activity":
		return "No activity was recorded in this window."
	case "feature_disabled":
		return "This feature is disabled."
	case "provider_usage_missing":
		return "The provider did not report usage."
	case "pricing_missing":
		return "Pricing is not configured for this usage."
	case "usage_invalid":
		return "Provider usage was invalid."
	case "scope_unsupported":
		return "This item is not supported at this scope."
	case "source_unconfigured":
		return "The telemetry source is not configured."
	case "query_failed":
		return "The telemetry query failed."
	case "ledger_unavailable":
		return "The lifecycle ledger is unavailable."
	default:
		return "Telemetry is unavailable."
	}
}

func markTelemetryUnavailableForNonLedger(cards []TelemetryCard, specs []telemetryQuerySpec, code string) {
	ledgerIDs := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if spec.Ledger {
			ledgerIDs[spec.ID] = struct{}{}
		}
	}
	for index := range cards {
		if _, ok := ledgerIDs[cards[index].ID]; ok || cards[index].Status == TelemetryItemInactive || cards[index].Status == TelemetryItemUnsupported {
			continue
		}
		cards[index].Available = false
		cards[index].Status = TelemetryItemUnavailable
		cards[index].ReasonCode = code
		cards[index].Reason = telemetryReason(code)
	}
}

func markTelemetryUnavailableSeries(series []TelemetrySeries, code string) {
	for index := range series {
		series[index].Status = TelemetryItemUnavailable
		series[index].ReasonCode = code
		series[index].Reason = telemetryReason(code)
	}
}
