//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	organization "github.com/markhuangai/dense-mem/internal/topics"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceDiscoveryCommittedDiagnostics(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			x := newDiscoveryFixture(t, []int{2}, nil, false)
			x.drain(t)
			definitions := coalescingDefinitions(t, x, 2, "overlap")
			enqueueCoalescingMarkers(t, x, definitions)
			if rollback {
				require.NoError(t, x.f.rls.WithSystemTx(t.Context(), x.f.admin, func(tx *gorm.DB) error {
					return tx.Exec(`UPDATE topic_maintenance_sources SET status='organized' WHERE team_id=?::uuid
						AND shared_space_id=?::uuid AND space_generation=? AND source_kind='evidence' AND source_id=?`,
						x.f.team, x.f.space, x.f.generation, x.sources[0].ID).Error
				}))
				topic := x.topic
				topic.Definition.Description = "PostgreSQL revised required definition"
				revalidationPublish(t, x.f, ontology.Change{ExpectedVersion: 1, Record: topic})
				require.NoError(t, x.f.admin.Exec(`CREATE FUNCTION reject_discovery_source() RETURNS trigger LANGUAGE plpgsql AS $$
				 BEGIN RAISE EXCEPTION 'synthetic-discovery-secret'; END $$;
				 CREATE TRIGGER reject_discovery_source BEFORE INSERT ON topic_maintenance_sources
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
			_, err := service.RunTurn(context.Background())
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
				case "topic maintenance discovery":
					discovery = fields
				case "topic maintenance preparation":
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
				for i := range queue {
					require.Equal(t, "pending", queue[i].Status)
					queue[i].Status = "organized"
				}
			}
			require.Equal(t, queue, x.queue(t))
			require.Equal(t, canonical, schedulerCanonical(t, x.f))
			afterHistory := revalidationReceiptHistory(t, x.f)
			if rollback {
				require.Equal(t, history, afterHistory)
			} else {
				var original, retained []json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(history), &original))
				require.NoError(t, json.Unmarshal([]byte(afterHistory), &retained))
				require.Len(t, retained, len(original)+1)
				require.Subset(t, retained, original, "completed work must preserve prior receipts exactly")
				batchID, ok := preparation["batch_id"].(string)
				require.True(t, ok)
				require.NotEmpty(t, batchID)
				var assessmentID string
				require.NoError(t, x.f.admin.Raw(`SELECT assessment_id::text FROM historical_organization_assessments
					WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND operation_key=?`,
					x.f.team, x.f.space, x.f.generation, "maintenance:"+batchID).Row().Scan(&assessmentID))
				require.Equal(t, queue[0].AssessmentID, assessmentID, "the completion receipt must reuse the earlier assessment")
			}
		})
	}
}
