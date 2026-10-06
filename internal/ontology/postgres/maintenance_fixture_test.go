package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspostgres "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/stretchr/testify/require"
)

func maintenanceOtherTeam(t *testing.T, parent *ontologyFixture) *ontologyFixture {
	t.Helper()
	f := &ontologyFixture{admin: parent.admin, app: parent.app, rls: parent.rls, store: parent.store, knowledge: parent.knowledge}
	team := &domain.Team{Name: "maintenance-fair-" + uuid.NewString()}
	require.NoError(t, access.NewTeamRepository(f.admin, f.rls).Create(context.Background(), team))
	f.team = team.ID.String()
	credential := &domain.Credential{ID: uuid.New(), TeamID: team.ID, Name: "actor-c", KeyHash: "synthetic-" + uuid.NewString(), KeyPrefix: uuid.NewString()[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}
	require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), credential))
	f.owners = []string{credential.ID.String()}
	require.NoError(t, f.admin.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, f.team).Row().Scan(&f.space, &f.generation))
	return f
}

func maintenanceEntity(t *testing.T, f *ontologyFixture, name string) ontology.SourceHandle {
	t.Helper()
	entity, err := f.knowledge.CreateEntity(context.Background(), knowledge.CreateEntityInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityKind: "project", CanonicalName: name})
	require.NoError(t, err)
	return ontology.SourceHandle{Kind: ontology.EntitySource, ID: entity.EntityID, Version: int64(entity.Version)}
}

func maintenanceSettings(t *testing.T, f *ontologyFixture) *settings.AppConfigServiceImpl {
	t.Helper()
	service := settings.NewAppConfigService(settingspostgres.NewAppConfigRepository(f.app, f.rls), nil)
	_, err := service.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyEnabled: "true", domain.AppConfigOntologyModel: "fixture-model"}, "control", "", "")
	require.NoError(t, err)
	_, err = service.UpdateGeneralSettings(context.Background(), map[string]string{domain.AppConfigTimezone: "UTC"}, "control", "", "")
	require.NoError(t, err)
	return service
}

func maintenanceWindow(t *testing.T, f *ontologyFixture, config *settings.AppConfigServiceImpl) *ontology.MaintenanceWindow {
	t.Helper()
	policy, err := config.OntologyMaintenanceRuntimeConfig(context.Background())
	require.NoError(t, err)
	window, err := f.store.EnsureMaintenanceWindow(context.Background(), policy, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, window)
	return window
}

func maintenanceClaim(t *testing.T, f *ontologyFixture, window *ontology.MaintenanceWindow) (*ontology.MaintenanceTurn, *ontology.MaintenanceClaim) {
	t.Helper()
	turn, err := f.store.ClaimMaintenanceTurn(context.Background(), window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	for range 8 {
		require.NoError(t, f.store.DiscoverMaintenance(context.Background(), *turn, ontology.MaintenancePageSize))
	}
	claim, err := f.store.ClaimMaintenanceBatch(context.Background(), *turn, window.ID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, claim)
	return turn, claim
}
