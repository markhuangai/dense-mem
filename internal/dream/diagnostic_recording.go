package dream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

type runDiagnosticPhase struct {
	phase         string
	outcome       string
	cause         string
	details       map[string]any
	hypothesisID  string
	captureState  string
	captureReason string
}

type runDiagnosticPhaseExpectation struct {
	Phase        string `json:"phase"`
	HypothesisID string `json:"hypothesis_id"`
	Count        int    `json:"count"`
}

type runDiagnosticPhaseKey struct {
	phase        string
	hypothesisID string
}

const (
	dreamDiagnosticCaptureTimeout = 5 * time.Second
	dreamDiagnosticRunTimeout     = 30 * time.Second
	dreamDiagnosticPhaseTimeout   = 20 * time.Second
)

func appendRunDiagnosticPhase(result *RunCycleResult, phase, outcome, cause string, details map[string]any) {
	if result == nil || strings.TrimSpace(phase) == "" || strings.TrimSpace(outcome) == "" {
		return
	}
	if len(result.diagnosticPhases) >= 256 {
		result.diagnosticPhasesTruncated = true
		return
	}
	result.diagnosticPhases = append(result.diagnosticPhases, runDiagnosticPhase{
		phase: phase, outcome: outcome, cause: cause, details: details,
	})
}

func appendRunDiagnosticHypothesisPhase(result *RunCycleResult, phase, outcome, cause, hypothesisID string, details map[string]any) {
	if result == nil || strings.TrimSpace(hypothesisID) == "" {
		return
	}
	if len(result.diagnosticPhases) >= 256 {
		result.diagnosticPhasesTruncated = true
		return
	}
	result.diagnosticPhases = append(result.diagnosticPhases, runDiagnosticPhase{
		phase: phase, outcome: outcome, cause: cause, hypothesisID: hypothesisID, details: details,
	})
}

func runDiagnosticPhaseExpectations(phases []runDiagnosticPhase) []runDiagnosticPhaseExpectation {
	counts := make(map[runDiagnosticPhaseKey]int, len(phases))
	for _, phase := range phases {
		counts[runDiagnosticPhaseKey{phase: phase.phase, hypothesisID: phase.hypothesisID}]++
	}
	expectations := make([]runDiagnosticPhaseExpectation, 0, len(counts))
	for key, count := range counts {
		expectations = append(expectations, runDiagnosticPhaseExpectation{
			Phase: key.phase, HypothesisID: key.hypothesisID, Count: count,
		})
	}
	sort.Slice(expectations, func(i, j int) bool {
		if expectations[i].Phase != expectations[j].Phase {
			return expectations[i].Phase < expectations[j].Phase
		}
		return expectations[i].HypothesisID < expectations[j].HypothesisID
	})
	return expectations
}

func (s *service) recordRunDiagnostic(ctx context.Context, result *RunCycleResult) {
	if s == nil || s.deps.Diagnostics == nil || result == nil || result.RunID == "" {
		return
	}
	if len(result.providerPayload) == 0 {
		if recorder := dreamDiagnosticRecorderFromContext(ctx); recorder != nil {
			result.providerPayload = recorder.Payload()
			result.providerCaptureState, result.providerCaptureReason = recorder.State()
		}
	}
	outcome := result.Status
	if outcome == "error" {
		outcome = "failed"
	} else if outcome == "completed" && result.CreatedDreams == 0 && result.ProviderProposals == 0 {
		outcome = "evaluated_zero"
	}
	details := map[string]any{
		"status":                     result.Status,
		"lane":                       result.Lane,
		"input_relationships":        result.InputRelationships,
		"created_hypotheses":         result.CreatedDreams,
		"rejected_hypotheses":        result.RejectedDreams,
		"provider_model":             result.ProviderModel,
		"provider_turns":             result.ProviderTurns,
		"provider_input_tokens":      result.ProviderInputTokens,
		"provider_output_tokens":     result.ProviderOutputTokens,
		"provider_proposals":         result.ProviderProposals,
		"attempted_paths":            result.AttemptedPaths,
		"evidence_targets":           result.EvidenceTargets,
		"evaluated_evidence_targets": result.EvaluatedEvidenceTargets,
		"outcome_summary":            result.OutcomeSummary,
	}
	if result.diagnosticPhasesTruncated {
		details["phase_trace_truncated"] = true
	}
	details["phase_trace_expected"] = runDiagnosticPhaseExpectations(result.diagnosticPhases)
	if result.Error != "" {
		details["error_code"] = diagnosticErrorCode(result.Error)
	}
	state := result.providerCaptureState
	if state == "" {
		state = "not_captured"
	}
	reason := result.providerCaptureReason
	if reason == "" {
		reason = "provider_payload_not_retained"
		if state == "unavailable" {
			reason = "credential_protection_unavailable"
		} else if len(result.providerPayload) > 2 {
			reason = ""
		}
	}
	if result.Error != "" {
		if state == "not_captured" {
			state = "unavailable"
			reason = "run_completed_with_error"
		}
	}
	diagnosticCtx, diagnosticCancel := context.WithTimeout(ctx, dreamDiagnosticRunTimeout)
	defer diagnosticCancel()
	phaseCtx, phaseCancel := context.WithTimeout(diagnosticCtx, dreamDiagnosticPhaseTimeout)
	defer phaseCancel()
	phaseDeadline, _ := phaseCtx.Deadline()
	details["phase_trace_deadline_at"] = phaseDeadline.UTC().Format(time.RFC3339Nano)
	newCaptureContext := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(phaseCtx, dreamDiagnosticCaptureTimeout)
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	input := dreamcontract.DreamDiagnosticCaptureInput{
		TeamID: result.TeamID, RunID: result.RunID, Phase: "run", Outcome: outcome,
		Cause: diagnosticErrorCode(result.Error), Details: details, CaptureState: state, CaptureReason: reason,
		Payload:    result.providerPayload,
		CapturedAt: &now,
	}
	fallback := input
	fallback.Payload = nil
	fallback.CaptureState = "unavailable"
	fallback.CaptureReason = "diagnostic_capture_failed"
	fallback.Details = map[string]any{"status": result.Status, "capture_failed": true}
	if expected, ok := details["phase_trace_expected"]; ok {
		fallback.Details["phase_trace_expected"] = expected
	}
	fallback.Details["phase_trace_deadline_at"] = details["phase_trace_deadline_at"]
	if result.diagnosticPhasesTruncated {
		fallback.Details["phase_trace_truncated"] = true
	}
	persistRunCapture := func(capture dreamcontract.DreamDiagnosticCaptureInput) error {
		captureCtx, captureCancel := newCaptureContext()
		defer captureCancel()
		return s.deps.Diagnostics.RecordDreamRunDiagnostics(captureCtx, capture)
	}
	runCaptureErr := persistRunCapture(input)
	if runCaptureErr != nil {
		runCaptureErr = persistRunCapture(fallback)
	}
	if runCaptureErr != nil {
		if s.deps.Logger != nil {
			s.deps.Logger.Error("dream diagnostic capture unavailable", runCaptureErr,
				observability.String("team_id", result.TeamID),
				observability.String("run_id", result.RunID),
				observability.String("error_code", "dream_diagnostic_capture_unavailable"),
			)
		}
		return
	}
	newPhaseCaptureContext := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(phaseCtx, dreamDiagnosticCaptureTimeout)
	}
	for _, phase := range result.diagnosticPhases {
		if phaseCtx.Err() != nil {
			break
		}
		phaseInput := dreamcontract.DreamDiagnosticCaptureInput{
			TeamID: result.TeamID, RunID: result.RunID, HypothesisID: phase.hypothesisID,
			Phase: phase.phase, Outcome: phase.outcome, Cause: diagnosticErrorCode(phase.cause),
			Details: phase.details, CaptureState: phase.captureState, CaptureReason: phase.captureReason,
			CapturedAt: input.CapturedAt,
		}
		if phaseInput.CaptureState == "" {
			phaseInput.CaptureState = "not_captured"
		}
		if phaseInput.CaptureReason == "" && phaseInput.CaptureState == "not_captured" {
			phaseInput.CaptureReason = "phase_metadata_only"
		}
		captureCtx, captureCancel := newPhaseCaptureContext()
		phaseErr := s.deps.Diagnostics.RecordDreamDiagnostic(captureCtx, phaseInput)
		captureCancel()
		if phaseErr != nil {
			phaseFallback := phaseInput
			phaseFallback.Details = map[string]any{"capture_failed": true}
			phaseFallback.CaptureState = "unavailable"
			phaseFallback.CaptureReason = "diagnostic_capture_failed"
			fallbackCtx, fallbackCancel := newPhaseCaptureContext()
			fallbackErr := s.deps.Diagnostics.RecordDreamDiagnostic(fallbackCtx, phaseFallback)
			fallbackCancel()
			if fallbackErr == nil {
				continue
			}
			if s.deps.Logger != nil {
				s.deps.Logger.Error("dream diagnostic phase unavailable", fallbackErr,
					observability.String("team_id", result.TeamID),
					observability.String("run_id", result.RunID),
					observability.String("phase", phase.phase),
					observability.String("error_code", "dream_diagnostic_phase_unavailable"),
				)
			}
			break
		}
	}
}

func (s *service) recordRunDiagnosticAfterCompletion(ctx context.Context, result *RunCycleResult, completionErr error) {
	if errors.Is(completionErr, dreamcontract.ErrDreamCycleLeaseLost) {
		return
	}
	appendRunDiagnosticPhase(result, "disposition", "failed", completionErr.Error(), map[string]any{"finalization": "complete_cycle"})
	s.recordRunDiagnostic(ctx, result)
}

func diagnosticErrorCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return "error_present:sha256:" + hex.EncodeToString(digest[:8])
}

func (s *service) recordHypothesisDiagnostic(ctx context.Context, record *dreamcontract.HypothesisRecord, phase, outcome, cause string, details map[string]any) {
	if s == nil || s.deps.Diagnostics == nil || record == nil || record.CycleRunID == "" {
		return
	}
	state, reason := "not_captured", "confirmation_payload_not_retained"
	if cause != "" {
		state, reason = "unavailable", "confirmation_completed_with_error"
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	diagnosticCtx, diagnosticCancel := context.WithTimeout(ctx, dreamDiagnosticCaptureTimeout)
	defer diagnosticCancel()
	newCaptureContext := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(diagnosticCtx, dreamDiagnosticCaptureTimeout)
	}
	input := dreamcontract.DreamDiagnosticCaptureInput{
		TeamID: record.TeamID, RunID: record.CycleRunID, HypothesisID: record.HypothesisID,
		Phase: phase, Outcome: outcome, Cause: diagnosticErrorCode(cause), Details: details,
		CaptureState: state, CaptureReason: reason, CapturedAt: &now,
	}
	captureCtx, cancel := newCaptureContext()
	err := s.deps.Diagnostics.RecordDreamDiagnostic(captureCtx, input)
	cancel()
	if err != nil {
		fallback := input
		fallback.Details = map[string]any{"capture_failed": true}
		fallback.CaptureState = "unavailable"
		fallback.CaptureReason = "diagnostic_capture_failed"
		fallbackCtx, fallbackCancel := newCaptureContext()
		fallbackErr := s.deps.Diagnostics.RecordDreamDiagnostic(fallbackCtx, fallback)
		fallbackCancel()
		if fallbackErr == nil {
			err = nil
		}
	}
	if err != nil && s.deps.Logger != nil {
		s.deps.Logger.Error("dream diagnostic capture unavailable", err,
			observability.String("team_id", record.TeamID),
			observability.String("run_id", record.CycleRunID),
			observability.String("hypothesis_id", record.HypothesisID),
			observability.String("error_code", "dream_diagnostic_capture_unavailable"),
		)
	}
}
