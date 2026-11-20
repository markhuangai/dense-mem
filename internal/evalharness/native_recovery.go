package evalharness

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

const nativeRateLimitCooldown = 5 * time.Minute

type nativeFailure struct {
	SourceKind     string     `json:"source_kind"`
	SourceID       string     `json:"source_id"`
	RunID          string     `json:"run_id"`
	BatchID        string     `json:"batch_id,omitempty"`
	AssessmentID   string     `json:"assessment_id,omitempty"`
	Reason         string     `json:"reason"`
	AttemptFailure string     `json:"attempt_failure,omitempty"`
	ReconciledAt   *time.Time `json:"reconciled_at,omitempty"`
	Check          string     `json:"freshness_check,omitempty"`
	Expected       string     `json:"expected,omitempty"`
	Observed       string     `json:"observed,omitempty"`
}

func (f nativeFailure) recoverable() bool {
	if _, err := uuid.Parse(f.RunID); err != nil {
		return false
	}
	return f.Reason == "stale_input" || (f.Reason == "provider_unavailable" &&
		f.AttemptFailure == modelprovider.ProviderFailureClassRateLimited && f.ReconciledAt != nil)
}

func (n *Native) failedOntologySources(ctx context.Context) ([]nativeFailure, error) {
	tx, err := n.systemTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	const failed = `SELECT s.source_kind,s.source_id::text,COALESCE(s.last_run_id::text,''),
 COALESCE(b.batch_id::text,''),COALESCE(s.assessment_id::text,''),s.reason,
 COALESCE(r.body#>>'{result,attempts,-1,failure_code}',''),a.reconciled_at,
 COALESCE(r.body#>>'{result,freshness_rejection,check}',''),
 COALESCE(r.body#>>'{result,freshness_rejection,expected}',''),
 COALESCE(r.body#>>'{result,freshness_rejection,observed}','')
 FROM ontology_maintenance_sources s
 JOIN teams t ON t.id=s.team_id AND t.status='active' AND t.deleted_at IS NULL
 JOIN memory_spaces m ON m.team_id=s.team_id AND m.id=s.shared_space_id
  AND m.generation=s.space_generation AND m.lifecycle_state='active' AND m.kind='team_shared'
 LEFT JOIN ontology_assessments r ON r.team_id=s.team_id AND r.shared_space_id=s.shared_space_id
  AND r.space_generation=s.space_generation AND r.assessment_id=s.assessment_id
 LEFT JOIN ontology_maintenance_batches b ON b.team_id=s.team_id AND b.shared_space_id=s.shared_space_id
  AND b.space_generation=s.space_generation AND b.run_id=s.last_run_id AND r.operation_key='maintenance:'||b.batch_id::text
 LEFT JOIN ontology_maintenance_attempts a ON a.batch_id=b.batch_id AND a.assessment_id=r.assessment_id
  AND a.attempt=(r.body#>>'{result,attempts,-1,number}')::integer
 WHERE s.team_id=$1::uuid AND s.shared_space_id=$2::uuid AND s.space_generation=$3
  AND s.eligible AND s.status='failed'
 ORDER BY s.last_run_id,s.source_kind,s.source_id`
	rows, err := tx.QueryContext(ctx, failed, n.Scope.TeamID, n.Scope.SpaceID, n.Scope.Generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var failures []nativeFailure
	for rows.Next() {
		var f nativeFailure
		if err := rows.Scan(&f.SourceKind, &f.SourceID, &f.RunID, &f.BatchID, &f.AssessmentID, &f.Reason,
			&f.AttemptFailure, &f.ReconciledAt, &f.Check, &f.Expected, &f.Observed); err != nil {
			return nil, err
		}
		failures = append(failures, f)
	}
	return failures, rows.Err()
}

type ontologyRecovery struct {
	frozen    bool
	remaining []string
	retry     ontology.MaintenanceRun
	observed  []nativeFailure
}

func (r *ontologyRecovery) advance(ctx context.Context, n *Native, manifest Manifest, observation NativeObservation) (waiting bool, err error) {
	if observation.Ontology.Counts.Failed == 0 && r.retry.ID == "" {
		return false, nil
	}
	capture := n.Client.Capture
	failures, err := n.failedOntologySources(ctx)
	if err != nil {
		return false, err
	}
	if !reflect.DeepEqual(failures, r.observed) {
		if err := capture.Emit("ontology_failed_sources", map[string]any{"scope": n.Scope, "failures": failures}); err != nil {
			return false, err
		}
		r.observed = failures
	}
	if r.retry.ID != "" {
		state, err := n.retryState(ctx, r.retry)
		if err != nil {
			return false, err
		}
		if state.Status == "pending" || state.Status == "running" {
			return true, nil
		}
		if err := capture.Emit("ontology_recovery_finished", state); err != nil {
			return false, err
		}
		if state.Status != "completed" {
			return false, errors.New("native ontology retry did not complete")
		}
		r.retry = ontology.MaintenanceRun{}
	}
	status := observation.Ontology
	if !status.DiscoveryComplete || status.Counts.Pending != 0 || nativeDrained(observation) != nil {
		return true, nil
	}
	if status.Counts.Failed != int64(len(failures)) {
		return false, errors.New("native failure count differs from the authenticated scope")
	}
	if len(failures) == 0 {
		return false, nil
	}
	if !status.Enabled || status.Paused {
		return false, errors.New("native ontology recovery requires enabled, unpaused maintenance")
	}
	if err := nativeOntologyPolicy(status, manifest, n.Scope.TeamID); err != nil {
		return false, err
	}
	for _, f := range failures {
		if !f.recoverable() {
			return false, errors.New("native ontology failure has no supported recovery")
		}
	}
	// The native retry command selects every failed source for a run across active scopes.
	// Freeze the original runs so a failure from recovery cannot become a new retry target.
	if !r.frozen {
		for _, f := range failures {
			if !slices.Contains(r.remaining, f.RunID) {
				r.remaining = append(r.remaining, f.RunID)
			}
		}
		r.frozen = true
	}
	for _, f := range failures {
		if !slices.Contains(r.remaining, f.RunID) {
			return false, errors.New("native recovery left a failed source outside its original runs")
		}
	}
	for _, origin := range r.remaining {
		count := 0
		var retryAt time.Time
		for _, f := range failures {
			if f.RunID != origin {
				continue
			}
			count++
			if f.Reason == "provider_unavailable" {
				when := f.ReconciledAt.Add(nativeRateLimitCooldown)
				if when.After(retryAt) {
					retryAt = when
				}
			}
		}
		if count == 0 {
			r.remaining = r.remaining[1:]
			continue
		}
		now := time.Now()
		if n.Now != nil {
			now = n.Now()
		}
		if delay := retryAt.Sub(now); delay > 0 {
			n.logRecovery("evaluation ontology recovery waiting for provider cooldown", origin, "wait_ms", delay.Milliseconds())
			return true, waitUntil(ctx, time.Now().Add(delay))
		}
		key := uuid.NewSHA1(uuid.NameSpaceOID, []byte(manifest.RunID+":"+origin)).String()
		batches := min(count, ontology.MaxManualBatches)
		n.logRecovery("evaluation ontology recovery requested", origin, "failed_sources", count, "max_batches", batches)
		var retry ontology.MaintenanceRun
		if err := n.Control(ctx, http.MethodPost, "/ontology/runs/"+origin+"/retry",
			map[string]any{"operation_key": key, "max_batches": batches}, &retry); err != nil {
			return false, err
		}
		if _, err := uuid.Parse(retry.ID); err != nil || retry.ID == origin || retry.Kind != "retry" || retry.OperationKey != key {
			return false, errors.New("native retry receipt does not identify the requested recovery")
		}
		r.retry = retry
		r.remaining = r.remaining[1:]
		return true, capture.Emit("ontology_recovery_started", map[string]any{"originating_run_id": origin, "failed_sources": count, "retry": retry})
	}
	return false, errors.New("native ontology recovery has no remaining run")
}

func (n *Native) retryState(ctx context.Context, retry ontology.MaintenanceRun) (ontology.MaintenanceRun, error) {
	tx, err := n.systemTx(ctx)
	if err != nil {
		return retry, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT status,failure_code,completed_batches FROM ontology_maintenance_runs
 WHERE run_id=$1::uuid AND operation_key=$2 AND kind='retry'`, retry.ID, retry.OperationKey).
		Scan(&retry.Status, &retry.FailureCode, &retry.CompletedBatches)
	return retry, err
}

func (n *Native) logRecovery(message, origin string, fields ...any) {
	capture := n.Client.Capture
	if capture != nil && capture.Logger != nil {
		capture.Logger.Warn(message, append([]any{"run_id", capture.RunID, "originating_run_id", origin, "team_id", n.Scope.TeamID}, fields...)...)
	}
}
