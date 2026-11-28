package topics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/markhuangai/dense-mem/internal/observability"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceFinalizationDiagnosticsSurviveCancellation(t *testing.T) {
	for _, parent := range []error{nil, context.Canceled, contract.ErrMaintenancePaused, contract.ErrWindowClosed} {
		var output bytes.Buffer
		scheduler := NewMaintenanceScheduler(nil, slog.New(slog.NewJSONHandler(&output, nil)))
		failure := &contract.MaintenanceFinalizationError{BatchID: "batch", RunID: "run", Phase: "source_updates", Duration: 5 * time.Second,
			Cause: errors.Join(context.DeadlineExceeded, &pgconn.PgError{Code: "57014", Message: "secret sentinel SQL parameters"})}
		scheduler.logTurnFailure(t.Context(), errors.Join(parent, failure))
		var fields map[string]any
		require.NoError(t, json.Unmarshal(output.Bytes(), &fields))
		require.Equal(t, "maintenance_finalization_failed", fields["error_code"])
		require.Equal(t, "batch", fields["batch_id"])
		require.Equal(t, "run", fields["run_id"])
		require.Equal(t, "source_updates", fields["phase"])
		require.Equal(t, "deadline_exceeded", fields["failure_class"])
		require.Equal(t, "57014", fields["sqlstate"])
		require.EqualValues(t, 5000, fields["duration_ms"])
		require.NotContains(t, output.String(), "sentinel")
		require.ErrorIs(t, failure, context.DeadlineExceeded)
	}
}

func TestMaintenanceFinalizationDiagnosticsBoundCauses(t *testing.T) {
	for _, item := range []struct {
		cause error
		class string
		state string
	}{
		{context.Canceled, "canceled", ""},
		{contract.ErrLeaseLost, "lease_lost", ""},
		{&pgconn.PgError{Code: "40P01", Message: "private details"}, "postgres", "40P01"},
		{&pgconn.PgError{Code: "secret-text"}, "internal_failure", ""},
		{errors.New("private details"), "internal_failure", ""},
	} {
		failure := &contract.MaintenanceFinalizationError{Cause: item.cause}
		require.Equal(t, item.class, failure.FailureClass())
		require.Equal(t, item.state, failure.SQLState())
		require.NotContains(t, failure.Error(), "private")
		NewMaintenanceScheduler(nil, nil).logTurnFailure(t.Context(), failure)
	}
	var output bytes.Buffer
	scheduler := NewMaintenanceScheduler(nil, slog.New(slog.NewTextHandler(&output, nil)))
	for _, err := range []error{nil, context.Canceled, contract.ErrMaintenanceDisabled, contract.ErrMaintenancePaused, contract.ErrWindowClosed} {
		scheduler.logTurnFailure(t.Context(), err)
	}
	require.Empty(t, output.String())
}

func TestMaintenanceSchedulerLogsTurnFailureDetails(t *testing.T) {
	diagnostics := MaintenanceDiagnostics(nil, observability.NewCredentialProtector(), nil)
	service := NewMaintenanceService(MaintenanceDependencies{Diagnostics: diagnostics})
	t.Run("accounting timeout with turn context", func(t *testing.T) {
		var output bytes.Buffer
		scheduler := NewMaintenanceScheduler(service, slog.New(slog.NewJSONHandler(&output, nil)))
		err := wrapMaintenanceTurnError("run-1", "batch-1", "team-1", "equivalence",
			errors.Join(contract.ErrAccounting, context.DeadlineExceeded))
		scheduler.logTurnFailure(t.Context(), err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(output.Bytes(), &fields))
		require.Equal(t, "topic maintenance failed", fields["msg"])
		require.Equal(t, "accounting_unavailable", fields["error_code"])
		require.Equal(t, "deadline_exceeded", fields["failure_class"])
		require.Equal(t, "run-1", fields["run_id"])
		require.Equal(t, "batch-1", fields["batch_id"])
		require.Equal(t, "team-1", fields["team_id"])
		require.Equal(t, "equivalence", fields["stage"])
		require.Contains(t, fields["error"], "topic maintenance accounting unavailable")
	})

	t.Run("postgres error preserves sqlstate", func(t *testing.T) {
		var output bytes.Buffer
		scheduler := NewMaintenanceScheduler(service, slog.New(slog.NewJSONHandler(&output, nil)))
		scheduler.logTurnFailure(t.Context(), &pgconn.PgError{Code: "40P01", Message: "deadlock detected"})
		var fields map[string]any
		require.NoError(t, json.Unmarshal(output.Bytes(), &fields))
		require.Equal(t, "maintenance_turn_failed", fields["error_code"])
		require.Equal(t, "postgres", fields["failure_class"])
		require.Equal(t, "40P01", fields["sqlstate"])
		require.Contains(t, fields["error"], "deadlock detected")
	})
}

func TestMaintenanceFailureDiagnosticsProtectErrorBeforeTruncation(t *testing.T) {
	for _, kind := range []string{"configured", "request-scoped"} {
		t.Run(kind, func(t *testing.T) {
			const secret = "synthetic-maintenance-diagnostic-secret"
			ctx := t.Context()
			protector := observability.NewCredentialProtector(secret)
			if kind == "request-scoped" {
				protector = observability.NewCredentialProtector()
				ctx = observability.WithAuthenticationSecrets(ctx, secret)
			}
			var output bytes.Buffer
			root := observability.NewConsoleWithProtector(slog.LevelDebug, protector, &output)
			sink := &captureLogSink{}
			service := NewMaintenanceService(MaintenanceDependencies{Diagnostics: MaintenanceDiagnostics(sink, protector, root.Slog())})
			prefix := strings.Repeat("x", maxDiagnosticErrorBytes-15)
			err := errors.New(prefix + secret + "界 beyond the cap")
			expected := prefix + observability.CredentialProtectionRedacted + "..."

			NewMaintenanceScheduler(service, root.Slog()).logTurnFailure(ctx, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &fields))
			require.Equal(t, expected, fields["error"])
			require.True(t, utf8.ValidString(expected))
			require.LessOrEqual(t, len(expected), maxDiagnosticErrorBytes)

			claim := contract.MaintenanceClaim{ID: uuid.NewString(), RunID: uuid.NewString(), TeamID: uuid.NewString()}
			service.recordFailureDiagnostics(ctx, claim, "assignment", contract.OrganizationResult{}, "maintenance_failure", err, nil)
			require.Len(t, sink.records, 1)
			require.Equal(t, "topic_failure_capture", sink.records[0].Message)
			require.Equal(t, expected, sink.records[0].Attrs["error"])
		})
	}
}
