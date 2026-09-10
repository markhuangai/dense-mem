package repository

// This compatibility seam keeps telemetry lifecycle reads available from the
// legacy LedgerRepository while the SQL adapter lives in operations/postgres.

import (
	"context"
	"fmt"
	"time"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	operationspostgres "github.com/markhuangai/dense-mem/internal/operations/postgres"
)

type TelemetryLifecycleReader = operationscontract.TelemetryLifecycleReader
type TelemetryLifecycleFilter = operationscontract.TelemetryLifecycleFilter
type TelemetryLifecycleSnapshot = operationscontract.TelemetryLifecycleSnapshot

func (r *LedgerRepositoryImpl) ReadTelemetryLifecycle(ctx context.Context, filter TelemetryLifecycleFilter, from, to time.Time) (TelemetryLifecycleSnapshot, error) {
	if r == nil || r.db == nil || r.rls == nil {
		return TelemetryLifecycleSnapshot{Transitions: map[string]float64{}, Current: map[string]float64{}}, fmt.Errorf("telemetry lifecycle reader is unavailable")
	}
	return operationspostgres.NewTelemetryLifecycleRepository(r.db, r.rls).ReadTelemetryLifecycle(ctx, filter, from, to)
}

var _ TelemetryLifecycleReader = (*LedgerRepositoryImpl)(nil)
