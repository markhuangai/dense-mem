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

func runnerMaintenanceSnapshot(t *testing.T, f *ontologyFixture) (evalharness.NativeObservation, []byte) {
	t.Helper()
	ctx := context.Background()
	pool, err := f.admin.DB()
	require.NoError(t, err)
	var raw []byte
	require.NoError(t, pool.QueryRowContext(ctx, evalharness.MaintenanceSnapshotSQL, f.team, f.owners[0], f.space, f.generation, f.credentials[0].ID).Scan(&raw))
	var observation evalharness.NativeObservation
	require.NoError(t, json.Unmarshal(raw, &observation))
	observation.Ontology, err = f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	return observation, raw
}

func TestRunnerCampaignTerminalUnknownUsageAndReceiptIdentity(t *testing.T) {
	for _, mode := range []string{"unknown_usage", "failed_unknown_usage"} {
		t.Run(mode, func(t *testing.T) {
			x := newFinalizationFixture(t)
			canonical := x.f.canonicalSnapshot(t)
			x.stageOutcome(t, mode)
			before, raw := runnerMaintenanceSnapshot(t, x.f)
			require.Positive(t, before.Ontology.Usage.UnavailableUsageAttempts)
			require.Zero(t, before.Ontology.Usage.UnreconciledAttempts)
			require.Positive(t, before.ActiveBatches, "unfinished durable work must block capture")
			require.NoError(t, x.f.store.CompleteMaintenanceBatch(context.Background(), *x.claim, x.result, x.result.FailureCode, time.Now()))
			completed, retained := runnerMaintenanceSnapshot(t, x.f)
			require.Zero(t, completed.ActiveBatches)
			measurement, err := evalharness.MeasureAttempts(completed.ProviderAttempts)
			require.NoError(t, err)
			require.Equal(t, len(completed.ProviderAttempts), measurement.UnknownUsage)
			require.Zero(t, measurement.Unfinished)
			require.Equal(t, canonical, x.f.canonicalSnapshot(t))
			var existing map[string]any
			require.NoError(t, json.Unmarshal(retained, &existing))
			require.NotEqual(t, string(raw), string(retained))
			require.NoError(t, x.f.admin.Exec(`INSERT INTO ontology_assessments(team_id,shared_space_id,space_generation,operation_key,assessment_id,input_hash,batch_hash,body)
			 SELECT team_id,shared_space_id,space_generation,?,assessment_id,input_hash,batch_hash,body FROM ontology_assessments WHERE operation_key=?`, "different-operation:"+uuid.NewString(), "maintenance:"+x.claim.ID).Error)
			joined, _ := runnerMaintenanceSnapshot(t, x.f)
			require.Equal(t, completed.ProviderAttempts, joined.ProviderAttempts, "assessment ID reuse must not multiply durable attempts")
			if mode == "failed_unknown_usage" {
				require.Positive(t, completed.Ontology.Counts.Failed)
				require.False(t, completed.Ontology.CoverageComplete)
			}
		})
	}
}

func TestRunnerCampaignCapacityActivationAndPausedAdmission(t *testing.T) {
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
			active, _ := runnerMaintenanceSnapshot(t, f)
			require.Equal(t, capacity, active.Ontology.Window.Policy.MaxConcurrency)
			require.Equal(t, capacity, active.Ontology.Window.Policy.TeamConcurrency(f.team))
			nextCapacity := 4
			if capacity == 4 {
				nextCapacity = 8
			}
			_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyConcurrency: fmt.Sprint(nextCapacity), domain.AppConfigOntologyTeamConcurrency: fmt.Sprint(nextCapacity)}, "control", "", "")
			require.NoError(t, err)
			queued, _ := runnerMaintenanceSnapshot(t, f)
			require.Equal(t, active.Ontology.Window.Policy, queued.Ontology.Window.Policy, "pending settings cannot be reported as effective capacity")
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
