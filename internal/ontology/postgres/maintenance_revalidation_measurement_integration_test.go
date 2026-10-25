//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func revalidationPublish(t *testing.T, f *ontologyFixture, changes ...ontology.Change) {
	t.Helper()
	page, err := f.store.ListRecords(context.Background(), f.team, "", "", 1)
	require.NoError(t, err)
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), page.Revision, changes...))
	require.NoError(t, err)
}

func revalidationDiscover(t *testing.T, f *ontologyFixture, window *ontology.MaintenanceWindow) {
	t.Helper()
	ctx := context.Background()
	turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	defer func() { require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn)) }()
	for range 8 {
		require.NoError(t, f.store.DiscoverMaintenance(ctx, *turn, ontology.MaintenancePageSize))
	}
}

type revalidationQueueState struct {
	Status, AssessmentID, Fingerprint string
	Revision                          int64
}

func revalidationQueue(t *testing.T, f *ontologyFixture, source ontology.SourceHandle) revalidationQueueState {
	t.Helper()
	var state revalidationQueueState
	require.NoError(t, f.admin.Raw(`SELECT status,COALESCE(assessment_id::text,''),fingerprint,revision FROM ontology_maintenance_sources
	 WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind=? AND source_id=?`, f.team, f.space, f.generation, source.Kind, source.ID).Row().Scan(&state.Status, &state.AssessmentID, &state.Fingerprint, &state.Revision))
	return state
}

func revalidationRefresh(t *testing.T, f *ontologyFixture, source ontology.SourceHandle) {
	t.Helper()
	require.NoError(t, f.store.withScope(context.Background(), f.team, false, func(tx *gorm.DB, fence scope) error {
		return refreshMaintenanceSource(tx, fence, source, true)
	}))
}

func revalidationMeasurement(t *testing.T, f *ontologyFixture, started time.Time) map[string]any {
	t.Helper()
	status, err := f.store.MaintenanceStatus(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	var markers, receipts, assessments, batches, appearances, attempts, input, output, unknownUsage int64
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_markers WHERE team_id=?::uuid`, f.team).Row().Scan(&markers))
	require.NoError(t, f.admin.Raw(`SELECT count(*),count(DISTINCT assessment_id) FROM ontology_assessments WHERE team_id=?::uuid`, f.team).Row().Scan(&receipts, &assessments))
	require.NoError(t, f.admin.Raw(`SELECT count(*),COALESCE(sum(jsonb_array_length(sources)),0) FROM ontology_maintenance_batches WHERE team_id=?::uuid`, f.team).Row().Scan(&batches, &appearances))
	require.NoError(t, f.admin.Raw(`WITH receipts AS (SELECT DISTINCT ON (assessment_id) body FROM ontology_assessments WHERE team_id=?::uuid ORDER BY assessment_id,operation_key)
	 SELECT count(*),COALESCE(sum((attempt->>'reported_input_tokens')::bigint),0),COALESCE(sum((attempt->>'reported_output_tokens')::bigint),0),
	 count(*) FILTER (WHERE NOT COALESCE((attempt->>'reported_usage_available')::boolean,false))
	 FROM receipts CROSS JOIN LATERAL jsonb_array_elements(body->'result'->'attempts') AS attempt`, f.team).Row().Scan(&attempts, &input, &output, &unknownUsage))
	return map[string]any{"case": t.Name(), "current_source_counts": status.Counts, "definition_markers": markers,
		"receipt_rows": receipts, "distinct_assessments": assessments, "prepared_batches": batches,
		"claimed_source_appearances": appearances, "provider_attempts": attempts, "reported_input_tokens": input,
		"reported_output_tokens": output, "attempts_without_reported_usage": unknownUsage,
		"elapsed_ms": time.Since(started).Milliseconds(), "provider": "deterministic HTTP fixture"}
}

func revalidationReport(t *testing.T, f *ontologyFixture, started time.Time) {
	t.Helper()
	encoded, err := json.Marshal(revalidationMeasurement(t, f, started))
	require.NoError(t, err)
	t.Logf("ontology_revalidation_measurement %s", encoded)
}

func TestOntologyMaintenanceRevalidationMeasurement(t *testing.T) {
	for repeat := range 3 {
		t.Run(fmt.Sprintf("repetition-%d", repeat+1), func(t *testing.T) {
			f := newOrganizationFixture(t)
			sources := []ontology.SourceHandle{
				f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil),
				f.organizationEvidence(t, 1, "Atlas relies on PostgreSQL.", nil),
			}
			canonical := schedulerCanonical(t, f)
			service, calls := maintenanceServiceFixture(t, f, maintenanceSettings(t, f), nil)
			started := time.Now()
			drainMaintenance(t, service)
			original := map[string]string{}
			for _, source := range sources {
				state := revalidationQueue(t, f, source)
				require.Equal(t, "organized", state.Status)
				original[source.ID] = state.AssessmentID
			}
			for round := range 4 {
				if round > 0 {
					topic := testTopic(fmt.Sprintf("atlas-vocabulary-%d", round))
					topic.Definition.Description = "PostgreSQL database storage for Atlas"
					revalidationPublish(t, f, ontology.Change{Record: topic})
					drainMaintenance(t, service)
				}
				changed := 0
				for _, source := range sources {
					state := revalidationQueue(t, f, source)
					require.Equal(t, "organized", state.Status)
					if state.AssessmentID != original[source.ID] {
						changed++
					}
				}
				report := revalidationMeasurement(t, f, started)
				report["fixture"] = "ontology-revalidation-v1"
				report["repetition"], report["vocabulary_round"] = repeat+1, round
				report["provider_calls"], report["changed_fixed_assessments"] = calls(), changed
				report["canonical_before"], report["canonical_after"] = canonical, schedulerCanonical(t, f)
				require.Equal(t, report["canonical_before"], report["canonical_after"])
				encoded, err := json.Marshal(report)
				require.NoError(t, err)
				t.Logf("ontology_revalidation_measurement %s", encoded)
			}
		})
	}
}
