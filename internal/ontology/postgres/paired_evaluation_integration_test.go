//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func pairedMaintenanceSnapshot(t *testing.T, f *ontologyFixture) (evalharness.PairObservation, []byte) {
	t.Helper()
	ctx := context.Background()
	pool, err := f.admin.DB()
	require.NoError(t, err)
	var raw []byte
	require.NoError(t, pool.QueryRowContext(ctx, evalharness.PairOntologySnapshotSQL, f.team, f.owners[0], f.space, f.generation, f.credentials[0].ID).Scan(&raw))
	var db struct {
		ActiveBatches      int64                             `json:"active_batches"`
		ActivePreparations int64                             `json:"active_preparations"`
		ActiveProjections  int64                             `json:"active_projections"`
		Attempts           []evalharness.PairProviderAttempt `json:"provider_attempts"`
	}
	require.NoError(t, json.Unmarshal(raw, &db))
	status, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	o := evalharness.PairObservation{ObservedAt: status.ObservedAt, Scope: evalharness.PairScope{TeamID: f.team, OwnerID: f.owners[0], SpaceID: f.space, Generation: f.generation},
		Enabled: status.Enabled, Paused: status.Paused, DiscoveryComplete: status.DiscoveryComplete, CoverageComplete: status.CoverageComplete,
		Pending: status.Counts.Pending, Failed: status.Counts.Failed, Ambiguous: status.Counts.Ambiguous, ActiveWork: int64(status.ActiveWork),
		ActiveBatches: db.ActiveBatches, ActivePreparations: db.ActivePreparations, ActiveProjections: db.ActiveProjections, ProviderAttempts: db.Attempts,
		SourceHash: "sha256:fixture", CanonicalHash: "sha256:fixture"}
	o.Usage = evalharness.PairUsage{Attempts: status.Usage.Attempts, Unreconciled: status.Usage.UnreconciledAttempts, Unavailable: status.Usage.UnavailableUsageAttempts, Partial: status.Usage.PartialUsageAttempts, Input: status.Usage.ReportedInputTokens, Output: status.Usage.ReportedOutputTokens, Total: status.Usage.ReportedTotalTokens}
	if status.Window != nil {
		o.WindowID = status.Window.ID
		o.Policy = evalharness.PairCapacity{Global: status.Window.Policy.MaxConcurrency, Team: status.Window.Policy.TeamConcurrency(f.team), ContextWindowTokens: status.Window.Policy.ContextWindowTokens}
	}
	return o, raw
}

func TestPairedEvaluationTerminalUnknownUsageAndReceiptIdentity(t *testing.T) {
	for _, mode := range []string{"unknown_usage", "failed_unknown_usage"} {
		t.Run(mode, func(t *testing.T) {
			x := newFinalizationFixture(t)
			canonical := x.f.canonicalSnapshot(t)
			x.stageOutcome(t, mode)
			before, raw := pairedMaintenanceSnapshot(t, x.f)
			require.Positive(t, before.Usage.Unavailable)
			require.Zero(t, before.Usage.Unreconciled)
			ready, err := evalharness.EvaluatePairReadiness("adr0014", before, false)
			require.NoError(t, err)
			require.False(t, ready.Ready)
			require.Contains(t, ready.WaitingFor, "active_batches")
			require.NoError(t, x.f.store.CompleteMaintenanceBatch(context.Background(), *x.claim, x.result, x.result.FailureCode, time.Now()))
			completed, retained := pairedMaintenanceSnapshot(t, x.f)
			require.Zero(t, completed.ActiveBatches)
			measurement, err := evalharness.MeasurePairAttempts(completed.ProviderAttempts)
			require.NoError(t, err)
			require.Equal(t, len(completed.ProviderAttempts), measurement.UnknownUsage)
			require.Zero(t, measurement.Unfinished)
			require.Equal(t, canonical, x.f.canonicalSnapshot(t))
			var existing map[string]any
			require.NoError(t, json.Unmarshal(retained, &existing))
			require.NotEqual(t, string(raw), string(retained))
			require.NoError(t, x.f.admin.Exec(`INSERT INTO ontology_assessments(team_id,shared_space_id,space_generation,operation_key,assessment_id,input_hash,batch_hash,body)
			 SELECT team_id,shared_space_id,space_generation,?,assessment_id,input_hash,batch_hash,body FROM ontology_assessments WHERE operation_key=?`, "different-operation:"+uuid.NewString(), "maintenance:"+x.claim.ID).Error)
			joined, _ := pairedMaintenanceSnapshot(t, x.f)
			require.Equal(t, completed.ProviderAttempts, joined.ProviderAttempts, "assessment ID reuse must not multiply durable attempts")
			if mode == "failed_unknown_usage" {
				require.Positive(t, completed.Failed)
				require.False(t, completed.CoverageComplete)
			}
		})
	}
}

func TestPairedEvaluationCapacityActivationAndPausedAdmission(t *testing.T) {
	for _, capacity := range []int{4, 8} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			f := newOrganizationFixture(t)
			concurrencySources(t, f)
			config := concurrencyConfig(t, f, capacity, capacity)
			window := maintenanceWindow(t, f, config)
			before := f.canonicalSnapshot(t)
			ctx := context.Background()
			_, err := f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: uuid.NewString()}, time.Now(), nil)
			require.NoError(t, err)
			claim, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
			require.ErrorIs(t, err, ontology.ErrMaintenancePaused)
			require.Nil(t, claim)
			active, _ := pairedMaintenanceSnapshot(t, f)
			require.Equal(t, capacity, active.Policy.Global)
			require.Equal(t, capacity, active.Policy.Team)
			nextCapacity := 4
			if capacity == 4 {
				nextCapacity = 8
			}
			_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyConcurrency: fmt.Sprint(nextCapacity), domain.AppConfigOntologyTeamConcurrency: fmt.Sprint(nextCapacity)}, "control", "", "")
			require.NoError(t, err)
			queued, _ := pairedMaintenanceSnapshot(t, f)
			require.Equal(t, active.Policy, queued.Policy, "pending settings cannot be reported as effective capacity")
			policy, err := config.OntologyMaintenanceRuntimeConfig(ctx)
			require.NoError(t, err)
			next, err := f.store.EnsureMaintenanceWindow(ctx, policy, window.EndsAt)
			require.NoError(t, err)
			require.Equal(t, nextCapacity, next.Policy.MaxConcurrency)
			require.Equal(t, nextCapacity, next.Policy.TeamConcurrency(f.team))
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
}
