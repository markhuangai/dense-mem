package processor

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type rememberInvocationWriter interface {
	RecordRememberInvocationDiagnostic(context.Context, repository.RememberInvocationDiagnosticInput) error
}

func processErrOrCause(processErr, fallback error) error {
	if processErr == nil {
		return fallback
	}
	if cause := errors.Unwrap(processErr); cause != nil {
		return cause
	}
	return processErr
}

func (p *rememberSynchronousProcessor) recordRememberInvocation(
	ctx context.Context,
	input rememberapp.RememberProcessRequest,
	invocationID, classification, canonicalAttemptID, phase string,
	cause error,
	status *rememberapp.SubmissionStatusResult,
	exchanges []modelprovider.ProviderExchange,
) {
	if p == nil || p.ledger == nil {
		return
	}
	requestctx.SetRememberInvocationID(ctx, invocationID)
	writer, ok := p.ledger.(rememberInvocationWriter)
	if !ok {
		return
	}
	if status == nil {
		var processErr *rememberapp.RememberProcessError
		if errors.As(cause, &processErr) {
			status = processErr.Status
		}
	}
	publicResult := map[string]any{}
	if status != nil {
		encoded, err := json.Marshal(status)
		if err == nil {
			_ = json.Unmarshal(encoded, &publicResult)
		}
	}
	var callerResponse []byte
	callerAvailable := false
	if status != nil {
		if capture := rememberapp.DiagnosticCaptureFromContext(ctx); capture != nil {
			callerAvailable = true
			callerResponse, _ = capture.ProjectResponse(publicResult, status != nil && len(status.Errors) > 0)
		}
	}
	diagnostics := rememberFailureDiagnosticsWithAuthenticationSecrets(
		input, publicResult, exchanges, callerResponse,
		rememberCallerResponseDelivered(ctx, cause), callerAvailable,
		observability.AuthenticationSecretsFromContext(ctx), p.protector,
	)
	requestBody := []byte(nil)
	requestCaptureState, requestCaptureReason := "", ""
	var callerBody []byte
	callerCaptureState, callerCaptureReason := "", ""
	providerExchanges := make([]repository.RememberAttemptDiagnosticInput, 0, len(exchanges))
	for _, item := range diagnostics {
		switch item.Kind {
		case "original_request":
			requestBody = append([]byte(nil), item.RequestBody...)
			requestCaptureState, requestCaptureReason = item.CaptureState, item.CaptureReason
		case "provider_exchange":
			providerExchanges = append(providerExchanges, item)
		case "caller_response":
			callerBody = append([]byte(nil), item.ResponseBody...)
			callerCaptureState, callerCaptureReason = item.CaptureState, item.CaptureReason
		}
	}
	outcome := "completed"
	if cause != nil {
		outcome = "failed"
	}
	if status != nil && len(status.Errors) > 0 {
		outcome = "failed"
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) ||
		errors.Is(cause, rememberapp.ErrRememberRequestCancelled) || errors.Is(cause, rememberapp.ErrRememberRequestTimeout) {
		outcome = "cancelled"
	} else if outcome == "completed" && classification == "replay" {
		outcome = "replayed"
	} else if outcome == "completed" && status != nil && len(status.RelationshipResults) == 0 {
		outcome = "evaluated_zero"
	}
	if classification == "conflict" && (outcome == "completed" || outcome == "evaluated_zero" || errors.Is(cause, rememberapp.ErrRememberConflict) || errors.Is(cause, repository.ErrIdempotencyConflict)) {
		outcome = "conflict"
	}
	errorCode := ""
	retryable := false
	if status != nil && len(status.Errors) > 0 {
		errorCode = status.Errors[0].Code
		retryable = status.Errors[0].Retryable
	} else if cause != nil {
		code := rememberFailureCode(phase, cause)
		errorCode = string(code)
		retryable = rememberapp.StatusError(code).Retryable
	}
	failedPhase := ""
	if outcome == "failed" || outcome == "cancelled" {
		failedPhase = phase
	}
	record := repository.RememberInvocationDiagnosticInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, InvocationID: invocationID,
		CanonicalAttemptID: canonicalAttemptID, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
		RequestHash: input.RequestHash, CorrelationID: rememberProcessCorrelationID(input.Metadata),
		Classification: classification, Outcome: outcome, FailedPhase: failedPhase, ErrorCode: errorCode,
		Retryable: retryable, RequestBody: requestBody, RequestCaptureState: requestCaptureState, RequestCaptureReason: requestCaptureReason,
		ProviderExchanges: providerExchanges, CallerResponse: callerBody,
		CallerResponseCaptureState: callerCaptureState, CallerResponseCaptureReason: callerCaptureReason,
		Duration:  rememberInvocationDuration(input.InvocationStartedAt),
		CreatedAt: input.InvocationStartedAt, CompletedAt: time.Now().UTC(),
	}
	writeCtx, cancel := rememberFailureRecoveryContext(ctx)
	defer cancel()
	if err := writer.RecordRememberInvocationDiagnostic(writeCtx, record); err != nil && p.logger != nil {
		warningEvent := "remember_invocation_diagnostic_unavailable"
		errorCode := "diagnostic_record_failed"
		diagnosticState := "unavailable"
		if errors.Is(err, repository.ErrRememberFailureRetentionDegraded) {
			warningEvent = "remember_invocation_retention_degraded"
			errorCode = "retention_sync_failed"
			diagnosticState = "recorded_retention_degraded"
		}
		warningAttrs := []observability.LogAttr{
			observability.String("error_code", errorCode),
			observability.String("diagnostic_state", diagnosticState),
			observability.String("invocation_id", invocationID),
		}
		if contextual, ok := p.logger.(observability.ContextLogProvider); ok {
			contextual.WarnContext(writeCtx, warningEvent, append(warningAttrs, observability.String("error", err.Error()))...)
		} else {
			p.logger.Warn(warningEvent, append(warningAttrs, observability.String("error", "[diagnostic unavailable]"))...)
		}
	}
	if p.logger != nil {
		attrs := []observability.LogAttr{
			observability.String("invocation_id", invocationID),
			observability.String("classification", classification),
			observability.String("outcome", outcome),
			observability.Bool("retryable", retryable),
			observability.String("phase", phase),
			observability.String("canonical_attempt_id", canonicalAttemptID),
			observability.String("request_hash", input.RequestHash),
			observability.Int("duration_ms", int(record.Duration/time.Millisecond)),
			observability.CorrelationID(rememberProcessCorrelationID(input.Metadata)),
		}
		if errorCode != "" {
			attrs = append(attrs, observability.String("error_code", errorCode))
		}
		switch outcome {
		case "failed", "cancelled", "conflict":
			logErr := cause
			if logErr == nil {
				logErr = errors.New("remember invocation completed with an error outcome")
			}
			if contextual, ok := p.logger.(observability.ContextLogProvider); ok {
				contextual.ErrorContext(writeCtx, "remember_invocation_completed", logErr, attrs...)
			} else {
				p.logger.Error("remember_invocation_completed", errors.New("[diagnostic unavailable]"), attrs...)
			}
		default:
			if contextual, ok := p.logger.(observability.ContextLogProvider); ok {
				contextual.InfoContext(writeCtx, "remember_invocation_completed", attrs...)
			} else {
				p.logger.Info("remember_invocation_completed", attrs...)
			}
		}
	}
}

func rememberInvocationDuration(started time.Time) time.Duration {
	if started.IsZero() {
		return 0
	}
	duration := time.Since(started)
	if duration < 0 {
		return 0
	}
	return duration
}
