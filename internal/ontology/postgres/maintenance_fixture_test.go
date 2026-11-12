package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspostgres "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
	f.credentials = []*domain.Credential{credential}
	require.NoError(t, f.admin.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, f.team).Row().Scan(&f.space, &f.generation))
	return f
}

func maintenanceEvidence(t *testing.T, f *ontologyFixture, text string) ontology.SourceHandle {
	t.Helper()
	return f.organizationEvidence(t, 0, text, nil)
}

func maintenanceSettings(t *testing.T, f *ontologyFixture) *settings.AppConfigServiceImpl {
	t.Helper()
	service := settings.NewAppConfigService(settingspostgres.NewAppConfigRepository(f.app, f.rls), nil)
	_, err := service.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyEnabled: "true", domain.AppConfigOntologyModel: "fixture-model", domain.AppConfigOntologyContextWindow: "300000"}, "control", "", "")
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
		require.NoError(t, f.store.discoverMaintenanceError(context.Background(), *turn, ontology.MaintenancePageSize))
	}
	claim, err := f.store.ClaimMaintenanceBatch(context.Background(), *turn, window.ID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, claim)
	return turn, claim
}

func (s *Store) discoverMaintenanceError(ctx context.Context, turn ontology.MaintenanceTurn, limit int) error {
	_, err := s.DiscoverMaintenance(ctx, turn, limit)
	return err
}

func appendMaintenanceOccurrence(tx *gorm.DB, f *ontologyFixture, source ontology.SourceHandle) error {
	ingestID := uuid.NewString()
	if err := tx.Exec(`INSERT INTO knowledge_ingests
 SELECT (jsonb_populate_record(NULL::knowledge_ingests,to_jsonb(ingest)||jsonb_build_object('ingest_id',?::uuid,'idempotency_key',?::text))).*
 FROM knowledge_ingests AS ingest JOIN evidence_fragments AS fragment ON fragment.team_id=ingest.team_id AND fragment.ingest_id=ingest.ingest_id
 WHERE fragment.team_id=?::uuid AND fragment.fragment_id=?::uuid`, ingestID, "maintenance:"+ingestID, f.team, source.ID).Error; err != nil {
		return err
	}
	return tx.Exec(`INSERT INTO evidence_occurrences
 SELECT (jsonb_populate_record(NULL::evidence_occurrences,to_jsonb(occurrence)||jsonb_build_object('occurrence_id',?::uuid,'ingest_id',?::uuid,'created_at',clock_timestamp()))).*
 FROM evidence_occurrences AS occurrence WHERE team_id=?::uuid AND canonical_fragment_id=?::uuid LIMIT 1`, uuid.NewString(), ingestID, f.team, source.ID).Error
}
