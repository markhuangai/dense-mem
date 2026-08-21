package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/markhuangai/dense-mem/internal/observability"
)

type telemetryQueryFailure struct {
	kind string
	id   string
	err  error
}

type telemetryQueryError struct {
	reason string
	cause  error
}

func (err *telemetryQueryError) Error() string {
	return "telemetry backend query failed"
}

func (err *telemetryQueryError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func wrapTelemetryQueryError(reason string, cause error) error {
	if cause == nil {
		return &telemetryQueryError{reason: reason}
	}
	return &telemetryQueryError{reason: reason, cause: cause}
}

func (s *PrometheusTelemetryService) logQueryFailures(window string, scope TelemetryScope, groups ...[]telemetryQueryFailure) {
	if s == nil || s.logger == nil {
		return
	}
	failures := make([]telemetryQueryFailure, 0)
	for _, group := range groups {
		failures = append(failures, group...)
	}
	if len(failures) == 0 {
		return
	}
	sort.Slice(failures, func(i, j int) bool {
		if failures[i].id != failures[j].id {
			return failures[i].id < failures[j].id
		}
		return failures[i].kind < failures[j].kind
	})
	const maxLoggedFailures = 64
	queryIDs := make([]string, 0, minInt(len(failures), maxLoggedFailures))
	kindCounts := map[string]int{}
	reasonCounts := map[string]int{}
	allCancellation := true
	for index, failure := range failures {
		if index < maxLoggedFailures {
			queryIDs = append(queryIDs, failure.id)
		}
		kindCounts[failure.kind]++
		reason := telemetryQueryFailureReason(failure.err)
		reasonCounts[reason]++
		if reason != "context_canceled" && reason != "context_deadline_exceeded" {
			allCancellation = false
		}
	}
	attrs := []observability.LogAttr{
		observability.String("window", window),
		observability.String("scope", scope.Type),
		observability.Int("failed_query_count", len(failures)),
		observability.String("query_ids", strings.Join(queryIDs, ",")),
		observability.String("query_kinds", formatTelemetryCounts(kindCounts)),
		observability.String("query_failure_reasons", formatTelemetryCounts(reasonCounts)),
	}
	if len(failures) > maxLoggedFailures {
		attrs = append(attrs, observability.Bool("query_ids_truncated", true))
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
	if allCancellation {
		s.logger.Warn("telemetry backend query canceled", attrs...)
		return
	}
	s.logger.Error("telemetry backend query failed", errors.New("telemetry backend query failed"), attrs...)
}

func telemetryQueryFailureReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context_deadline_exceeded"
	}
	var queryErr *telemetryQueryError
	if errors.As(err, &queryErr) && queryErr.reason != "" {
		return queryErr.reason
	}
	message := strings.ToLower(strings.TrimSpace(errorString(err)))
	switch {
	case strings.Contains(message, "returned status"):
		return "http_status"
	case strings.Contains(message, "query_range failed"), strings.Contains(message, "query failed"):
		return "prometheus_api_error"
	case strings.Contains(message, "invalid character"), strings.Contains(message, "cannot unmarshal"), strings.Contains(message, "decode"):
		return "response_decode_failed"
	default:
		return "transport_failed"
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func formatTelemetryCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+strconv.Itoa(counts[key]))
	}
	return strings.Join(parts, ",")
}
