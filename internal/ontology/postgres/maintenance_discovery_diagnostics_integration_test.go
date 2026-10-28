//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologyMaintenanceDiscoveryCommittedDiagnostics(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			x := newDiscoveryFixture(t, []int{2}, nil, false)
			page, err := x.f.store.ListRecords(context.Background(), x.f.team, ontology.EntityClass, "", 1)
			require.NoError(t, err)
			_, err = x.f.store.SeedDefinitions(context.Background(), x.f.team, ontology.SeedInput{OperationKey: "diagnostics-seed", ExpectedRevision: page.Revision, Limit: 20})
			require.NoError(t, err)
			x.drain(t)
			definitions := coalescingDefinitions(t, x, 2, "overlap")
			enqueueCoalescingMarkers(t, x, definitions)
			if rollback {
				topic := x.topic
				topic.Definition.Description = "PostgreSQL revised required definition"
				revalidationPublish(t, x.f, ontology.Change{ExpectedVersion: 1, Record: topic})
				require.NoError(t, x.f.admin.Exec(`CREATE FUNCTION reject_discovery_source() RETURNS trigger LANGUAGE plpgsql AS $$
				 BEGIN RAISE EXCEPTION 'synthetic-discovery-secret'; END $$;
				 CREATE TRIGGER reject_discovery_source BEFORE INSERT ON ontology_maintenance_sources
				 FOR EACH ROW WHEN (NEW.source_id='00000000-0000-4000-8000-000000000002') EXECUTE FUNCTION reject_discovery_source()`).Error)
			}
			canonical, history, queue, markers := schedulerCanonical(t, x.f), revalidationReceiptHistory(t, x.f), x.queue(t), coalescingMarkerCursors(t, x)
			require.NoError(t, x.f.store.ReleaseMaintenanceTurn(context.Background(), x.turn))
			var output bytes.Buffer
			service := organization.NewMaintenanceService(organization.MaintenanceDependencies{
				Repository: x.f.store, Config: maintenanceSettings(t, x.f), DefaultModel: "fixture-model", ProviderTimeout: time.Minute,
				Logger: slog.New(slog.NewJSONHandler(&output, nil)),
				Organizer: func(model string, contextWindowTokens int64, accounting assessment.AttemptAccounting) *organization.Service {
					organizer, _ := organizationFixtureServiceWithAccounting(t, x.f, nil, nil, model, maintenanceFixtureLimits(), accounting, int(contextWindowTokens))
					return organizer
				},
			})
			_, err = service.RunTurn(context.Background())
			if rollback {
				require.ErrorContains(t, err, "synthetic-discovery-secret")
				require.Equal(t, markers, coalescingMarkerCursors(t, x))
			} else {
				require.NoError(t, err)
				require.Empty(t, coalescingMarkerCursors(t, x))
			}
			require.NotContains(t, output.String(), "synthetic-discovery-secret")
			require.NotContains(t, output.String(), "PostgreSQL discovery measurement")
			var discovery, preparation map[string]any
			for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
				var fields map[string]any
				require.NoError(t, json.Unmarshal(line, &fields))
				switch fields["msg"] {
				case "ontology maintenance discovery":
					discovery = fields
				case "ontology maintenance preparation":
					preparation = fields
				}
			}
			require.NotEmpty(t, discovery)
			require.NotEmpty(t, preparation)
			require.Equal(t, !rollback, discovery["committed"])
			require.Equal(t, true, preparation["includes_discovery"])
			require.Positive(t, discovery["duration_ms"].(float64))
			require.GreaterOrEqual(t, preparation["duration_ms"].(float64), discovery["duration_ms"].(float64))
			counts, attempted := discovery["counts"].(map[string]any), discovery["attempted"].(map[string]any)
			if rollback {
				for _, value := range counts {
					require.Zero(t, value)
				}
				require.Positive(t, attempted["requeues"])
				require.EqualValues(t, 1, attempted["refreshed_sources"])
				require.NotEmpty(t, discovery["error_code"])
				require.NotEmpty(t, preparation["error_code"])
			} else {
				require.Equal(t, attempted, counts)
				require.EqualValues(t, 2, counts["refreshed_sources"])
				require.EqualValues(t, 2, counts["completed_markers"])
				require.Empty(t, discovery["error_code"])
			}
			require.Equal(t, queue, x.queue(t))
			require.Equal(t, canonical, schedulerCanonical(t, x.f))
			require.Equal(t, history, revalidationReceiptHistory(t, x.f))
		})
	}
}
