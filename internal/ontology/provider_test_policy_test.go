package ontology

import (
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestProviderTestMaintenancePolicySelectsActiveWindowUntilExpiry(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	pending := domain.OntologyMaintenanceConfig{Model: "pending-model", SettingsVersion: "pending"}
	active := domain.OntologyMaintenanceConfig{Model: "active-model", SettingsVersion: "active"}
	status := contract.MaintenanceStatus{ObservedAt: now, PendingPolicy: pending}
	require.Equal(t, pending, maintenanceEffectivePolicy(status))
	status.Window = &contract.MaintenanceWindow{EndsAt: now.Add(time.Minute), Policy: active}
	require.Equal(t, active, maintenanceEffectivePolicy(status))
	status.ObservedAt = status.Window.EndsAt
	require.Equal(t, pending, maintenanceEffectivePolicy(status))
}
