package postgres

import (
	"context"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

type maintenanceDiscoveryStatsKey struct{}

type maintenanceDiscoveryStats struct {
	counts          ontology.MaintenanceDiscoveryCounts
	markerExpansion bool
	refreshed       map[string]bool
	receipts        map[[2]string]bool
}

func newMaintenanceDiscoveryStats() *maintenanceDiscoveryStats {
	return &maintenanceDiscoveryStats{refreshed: map[string]bool{}, receipts: map[[2]string]bool{}}
}

func discoveryStats(ctx context.Context) *maintenanceDiscoveryStats {
	stats, _ := ctx.Value(maintenanceDiscoveryStatsKey{}).(*maintenanceDiscoveryStats)
	return stats
}

func recordMaintenanceMatches(ctx context.Context, count int) {
	if stats := discoveryStats(ctx); stats != nil {
		stats.counts.EnumeratedMatches += count
	}
}

func recordMaintenanceSource(ctx context.Context, handle ontology.SourceHandle, phase string, err error) {
	stats := discoveryStats(ctx)
	if stats == nil || !stats.markerExpansion || err != nil {
		return
	}
	if phase == "source_finished" {
		stats.refreshed[ontology.SourceKey(handle)] = true
		stats.counts.RefreshedSources = len(stats.refreshed)
	} else if phase == "source_requeued" {
		stats.counts.Requeues++
	}
}

func recordMaintenanceReceipt(ctx context.Context, receipt ontology.OrganizationReceipt) {
	if stats := discoveryStats(ctx); stats != nil && stats.markerExpansion {
		stats.counts.FreshnessInvocations++
		stats.receipts[[2]string{receipt.ID, receipt.OperationKey}] = true
		stats.counts.DistinctReceipts = len(stats.receipts)
	}
}
