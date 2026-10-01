//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	operationsapp "github.com/markhuangai/dense-mem/internal/operations"
)

func operationLogOwnershipIDs(first, last, step int) []string {
	var ids []string
	for i := first; i <= last; i += step {
		ids = append(ids, fmt.Sprintf("00000000-0000-4000-8000-%012d", 2000+i))
	}
	return ids
}

func TestOperationLogOwnershipEquivalentFilters(t *testing.T) {
	f := newOperationLogOwnershipFixture(t)
	ctx := context.Background()
	falseValue, trueValue := false, true
	zeroTeam := uuid.Nil
	from := f.now.Add(-3 * time.Second).In(time.FixedZone("offset", 2*60*60))
	to := f.now.Add(-time.Second).In(from.Location())
	point := f.now.Add(-2 * time.Second).In(from.Location())
	for _, tc := range []struct {
		name   string
		filter domain.OperationLogFilter
		total  int64
		ids    []string
	}{
		{"default", domain.OperationLogFilter{}, 2000, operationLogOwnershipIDs(1, 100, 1)},
		{"maximum", domain.OperationLogFilter{Limit: 999}, 2000, operationLogOwnershipIDs(1, 500, 1)},
		{"negative and unknown defaults", domain.OperationLogFilter{Limit: -1, Offset: -1, Sort: "other", Direction: "other"}, 2000, operationLogOwnershipIDs(1, 100, 1)},
		{"team", domain.OperationLogFilter{TeamID: &f.teamA}, 1200, operationLogOwnershipIDs(1, 100, 1)},
		{"team invocation", domain.OperationLogFilter{TeamID: &f.teamA, InvocationID: " invocation-1 "}, 600, operationLogOwnershipIDs(1, 100, 1)},
		{"team invocation maximum", domain.OperationLogFilter{TeamID: &f.teamA, InvocationID: "invocation-1", Limit: 500}, 600, operationLogOwnershipIDs(1, 500, 1)},
		{"unscoped invocation", domain.OperationLogFilter{InvocationID: "invocation-1"}, 1400, operationLogOwnershipIDs(1, 100, 1)},
		{"zero team invocation", domain.OperationLogFilter{TeamID: &zeroTeam, InvocationID: "invocation-1"}, 1400, operationLogOwnershipIDs(1, 100, 1)},
		{"other team same invocation", domain.OperationLogFilter{TeamID: &f.teamB, InvocationID: "invocation-1"}, 800, operationLogOwnershipIDs(1201, 1300, 1)},
		{"request hash", domain.OperationLogFilter{TeamID: &f.teamA, InvocationID: " invocation-1 ", RequestHash: " hash-1 "}, 3, operationLogOwnershipIDs(1, 3, 1)},
		{"correlation", domain.OperationLogFilter{CorrelationID: " corr-1 "}, 3, operationLogOwnershipIDs(1, 3, 1)},
		{"attempt aliases", domain.OperationLogFilter{AttemptID: " attempt-1 "}, 3, operationLogOwnershipIDs(1, 3, 1)},
		{"classification", domain.OperationLogFilter{RequestHash: "hash-1", Classification: " replay "}, 1, operationLogOwnershipIDs(2, 2, 1)},
		{"explicit false", domain.OperationLogFilter{RequestHash: "hash-1", Retryable: &falseValue}, 2, operationLogOwnershipIDs(1, 3, 2)},
		{"explicit true", domain.OperationLogFilter{RequestHash: "hash-1", Retryable: &trueValue}, 1, operationLogOwnershipIDs(2, 2, 1)},
		{"event and reference", domain.OperationLogFilter{Event: " operation ownership ", ReferenceType: " submission ", ReferenceID: " ref-1 "}, 3, operationLogOwnershipIDs(1, 3, 1)},
		{"UTC inclusive range", domain.OperationLogFilter{From: &from, To: &to}, 3, operationLogOwnershipIDs(1, 3, 1)},
		{"UTC inclusive point", domain.OperationLogFilter{From: &point, To: &point}, 1, operationLogOwnershipIDs(2, 2, 1)},
		{"severity", domain.OperationLogFilter{Severity: " debug ", RequestHash: "hash-1"}, 1, operationLogOwnershipIDs(2, 2, 1)},
		{"severity descending", domain.OperationLogFilter{Sort: " SEVERITY ", Direction: " DESC ", Limit: 7}, 2000, operationLogOwnershipIDs(6, 42, 6)},
		{"severity ascending", domain.OperationLogFilter{Sort: "severity", Direction: "asc", Limit: 7}, 2000, operationLogOwnershipIDs(1, 37, 6)},
		{"timestamp ascending", domain.OperationLogFilter{Sort: "timestamp", Direction: "asc", Limit: 2}, 2000, []string{operationLogOwnershipIDs(2000, 2000, 1)[0], operationLogOwnershipIDs(1999, 1999, 1)[0]}},
		{"offset", domain.OperationLogFilter{RequestHash: "hash-1", Offset: 2}, 3, operationLogOwnershipIDs(3, 3, 1)},
		{"empty severity", domain.OperationLogFilter{Severity: " \t "}, 2000, operationLogOwnershipIDs(1, 100, 1)},
		{"native severity", domain.OperationLogFilter{Severity: " native "}, 0, nil},
		{"all identifiers", domain.OperationLogFilter{Limit: 2, Sort: "severity", Direction: "asc", Severity: " DEBUG ", Event: " operation ownership ", TeamID: &f.teamA, CorrelationID: " corr-1 ", InvocationID: " invocation-1 ", RequestHash: " hash-1 ", AttemptID: " attempt-1 ", Classification: " replay ", Retryable: &trueValue, ReferenceType: " submission ", ReferenceID: " ref-1 ", From: &from, To: &to}, 1, operationLogOwnershipIDs(2, 2, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.counters.reset()
			direct, err := f.repo.List(ctx, tc.filter)
			require.NoError(t, err)
			directSignature, directCounts := f.counters.querySignature(), f.counters.counts()
			f.counters.reset()
			page, err := f.service.ListOperationLogs(ctx, tc.filter)
			require.NoError(t, err)
			require.Equal(t, direct, page)
			require.Equal(t, directSignature, f.counters.querySignature())
			require.Equal(t, directCounts, f.counters.counts())
			require.Equal(t, 1, f.counters.transactions)
			require.Equal(t, 1, f.counters.commits)
			require.Zero(t, f.counters.rollbacks)
			require.Equal(t, tc.total, page.Total)
			var ids []string
			for _, item := range page.Items {
				ids = append(ids, item.ID.String())
			}
			require.Equal(t, tc.ids, ids)
			if tc.name == "team invocation" {
				require.Equal(t, f.profileA, *page.Items[0].ProfileID)
				require.Equal(t, f.profileB, *page.Items[1].ProfileID)
				for _, item := range page.Items {
					require.Equal(t, f.teamA, *item.TeamID)
				}
			}
			evidence, err := json.Marshal(map[string]any{
				"case": tc.name, "result_signature_sha256": operationLogOwnershipSignature(page),
				"query_contract_sha256": directSignature, "counts": directCounts, "provider_calls": 0,
			})
			require.NoError(t, err)
			t.Logf("operation_log_equivalence=%s", evidence)
		})
	}
	require.Same(t, from.Location(), to.Location())
	appPool, err := f.appDB.DB()
	require.NoError(t, err)
	require.NoError(t, appPool.PingContext(ctx))
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, truncateLedgerFixtures))
	page, err := f.repo.List(ctx, domain.OperationLogFilter{})
	require.NoError(t, err)
	require.Zero(t, page.Total)
	require.Empty(t, page.Items)
}

func TestOperationLogOwnershipWritesFlushBeforeReadsAndPreserveFailures(t *testing.T) {
	f := newOperationLogOwnershipFixture(t)
	ctx := context.Background()
	for i, tc := range []struct {
		label, severity string
		rank            int
	}{
		{"blank", " ", 0}, {"native", " native ", 0}, {"provided", " warn ", 17},
	} {
		require.NoError(t, f.service.WriteLog(ctx, observability.LogRecord{
			Message: "queued severity", Severity: tc.severity, SeverityRank: tc.rank,
			Timestamp: f.now.Add(time.Duration(i+1) * time.Second), Attrs: map[string]any{"case": tc.label},
		}))
		before, err := f.repo.List(ctx, domain.OperationLogFilter{Event: "queued severity"})
		require.NoError(t, err)
		require.Zero(t, before.Total)
	}
	page, err := f.service.ListOperationLogs(ctx, domain.OperationLogFilter{Event: " queued severity "})
	require.NoError(t, err)
	require.EqualValues(t, 3, page.Total)
	for _, item := range page.Items {
		switch item.Attrs["case"] {
		case "blank":
			require.Equal(t, "INFO", item.Severity)
			require.Equal(t, 20, item.SeverityRank)
		case "native":
			require.Equal(t, "NATIVE", item.Severity)
			require.Equal(t, 20, item.SeverityRank)
		case "provided":
			require.Equal(t, "WARN", item.Severity)
			require.Equal(t, 17, item.SeverityRank)
		default:
			t.Fatalf("unknown queued log case: %v", item.Attrs["case"])
		}
	}
	require.NoError(t, f.repo.AppendBatch(ctx, []domain.OperationLog{{ID: uuid.MustParse("00000000-0000-4000-8000-000000009001"), Timestamp: f.now, Severity: " ", SeverityRank: 7, Message: "direct severity"}}))
	direct, err := f.repo.List(ctx, domain.OperationLogFilter{Event: "direct severity"})
	require.NoError(t, err)
	require.Len(t, direct.Items, 1)
	require.Equal(t, "INFO", direct.Items[0].Severity)
	require.Equal(t, 7, direct.Items[0].SeverityRank)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, canceledErr := f.repo.List(canceled, domain.OperationLogFilter{})
	require.ErrorIs(t, canceledErr, context.Canceled)
	require.ErrorContains(t, canceledErr, "failed to list operation logs")
	var unavailable *operationsapp.OperationLogServiceImpl
	_, unavailableErr := unavailable.ListOperationLogs(ctx, domain.OperationLogFilter{})
	require.EqualError(t, unavailableErr, "operation log service unavailable")
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`CREATE FUNCTION operation_log_ownership_fail() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'operation log ownership sink failure'; END $$;
			CREATE TRIGGER operation_log_ownership_fail BEFORE INSERT ON operation_logs
			FOR EACH ROW EXECUTE FUNCTION operation_log_ownership_fail();`).Error
	}))
	t.Cleanup(func() {
		require.NoError(t, f.adminDB.Exec(`DROP TRIGGER operation_log_ownership_fail ON operation_logs; DROP FUNCTION operation_log_ownership_fail();`).Error)
	})
	require.NoError(t, f.service.WriteLog(ctx, observability.LogRecord{Message: "failed flush", Timestamp: f.now}))
	f.counters.reset()
	_, flushErr := f.service.ListOperationLogs(ctx, domain.OperationLogFilter{})
	require.ErrorContains(t, flushErr, "failed to append operation logs")
	require.ErrorContains(t, flushErr, "operation log ownership sink failure")
	for _, query := range f.counters.queries {
		require.NotContains(t, strings.ToLower(query), "select count(*) from operation_logs")
	}
	require.Equal(t, 1, f.counters.transactions)
	require.Equal(t, 1, f.counters.rollbacks)
	require.ErrorIs(t, f.service.CheckReadiness(ctx), operationsapp.ErrOperationLogSinkUnavailable)
	evidence, err := json.Marshal(map[string]any{"flush_error": flushErr.Error(), "cancel_error": canceledErr.Error(), "unavailable_error": unavailableErr.Error(), "read_after_failed_flush": false, "typed_sink_unavailable": true})
	require.NoError(t, err)
	t.Logf("operation_log_failure_equivalence=%s", evidence)
}
