package migrationcontrol

import (
	"context"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const DefaultCutoverMarkerVersion = "dense-mem.v2.1.cutover.v1"

type Store interface {
	GetLatestRun(ctx context.Context) (*domain.V2MigrationRun, error)
	GetLatestMarker(ctx context.Context) (*domain.V2CompatibilityMarker, error)
	ListOperatorActions(ctx context.Context, runID string, limit int) ([]domain.V2MigrationOperatorAction, error)
}

type Service interface {
	Status(ctx context.Context) (*domain.V2MigrationControlStatus, error)
}

type Config struct {
	Required bool
}

type service struct {
	store Store
	cfg   Config
}

func New(store Store, cfg Config) Service {
	return &service{store: store, cfg: cfg}
}

func (s *service) Status(ctx context.Context) (*domain.V2MigrationControlStatus, error) {
	if s.store == nil {
		return markerRequiredStatus(s.cfg.Required, nil, nil), nil
	}
	marker, err := s.store.GetLatestMarker(ctx)
	if err != nil {
		return nil, err
	}
	run, err := s.store.GetLatestRun(ctx)
	if err != nil {
		return nil, err
	}
	actions, err := s.actions(ctx, run)
	if err != nil {
		return nil, err
	}
	return statusFromRunMarker(s.cfg.Required, run, marker, actions), nil
}

func (s *service) actions(ctx context.Context, run *domain.V2MigrationRun) ([]domain.V2MigrationOperatorAction, error) {
	if run == nil || run.RunID == "" {
		return nil, nil
	}
	return s.store.ListOperatorActions(ctx, run.RunID, 20)
}

func statusFromRunMarker(
	required bool,
	run *domain.V2MigrationRun,
	marker *domain.V2CompatibilityMarker,
	actions []domain.V2MigrationOperatorAction,
) *domain.V2MigrationControlStatus {
	if marker != nil {
		switch marker.Status {
		case domain.V2MigrationMarkerCompatible:
			message := "compatible V2 authority marker present"
			if v2FreshInstallMarker(marker) {
				message = "fresh V2 authority marker present"
			}
			return &domain.V2MigrationControlStatus{
				State:            domain.V2MigrationStateCutOver,
				Required:         required,
				DataPlaneAllowed: true,
				ReadinessMessage: message,
				Run:              run,
				Marker:           marker,
				Actions:          actions,
			}
		case domain.V2MigrationMarkerIncompatible, domain.V2MigrationMarkerCorrupt:
			return &domain.V2MigrationControlStatus{
				State:            domain.V2MigrationStateIncompatible,
				Required:         true,
				DataPlaneAllowed: false,
				ReadinessMessage: "V2 authority marker is incompatible or corrupt",
				Run:              run,
				Marker:           marker,
				Actions:          actions,
			}
		}
	}
	return markerRequiredStatus(required, run, actions)
}

func markerRequiredStatus(
	_ bool,
	run *domain.V2MigrationRun,
	actions []domain.V2MigrationOperatorAction,
) *domain.V2MigrationControlStatus {
	state := domain.V2MigrationStateRequired
	if run != nil && strings.TrimSpace(run.State) != "" {
		state = run.State
	}
	return &domain.V2MigrationControlStatus{
		State:            state,
		Required:         true,
		DataPlaneAllowed: false,
		ReadinessMessage: "compatible V2 authority marker is required",
		Run:              run,
		Actions:          actions,
	}
}

func v2FreshInstallMarker(marker *domain.V2CompatibilityMarker) bool {
	if marker == nil || len(marker.Metadata) == 0 {
		return false
	}
	value, ok := marker.Metadata["fresh_install"]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}
