//go:build integration

package postgres

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestOntologyRefinementRefreshMigration(t *testing.T) {
	f := newOrganizationFixture(t)
	source := f.organizationEvidence(t, 0, "Preserve this historical receipt and current assignment.", nil)
	service, _ := organizationFixtureService(t, f, nil, nil)
	receipt, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "preserved-v2-receipt", Sources: []ontology.SourceHandle{source}})
	require.NoError(t, err)
	historical, deleted, archived := maintenanceOtherTeam(t, f), maintenanceOtherTeam(t, f), maintenanceOtherTeam(t, f)
	for _, x := range []*ontologyFixture{f, historical, deleted, archived} {
		for _, kind := range []string{"entity", "predicate", "evidence", "relationship"} {
			for _, status := range []string{"organized", "pending", "failed", "ambiguous", "unavailable"} {
				for _, eligible := range []bool{true, false} {
					require.NoError(t, f.admin.Exec(`INSERT INTO ontology_maintenance_sources
					 (team_id,shared_space_id,space_generation,source_kind,source_id,source_version,eligible,status,reason,revision)
					 VALUES(?::uuid,?::uuid,?,?,?,1,?,?, 'retained_reason',7)`, x.team, x.space, x.generation, kind, uuid.NewString(), eligible, status).Error)
				}
			}
		}
	}
	require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_sources SET assessment_id=?::uuid`, receipt.AssessmentID).Error)
	require.NoError(t, f.admin.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE id=?::uuid`, historical.space).Error)
	require.NoError(t, f.admin.Exec(`UPDATE teams SET deleted_at=clock_timestamp() WHERE id=?::uuid`, deleted.team).Error)
	require.NoError(t, f.admin.Exec(`UPDATE teams SET status='archived' WHERE id=?::uuid`, archived.team).Error)
	// Queue scope itself excludes private spaces, independent of the refresh filter.
	require.Error(t, f.admin.Exec(`INSERT INTO ontology_maintenance_sources(team_id,shared_space_id,space_generation,source_kind,source_id,source_version,eligible,status)
	 VALUES(?::uuid,?::uuid,?,'entity',?,1,true,'organized')`, f.team, f.credentials[0].MemorySpaceID, f.credentials[0].MemorySpaceGeneration, uuid.NewString()).Error)
	retained := func() map[string]string {
		result := f.canonicalSnapshot(t)
		for _, table := range []string{"ontology_assessments", "ontology_record_heads", "ontology_record_revisions", "ontology_publications"} {
			var body string
			require.NoError(t, f.admin.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY to_jsonb(row)::text)::text,'[]') FROM "+table+" AS row").Row().Scan(&body))
			result[table] = body
		}
		return result
	}
	queue := func() string {
		var body string
		require.NoError(t, f.admin.Raw(`SELECT jsonb_agg(to_jsonb(source) ORDER BY source_id)::text FROM ontology_maintenance_sources AS source`).Row().Scan(&body))
		return body
	}
	before := retained()
	require.NoError(t, f.admin.Exec("GRANT CREATE ON SCHEMA public TO ontology_app").Error)
	require.NoError(t, f.admin.Exec("ALTER TABLE teams OWNER TO ontology_app").Error)
	pool, err := f.app.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	name := "20261108010000_ontology_refinement_refresh.sql"
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "postgres", "v2_6", name))
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, pool, fstest.MapFS{name: &fstest.MapFile{Data: body}},
		goose.WithTableName("ontology_refinement_refresh_probe"), goose.WithDisableGlobalRegistry(true))
	require.NoError(t, err)
	locked := f.admin.Begin()
	require.NoError(t, locked.Error)
	require.NoError(t, locked.Exec(`SELECT 1 FROM ontology_maintenance_sources WHERE team_id=?::uuid AND eligible AND status='organized' AND source_kind='entity' FOR UPDATE`, f.team).Error)
	queueBefore := queue()
	started := time.Now()
	_, err = provider.Up(context.Background())
	require.ErrorContains(t, err, "lock timeout")
	require.Less(t, time.Since(started), 8*time.Second)
	require.NoError(t, locked.Rollback().Error)
	require.Equal(t, queueBefore, queue(), "a lock timeout must roll back the entire refresh")
	_, err = provider.Up(context.Background())
	require.NoError(t, err)
	var refreshed, changedWrongly int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND revision=8
	 AND status='pending' AND reason='organization_policy_v2' AND eligible AND source_kind IN ('entity','predicate')`, f.team).Row().Scan(&refreshed))
	require.Equal(t, 2, refreshed)
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE NOT
	 (team_id=?::uuid AND eligible AND source_kind IN ('entity','predicate') AND revision=8)
	 AND (revision<>7 OR reason<>'retained_reason')`, f.team).Row().Scan(&changedWrongly))
	require.Zero(t, changedWrongly)
	require.Equal(t, before, retained())
	after := queue()
	// Goose restart must not dispatch the one-time refresh again.
	_, err = provider.Up(context.Background())
	require.NoError(t, err)
	require.Equal(t, after, queue())
	_, err = provider.Down(context.Background())
	require.ErrorContains(t, err, "forward-only")
	require.Equal(t, after, queue())
	require.Equal(t, before, retained())
	var policies int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM pg_policies WHERE policyname='ontology_refinement_refresh_teams_read'`).Row().Scan(&policies))
	require.Zero(t, policies)
	var timeout string
	require.NoError(t, f.app.Raw("SHOW lock_timeout").Row().Scan(&timeout))
	require.Equal(t, "0", timeout)
}
