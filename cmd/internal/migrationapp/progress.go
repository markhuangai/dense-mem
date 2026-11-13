package migrationapp

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type progressSlotKey struct{}
type progressSlot struct {
	mu      sync.Mutex
	current *postgres.MigrationProgress
}

func progressAttrs(s postgres.MigrationSnapshot) []any {
	if s.MigrationVersion >= postgres.FeatureStoragePrepareVersion {
		return []any{"migration_execution_id", s.ExecutionID, "migration_version", s.MigrationVersion, "executor_version", postgres.FeatureStorageLayout,
			"inventory_identity", s.InventoryFingerprint, "source_schema_fingerprint", s.SourceSchemaFingerprint,
			"source_fingerprint", s.SourceFingerprint, "phase", s.Phase, "work_unit", s.WorkUnit, "state", s.State,
			"phase_started_at", s.PhaseStartedAt, "phase_elapsed_ms", time.Since(s.PhaseStartedAt).Milliseconds(), "observed_at", s.ObservedAt,
			"last_progress_at", s.LastProgressAt, "metric", s.Metric, "unit", s.Unit, "completed", s.Completed, "total", s.Total, "total_kind", "unknown",
			"committed_rows", s.DurableRows, "committed_batches", s.DurableBatches, "checkpoint_fingerprint", s.CheckpointFingerprint,
			"work_durability", s.WorkDurability, "transaction_outcome", s.TransactionOutcome, "capture_state", s.CaptureState, "capture_reason", s.CaptureReason,
			"failure_reason", s.FailureReason, "sqlstate", s.SQLState, "expected_fingerprint", s.ExpectedFingerprint, "observed_fingerprint", s.ObservedFingerprint,
			"recovery_action", "resume_matching_application_after_resolving_reported_cause", "eta_seconds", nil, "eta_unavailable_reason", "remaining_phase_cost_unknown"}
	}
	var version any
	if s.MigrationVersion != 0 {
		version = s.MigrationVersion
	}
	var last any
	if !s.LastProgressAt.IsZero() {
		last = s.LastProgressAt
	}
	var remaining any
	totalKind := "unknown"
	if s.Total != nil {
		totalKind = "exact"
		if s.Completed != nil {
			remaining = *s.Total - *s.Completed
		}
	}
	return []any{"migration_execution_id", s.ExecutionID, "migration_version", version, "target_migration_version", evidenceOnlyMigrationVersion,
		"inventory_identity", postgres.EvidenceOnlyMigrationInventorySHA256, "phase", s.Phase, "work_unit", s.WorkUnit, "state", s.State,
		"phase_started_at", s.PhaseStartedAt, "phase_elapsed_ms", time.Since(s.PhaseStartedAt).Milliseconds(), "observed_at", s.ObservedAt,
		"last_progress_at", last, "metric", s.Metric, "unit", s.Unit, "completed", s.Completed, "total", s.Total, "remaining", remaining, "total_kind", totalKind,
		"discovered", s.Discovered, "iterations", s.Iterations, "sequence", s.Sequence, "attempted", s.TransactionStarted, "work_durability", "transaction_local",
		"capture_state", s.CaptureState, "capture_reason", s.CaptureReason, "eta_seconds", nil, "eta_unavailable_reason", "remaining_phase_cost_unknown"}
}

func withProgressSlot(ctx context.Context) context.Context {
	return context.WithValue(ctx, progressSlotKey{}, &progressSlot{})
}

func attachProgress(ctx context.Context, p *postgres.MigrationProgress) func() {
	slot, _ := ctx.Value(progressSlotKey{}).(*progressSlot)
	if slot == nil {
		return p.Close
	}
	slot.mu.Lock()
	slot.current = p
	slot.mu.Unlock()
	return func() { slot.mu.Lock(); defer slot.mu.Unlock(); slot.current = nil; p.Close() }
}

func heartbeatProgress(ctx context.Context, logger infoLogger, db *sql.DB, sample bool) {
	slot, _ := ctx.Value(progressSlotKey{}).(*progressSlot)
	if slot == nil {
		return
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	p := slot.current
	if p == nil {
		return
	}
	prefix := "evidence_only_migration_"
	if p.Snapshot().MigrationVersion >= postgres.FeatureStoragePrepareVersion {
		prefix = "feature_storage_migration_"
	}
	if !sample {
		logInfo(context.WithoutCancel(ctx), logger, prefix+"heartbeat", progressAttrs(p.Snapshot())...)
		return
	}
	if db == nil {
		return
	}
	observationCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	kind, event, blockers, err := p.SampleWait(observationCtx, db)
	if err != nil {
		if p.Snapshot().CaptureReason != "wait_observation_unavailable" {
			p.Incomplete("wait_observation_unavailable")
			logWarn(context.WithoutCancel(ctx), logger, prefix+"capture_incomplete", progressAttrs(p.Snapshot())...)
		}
		return
	}
	if kind != "" || blockers > 0 {
		attrs := append(progressAttrs(p.Snapshot()), "wait_event_type", kind, "wait_event", event, "blocker_count", blockers)
		logInfo(ctx, logger, prefix+"wait", attrs...)
	}
}

func logWarn(ctx context.Context, logger infoLogger, message string, args ...any) {
	if contextual, ok := logger.(interface {
		WarnContext(context.Context, string, ...any)
	}); ok {
		contextual.WarnContext(ctx, message, args...)
		return
	}
	if warning, ok := logger.(interface{ Warn(string, ...any) }); ok {
		warning.Warn(message, args...)
		return
	}
	logger.Error(message, args...)
}
