package remember

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

var ErrSecurityAuditPersistence = errors.New("memory security: audit persistence failed")

// SecurityRejectionAuditor records a pre-staging rejection without accepting
// or retaining the rejected evidence in the knowledge ledger.
// SecurityRejectionAuditInput intentionally excludes evidence, decoded text,
// content hashes, provider data, and credential material.
func recordSubmissionSecurityRejection(
	ctx context.Context,
	auditor SecurityRejectionAuditor,
	logger observability.LogProvider,
	actor requestctx.Actor,
	surface string,
	scan SubmissionSecurityBatchScan,
	rejection error,
) error {
	return RecordSecurityRejectionAudit(ctx, auditor, logger, securityRejectionAuditInput(ctx, actor, surface, scan, rejection))
}

func securityRejectionAuditInput(
	ctx context.Context,
	actor requestctx.Actor,
	surface string,
	scan SubmissionSecurityBatchScan,
	rejection error,
) SecurityRejectionAuditInput {
	return SecurityRejectionAuditInput{
		EventID:          uuid.NewString(),
		TeamID:           actor.TeamID.String(),
		ActorProfileID:   actor.OwnerID.String(),
		ActorRole:        strings.TrimSpace(actor.Role),
		CorrelationID:    correlation.FromContext(ctx),
		Surface:          strings.TrimSpace(surface),
		ReasonCode:       securityRejectionReasonCode(rejection),
		EvidenceCount:    scan.EvidenceCount,
		Signals:          securityRejectionAuditSignals(scan.Signals),
		SignalsTruncated: scan.SignalsTruncated,
	}
}

func securityRejectionAuditInputForIdempotency(
	ctx context.Context,
	actor requestctx.Actor,
	surface string,
	scan SubmissionSecurityBatchScan,
	rejection error,
	idempotencyKey string,
) SecurityRejectionAuditInput {
	input := securityRejectionAuditInput(ctx, actor, surface, scan, rejection)
	seed := strings.Join([]string{
		"dense-mem/security-rejection",
		actor.TeamID.String(),
		actor.OwnerID.String(),
		strings.TrimSpace(surface),
		strings.TrimSpace(idempotencyKey),
	}, "\x00")
	input.EventID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed)).String()
	return input
}

// RecordSecurityRejectionAudit persists bounded security metadata after the
// caller has established that no terminal attempt is already replayable.
func RecordSecurityRejectionAudit(
	ctx context.Context,
	auditor SecurityRejectionAuditor,
	logger observability.LogProvider,
	input SecurityRejectionAuditInput,
) error {
	if auditor == nil {
		return ErrSecurityAuditPersistence
	}
	if err := auditor.RecordSecurityRejection(ctx, input); err != nil {
		if logger != nil {
			logger.Warn("remember_security_audit_failed",
				observability.String("error_class", fmt.Sprintf("%T", err)),
				observability.String("surface", strings.TrimSpace(input.Surface)),
			)
		}
		return ErrSecurityAuditPersistence
	}
	return nil
}

func securityRejectionAuditSignals(signals []SubmissionSecurityBatchSignal) []SecurityRejectionAuditSignal {
	result := make([]SecurityRejectionAuditSignal, 0, len(signals))
	for _, signal := range signals {
		result = append(result, SecurityRejectionAuditSignal{
			EvidenceIndex: signal.EvidenceIndex,
			Source:        signal.Source,
			Kind:          signal.Kind,
			RuleID:        signal.RuleID,
			Severity:      signal.Severity,
			SpanStart:     signal.Start,
			SpanEnd:       signal.End,
		})
	}
	return result
}

func securityRejectionReasonCode(rejection error) string {
	reason := SubmissionSecurityErrorRejected
	var typed *SubmissionSecurityError
	if errors.As(rejection, &typed) && strings.TrimSpace(typed.Code) != "" {
		reason = typed.Code
	}
	return reason
}
