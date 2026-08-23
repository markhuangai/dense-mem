package serverapp

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

type securityRejectionAuditAdapter struct {
	audit securityRejectionAuditAppender
}

type securityRejectionAuditAppender interface {
	Append(ctx context.Context, entry service.AuditLogEntry) error
}

func newSecurityRejectionAuditAdapter(audit securityRejectionAuditAppender) memoryservice.SecurityRejectionAuditor {
	return securityRejectionAuditAdapter{audit: audit}
}

func newRememberSecurityRejectionAuditAdapter(audit securityRejectionAuditAppender) rememberapp.SecurityRejectionAuditor {
	return rememberSecurityRejectionAuditAdapter{audit: audit}
}

type rememberSecurityRejectionAuditAdapter struct {
	audit securityRejectionAuditAppender
}

func (a rememberSecurityRejectionAuditAdapter) RecordSecurityRejection(
	ctx context.Context,
	input rememberapp.SecurityRejectionAuditInput,
) error {
	if a.audit == nil {
		return errors.New("security rejection audit appender is required")
	}
	signals := make([]any, 0, len(input.Signals))
	for _, signal := range input.Signals {
		signals = append(signals, map[string]any{
			"evidence_index": signal.EvidenceIndex,
			"source":         signal.Source,
			"kind":           signal.Kind,
			"rule_id":        signal.RuleID,
			"severity":       signal.Severity,
			"span_start":     signal.SpanStart,
			"span_end":       signal.SpanEnd,
		})
	}
	teamID := input.TeamID
	actorProfileID := input.ActorProfileID
	return a.audit.Append(ctx, service.AuditLogEntry{
		ID:            input.EventID,
		ProfileID:     &teamID,
		Operation:     "SECURITY_REJECTED",
		EntityType:    "memory_intake_attempt",
		EntityID:      input.EventID,
		ActorKeyID:    &actorProfileID,
		ActorRole:     input.ActorRole,
		CorrelationID: input.CorrelationID,
		Metadata: map[string]any{
			"surface":           input.Surface,
			"reason_code":       input.ReasonCode,
			"evidence_count":    input.EvidenceCount,
			"signals":           signals,
			"signals_truncated": input.SignalsTruncated,
		},
	})
}

func (a securityRejectionAuditAdapter) RecordSecurityRejection(
	ctx context.Context,
	input memoryservice.SecurityRejectionAuditInput,
) error {
	if a.audit == nil {
		return errors.New("security rejection audit appender is required")
	}
	signals := make([]any, 0, len(input.Signals))
	for _, signal := range input.Signals {
		signals = append(signals, map[string]any{
			"evidence_index": signal.EvidenceIndex,
			"source":         signal.Source,
			"kind":           signal.Kind,
			"rule_id":        signal.RuleID,
			"severity":       signal.Severity,
			"span_start":     signal.SpanStart,
			"span_end":       signal.SpanEnd,
		})
	}
	teamID := input.TeamID
	actorProfileID := input.ActorProfileID
	return a.audit.Append(ctx, service.AuditLogEntry{
		ID:            input.EventID,
		ProfileID:     &teamID,
		Operation:     "SECURITY_REJECTED",
		EntityType:    "memory_intake_attempt",
		EntityID:      input.EventID,
		ActorKeyID:    &actorProfileID,
		ActorRole:     input.ActorRole,
		CorrelationID: input.CorrelationID,
		Metadata: map[string]any{
			"surface":           input.Surface,
			"reason_code":       input.ReasonCode,
			"evidence_count":    input.EvidenceCount,
			"signals":           signals,
			"signals_truncated": input.SignalsTruncated,
		},
	})
}
