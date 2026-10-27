package service

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

type assessmentDiagnostics struct {
	started       time.Time
	remainingMS   *int64
	preparationMS *int64
	turnStarted   time.Time
	current       map[string]any
	turns         []map[string]any
	truncated     bool
}

func remainingAssessmentBudget(ctx context.Context) *int64 {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	remaining := max(int64(0), time.Until(deadline).Milliseconds())
	return &remaining
}

func (d *assessmentDiagnostics) beginTurn(ctx context.Context) {
	if d == nil {
		return
	}
	d.current = nil
	if len(d.turns) >= SemanticMaxAssessorTurns {
		d.truncated = true
		return
	}
	d.turnStarted = time.Now()
	if d.preparationMS == nil {
		elapsed := d.turnStarted.Sub(d.started).Milliseconds()
		d.preparationMS = &elapsed
	}
	d.current = map[string]any{"attempt": len(d.turns) + 1}
	if remaining := remainingAssessmentBudget(ctx); remaining != nil {
		d.current["remaining_budget_ms"] = *remaining
	}
	d.turns = append(d.turns, d.current)
}

func (d *assessmentDiagnostics) finishProviderTurn(turn assessor.SemanticAssessmentTurn, err error) {
	if d == nil || d.current == nil {
		return
	}
	d.current["elapsed_ms"] = time.Since(d.turnStarted).Milliseconds()
	if turn.Diagnostics != nil {
		if turn.Diagnostics.Valid() {
			copy := *turn.Diagnostics
			d.current["provider"] = copy
		} else {
			d.current["provider_diagnostics_unavailable"] = true
		}
	}
	if turn.ValidationMS != nil && *turn.ValidationMS >= 0 {
		d.current["validation_ms"] = *turn.ValidationMS
	}
	if turn.ValidationStage != "" {
		d.current["validation_stage"] = boundedAssessorValidationStage(turn.ValidationStage)
	}
	if err != nil {
		failure := modelprovider.ProviderFailureDetails(err)
		d.current["failure_class"] = boundedAssessmentProviderFailureClass(failure.Class)
		if failure.StatusCode >= 100 && failure.StatusCode <= 599 {
			d.current["status_code"] = failure.StatusCode
		}
		if failure.RetryAfter > 0 {
			d.current["retry_after_ms"] = failure.RetryAfter.Milliseconds()
		}
	}
}

func (d *assessmentDiagnostics) finishValidation(started time.Time, issues []assessor.SemanticValidationError) {
	if d == nil || d.current == nil {
		return
	}
	d.current["elapsed_ms"] = time.Since(d.turnStarted).Milliseconds()
	providerValidation, _ := d.current["validation_ms"].(int64)
	d.current["validation_ms"] = providerValidation + time.Since(started).Milliseconds()
	d.current["error_count"] = min(len(issues), maxAssessorValidationFields)
	d.current["truncated"] = len(issues) > maxAssessorValidationFields
	bounded := make([]map[string]any, 0, min(len(issues), maxAssessorValidationFields))
	for _, issue := range issues[:min(len(issues), maxAssessorValidationFields)] {
		field, _ := normalizeAssessmentValidationField(issue.Field)
		bounded = append(bounded, map[string]any{"field": field, "code": boundedAssessmentValidationCode(issue.Code)})
	}
	d.current["issues"] = bounded
}

func (d *assessmentDiagnostics) snapshot() map[string]any {
	if d == nil {
		return nil
	}
	preparation := time.Since(d.started).Milliseconds()
	if d.preparationMS != nil {
		preparation = *d.preparationMS
	}
	turns := d.turns
	if turns == nil {
		turns = []map[string]any{}
	}
	result := map[string]any{
		"preparation_ms": preparation, "elapsed_ms": time.Since(d.started).Milliseconds(),
		"turns": turns, "truncated": d.truncated,
	}
	if d.remainingMS != nil {
		result["remaining_budget_ms"] = *d.remainingMS
	}
	return result
}

type assessmentDiagnosticsError struct {
	cause       error
	diagnostics map[string]any
}

func (e *assessmentDiagnosticsError) Error() string { return e.cause.Error() }
func (e *assessmentDiagnosticsError) Unwrap() error { return e.cause }

// WithSynchronousAssessmentDiagnostics carries completed assessment measurements through a later embedding or commit failure.
func WithSynchronousAssessmentDiagnostics(err error, diagnostics map[string]any) error {
	if err == nil || diagnostics == nil {
		return err
	}
	return &assessmentDiagnosticsError{cause: err, diagnostics: diagnostics}
}

func SynchronousAssessmentDiagnostics(err error) map[string]any {
	var diagnosticErr *assessmentDiagnosticsError
	if errors.As(err, &diagnosticErr) {
		return diagnosticErr.diagnostics
	}
	return nil
}

func boundedAssessmentValidationCode(code string) string {
	switch code {
	case "predicate_resolution_required", "predicate_registration_must_be_null", "predicate_outside_allowlist",
		"predicate_subject_kind_mismatch", "predicate_object_kind_mismatch", "predicate_resolution_must_be_null",
		"predicate_registration_required", "predicate_key_invalid", "predicate_relationship_kind_invalid",
		"predicate_cardinality_invalid", "predicate_status_invalid", "predicate_inactive", "predicate_alias_ambiguous",
		"predicate_alias_collision", "predicate_relationship_kind_mismatch", "predicate_cardinality_mismatch",
		"predicate_subject_kind_invalid", "predicate_object_kind_invalid", "evidence_id_required",
		"evidence_id_unknown", "evidence_id_duplicate", "evidence_result_count_mismatch", "evidence_result_missing",
		"range_required", "range_evidence_unknown", "range_boundary_unknown", "range_order_invalid", "range_not_covered",
		"predicate_phrase_blank", "predicate_phrase_too_long":
		return code
	default:
		return "other"
	}
}

func boundedAssessmentProviderFailureClass(value string) string {
	switch value {
	case modelprovider.ProviderFailureClassTimeout, modelprovider.ProviderFailureClassRateLimited,
		modelprovider.ProviderFailureClassHTTPClient, modelprovider.ProviderFailureClassHTTPServer,
		modelprovider.ProviderFailureClassHTTPUnexpected, modelprovider.ProviderFailureClassTransport,
		modelprovider.ProviderFailureClassProtocol, modelprovider.ProviderFailureClassRequestInvalid,
		modelprovider.ProviderFailureClassProviderUnavailable:
		return value
	default:
		return "other"
	}
}
