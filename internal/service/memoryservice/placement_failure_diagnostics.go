package memoryservice

import (
	"context"
	"errors"
	"strings"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type placementFailureDiagnostic struct {
	ReasonCode              string
	Stage                   string
	Class                   string
	ValidationStage         string
	ValidationFieldFamilies []string
	Measurement             *verifier.FailureMeasurement
	ProviderStatus          int
	AssessorTurns           int
}

// PlacementWorkerFailure contains only bounded context that is safe for the
// active worker pool to attach to an operational log event.
type PlacementWorkerFailure struct {
	TeamID       string
	SubmissionID string
	Stage        string
	ReasonCode   string
	Class        string
}

type placementWorkerError struct {
	failure PlacementWorkerFailure
	cause   error
}

func (err *placementWorkerError) Error() string {
	return "placement worker persistence failed"
}

func (err *placementWorkerError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func newPlacementWorkerError(teamID, submissionID, stage string, cause error) error {
	diagnostic := placementFailureDiagnosticFor(stage, cause)
	diagnostic.Class = "repository"
	diagnostic.ReasonCode = "repository_persistence_failed"
	return newPlacementWorkerDiagnosticError(teamID, submissionID, diagnostic, cause)
}

func newPlacementWorkerDiagnosticError(teamID, submissionID string, diagnostic placementFailureDiagnostic, cause error) error {
	return &placementWorkerError{
		failure: PlacementWorkerFailure{
			TeamID:       strings.TrimSpace(teamID),
			SubmissionID: strings.TrimSpace(submissionID),
			Stage:        diagnostic.Stage,
			ReasonCode:   diagnostic.ReasonCode,
			Class:        diagnostic.Class,
		},
		cause: cause,
	}
}

func placementWorkerFailureFromError(err error) (PlacementWorkerFailure, bool) {
	var workerErr *placementWorkerError
	if !errors.As(err, &workerErr) {
		return PlacementWorkerFailure{}, false
	}
	return workerErr.failure, true
}

// PlacementWorkerFailureFromError exposes only the bounded worker context to
// the process-level pool logger.
func PlacementWorkerFailureFromError(err error) (PlacementWorkerFailure, bool) {
	return placementWorkerFailureFromError(err)
}

func placementFailureDiagnosticFor(stage string, cause error) placementFailureDiagnostic {
	diagnostic := placementFailureDiagnostic{
		Stage: boundedPlacementFailureStage(stage),
		Class: "internal",
	}
	var preflight *semanticAssessmentPreflightError
	if errors.As(cause, &preflight) {
		if preflight.reasonCode != "" {
			diagnostic.ReasonCode = preflight.reasonCode
		}
		if preflight.failureClass != "" {
			diagnostic.Class = boundedPlacementFailureClass(preflight.failureClass)
		}
		diagnostic.Measurement = cloneFailureMeasurement(preflight.measurement)
	}
	var malformed *verifier.MalformedResponseError
	if errors.As(cause, &malformed) {
		diagnostic.Class = boundedPlacementFailureClass(malformed.FailureClass)
		diagnostic.ValidationStage = boundedValidationStage(malformed.ValidationStage)
		diagnostic.ValidationFieldFamilies = boundedValidationFieldFamilies(malformed.ValidationFieldFamilies)
		diagnostic.Measurement = cloneFailureMeasurement(malformed.Measurement)
		diagnostic.AssessorTurns = boundedPositive(malformed.Attempts)
	}
	if isVerifierProviderFailure(cause) {
		failure := verifier.ProviderFailureDetails(cause)
		diagnostic.Class = boundedPlacementFailureClass(failure.Class)
		diagnostic.ProviderStatus = boundedProviderStatus(failure.StatusCode)
	}
	if errors.Is(cause, repository.ErrPlacementLeaseLost) || errors.Is(cause, repository.ErrPlacementLeaseConflict) {
		diagnostic.Class = "lease_lost"
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		diagnostic.Class = "deadline"
	}
	if errors.Is(cause, context.Canceled) {
		diagnostic.Class = "canceled"
	}
	if diagnostic.ReasonCode == "" {
		diagnostic.ReasonCode = placementFailureReasonCode(diagnostic.Stage, diagnostic.Class)
	}
	return diagnostic
}

func isVerifierProviderFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, verifier.ErrVerifierTimeout) || errors.Is(err, verifier.ErrVerifierRateLimit) || errors.Is(err, verifier.ErrVerifierProvider) {
		return true
	}
	var provider *verifier.ProviderError
	var rateLimit *verifier.RateLimitError
	return errors.As(err, &provider) || errors.As(err, &rateLimit)
}

func placementFailureDiagnosticForProvider(stage string, failure verifier.ProviderFailureMetadata) placementFailureDiagnostic {
	diagnostic := placementFailureDiagnosticFor(stage, nil)
	diagnostic.Class = boundedPlacementFailureClass(failure.Class)
	diagnostic.ProviderStatus = boundedProviderStatus(failure.StatusCode)
	diagnostic.ReasonCode = placementFailureReasonCode(diagnostic.Stage, diagnostic.Class)
	return diagnostic
}

func (diagnostic placementFailureDiagnostic) payload(providerAttempted bool) map[string]any {
	payload := map[string]any{
		"failure_reason_code":         diagnostic.ReasonCode,
		"failure_stage":               diagnostic.Stage,
		"failure_class":               diagnostic.Class,
		"assessor_provider_attempted": providerAttempted,
	}
	if diagnostic.ValidationStage != "" {
		payload["validation_stage"] = diagnostic.ValidationStage
	}
	if len(diagnostic.ValidationFieldFamilies) > 0 {
		payload["validation_field_families"] = append([]string(nil), diagnostic.ValidationFieldFamilies...)
	}
	if measurement := failureMeasurementPayload(diagnostic.Measurement); measurement != nil {
		payload["failure_measurement"] = measurement
	}
	if diagnostic.ProviderStatus > 0 {
		payload["provider_status"] = diagnostic.ProviderStatus
	}
	if diagnostic.AssessorTurns > 0 {
		payload["assessor_turns"] = diagnostic.AssessorTurns
	}
	return payload
}

func placementFailureReasonCode(stage, class string) string {
	switch stage {
	case "entity_catalog":
		return "entity_catalog_candidate_limit_exceeded"
	case "catalog_context":
		return "candidate_context_token_limit_exceeded"
	case "assessment_input":
		return "assessment_input_token_limit_exceeded"
	case "predicate_options_overflow":
		return "predicate_option_limit_exceeded"
	case "contract_superseded":
		return "contract_superseded"
	case "replacement_conflict":
		return "replacement_conflict"
	case "assessment_attempt_consumed":
		return "assessor_attempt_consumed"
	case "deterministic_security_scan", "security_signal":
		return "security_quarantine"
	case "semantic_commit":
		return "semantic_commit_failed"
	case "placement_load":
		return "placement_load_failed"
	}
	switch class {
	case "malformed_response", "malformed_exhausted", "input_budget", "validation_failed", "provider_protocol", "request_invalid":
		return "assessor_response_invalid"
	case "timeout", "rate_limited", "http_4xx", "http_5xx", "http_unexpected", "transport", "provider_unavailable":
		return "assessor_provider_failed"
	case "lease_lost":
		return "lease_lost"
	}
	return "unknown_internal_failure"
}

func boundedPlacementFailureStage(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "entity_catalog", "catalog_context", "assessment_input", "catalog_context_validation", "trusted_context_validation", "predicate_options_overflow", "placement_load", "assessment", "assessment_attempt_consumed", "confidence_policy", "policy_review", "deterministic_policy", "commit_review", "conflict_context_stale", "semantic_commit", "contract_superseded", "replacement_conflict", "stale_source", "deterministic_security_scan", "security_signal", "assessment_scope", "review_override", "placement_item":
		return value
	default:
		return "unknown"
	}
}

func boundedPlacementFailureClass(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "timeout", "rate_limited", "http_4xx", "http_5xx", "http_unexpected", "transport", "provider_protocol", "provider_unavailable", "request_invalid", "malformed_response", "malformed_exhausted", "input_budget", "validation_failed", "internal", "repository", "lease_lost", "canceled", "deadline":
		return value
	default:
		return "internal"
	}
}

func boundedValidationStage(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "response_json", "response_contract", "response_output_tokens", "conversation_input_tokens", "stored_response":
		return value
	default:
		return ""
	}
}

func boundedValidationFieldFamilies(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 64 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == 32 {
			break
		}
	}
	return result
}

func cloneFailureMeasurement(measurement *verifier.FailureMeasurement) *verifier.FailureMeasurement {
	if measurement == nil {
		return nil
	}
	copy := *measurement
	if copy.Unit != "tokens" && copy.Unit != "candidates" {
		return nil
	}
	if copy.Observed < 0 || copy.Limit < 0 {
		return nil
	}
	return &copy
}

func failureMeasurementPayload(measurement *verifier.FailureMeasurement) map[string]any {
	measurement = cloneFailureMeasurement(measurement)
	if measurement == nil {
		return nil
	}
	payload := map[string]any{"unit": measurement.Unit, "limit": measurement.Limit}
	if measurement.ObservedAtLeast {
		payload["observed_at_least"] = measurement.Observed
	} else {
		payload["observed"] = measurement.Observed
	}
	return payload
}

func boundedPositive(value int) int {
	if value < 0 {
		return 0
	}
	if value > 1000 {
		return 1000
	}
	return value
}

func boundedProviderStatus(value int) int {
	if value < 100 || value > 599 {
		return 0
	}
	return value
}

func retryAfterError(original error, persist func() error) error {
	if persistenceErr := persist(); persistenceErr != nil {
		return errors.Join(original, persistenceErr)
	}
	return nil
}

func firstError(values []error) error {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}
