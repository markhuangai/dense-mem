package serverapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func (p *rememberSynchronousProcessor) logRememberFailureRecordError(
	input rememberapp.RememberProcessRequest,
	attemptID string,
	phase string,
	errorCode string,
	correlationID string,
	failure error,
) {
	if p == nil || p.logger == nil {
		return
	}
	attrs := rememberFailureLogAttrs(input, attemptID, phase, errorCode, correlationID)
	attrs = append(attrs, observability.String("recovery_error_code", rememberFailureRecoveryErrorCode(failure)))
	p.logger.Error("remember_failure_record_failed", rememberFailureRecoveryLogError(failure), attrs...)
}

func (p *rememberSynchronousProcessor) logRememberFailureRetentionDegraded(
	input rememberapp.RememberProcessRequest,
	attemptID string,
	phase string,
) {
	if p == nil || p.logger == nil {
		return
	}
	attrs := rememberFailureLogAttrs(input, attemptID, phase, "retention_sync_failed", rememberProcessCorrelationID(input.Metadata))
	attrs = append(attrs, observability.String("degradation_code", "legal_hold_retention_sync_failed"))
	p.logger.Warn("remember_failure_retention_degraded", attrs...)
}

func rememberFailureRecoveryLogError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("remember failure record persistence timed out: %w", context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("remember failure record persistence was cancelled: %w", context.Canceled)
	default:
		return errors.New("remember failure record persistence failed")
	}
}

func rememberFailureLogAttrs(
	input rememberapp.RememberProcessRequest,
	attemptID string,
	phase string,
	errorCode string,
	correlationID string,
) []observability.LogAttr {
	return []observability.LogAttr{
		observability.String("team_id", input.TeamID),
		observability.String("profile_id", input.OwnerProfileID),
		observability.CorrelationID(correlationID),
		observability.String("reference_type", "remember_attempt"),
		observability.String("reference_id", attemptID),
		observability.String("submission_id", attemptID),
		observability.String("failed_phase", phase),
		observability.String("error_code", errorCode),
	}
}

func rememberProcessCorrelationID(metadata map[string]any) string {
	if actor, ok := metadata["actor"].(map[string]any); ok {
		correlationID, _ := actor["correlation_id"].(string)
		return strings.TrimSpace(correlationID)
	}
	return ""
}

func rememberFailureRecoveryErrorCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "request_cancelled"
	default:
		return "persistence_failed"
	}
}
