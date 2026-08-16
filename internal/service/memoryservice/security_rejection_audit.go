package memoryservice

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

var ErrSecurityAuditPersistence = errors.New("memory security: audit persistence failed")

// SecurityRejectionAuditor records a pre-staging rejection without accepting
// or retaining the rejected evidence in the knowledge ledger.
type SecurityRejectionAuditor interface {
	RecordSecurityRejection(ctx context.Context, input SecurityRejectionAuditInput) error
}

// SecurityRejectionAuditInput intentionally excludes evidence, decoded text,
// content hashes, provider data, and credential material.
type SecurityRejectionAuditInput struct {
	EventID          string
	TeamID           string
	ActorProfileID   string
	ActorRole        string
	CorrelationID    string
	Surface          string
	ReasonCode       string
	EvidenceCount    int
	Signals          []SecurityRejectionAuditSignal
	SignalsTruncated bool
}

type SecurityRejectionAuditSignal struct {
	EvidenceIndex int
	Source        string
	Kind          string
	RuleID        string
	Severity      string
	SpanStart     int
	SpanEnd       int
}

func recordSubmissionSecurityRejection(
	ctx context.Context,
	auditor SecurityRejectionAuditor,
	actor requestctx.Actor,
	surface string,
	scan SubmissionSecurityBatchScan,
	rejection error,
) error {
	if auditor == nil {
		return ErrSecurityAuditPersistence
	}
	signals := make([]SecurityRejectionAuditSignal, 0, len(scan.Signals))
	for _, signal := range scan.Signals {
		signals = append(signals, SecurityRejectionAuditSignal{
			EvidenceIndex: signal.EvidenceIndex,
			Source:        signal.Source,
			Kind:          signal.Kind,
			RuleID:        signal.RuleID,
			Severity:      signal.Severity,
			SpanStart:     signal.Start,
			SpanEnd:       signal.End,
		})
	}
	reason := SubmissionSecurityErrorRejected
	var typed *SubmissionSecurityError
	if errors.As(rejection, &typed) && strings.TrimSpace(typed.Code) != "" {
		reason = typed.Code
	}
	if err := auditor.RecordSecurityRejection(ctx, SecurityRejectionAuditInput{
		EventID:          uuid.NewString(),
		TeamID:           actor.TeamID.String(),
		ActorProfileID:   actor.OwnerID.String(),
		ActorRole:        strings.TrimSpace(actor.Role),
		CorrelationID:    correlation.FromContext(ctx),
		Surface:          strings.TrimSpace(surface),
		ReasonCode:       reason,
		EvidenceCount:    scan.EvidenceCount,
		Signals:          signals,
		SignalsTruncated: scan.SignalsTruncated,
	}); err != nil {
		return ErrSecurityAuditPersistence
	}
	return nil
}
