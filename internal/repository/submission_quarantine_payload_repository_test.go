package repository

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type submissionQuarantinePurgeMetricsStub struct {
	failures int
}

func (s *submissionQuarantinePurgeMetricsStub) IncSubmissionQuarantinePurgeFailure() {
	s.failures++
}

func TestDrainExpiredSubmissionQuarantinePayloadsDrainsFullBatches(t *testing.T) {
	calls := 0
	err := drainExpiredSubmissionQuarantinePayloads(
		context.Background(),
		time.Unix(10, 0).UTC(),
		100,
		func(context.Context, time.Time, int) (int, error) {
			calls++
			if calls == 1 {
				return 100, nil
			}
			return 2, nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}

func TestObserveSubmissionQuarantinePurgeFailureIsBoundedAndMetered(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	metrics := &submissionQuarantinePurgeMetricsStub{}
	observeSubmissionQuarantinePurgeFailure(context.Background(), logger, metrics, &pgconn.PgError{
		Code:    "57014",
		Message: "database password leaked",
	})
	require.Equal(t, 1, metrics.failures)
	require.Contains(t, logs.String(), "submission quarantine purge failed")
	require.Contains(t, logs.String(), "error_class=sqlstate_57014")
	require.NotContains(t, logs.String(), "database password leaked")
}

func TestDrainExpiredSubmissionQuarantinePayloadsReturnsFailure(t *testing.T) {
	wantErr := errors.New("database password leaked")
	err := drainExpiredSubmissionQuarantinePayloads(
		context.Background(),
		time.Unix(10, 0).UTC(),
		100,
		func(context.Context, time.Time, int) (int, error) {
			return 0, wantErr
		},
	)
	require.ErrorIs(t, err, wantErr)
}
