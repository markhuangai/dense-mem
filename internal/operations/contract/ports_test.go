package contract

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestNormalizeOperationLogFilterBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter domain.OperationLogFilter
		want   domain.OperationLogFilter
	}{
		{"default", domain.OperationLogFilter{}, domain.OperationLogFilter{Limit: 100, Sort: "timestamp", Direction: "desc"}},
		{"negative", domain.OperationLogFilter{Limit: -1, Offset: -1, Sort: "unknown", Direction: "unknown"}, domain.OperationLogFilter{Limit: 100, Sort: "timestamp", Direction: "desc"}},
		{"maximum", domain.OperationLogFilter{Limit: 501, Offset: 9, Sort: " SEVERITY ", Direction: " ASC ", Severity: " warn "}, domain.OperationLogFilter{Limit: 500, Offset: 9, Sort: "severity", Direction: "asc", Severity: "WARN"}},
		{"explicit", domain.OperationLogFilter{Limit: 1, Sort: " TIMESTAMP ", Direction: " DESC ", Severity: " native "}, domain.OperationLogFilter{Limit: 1, Sort: "timestamp", Direction: "desc", Severity: "NATIVE"}},
		{"empty severity", domain.OperationLogFilter{Severity: " \t "}, domain.OperationLogFilter{Limit: 100, Sort: "timestamp", Direction: "desc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeOperationLogFilter(tc.filter)
			require.Equal(t, tc.want, got)
			require.Equal(t, got, NormalizeOperationLogFilter(got))
		})
	}
}

func TestNormalizeOperationLogFilterPreservesIdentityAndUTCInstants(t *testing.T) {
	teamID := uuid.New()
	retryable := false
	from := time.Date(2026, 9, 1, 12, 0, 0, 0, time.FixedZone("offset", 2*60*60))
	to := from.Add(time.Hour)
	filter := domain.OperationLogFilter{
		Event: " event ", CorrelationID: " corr ", InvocationID: " invocation ", RequestHash: " hash ",
		AttemptID: " attempt ", Classification: " replay ", ReferenceType: " type ", ReferenceID: " ref ",
		TeamID: &teamID, Retryable: &retryable, From: &from, To: &to,
	}
	got := NormalizeOperationLogFilter(filter)
	utcFrom, utcTo := from.UTC(), to.UTC()
	require.Equal(t, domain.OperationLogFilter{
		Limit: 100, Sort: "timestamp", Direction: "desc", Event: "event", CorrelationID: "corr",
		InvocationID: "invocation", RequestHash: "hash", AttemptID: "attempt", Classification: "replay",
		ReferenceType: "type", ReferenceID: "ref", TeamID: &teamID, Retryable: &retryable, From: &utcFrom, To: &utcTo,
	}, got)
	require.Same(t, filter.TeamID, got.TeamID)
	require.Same(t, filter.Retryable, got.Retryable)
	require.Same(t, time.UTC, got.From.Location())
	require.Same(t, time.UTC, got.To.Location())
	require.NotSame(t, filter.From, got.From)
	require.NotSame(t, filter.To, got.To)
	require.Equal(t, 12, from.Hour())
	require.Equal(t, 13, to.Hour())
	require.Equal(t, " invocation ", filter.InvocationID)
	nilTeam := uuid.Nil
	require.Same(t, &nilTeam, NormalizeOperationLogFilter(domain.OperationLogFilter{TeamID: &nilTeam}).TeamID)
}

func TestNormalizeOperationLogSeverity(t *testing.T) {
	for input, want := range map[string]string{"": "INFO", " \t ": "INFO", " warn ": "WARN", "trace": "TRACE", " native ": "NATIVE"} {
		t.Run(input, func(t *testing.T) { require.Equal(t, want, NormalizeOperationLogSeverity(input)) })
	}
}
