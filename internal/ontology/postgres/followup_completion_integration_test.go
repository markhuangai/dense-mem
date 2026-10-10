//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func maintenanceRunByID(t *testing.T, f *ontologyFixture, id string) ontology.MaintenanceRun {
	t.Helper()
	page, err := f.store.ListMaintenanceRuns(context.Background(), "", ontology.MaxPageSize)
	require.NoError(t, err)
	for _, run := range page.Runs {
		if run.ID == id {
			return run
		}
	}
	t.Fatalf("run %s missing", id)
	return ontology.MaintenanceRun{}
}

func TestOntologyMaintenanceBoundedCompletionUsesPersistedOutcomes(t *testing.T) {
	for _, outcome := range []string{"ambiguous comparison", "oversized", "organized"} {
		t.Run(outcome, func(t *testing.T) {
			f := newOrganizationFixture(t)
			text := "Atlas stores data in PostgreSQL."
			if outcome == "oversized" {
				text = strings.Repeat("Atlas uses PostgreSQL. ", 50000)
				limits := maintenanceFixtureLimits()
				tokens, err := assessor.CountTokens(text, limits.Tokenizer)
				require.NoError(t, err)
				require.Greater(t, tokens, limits.MaxInputTokens)
			}
			f.organizationEvidence(t, 0, text, nil)
			if outcome == "ambiguous comparison" {
				f.organizationEvidence(t, 1, "PostgreSQL is Atlas's data store.", nil)
			}
			before := f.canonicalSnapshot(t)
			settings := maintenanceSettings(t, f)
			window := maintenanceWindow(t, f, settings)
			run, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "run", OperationKey: uuid.NewString(), MaxBatches: 1}, time.Now().UTC(), nil)
			require.NoError(t, err)
			service, calls := maintenanceServiceFixture(t, f, settings, func(_ assessment.Request, response *assessment.Response) {
				if outcome == "ambiguous comparison" {
					require.NotEmpty(t, response.Equivalence)
					for i := range response.Equivalence {
						response.Equivalence[i].Relation = "ambiguous"
					}
				}
			})
			drainMaintenance(t, service)
			completed := maintenanceRunByID(t, f, run.ID)
			require.Equal(t, 1, completed.CompletedBatches)
			require.Equal(t, 1, completed.MaxBatches)
			require.Equal(t, window.ID, completed.WindowID)
			require.Empty(t, completed.FailureCode)
			require.False(t, completed.Retryable)
			status, err := service.Status(context.Background())
			require.NoError(t, err)
			require.Zero(t, status.Counts.Failed)
			if outcome == "organized" {
				require.Equal(t, "completed", completed.Status)
				require.Zero(t, status.Counts.Ambiguous)
			} else {
				require.Equal(t, "incomplete", completed.Status)
				require.Positive(t, status.Counts.Ambiguous)
				require.False(t, status.CoverageComplete)
			}
			if outcome == "oversized" {
				require.Zero(t, calls())
			} else {
				require.Positive(t, calls())
			}
			var reason string
			require.NoError(t, f.admin.Raw(`SELECT failure_code FROM ontology_maintenance_batches WHERE run_id=?::uuid`, run.ID).Row().Scan(&reason))
			require.Empty(t, reason)
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
}

func TestOntologyMaintenanceEarlierAmbiguityKeepsBoundedRunIncomplete(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Alpha.", nil)
	f.organizationEvidence(t, 1, "Zebra.", nil)
	settings := maintenanceSettings(t, f)
	maintenanceWindow(t, f, settings)
	run, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "run", OperationKey: "two-batches", MaxBatches: 2}, time.Now().UTC(), nil)
	require.NoError(t, err)
	first := true
	service, _ := maintenanceServiceFixture(t, f, settings, func(request assessment.Request, response *assessment.Response) {
		if first {
			ambiguousClassification(request, response)
			first = false
		}
	})
	drainMaintenance(t, service)
	completed := maintenanceRunByID(t, f, run.ID)
	require.Equal(t, 2, completed.CompletedBatches)
	require.Equal(t, "incomplete", completed.Status)
	require.Empty(t, completed.FailureCode)
	status, err := service.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Ambiguous)
	require.EqualValues(t, 1, status.Counts.Organized)
}

func TestOntologyMaintenanceConcurrentInterruptionPreservesRunWork(t *testing.T) {
	f := newOrganizationFixture(t)
	other := maintenanceOtherTeam(t, f)
	settings := maintenanceSettings(t, f)
	ctx := context.Background()
	_, err := settings.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyConcurrency: "2"}, "control", "", "")
	require.NoError(t, err)
	window := maintenanceWindow(t, f, settings)
	for _, failure := range []string{"maintenance_paused", "maintenance_disabled", ""} {
		t.Run(failure, func(t *testing.T) {
			f.organizationEvidence(t, 0, "Atlas uses PostgreSQL "+uuid.NewString(), nil)
			maintenanceEntity(t, other, "Beta-"+uuid.NewString())
			run, err := f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "run", OperationKey: uuid.NewString(), MaxBatches: 2}, time.Now().UTC(), nil)
			require.NoError(t, err)
			firstTurn, first := maintenanceClaim(t, f, window)
			lastTurn, last := maintenanceClaim(t, f, window)
			require.Equal(t, run.ID, first.RunID)
			require.Equal(t, run.ID, last.RunID)
			require.NotEqual(t, first.TeamID, last.TeamID)
			if failure == "maintenance_paused" {
				_, err = f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: uuid.NewString()}, time.Now().UTC(), nil)
				require.NoError(t, err)
				defer func() {
					_, err := f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "resume", OperationKey: uuid.NewString()}, time.Now().UTC(), nil)
					require.NoError(t, err)
				}()
			} else if failure == "maintenance_disabled" {
				_, err = settings.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "false"}, "control", "", "")
				require.NoError(t, err)
				defer func() {
					_, err := settings.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "true"}, "control", "", "")
					require.NoError(t, err)
				}()
			} else {
				unrelated := f.organizationEvidence(t, 0, "Unclaimed work "+uuid.NewString(), nil)
				require.NoError(t, f.store.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
					return refreshMaintenanceSource(tx, scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation}, unrelated, false)
				}))
			}
			before, otherBefore := f.canonicalSnapshot(t), other.canonicalSnapshot(t)
			result := func(claim *ontology.MaintenanceClaim) ontology.OrganizationResult {
				result := ontology.OrganizationResult{Current: true}
				for _, source := range claim.Sources {
					result.Outcomes = append(result.Outcomes, ontology.OrganizationOutcome{Source: source, Status: "organized"})
				}
				return result
			}
			require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *first, result(first), failure, time.Now().UTC()))
			require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *firstTurn))
			require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *last, result(last), "", time.Now().UTC()))
			require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *lastTurn))
			completed := maintenanceRunByID(t, f, run.ID)
			require.Equal(t, 2, completed.CompletedBatches)
			require.Equal(t, failure, completed.FailureCode)
			expected := "incomplete"
			if failure == "" {
				expected = "completed"
			}
			require.Equal(t, expected, completed.Status)
			var historicalReason string
			require.NoError(t, f.admin.Raw(`SELECT failure_code FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, first.ID).Row().Scan(&historicalReason))
			require.Equal(t, failure, historicalReason)
			require.Equal(t, before, f.canonicalSnapshot(t))
			require.Equal(t, otherBefore, other.canonicalSnapshot(t))
		})
	}
}

func TestOntologyMaintenanceScheduledRecoveryClearsCurrentReason(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	settings := maintenanceSettings(t, f)
	window := maintenanceWindow(t, f, settings)
	turn, claim := maintenanceClaim(t, f, window)
	require.NoError(t, f.store.CompleteMaintenanceBatch(context.Background(), *claim, ontology.OrganizationResult{}, "maintenance_paused", time.Now().UTC()))
	require.NoError(t, f.store.RecordMaintenanceFailure(context.Background(), turn.RunID, "maintenance_paused", time.Now().UTC()))
	require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
	before := f.canonicalSnapshot(t)
	service, calls := maintenanceServiceFixture(t, f, settings, nil)
	drainMaintenance(t, service)
	run := maintenanceRunByID(t, f, turn.RunID)
	require.Equal(t, "completed", run.Status)
	require.Empty(t, run.FailureCode)
	require.False(t, run.Retryable)
	require.Equal(t, window.ID, run.WindowID)
	require.EqualValues(t, 1, calls())
	status, err := service.Status(context.Background())
	require.NoError(t, err)
	require.True(t, status.CoverageComplete)
	require.Zero(t, status.Counts.Failed)
	var historicalReason string
	require.NoError(t, f.admin.Raw(`SELECT failure_code FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, claim.ID).Row().Scan(&historicalReason))
	require.Equal(t, "maintenance_paused", historicalReason)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyMaintenanceCompletedRunDoesNotHideUnrelatedFailures(t *testing.T) {
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Alpha.", nil)
	settings := maintenanceSettings(t, f)
	window := maintenanceWindow(t, f, settings)
	broken := true
	service, _ := maintenanceServiceFixture(t, f, settings, func(_ assessment.Request, response *assessment.Response) {
		if broken {
			response.Items = nil
		}
	})
	for range 30 {
		progress, err := service.RunTurn(context.Background())
		if err != nil {
			require.True(t, progress)
			break
		}
		if !progress {
			t.Fatal("expected required provider failure")
		}
	}
	status, err := service.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Failed)
	var failedRun string
	require.NoError(t, f.admin.Raw(`SELECT run_id::text FROM ontology_maintenance_runs WHERE window_id=?::uuid AND kind='scheduled'`, window.ID).Row().Scan(&failedRun))
	failed := maintenanceRunByID(t, f, failedRun)
	require.NotEmpty(t, failed.FailureCode)
	broken = false
	f.organizationEvidence(t, 1, "Zebra.", nil)
	manual, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "run", OperationKey: "unrelated-success", MaxBatches: 1}, time.Now().UTC(), nil)
	require.NoError(t, err)
	drainMaintenance(t, service)
	completed := maintenanceRunByID(t, f, manual.ID)
	require.Equal(t, "completed", completed.Status)
	require.Empty(t, completed.FailureCode)
	require.False(t, completed.Retryable)
	retained := maintenanceRunByID(t, f, failedRun)
	require.Equal(t, "incomplete", retained.Status)
	require.Equal(t, failed.FailureCode, retained.FailureCode)
	require.True(t, retained.Retryable)
	status, err = service.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Failed)
	require.False(t, status.CoverageComplete)
	var historyCount int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE run_id=?::uuid AND failure_code<>''`, failedRun).Row().Scan(&historyCount))
	require.Positive(t, historyCount)
}
