package migrationcontrol

import (
	"context"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestStatusAllowsDataPlaneOnlyWithCompatibleMarker(t *testing.T) {
	store := &statusStore{
		run: &domain.V2MigrationRun{
			RunID: "run-1",
			State: domain.V2MigrationStateCutOver,
		},
		marker: &domain.V2CompatibilityMarker{
			Status: domain.V2MigrationMarkerCompatible,
		},
	}

	status, err := New(store, Config{}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != domain.V2MigrationStateCutOver || !status.DataPlaneAllowed {
		t.Fatalf("status = %+v", status)
	}
	if status.ReadinessMessage != "compatible V2 authority marker present" {
		t.Fatalf("readiness message = %q", status.ReadinessMessage)
	}
	if len(status.Actions) != 1 {
		t.Fatalf("actions = %+v", status.Actions)
	}
}

func TestStatusReportsFreshAuthorityMarker(t *testing.T) {
	store := &statusStore{
		marker: &domain.V2CompatibilityMarker{
			Status:   domain.V2MigrationMarkerCompatible,
			Metadata: map[string]any{"fresh_install": true},
		},
	}

	status, err := New(store, Config{}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.DataPlaneAllowed {
		t.Fatalf("status = %+v", status)
	}
	if status.ReadinessMessage != "fresh V2 authority marker present" {
		t.Fatalf("readiness message = %q", status.ReadinessMessage)
	}
}

func TestStatusBlocksWithoutCompatibleMarker(t *testing.T) {
	store := &statusStore{
		run: &domain.V2MigrationRun{
			RunID: "run-1",
			State: domain.V2MigrationStateCutOver,
		},
	}

	status, err := New(store, Config{}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.DataPlaneAllowed {
		t.Fatalf("status = %+v", status)
	}
	if status.ReadinessMessage != "compatible V2 authority marker is required" {
		t.Fatalf("readiness message = %q", status.ReadinessMessage)
	}
}

func TestStatusBlocksIncompatibleMarker(t *testing.T) {
	store := &statusStore{
		marker: &domain.V2CompatibilityMarker{
			Status: domain.V2MigrationMarkerIncompatible,
		},
	}

	status, err := New(store, Config{}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != domain.V2MigrationStateIncompatible || status.DataPlaneAllowed {
		t.Fatalf("status = %+v", status)
	}
	if status.ReadinessMessage != "V2 authority marker is incompatible or corrupt" {
		t.Fatalf("readiness message = %q", status.ReadinessMessage)
	}
}

type statusStore struct {
	run    *domain.V2MigrationRun
	marker *domain.V2CompatibilityMarker
}

func (s *statusStore) GetLatestRun(context.Context) (*domain.V2MigrationRun, error) {
	return s.run, nil
}

func (s *statusStore) GetLatestMarker(context.Context) (*domain.V2CompatibilityMarker, error) {
	return s.marker, nil
}

func (s *statusStore) ListOperatorActions(context.Context, string, int) ([]domain.V2MigrationOperatorAction, error) {
	return []domain.V2MigrationOperatorAction{{Action: domain.V2MigrationActionCutoverCommitted}}, nil
}
