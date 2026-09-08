package audit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

type rejectionAppenderStub struct {
	entry accessservice.AuditLogEntry
	err   error
}

func (s *rejectionAppenderStub) Append(_ context.Context, entry accessservice.AuditLogEntry) error {
	s.entry = entry
	return s.err
}

func TestRememberSecurityRejectionAuditorWritesBoundedMetadata(t *testing.T) {
	appender := &rejectionAppenderStub{}
	auditor := NewRememberSecurityRejectionAuditor(appender)
	err := auditor.RecordSecurityRejection(context.Background(), rememberapp.SecurityRejectionAuditInput{
		EventID:          "event-1",
		TeamID:           "team-1",
		ActorProfileID:   "profile-1",
		ActorRole:        "member",
		CorrelationID:    "corr-1",
		Surface:          "remember",
		ReasonCode:       rememberapp.SubmissionSecurityErrorRejected,
		EvidenceCount:    1,
		SignalsTruncated: true,
		Signals: []rememberapp.SecurityRejectionAuditSignal{{
			EvidenceIndex: 0,
			Source:        "evidence",
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
	require.Equal(t, "team-1", *appender.entry.ProfileID)
	require.Equal(t, "profile-1", *appender.entry.ActorKeyID)
	require.Equal(t, true, appender.entry.Metadata["signals_truncated"])
	metadata, err := json.Marshal(appender.entry.Metadata)
	require.NoError(t, err)
	require.NotContains(t, string(metadata), "evidence content")
}

func TestRememberSecurityRejectionAuditorPropagatesAppenderFailure(t *testing.T) {
	auditor := NewRememberSecurityRejectionAuditor(&rejectionAppenderStub{err: errors.New("storage unavailable")})
	err := auditor.RecordSecurityRejection(context.Background(), rememberapp.SecurityRejectionAuditInput{})
	require.ErrorContains(t, err, "storage unavailable")
}
