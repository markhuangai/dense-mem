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

	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceParallelSchedulerMeasurement(t *testing.T) {
	name := "evidence"
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
		m := &schedulerMeasurement{Fixture: "scheduler-evidence-v3", MaxTeamAdmitted: map[string]int{},
			Configuration: map[string]int{"global_concurrency": 4, "team_concurrency": 4,
				"workload_sources": 25, "provider_delay_ms": 200}}
		provider := schedulerProvider(m, f, make(chan struct{}, 100), nil)
		observer := &schedulerPreparationObserver{MaintenanceRepository: f.store, started: map[string]time.Time{}}
		service, _ := schedulerFixtureService(t, f, config, func(request assessment.Request, response *assessment.Response) {
			provider(request, response)
		}, observer)
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
		}, 2*time.Minute, 20*time.Millisecond)
		stop()
		m.CompletionMS = float64(time.Since(m.started).Microseconds()) / 1000
		schedulerAssertDrained(t, f)
		m.CanonicalAfter = schedulerCanonical(t, f)
		require.Equal(t, m.CanonicalBefore, m.CanonicalAfter)
		require.Empty(t, m.Errors)
		require.Positive(t, m.ProviderCalls)
		require.LessOrEqual(t, m.MaxAdmitted, 4)
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
