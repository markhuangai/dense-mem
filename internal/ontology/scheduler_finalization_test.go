package ontology

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceFinalizationDiagnosticsSurviveCancellation(t *testing.T) {
	for _, parent := range []error{nil, context.Canceled, contract.ErrMaintenancePaused, contract.ErrWindowClosed} {
		var output bytes.Buffer
		scheduler := NewMaintenanceScheduler(nil, slog.New(slog.NewJSONHandler(&output, nil)))
		failure := &contract.MaintenanceFinalizationError{BatchID: "batch", RunID: "run", Phase: "source_updates", Duration: 5 * time.Second,
			Cause: errors.Join(context.DeadlineExceeded, &pgconn.PgError{Code: "57014", Message: "secret sentinel SQL parameters"})}
		scheduler.logTurnFailure(errors.Join(parent, failure))
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
		NewMaintenanceScheduler(nil, nil).logTurnFailure(failure)
	}
	var output bytes.Buffer
	scheduler := NewMaintenanceScheduler(nil, slog.New(slog.NewTextHandler(&output, nil)))
	for _, err := range []error{nil, context.Canceled, contract.ErrMaintenanceDisabled, contract.ErrMaintenancePaused, contract.ErrWindowClosed} {
		scheduler.logTurnFailure(err)
	}
	require.Empty(t, output.String())
}
