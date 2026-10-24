//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	community "github.com/markhuangai/dense-mem/internal/community/contract"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceParallelSchedulerMeasurement(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		name := "no-projection"
		if mixed {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			f := newOrganizationFixture(t)
			schedulerTopic(t, f)
			config := schedulerConfig(t, f, 4, 4)
			setup, _ := schedulerFixtureService(t, f, config, nil)
			for range 20 {
				progress, err := setup.RunTurn(context.Background())
				require.NoError(t, err)
				if !progress {
					break
				}
			}
			initial, err := f.store.MaintenanceStatus(context.Background(), time.Now())
			require.NoError(t, err)
			require.True(t, initial.CoverageComplete, "measurement setup must converge: %+v", initial)
			for i := range 25 {
				f.organizationEvidence(t, i%len(f.owners), fmt.Sprintf("Scheduler project uses PostgreSQL for workload %d.", i), nil)
			}
			m := &schedulerMeasurement{Fixture: "scheduler-mixed-v1", MaxTeamAdmitted: map[string]int{},
				Configuration: map[string]int{"global_concurrency": 4, "team_concurrency": 4,
					"workload_sources": 25, "provider_delay_ms": 200, "projection_delay_ms": 250}}
			provider := schedulerProvider(m, f, make(chan struct{}, 100), nil)
			observer := &schedulerPreparationObserver{MaintenanceRepository: f.store, started: map[string]time.Time{}}
			service, _ := schedulerFixtureService(t, f, config, func(request assessment.Request, response *assessment.Response) {
				provider(request, response)
			}, observer)
			projections, store := schedulerProjections(f, config, func(ctx context.Context, _ community.TopicProjectionBatch) error {
				m.mu.Lock()
				m.projections++
				m.MixedOverlap = m.MixedOverlap || m.providers > 0
				m.mu.Unlock()
				defer func() { m.mu.Lock(); m.projections--; m.mu.Unlock() }()
				m.sample(f)
				select {
				case <-time.After(250 * time.Millisecond):
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}, func(error) {
				m.mu.Lock()
				defer m.mu.Unlock()
				if m.FirstProjectionMS == 0 {
					m.FirstProjectionMS = float64(time.Since(m.started).Microseconds()) / 1000
				}
			})
			if mixed {
				service.SetProjectionRunner(projections)
			}
			m.CanonicalBefore = schedulerCanonical(t, f)
			counter := &organizationReadCounter{Interface: f.store.db.Logger}
			f.store.db = f.store.db.Session(&gorm.Session{Logger: counter})
			observer.MaintenanceRepository = organizationPreparationReads{MaintenanceRepository: f.store}
			m.started = time.Now()
			_, stop := startScheduler(t, service, nil, 1)
			defer stop()
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				status, err := f.store.MaintenanceStatus(context.Background(), time.Now())
				require.NoError(c, err)
				require.True(c, status.CoverageComplete, "maintenance status: %+v", status)
				require.Positive(c, status.Counts.Organized)
				if !mixed {
					return
				}
				coverage, err := store.TopicProjectionCoverage(f.actor(1, "member"), f.team)
				require.NoError(c, err)
				require.Positive(c, coverage.CurrentTopics)
				require.True(c, coverage.CoverageComplete, "projection coverage: %+v", coverage)
			}, 2*time.Minute, 20*time.Millisecond)
			stop()
			m.CompletionMS = float64(time.Since(m.started).Microseconds()) / 1000
			schedulerAssertDrained(t, f)
			m.CanonicalAfter = schedulerCanonical(t, f)
			require.Equal(t, m.CanonicalBefore, m.CanonicalAfter)
			require.Empty(t, m.Errors)
			require.Positive(t, m.ProviderCalls)
			require.LessOrEqual(t, m.MaxAdmitted, 4)
			projections.mu.Lock()
			for _, duration := range projections.turns {
				m.ProjectionTurnsMS = append(m.ProjectionTurnsMS, float64(duration.Microseconds())/1000)
			}
			projections.mu.Unlock()
			observer.mu.Lock()
			m.PreparationTurnsMS = append(m.PreparationTurnsMS, observer.turns...)
			observer.mu.Unlock()
			m.TerminalOutcomes = map[string]int{}
			var outcomes []struct {
				Status string
				Count  int
			}
			require.NoError(t, f.admin.Raw(`SELECT status,count(*) FROM ontology_maintenance_sources GROUP BY status`).Scan(&outcomes).Error)
			for _, outcome := range outcomes {
				m.TerminalOutcomes[outcome.Status] = outcome.Count
			}
			status, err := f.store.MaintenanceStatus(context.Background(), time.Now())
			require.NoError(t, err)
			m.ReportedInput = status.Usage.ReportedInputTokens - initial.Usage.ReportedInputTokens
			m.ReportedOutput = status.Usage.ReportedOutputTokens - initial.Usage.ReportedOutputTokens
			m.UnavailableUsageAttempts = status.Usage.UnavailableUsageAttempts - initial.Usage.UnavailableUsageAttempts
			m.PartialUsageAttempts = status.Usage.PartialUsageAttempts - initial.Usage.PartialUsageAttempts
			if mixed {
				m.ProjectionCoverage, err = store.TopicProjectionCoverage(f.actor(1, "member"), f.team)
				require.NoError(t, err)
			}
			encoded, err := json.Marshal(struct {
				*schedulerMeasurement
				SQLQueries         int64 `json:"sql_queries"`
				PreparationQueries int64 `json:"preparation_sql_queries"`
			}{m, counter.queries.Load(), counter.preparationQueries.Load()})
			require.NoError(t, err)
			t.Logf("scheduler_measurement %s", encoded)
			if directory := os.Getenv("DENSE_MEM_SCHEDULER_REPORT_DIR"); directory != "" {
				require.NoError(t, os.MkdirAll(directory, 0700))
				file, err := os.CreateTemp(directory, name+"-*.json")
				require.NoError(t, err)
				_, err = file.Write(encoded)
				require.NoError(t, err)
				require.NoError(t, file.Close())
				require.Equal(t, directory, filepath.Dir(file.Name()))
			}
		})
	}
}
