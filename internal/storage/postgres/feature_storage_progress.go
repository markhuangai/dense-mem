package postgres

import (
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var featureFingerprintDetail = regexp.MustCompile(`^expected=([a-f0-9]{64});observed=([a-f0-9]{64}|missing)$`)

func (p *MigrationProgress) FeatureStorageDatabaseFailure(err error) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.SQLState = pgErr.Code
	if fields := featureFingerprintDetail.FindStringSubmatch(pgErr.Detail); len(fields) == 3 {
		p.snapshot.ExpectedFingerprint, p.snapshot.ObservedFingerprint = fields[1], fields[2]
	}
}

func (p *MigrationProgress) BindFeatureTransaction(pid int, started time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.BackendPID, p.snapshot.BackendStart = pid, started
	p.snapshot.TransactionStarted = true
	p.snapshot.TransactionOutcome = "in_progress"
	if p.snapshot.CaptureReason == "" {
		p.snapshot.CaptureState = "capturing"
	}
}

func (p *MigrationProgress) FeatureStorageCheckpoint(phase, work, state string, rows, batches int64, fingerprint, inventory, source, schema string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.snapshot.CheckpointFingerprint = fingerprint
	p.snapshot.InventoryFingerprint = inventory
	p.snapshot.SourceFingerprint = source
	p.snapshot.SourceSchemaFingerprint = schema
	p.snapshot.DurableRows, p.snapshot.DurableBatches = rows, batches
	p.snapshot.WorkDurability = "committed_checkpoints"
	p.snapshot.TransactionOutcome = "committed"
	if state == "waiting" || state == "started" {
		p.snapshot.TransactionOutcome = "not_started"
	}
	if phase == "cutover" && state == "completed" && p.snapshot.CaptureReason == "" {
		p.snapshot.CaptureState = "complete"
	}
	p.recordLocked(MigrationObservation{Phase: phase, WorkUnit: work, State: state, Metric: "completed", Unit: "rows", Completed: &rows}, time.Now().UTC())
}

func (p *MigrationProgress) FeatureStorageFailure(state, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.FailureReason = reason
	if p.snapshot.TransactionOutcome == "in_progress" || p.snapshot.TransactionOutcome == "not_started" {
		p.snapshot.TransactionOutcome = "unconfirmed"
	}
	p.snapshot.CaptureState = "incomplete"
	if p.snapshot.CaptureReason == "" {
		p.snapshot.CaptureReason = "checkpoint_execution_interrupted"
	}
	o := p.snapshot.MigrationObservation
	o.State = state
	p.recordLocked(o, time.Now().UTC())
}

func (p *MigrationProgress) SetFeatureTransactionOutcome(outcome string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.TransactionOutcome = outcome
}

func (p *MigrationProgress) FeatureFingerprintMismatch(expected, observed string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.ExpectedFingerprint, p.snapshot.ObservedFingerprint = expected, observed
}
