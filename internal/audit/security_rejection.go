package audit

import (
	"context"
	"errors"

	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

// AuditAppender is the narrow write port used by the Remember security
// rejection bridge. It prevents rejected evidence from entering the audit API.
type AuditAppender interface {
	Append(context.Context, accessservice.AuditLogEntry) error
}

type rememberSecurityRejectionAuditor struct {
	audit AuditAppender
}

// NewRememberSecurityRejectionAuditor adapts bounded Remember scanner metadata
// to the existing audit event contract.
func NewRememberSecurityRejectionAuditor(audit AuditAppender) rememberapp.SecurityRejectionAuditor {
	return rememberSecurityRejectionAuditor{audit: audit}
}

func (a rememberSecurityRejectionAuditor) RecordSecurityRejection(
	ctx context.Context,
	input rememberapp.SecurityRejectionAuditInput,
) error {
	if a.audit == nil {
		return errors.New("security rejection audit appender is required")
	}
	return a.audit.Append(ctx, securityRejectionAuditEntry(
		input.EventID, input.TeamID, input.ActorProfileID, input.ActorRole,
		input.CorrelationID, input.Surface, input.ReasonCode, input.EvidenceCount,
		input.Signals, input.SignalsTruncated,
	))
}

func securityRejectionAuditEntry(
	eventID, teamID, actorProfileID, actorRole, correlationID, surface, reasonCode string,
	evidenceCount int, inputSignals []rememberapp.SecurityRejectionAuditSignal, signalsTruncated bool,
) accessservice.AuditLogEntry {
	signals := make([]any, 0, len(inputSignals))
	for _, signal := range inputSignals {
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
	return accessservice.AuditLogEntry{
		ID:            eventID,
		ProfileID:     &teamID,
		Operation:     "SECURITY_REJECTED",
		EntityType:    "memory_intake_attempt",
		EntityID:      eventID,
		ActorKeyID:    &actorProfileID,
		ActorRole:     actorRole,
		CorrelationID: correlationID,
		Metadata: map[string]any{
			"surface":           surface,
			"reason_code":       reasonCode,
			"evidence_count":    evidenceCount,
			"signals":           signals,
			"signals_truncated": signalsTruncated,
		},
	}
}
