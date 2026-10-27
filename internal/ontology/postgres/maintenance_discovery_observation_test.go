package postgres

import (
	"context"
	"errors"
	"testing"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceDiscoveryObservation(t *testing.T) {
	fence := scope{TeamID: "team", SpaceID: "space", Generation: 2}
	handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: "source", Version: 1}
	receipt := ontology.OrganizationReceipt{ID: "receipt", OperationKey: "operation",
		Sources: []ontology.SourceDependency{{SourceHandle: handle}}, Dependencies: []ontology.RevisionRef{{ID: "definition", Version: 1}}}
	require.Nil(t, observeMaintenanceReceipt(context.Background(), fence, handle, receipt))
	observeMaintenanceSource(context.Background(), fence, handle, "source_selected", nil)
	var events []maintenanceDiscoveryEvent
	ctx := context.WithValue(context.Background(), maintenanceDiscoveryObservationKey{}, maintenanceDiscoveryObserver(func(event maintenanceDiscoveryEvent) {
		events = append(events, event)
	}))
	for _, outcome := range []struct {
		current bool
		err     error
	}{{true, nil}, {false, nil}, {false, context.Canceled}, {false, errors.New("database failure")}} {
		observeMaintenanceSource(ctx, fence, handle, "source_selected", nil)
		finish := observeMaintenanceReceipt(ctx, fence, handle, receipt)
		require.NotNil(t, finish)
		require.Equal(t, "receipt_started", events[len(events)-1].Phase)
		finish(outcome.current, outcome.err)
		completed := events[len(events)-1]
		require.Equal(t, "receipt_finished", completed.Phase)
		require.Equal(t, outcome.current, completed.Current)
		require.Equal(t, outcome.err, completed.Err)
		require.Equal(t, fence, completed.Scope)
		require.Equal(t, handle, completed.Source)
		require.Equal(t, receipt.ID, completed.ReceiptID)
		require.Equal(t, receipt.OperationKey, completed.OperationKey)
		require.Equal(t, 1, completed.Sources)
		require.Equal(t, 1, completed.Dependencies)
		require.GreaterOrEqual(t, completed.Duration.Nanoseconds(), int64(0))
		observeMaintenanceSource(ctx, fence, handle, "source_finished", outcome.err)
	}
	require.Len(t, events, 16)
	require.Nil(t, observeMaintenanceReceipt(context.Background(), fence, handle, receipt), "observation must not escape its context")
}
