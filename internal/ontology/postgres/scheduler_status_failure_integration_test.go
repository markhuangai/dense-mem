//go:build integration

package postgres

import (
	"bytes"
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	communitypg "github.com/markhuangai/dense-mem/internal/community/postgres"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/domain"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	"github.com/stretchr/testify/require"
)

type statusFailureWriter struct {
	seen    atomic.Bool
	release func()
}

func (writer *statusFailureWriter) Write(body []byte) (int, error) {
	if bytes.Contains(body, []byte("projection_status_unavailable")) && writer.seen.CompareAndSwap(false, true) {
		writer.release()
	}
	return len(body), nil
}

func TestOntologySchedulerStatusFailurePreservesRealOrganizationTurns(t *testing.T) {
	for _, mode := range []string{"active", "paused", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := newOrganizationFixture(t)
			f.organizationEvidence(t, 0, "Atlas stores PostgreSQL data.", nil)
			config := maintenanceSettings(t, f)
			maintenanceWindow(t, f, config)
			pool, err := f.app.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			require.NoError(t, f.app.Exec(`SET statement_timeout='100ms'`).Error)
			require.NoError(t, f.admin.Exec(`UPDATE community_topic_discovery SET after_team='70000000-0000-4000-8000-000000000001',after_topic='70000000-0000-4000-8000-000000000001' WHERE singleton`).Error)
			held := f.admin.Begin()
			require.NoError(t, held.Error)
			t.Cleanup(func() { _ = held.Rollback().Error })
			require.NoError(t, held.Exec(`LOCK TABLE ontology_maintenance_sources IN ACCESS EXCLUSIVE MODE`).Error)
			writer := &statusFailureWriter{release: func() {
				require.NoError(t, held.Commit().Error)
				require.NoError(t, f.app.Exec(`SET statement_timeout=0`).Error)
				if mode == "paused" {
					require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_state SET paused=true WHERE singleton`).Error)
				}
				if mode == "disabled" {
					_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyEnabled: "false"}, "control", "", "")
					require.NoError(t, err)
				}
			}}
			communityStore := communitypg.NewStore(f.app, f.rls).WithTopics(f.store.NewTopicCatalogReader(), f.store.NewTopicMembershipReader(), f.store.NewTopicProjectionAdmission(), f.store.NewTopicProjectionRelease())
			projections := communityapp.New(communityapp.Dependencies{Store: communityStore, AppConfig: config})
			var calls []*atomic.Int32
			service := organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: f.store, Config: config, Projections: projections, DefaultModel: "fixture-model",
				Organizer: func(model string, contextWindowTokens int64, accounting assessment.AttemptAccounting) *organization.Service {
					if accounting == nil {
						return organization.NewService(nil, assessment.NewProvider(nil, model, maintenanceFixtureLimits(), int(contextWindowTokens)))
					}
					organizer, counter := organizationFixtureServiceWithAccounting(t, f, nil, nil, model, maintenanceFixtureLimits(), accounting, int(contextWindowTokens))
					calls = append(calls, counter)
					return organizer
				},
			})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			stop := func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("scheduler did not finish cancellation")
				}
			}
			t.Cleanup(stop)
			go func() {
				defer close(done)
				organization.NewMaintenanceScheduler(service, slog.New(slog.NewTextHandler(writer, nil))).Start(ctx)
			}()
			require.Eventually(t, writer.seen.Load, 10*time.Second, 10*time.Millisecond)
			if mode == "active" {
				require.Eventually(t, func() bool {
					status, err := f.store.MaintenanceStatus(context.Background(), time.Now())
					return err == nil && status.Counts.Organized > 0
				}, 20*time.Second, 20*time.Millisecond)
			}
			stop()
			var providerCalls int32
			for _, counter := range calls {
				providerCalls += counter.Load()
			}
			if mode == "active" {
				require.Positive(t, providerCalls)
			} else {
				require.Zero(t, providerCalls)
			}
			var cursor string
			require.NoError(t, f.admin.Raw(`SELECT after_topic FROM community_topic_discovery WHERE singleton`).Row().Scan(&cursor))
			require.Equal(t, "70000000-0000-4000-8000-000000000001", cursor, "projection work must be skipped for this failed status invocation")
		})
	}
}
