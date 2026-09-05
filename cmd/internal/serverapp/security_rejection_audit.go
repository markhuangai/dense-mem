package serverapp

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/service"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

type securityRejectionAuditAdapter struct {
	audit securityRejectionAuditAppender
}

type securityRejectionAuditAppender interface {
	Append(ctx context.Context, entry service.AuditLogEntry) error
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
	teamID := input.TeamID
	actorProfileID := input.ActorProfileID
	return a.audit.Append(ctx, securityRejectionAuditEntry(
		input.EventID, teamID, actorProfileID, input.ActorRole, input.CorrelationID,
		input.Surface, input.ReasonCode, input.EvidenceCount, flattenRememberSecuritySignals(input.Signals), input.SignalsTruncated,
	))
}

type securityRejectionAuditSignal struct {
	EvidenceIndex int
	Source        string
	Kind          string
	RuleID        string
	Severity      string
	SpanStart     int
	SpanEnd       int
}

func flattenRememberSecuritySignals(input []rememberapp.SecurityRejectionAuditSignal) []securityRejectionAuditSignal {
	result := make([]securityRejectionAuditSignal, 0, len(input))
	for _, signal := range input {
		result = append(result, securityRejectionAuditSignal{
			EvidenceIndex: signal.EvidenceIndex, Source: signal.Source, Kind: signal.Kind,
			RuleID: signal.RuleID, Severity: signal.Severity, SpanStart: signal.SpanStart, SpanEnd: signal.SpanEnd,
		})
	}
	return result
}

func securityRejectionAuditEntry(
	eventID, teamID, actorProfileID, actorRole, correlationID, surface, reasonCode string,
	evidenceCount int, inputSignals []securityRejectionAuditSignal, signalsTruncated bool,
) service.AuditLogEntry {
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
	return service.AuditLogEntry{
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
