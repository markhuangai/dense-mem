package topics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/topics/assignment"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
)

const maxDiagnosticErrorBytes = 512

type failureDiagnosticDetail struct {
	Stage string
	Error string
}

type failureDiagnosticDetailKey struct{}

func withFailureDiagnosticDetail(ctx context.Context, stage, detail string) context.Context {
	return context.WithValue(ctx, failureDiagnosticDetailKey{}, failureDiagnosticDetail{Stage: stage, Error: detail})
}

func failureDiagnosticDetailFromContext(ctx context.Context) failureDiagnosticDetail {
	if ctx == nil {
		return failureDiagnosticDetail{}
	}
	detail, _ := ctx.Value(failureDiagnosticDetailKey{}).(failureDiagnosticDetail)
	return detail
}

type maintenanceTurnError struct {
	RunID   string
	BatchID string
	TeamID  string
	Stage   string
	Cause   error
}

func (e *maintenanceTurnError) Error() string {
	if e == nil || e.Cause == nil {
		return ""
	}
	return e.Cause.Error()
}

func (e *maintenanceTurnError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func wrapMaintenanceTurnError(runID, batchID, teamID, stage string, err error) error {
	if err == nil {
		return nil
	}
	var existing *maintenanceTurnError
	if errors.As(err, &existing) {
		return err
	}
	return &maintenanceTurnError{RunID: runID, BatchID: batchID, TeamID: teamID, Stage: stage, Cause: err}
}

func maintenanceDiagnosticError(err error) string {
	if err == nil {
		return ""
	}
	if code := MaintenanceErrorCode(err); code != "" {
		return code
	}
	var organizationErr *OrganizationError
	if errors.As(err, &organizationErr) && organizationErr.Code != "" {
		return organizationErr.Code
	}
	if errors.Is(err, assignment.ErrStale) || errors.Is(err, contract.ErrSourceStale) {
		return "stale_input"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "request_cancelled"
	}
	var rateLimit *modelprovider.RateLimitError
	var malformedErr *modelprovider.MalformedResponseError
	var providerErr *modelprovider.ProviderError
	switch {
	case errors.As(err, &rateLimit):
		return modelprovider.ProviderFailureClassRateLimited
	case errors.Is(err, modelprovider.ErrVerifierTimeout):
		return modelprovider.ProviderFailureClassTimeout
	case errors.As(err, &malformedErr):
		return "provider_response_invalid"
	case errors.As(err, &providerErr):
		return modelprovider.ProviderFailureDetails(err).Class
	}
	return "maintenance_failure"
}

func (service *MaintenanceService) protectedDiagnosticErrorText(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	if service == nil || service.deps.Diagnostics.protector == nil {
		return fmt.Sprintf("diagnostic error unavailable (reason %d)", observability.CredentialProtectionUnsupported)
	}
	text, reason := service.deps.Diagnostics.protector.ProtectErrorText(err, observability.MaxOperationMetadataBytes, observability.AuthenticationSecretsFromContext(ctx)...)
	if reason != observability.CredentialProtectionAvailable {
		return fmt.Sprintf("diagnostic error unavailable (reason %d)", reason)
	}
	if len(text) <= maxDiagnosticErrorBytes {
		return text
	}
	end := maxDiagnosticErrorBytes - len("...")
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "..."
}

func sqlStateOf(err error) string {
	var state interface{ SQLState() string }
	if !errors.As(err, &state) {
		return ""
	}
	code := state.SQLState()
	if len(code) != 5 {
		return ""
	}
	for _, c := range code {
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return ""
		}
	}
	return code
}

func turnFailureClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, contract.ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, contract.ErrAccounting):
		return "accounting_unavailable"
	case sqlStateOf(err) != "":
		return "postgres"
	default:
		var rateLimit *modelprovider.RateLimitError
		var providerErr *modelprovider.ProviderError
		var malformedErr *modelprovider.MalformedResponseError
		if errors.As(err, &rateLimit) || errors.As(err, &providerErr) || errors.As(err, &malformedErr) || errors.Is(err, modelprovider.ErrVerifierTimeout) {
			return "provider"
		}
		return "internal_failure"
	}
}

func discoveryCountsLogValue(counts contract.MaintenanceDiscoveryCounts) map[string]any {
	return map[string]any{
		"initial_sources":       counts.InitialSources,
		"enumerated_matches":    counts.EnumeratedMatches,
		"refreshed_sources":     counts.RefreshedSources,
		"freshness_invocations": counts.FreshnessInvocations,
		"distinct_receipts":     counts.DistinctReceipts,
		"requeues":              counts.Requeues,
		"completed_markers":     counts.CompletedMarkers,
	}
}

func sourceHandleLogValue(source contract.SourceHandle) map[string]any {
	return map[string]any{
		"kind":    string(source.Kind),
		"id":      source.ID,
		"version": source.Version,
	}
}

func organizationOutcomesLogValue(outcomes []contract.OrganizationOutcome) []map[string]any {
	items := make([]map[string]any, 0, len(outcomes))
	for _, outcome := range outcomes {
		entry := map[string]any{
			"source": sourceHandleLogValue(outcome.Source),
			"status": outcome.Status,
		}
		if outcome.Reason != "" {
			entry["reason"] = outcome.Reason
		}
		if len(outcome.RecordIDs) > 0 {
			entry["record_ids"] = append([]string(nil), outcome.RecordIDs...)
		}
		items = append(items, entry)
	}
	return items
}

func assessmentAttemptsLogValue(attempts []contract.AssessmentAttempt) []map[string]any {
	items := make([]map[string]any, 0, len(attempts))
	for _, attempt := range attempts {
		entry := map[string]any{
			"number":                   attempt.Number,
			"estimated_input_tokens":   attempt.EstimatedInputTokens,
			"estimated_output_tokens":  attempt.EstimatedOutputTokens,
			"reported_input_tokens":    attempt.ReportedInputTokens,
			"reported_output_tokens":   attempt.ReportedOutputTokens,
			"reported_total_tokens":    attempt.ReportedTotalTokens,
			"reported_usage_available": attempt.ReportedUsageAvailable,
		}
		if attempt.FailureCode != "" {
			entry["failure_code"] = attempt.FailureCode
		}
		if attempt.Diagnostics != nil {
			if fields := attempt.Diagnostics.LogFields(); len(fields) > 0 {
				entry["diagnostics"] = fields
			}
		}
		if attempt.Validation != nil {
			validation := map[string]any{
				"stage":        attempt.Validation.Stage,
				"code":         attempt.Validation.Code,
				"field_family": attempt.Validation.FieldFamily,
			}
			if attempt.Validation.ExpectedCount != nil {
				validation["expected_count"] = *attempt.Validation.ExpectedCount
			}
			if attempt.Validation.ActualCount != nil {
				validation["actual_count"] = *attempt.Validation.ActualCount
			}
			entry["validation"] = validation
		}
		items = append(items, entry)
	}
	return items
}

func revisionRefsLogValue(records []contract.RevisionRef) []map[string]any {
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		items = append(items, map[string]any{
			"id":      record.ID,
			"version": record.Version,
		})
	}
	return items
}

func decisionAnswerLogValue(answer modelprovider.DecisionAnswer) map[string]any {
	fields := map[string]any{"type": string(answer.Type)}
	if answer.Choice != "" {
		fields["choice"] = answer.Choice
	}
	if answer.Probability != nil {
		fields["probability"] = *answer.Probability
	}
	if answer.Score != nil {
		fields["score"] = *answer.Score
	}
	if answer.Confidence != nil {
		fields["confidence"] = *answer.Confidence
	}
	if len(answer.Probabilities) > 0 {
		probabilities := make(map[string]any, len(answer.Probabilities))
		for key, value := range answer.Probabilities {
			probabilities[key] = value
		}
		fields["probabilities"] = probabilities
	}
	return fields
}

func (s *MaintenanceService) logMaintenanceDiscovery(ctx context.Context, turn contract.MaintenanceTurn, result contract.MaintenanceDiscoveryResult, err error) {
	if s.deps.Logger == nil {
		return
	}
	s.deps.Logger.LogAttrs(ctx, slog.LevelInfo, "topic maintenance discovery",
		slog.String("run_id", turn.RunID), slog.String("team_id", turn.TeamID),
		slog.Bool("committed", result.Committed),
		slog.Any("counts", discoveryCountsLogValue(result.Counts)),
		slog.Any("attempted", discoveryCountsLogValue(result.Attempted)),
		slog.Float64("duration_ms", float64(result.Duration)/float64(time.Millisecond)),
		slog.String("error_code", maintenanceDiagnosticError(err)))
}

func (s *MaintenanceService) logMaintenancePreparation(ctx context.Context, turn contract.MaintenanceTurn, work *maintenanceWork, duration time.Duration, err error) {
	if s.deps.Logger == nil {
		return
	}
	batchID := ""
	if work != nil {
		batchID = work.claim.ID
	}
	s.deps.Logger.LogAttrs(ctx, slog.LevelInfo, "topic maintenance preparation",
		slog.String("run_id", turn.RunID), slog.String("team_id", turn.TeamID), slog.String("batch_id", batchID),
		slog.Bool("batch_prepared", work != nil), slog.Bool("includes_discovery", true),
		slog.Float64("duration_ms", float64(duration)/float64(time.Millisecond)), slog.String("error_code", maintenanceDiagnosticError(err)))
}

func (s *MaintenanceService) logFreshnessRejection(ctx context.Context, claim contract.MaintenanceClaim, result contract.OrganizationResult) {
	if s.deps.Logger == nil || result.FreshnessRejection == nil {
		return
	}
	rejection := result.FreshnessRejection
	var source any
	if rejection.Source != nil {
		source = sourceHandleLogValue(*rejection.Source)
	}
	s.deps.Logger.LogAttrs(ctx, slog.LevelWarn, "topic_freshness_rejected",
		slog.String("team_id", claim.TeamID), slog.String("run_id", claim.RunID), slog.String("batch_id", claim.ID),
		slog.String("assessment_id", result.AssessmentID), slog.String("check", rejection.Check),
		slog.Any("source", source), slog.String("record_id", rejection.RecordID),
		slog.String("expected", rejection.Expected), slog.String("observed", rejection.Observed))
}
