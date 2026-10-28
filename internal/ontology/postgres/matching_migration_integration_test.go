//go:build integration

package postgres

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMatchingRefreshMigration(t *testing.T) {
	f := newOrganizationFixture(t)
	other := maintenanceOtherTeam(t, f)
	historical := maintenanceOtherTeam(t, f)
	deleted := maintenanceOtherTeam(t, f)
	active, retired := testTopic("refresh-active"), testTopic("refresh-retired")
	foreign, old, inactive := testTopic("refresh-foreign"), testTopic("refresh-old"), testTopic("refresh-deleted")
	revalidationPublish(t, f, ontology.Change{Record: active}, ontology.Change{Record: retired})
	retired.Retired = true
	revalidationPublish(t, f, ontology.Change{ExpectedVersion: 1, Record: retired})
	revalidationPublish(t, other, ontology.Change{Record: foreign})
	revalidationPublish(t, historical, ontology.Change{Record: old})
	revalidationPublish(t, deleted, ontology.Change{Record: inactive})
	require.NoError(t, f.admin.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE id=?::uuid`, historical.space).Error)
	require.NoError(t, f.admin.Exec(`UPDATE teams SET deleted_at=clock_timestamp() WHERE id=?::uuid`, deleted.team).Error)
	source := f.organizationEvidence(t, 0, "refresh-active", nil)
	service, _ := organizationFixtureService(t, f, nil, nil)
	_, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "refresh-preserved-receipt", Sources: []ontology.SourceHandle{source}})
	require.NoError(t, err)
	require.NoError(t, f.admin.Exec(`DELETE FROM ontology_maintenance_markers`).Error)
	require.NoError(t, f.admin.Exec(`INSERT INTO ontology_maintenance_teams(team_id,shared_space_id,space_generation,discovery_kind) VALUES(?::uuid,?::uuid,?,4)`, f.team, f.space, f.generation).Error)
	require.NoError(t, f.admin.Exec(`SELECT dense_mem_enqueue_ontology_marker(?::uuid,?::uuid,?,'ontology_record_heads',?,'definition',?)`, f.team, f.space, f.generation, active.ID, active.ID).Error)
	require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_markers SET cursor='evidence:partially-consumed' WHERE team_id=?::uuid AND anchor_id=?`, f.team, active.ID).Error)
	var oldSequence int64
	require.NoError(t, f.admin.Raw(`SELECT marker_sequence FROM ontology_maintenance_markers WHERE team_id=?::uuid AND anchor_id=?`, f.team, active.ID).Row().Scan(&oldSequence))
	require.NoError(t, f.admin.Exec(`SELECT dense_mem_enqueue_ontology_marker(?::uuid,?::uuid,?,'source_dependency','retained-notification','definition',?)`, f.team, f.space, f.generation, source.ID).Error)
	preserved := func() map[string]string {
		result := f.canonicalSnapshot(t)
		for _, table := range []string{"ontology_assessments", "ontology_record_heads", "ontology_record_revisions", "ontology_publications", "app_config"} {
			var value string
			require.NoError(t, f.admin.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY to_jsonb(row)::text)::text,'[]') FROM "+table+" AS row").Row().Scan(&value))
			result[table] = value
		}
		return result
	}
	before := preserved()
	require.NoError(t, f.admin.Exec("GRANT CREATE ON SCHEMA public TO ontology_app").Error)
	require.NoError(t, f.admin.Exec("ALTER TABLE teams OWNER TO ontology_app").Error)
	pool, err := f.app.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	var restricted bool
	require.NoError(t, f.app.Raw(`SELECT NOT (rolsuper OR rolbypassrls) FROM pg_roles WHERE rolname=current_user`).Row().Scan(&restricted))
	require.True(t, restricted)
	assertMigrationTeamScope := func() {
		require.NoError(t, f.rls.WithSystemTx(context.Background(), f.app, func(tx *gorm.DB) error {
			if err := tx.Exec(`SELECT set_config('app.tx_mode','migration',true)`).Error; err != nil {
				return err
			}
			var visible int
			if err := tx.Raw("SELECT count(*) FROM teams").Row().Scan(&visible); err != nil {
				return err
			}
			require.Zero(t, visible, "migration-only team access must not persist outside the refresh")
			return nil
		}))
	}
	assertMigrationTeamScope()
	name := "20261028010000_ontology_matching_refresh.sql"
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "postgres", "v2_6", name))
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, pool, fstest.MapFS{name: &fstest.MapFile{Data: body}},
		goose.WithTableName("ontology_matching_refresh_probe"), goose.WithDisableGlobalRegistry(true))
	require.NoError(t, err)
	for _, direction := range []string{"up", "down", "up"} {
		t.Run(direction, func(t *testing.T) {
			require.NoError(t, f.app.Exec("RESET lock_timeout").Error)
			if direction == "down" {
				_, err = provider.Down(context.Background())
			} else {
				_, err = provider.Up(context.Background())
			}
			require.NoError(t, err)
			assertMigrationTeamScope()
			require.Equal(t, before, preserved(), "refresh must not rewrite accepted data or history")
			var ids []string
			require.NoError(t, f.admin.Raw(`SELECT anchor_id FROM ontology_maintenance_markers WHERE anchor_kind='ontology_record_heads' ORDER BY anchor_id`).Scan(&ids).Error)
			require.ElementsMatch(t, []string{active.ID, foreign.ID}, ids)
			var sequence int64
			var cursor string
			require.NoError(t, f.admin.Raw(`SELECT marker_sequence,cursor FROM ontology_maintenance_markers WHERE team_id=?::uuid AND anchor_id=?`, f.team, active.ID).Row().Scan(&sequence, &cursor))
			require.Greater(t, sequence, oldSequence)
			require.Empty(t, cursor)
			var retained int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_markers WHERE anchor_id='retained-notification'`).Row().Scan(&retained))
			require.Equal(t, 1, retained)
			var timeout string
			require.NoError(t, f.app.Raw("SHOW lock_timeout").Row().Scan(&timeout))
			require.Equal(t, "0", timeout)
			f.store = NewStore(f.app, f.rls)
			status, err := f.store.TeamOntologyStatus(f.actor(0, "manager"), f.team)
			require.NoError(t, err)
			require.Greater(t, status.PendingMarkers, 0)
			require.False(t, status.DiscoveryComplete)
			require.False(t, status.CoverageComplete)
			require.NoError(t, f.admin.Exec(`DELETE FROM ontology_maintenance_markers WHERE anchor_kind='ontology_record_heads'`).Error)
		})
	}
}
