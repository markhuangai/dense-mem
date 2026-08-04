package serverapp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestSecurityRejectionAuditAdapterWritesBoundedAuditEntry(t *testing.T) {
	appender := &securityRejectionAuditAppenderStub{}
	auditor := newSecurityRejectionAuditAdapter(appender)
	err := auditor.RecordSecurityRejection(context.Background(), memoryservice.SecurityRejectionAuditInput{
		EventID:        "e3f30b90-62b0-47c6-bdae-2b5cc0c0d9d0",
		TeamID:         "adc56b94-9853-45d6-b970-aafadf2d1c5d",
		ActorProfileID: "b5ca9fc3-60a6-4e65-9227-c22a91f85d5c",
		ActorRole:      "member",
		CorrelationID:  "security-rejection-test",
		Surface:        "remember",
		ReasonCode:     memoryservice.SubmissionSecurityErrorRejected,
		EvidenceCount:  1,
		PolicyVersion:  "dense-mem.remember-intake-security.v1",
		PolicyHash:     "sha256:test",
		Signals: []memoryservice.SecurityRejectionAuditSignal{{
			EvidenceIndex: 0,
			Kind:          "instruction_override",
			RuleID:        "instruction_override",
			Severity:      "critical",
			SpanStart:     1,
			SpanEnd:       9,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "SECURITY_REJECTED", appender.entry.Operation)
	require.Equal(t, "memory_intake_attempt", appender.entry.EntityType)
	require.Equal(t, "security-rejection-test", appender.entry.CorrelationID)
	require.Nil(t, appender.entry.BeforePayload)
	require.Nil(t, appender.entry.AfterPayload)
	require.Equal(t, "remember", appender.entry.Metadata["surface"])
	require.Equal(t, memoryservice.SubmissionSecurityErrorRejected, appender.entry.Metadata["reason_code"])
	signals, ok := appender.entry.Metadata["signals"].([]any)
	require.True(t, ok)
	require.Len(t, signals, 1)
	entry := appender.entry
	require.NotContains(t, entry.Metadata, "content")
}

func TestSecurityRejectionAuditAdapterPropagatesAppenderFailure(t *testing.T) {
	auditor := newSecurityRejectionAuditAdapter(&securityRejectionAuditAppenderStub{err: errors.New("storage unavailable")})
	err := auditor.RecordSecurityRejection(context.Background(), memoryservice.SecurityRejectionAuditInput{})
	require.ErrorContains(t, err, "storage unavailable")
}

type securityRejectionAuditAppenderStub struct {
	entry service.AuditLogEntry
	err   error
}

func (s *securityRejectionAuditAppenderStub) Append(_ context.Context, entry service.AuditLogEntry) error {
	s.entry = entry
	return s.err
}
