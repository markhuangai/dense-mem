package postgres

import (
	"context"
	"time"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

type maintenanceDiscoveryObservationKey struct{}

type maintenanceDiscoveryEvent struct {
	Phase                   string
	Scope                   scope
	Source                  ontology.SourceHandle
	ReceiptID, OperationKey string
	Sources, Dependencies   int
	Current                 bool
	Err                     error
	Duration                time.Duration
}

type maintenanceDiscoveryObserver func(maintenanceDiscoveryEvent)

func observeMaintenanceSource(ctx context.Context, fence scope, handle ontology.SourceHandle, phase string, err error) {
	if observer, ok := ctx.Value(maintenanceDiscoveryObservationKey{}).(maintenanceDiscoveryObserver); ok && observer != nil {
		observer(maintenanceDiscoveryEvent{Phase: phase, Scope: fence, Source: handle, Err: err})
	}
}

// The private observer measures actual calls without retaining receipt bodies or changing freshness decisions.
func observeMaintenanceReceipt(ctx context.Context, fence scope, handle ontology.SourceHandle, receipt ontology.OrganizationReceipt) func(bool, error) {
	observer, ok := ctx.Value(maintenanceDiscoveryObservationKey{}).(maintenanceDiscoveryObserver)
	if !ok || observer == nil {
		return nil
	}
	event := maintenanceDiscoveryEvent{Phase: "receipt_started", Scope: fence, Source: handle,
		ReceiptID: receipt.ID, OperationKey: receipt.OperationKey, Sources: len(receipt.Sources), Dependencies: len(receipt.Dependencies)}
	observer(event)
	started := time.Now()
	return func(current bool, err error) {
		event.Phase, event.Current, event.Err, event.Duration = "receipt_finished", current, err, time.Since(started)
		observer(event)
	}
}
