package ontology

import (
	"context"
	"log/slog"
	"time"

	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

func maintenanceDiagnosticError(err error) string {
	if err == nil {
		return ""
	}
	if code := MaintenanceErrorCode(err); code != "" {
		return code
	}
	return "maintenance_failure"
}

func (s *MaintenanceService) logMaintenanceDiscovery(ctx context.Context, turn contract.MaintenanceTurn, result contract.MaintenanceDiscoveryResult, err error) {
	if s.deps.Logger == nil {
		return
	}
	s.deps.Logger.LogAttrs(ctx, slog.LevelInfo, "ontology maintenance discovery",
		slog.String("run_id", turn.RunID), slog.String("team_id", turn.TeamID),
		slog.Bool("committed", result.Committed), slog.Any("counts", result.Counts), slog.Any("attempted", result.Attempted),
		slog.Float64("duration_ms", float64(result.Duration)/float64(time.Millisecond)), slog.String("error_code", maintenanceDiagnosticError(err)))
}

func (s *MaintenanceService) logMaintenancePreparation(ctx context.Context, turn contract.MaintenanceTurn, work *maintenanceWork, duration time.Duration, err error) {
	if s.deps.Logger == nil {
		return
	}
	batchID := ""
	if work != nil {
		batchID = work.claim.ID
	}
	s.deps.Logger.LogAttrs(ctx, slog.LevelInfo, "ontology maintenance preparation",
		slog.String("run_id", turn.RunID), slog.String("team_id", turn.TeamID), slog.String("batch_id", batchID),
		slog.Bool("batch_prepared", work != nil), slog.Bool("includes_discovery", true),
		slog.Float64("duration_ms", float64(duration)/float64(time.Millisecond)), slog.String("error_code", maintenanceDiagnosticError(err)))
}

func (s *MaintenanceService) logFreshnessRejection(ctx context.Context, claim contract.MaintenanceClaim, result contract.OrganizationResult) {
	if s.deps.Logger == nil || result.FreshnessRejection == nil {
		return
	}
	rejection := result.FreshnessRejection
	s.deps.Logger.LogAttrs(ctx, slog.LevelWarn, "ontology_freshness_rejected",
		slog.String("team_id", claim.TeamID), slog.String("run_id", claim.RunID), slog.String("batch_id", claim.ID),
		slog.String("assessment_id", result.AssessmentID), slog.String("check", rejection.Check),
		slog.Any("source", rejection.Source), slog.String("record_id", rejection.RecordID),
		slog.String("expected", rejection.Expected), slog.String("observed", rejection.Observed))
}
