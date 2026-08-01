package service

import (
	"context"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	telemetryFirstDispositionBackfillBatchSize       = 250
	telemetryFirstDispositionBackfillPause           = 10 * time.Millisecond
	telemetryFirstDispositionBackfillContentionRetry = time.Second
	telemetryFirstDispositionBackfillRetry           = time.Minute
)

type PlacementFirstDispositionBackfillRepository interface {
	BackfillPlacementFirstDispositionMarkers(context.Context, int) (repository.PlacementFirstDispositionBackfillResult, error)
}

type PlacementFirstDispositionBackfillService struct {
	repository PlacementFirstDispositionBackfillRepository
	logger     observability.LogProvider
}

func NewPlacementFirstDispositionBackfillService(
	repository PlacementFirstDispositionBackfillRepository,
	logger observability.LogProvider,
) *PlacementFirstDispositionBackfillService {
	return &PlacementFirstDispositionBackfillService{repository: repository, logger: logger}
}

func (s *PlacementFirstDispositionBackfillService) Start(ctx context.Context) {
	if s == nil || s.repository == nil {
		return
	}
	go s.run(ctx)
}

func (s *PlacementFirstDispositionBackfillService) run(ctx context.Context) {
	for {
		result, err := s.repository.BackfillPlacementFirstDispositionMarkers(ctx, telemetryFirstDispositionBackfillBatchSize)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("telemetry first disposition marker backfill failed", observability.String("reason", "repository_error"))
			}
			if !waitForTelemetryBackfill(ctx, telemetryFirstDispositionBackfillRetry) {
				return
			}
			continue
		}
		if result.SweepComplete {
			return
		}
		pause := telemetryFirstDispositionBackfillPause
		if result.WaitingForActiveRun {
			pause = telemetryFirstDispositionBackfillRetry
		} else if result.Candidates == 0 {
			pause = telemetryFirstDispositionBackfillContentionRetry
		}
		if !waitForTelemetryBackfill(ctx, pause) {
			return
		}
	}
}

func waitForTelemetryBackfill(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
