package contract

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaintenanceUpgradeDisposition(t *testing.T) {
	for _, test := range []struct {
		name  string
		state MaintenanceUpgradeState
		want  string
	}{
		{"already upgraded", MaintenanceUpgradeState{}, "not_required"},
		{"ready", MaintenanceUpgradeState{Required: true, Paused: true}, "ready"},
		{"live batch", MaintenanceUpgradeState{Required: true, LiveBatches: 1}, "waiting"},
		{"live preparation", MaintenanceUpgradeState{Required: true, LiveTeamLeases: 1}, "waiting"},
		{"unfinished attempt", MaintenanceUpgradeState{Required: true, UnreconciledAttempts: 1}, "blocked"},
		{"unfinished live attempt", MaintenanceUpgradeState{Required: true, LiveBatches: 1, UnreconciledAttempts: 1}, "blocked"},
		{"restore after interrupted migration", MaintenanceUpgradeState{Required: true, MigrationApplied: true, OwnsPause: true}, "ready"},
	} {
		t.Run(test.name, func(t *testing.T) { require.Equal(t, test.want, test.state.Disposition()) })
	}
}
