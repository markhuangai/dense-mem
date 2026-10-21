//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspg "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCommunityTopicRejectsPrivateForeignSourcesAndFencesCorrection(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	f.drain(t, 10)
	semantic := knowledge.NewStore(f.appDB, f.rls, knowledge.ConflictRuntimeConfig{})
	private, err := ensurePrivateSpaceForOwner(t, f.adminDB, f.rls, context.Background(), uuid.MustParse(f.teamID), uuid.MustParse(f.ownerA))
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE team_id=?::uuid AND id=?::uuid AND lifecycle_state='active'`,
			f.teamID, private.ID.String()).Row().Scan(&private.Generation)
	}))
	privateIngest, err := semantic.CreateIngestForTest(context.Background(), knowledge.CreateIngestInput{TeamID: f.teamID, OwnerProfileID: f.ownerA, SpaceID: private.ID.String(), SpaceGeneration: private.Generation, IdempotencyKey: uuid.NewString(), RequestHash: "private-fixture", Evidence: []knowledge.EvidenceInput{{Content: "Private topic secret"}}})
	require.NoError(t, err)
	var foreignOwner string
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id::text FROM credentials WHERE team_id=?::uuid LIMIT 1`, f.otherTeam).Row().Scan(&foreignOwner)
	}))
	foreign := createSemanticIngest(t, context.Background(), semantic, f.otherTeam, foreignOwner, "foreign-topic", "Foreign topic secret")
	for _, id := range []string{privateIngest.Evidence[0].FragmentID, foreign.Evidence[0].FragmentID} {
		page, err := f.ontology.ListRecords(context.Background(), f.teamID, "", "", 1)
		require.NoError(t, err)
		handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: id, Version: 1}
		_, err = f.ontology.PublishAutomatic(context.Background(), f.teamID, ontology.Publication{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision, Reason: "isolation regression", Changes: []ontology.Change{{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: f.topics["runtime"]}, Sources: []ontology.SourceDependency{{SourceHandle: handle, Fingerprint: "sha256:" + strings.Repeat("0", 64)}}}}}})
		require.Error(t, err, "shared assignment admitted an inaccessible source")
	}
	insertSearchTestContract(t, f.adminDB, f.rls, "topic-correction", 3, "exact", "")
	correctObject := createSemanticEntity(t, context.Background(), semantic, f.teamID, f.ownerA, "product", "Corrected source object")
	input := knowledge.CorrectRelationshipInput{TeamID: f.teamID, OwnerProfileID: f.ownerA, Action: "submit", RelationshipID: f.sources[0].RelationshipID, ExpectedVersion: f.sources[0].RelationshipVersion,
		Patch: knowledge.RelationshipCorrectionPatch{ObjectEntity: &knowledge.RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}}, Supports: []knowledge.RelationshipCorrectionSupport{{EvidenceID: f.evidenceIDs[0], Start: 0, End: len("Community subject uses Community object 00.")}}, Reason: "topic correction freshness", IdempotencyKey: uuid.NewString()}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(f.actor(f.ownerA), input)
	require.NoError(t, err)
	embeddings := []knowledge.RelationshipCorrectionEmbedding{}
	for _, document := range plan.Documents {
		embeddings = append(embeddings, knowledge.RelationshipCorrectionEmbedding{DocumentHash: document.DocumentHash, Embedding: make([]float32, plan.EmbeddingDimensions), EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions, EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration})
	}
	result, err := semantic.CorrectRelationshipWithEmbeddings(f.actor(f.ownerA), input, embeddings)
	require.NoError(t, err)
	require.Equal(t, "completed", result.ProcessingState)
	records, _, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 2)
	f.drain(t, 10)
	records, _, err = f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20})
	require.NoError(t, err)
	for _, record := range records {
		for _, relationship := range record.Relationships {
			require.NotEqual(t, f.sources[0].RelationshipID, relationship.RelationshipID)
			require.NotEqual(t, result.Correction.SuccessorRelationshipID, relationship.RelationshipID)
		}
	}
}

func TestCommunityTopicWithdrawQuarantineAndGeneration(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	f.drain(t, 10)
	semantic := knowledge.NewStore(f.appDB, f.rls, knowledge.ConflictRuntimeConfig{})
	var support string
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT support_id::text FROM relationship_evidence_supports WHERE team_id=?::uuid AND relationship_id=?::uuid`, f.teamID, f.sources[9].RelationshipID).Row().Scan(&support)
	}))
	_, err := semantic.ApplyRelationshipSupportDecision(f.actor(f.ownerB), knowledge.ApplyRelationshipSupportDecisionInput{
		TeamID: f.teamID, OwnerProfileID: f.ownerB, RelationshipID: f.sources[9].RelationshipID, SupportID: support, Decision: "revoke", Reason: "source withdrawn", IdempotencyKey: uuid.NewString(),
	})
	require.NoError(t, err)
	require.NoError(t, f.rls.WithTeamProfileTx(f.actor(f.ownerA), f.appDB, f.teamID, f.ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO evidence_quarantines(team_id,fragment_id,ingest_id,owner_profile_id,reason,space_id,space_generation)
			SELECT team_id,fragment_id,ingest_id,owner_profile_id,'fixture quarantine',space_id,space_generation FROM evidence_fragments
			WHERE team_id=?::uuid AND fragment_id=?::uuid`, f.teamID, f.evidenceIDs[18]).Error
	}))
	records, _, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, f.topics["runtime"], records[0].LogicalCommunityID)
	f.drain(t, 10)
	records, _, err = f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20})
	require.NoError(t, err)
	require.Len(t, records, 3)
	for _, record := range records {
		for _, relationship := range record.Relationships {
			require.NotEqual(t, f.sources[9].RelationshipID, relationship.RelationshipID)
			require.NotContains(t, relationship.EvidenceIDs, f.evidenceIDs[18])
		}
	}
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND id=?::uuid`, f.teamID, f.space).Error
	}))
	records, _, err = f.store.RecallCommunities(context.Background(), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, records)
}

func TestCommunityTopicForcedRLSAndRollbackBoundary(t *testing.T) {
	f := newTopicProjectionFixture(t)
	require.NoError(t, f.rls.WithTeamTx(f.actor(f.reader), f.appDB, f.teamID, func(tx *gorm.DB) error {
		var enabled bool
		if err := tx.Raw(`SELECT value='true' FROM app_config WHERE key='ONTOLOGY_MAINTENANCE_ENABLED'`).Row().Scan(&enabled); err != nil {
			return err
		}
		require.True(t, enabled)
		var otherSettings int
		if err := tx.Raw(`SELECT count(*) FROM app_config WHERE key<>'ONTOLOGY_MAINTENANCE_ENABLED'`).Row().Scan(&otherSettings); err != nil {
			return err
		}
		require.Zero(t, otherSettings)
		return nil
	}))
	err := f.rls.WithTeamTx(f.actor(f.reader), f.appDB, f.teamID, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE app_config SET value='false' WHERE key='ONTOLOGY_MAINTENANCE_ENABLED'`).Error
	})
	require.ErrorContains(t, err, "row-level security policy")
	f.seedCohort(t)
	f.drain(t, 10)
	before := f.currentIDs(t)
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.appDB, f.otherTeam, func(tx *gorm.DB) error {
		for _, table := range []string{"community_topic_versions", "community_topic_dependencies", "community_topic_work"} {
			var count int
			if err := tx.Raw("SELECT count(*) FROM "+table+" WHERE team_id=?::uuid", f.teamID).Row().Scan(&count); err != nil {
				return err
			}
			require.Zero(t, count)
		}
		return nil
	}))
	migrator := topicMigrationProvider(t, f.adminDB)
	rollbackMigrationsAfterTopicProjection(t, migrator)
	_, err = migrator.ApplyVersion(context.Background(), 20261011125000, false)
	require.ErrorContains(t, err, "ontology community projection history is populated; recover by rolling forward")
	require.Equal(t, before, f.currentIDs(t))
}

func TestCommunityTopicSchedulerRunsWithoutOrganizationBudget(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	config := settings.NewAppConfigService(settingspg.NewAppConfigRepository(f.appDB, f.rls), nil)
	_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyEnabled: "true", domain.AppConfigOntologyModel: "fixture-model"}, "control", "", "")
	require.NoError(t, err)
	_, err = config.UpdateGeneralSettings(context.Background(), map[string]string{domain.AppConfigTimezone: "UTC"}, "control", "", "")
	require.NoError(t, err)
	policy, err := config.OntologyMaintenanceRuntimeConfig(context.Background())
	require.NoError(t, err)
	window, err := f.ontology.EnsureMaintenanceWindow(context.Background(), policy, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, window)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_windows SET charged_input=?,charged_output=? WHERE window_id=?::uuid`, policy.InputTokens, policy.OutputTokens, window.ID).Error
	}))
	maintenance := organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: f.ontology, Config: config, Projections: f.service,
		Organizer: func(model string, accounting assessment.AttemptAccounting) *organization.Service {
			return organization.NewService(f.ontology, assessment.NewProviderWithAccounting(nil, model, assessor.DefaultSemanticAssessmentLimits(), accounting))
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("maintenance did not release on cancellation")
		}
	}
	t.Cleanup(stop)
	go func() { defer close(done); organization.NewMaintenanceScheduler(maintenance, nil).Start(ctx) }()
	require.Eventually(t, func() bool { return len(f.currentIDs(t)) == 3 }, 20*time.Second, 20*time.Millisecond)
	stop()
	status, err := f.ontology.MaintenanceStatus(context.Background(), time.Now())
	require.NoError(t, err)
	require.Equal(t, policy.InputTokens, status.Window.ChargedInput)
	require.Equal(t, policy.OutputTokens, status.Window.ChargedOutput)
}

func TestCommunityTopicMigrationUsesNonSuperuserWithForcedRLS(t *testing.T) {
	f := newTopicProjectionFixture(t)
	ctx := context.Background()
	before := f.canonicalSnapshot(t)
	adminMigrator := topicMigrationProvider(t, f.adminDB)
	rollbackMigrationsAfterTopicProjection(t, adminMigrator)
	_, err := adminMigrator.ApplyVersion(ctx, 20261011125000, false)
	require.NoError(t, err)
	var projectionTable *string
	require.NoError(t, f.adminDB.Raw(`SELECT to_regclass('community_topic_versions')::text`).Row().Scan(&projectionTable))
	require.Nil(t, projectionTable)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf(`GRANT CREATE ON SCHEMA public TO %s`, ledgerTestRole)).Error; err != nil {
			return err
		}
		if err := tx.Exec(fmt.Sprintf(`GRANT REFERENCES,TRIGGER ON ALL TABLES IN SCHEMA public TO %s`, ledgerTestRole)).Error; err != nil {
			return err
		}
		for _, table := range []string{"app_config", "community_records", "session_events", "ontology_maintenance_windows", "ontology_maintenance_runs", "ontology_maintenance_batches"} {
			if err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s OWNER TO %s`, table, ledgerTestRole)).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	var superuser, bypass bool
	require.NoError(t, f.appDB.Raw(`SELECT rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Row().Scan(&superuser, &bypass))
	require.False(t, superuser)
	require.False(t, bypass)
	migrator := topicMigrationProvider(t, f.appDB)
	_, err = migrator.Up(ctx)
	require.NoError(t, err)
	require.Equal(t, before, f.canonicalSnapshot(t))
	require.NoError(t, f.rls.WithTeamTx(f.actor(f.reader), f.appDB, f.teamID, func(tx *gorm.DB) error {
		var enabled bool
		if err := tx.Raw(`SELECT value='true' FROM app_config WHERE key='ONTOLOGY_MAINTENANCE_ENABLED'`).Row().Scan(&enabled); err != nil {
			return err
		}
		require.True(t, enabled, "migration overwrote configured enablement")
		return nil
	}))
}

func topicMigrationProvider(t *testing.T, db *gorm.DB) *goose.Provider {
	t.Helper()
	paths, err := filepath.Glob("../../../migrations/postgres/v2_6/*_ontology_*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	files := fstest.MapFS{}
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		files[filepath.Base(path)] = &fstest.MapFile{Data: contents}
	}
	sqlDB, ok := db.ConnPool.(*sql.DB)
	require.True(t, ok)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, files,
		goose.WithDisableGlobalRegistry(true), goose.WithAllowOutofOrder(true))
	require.NoError(t, err)
	return provider
}

func rollbackMigrationsAfterTopicProjection(t *testing.T, migrator *goose.Provider) {
	t.Helper()
	sources := migrator.ListSources()
	slices.Reverse(sources)
	for _, source := range sources {
		if source.Version <= 20261011125000 {
			break
		}
		_, err := migrator.ApplyVersion(context.Background(), source.Version, false)
		require.NoError(t, err)
	}
}

func TestCommunityTopicMigrationValidationAllowsConcurrentReadsAndWrites(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	f.drain(t, 10)
	script, err := os.ReadFile("../../../migrations/postgres/v2_6/20261011125000_ontology_community_projections.sql")
	require.NoError(t, err)
	up := strings.Split(string(script), "-- +goose Down")[0]
	var validation string
	for _, block := range strings.Split(up, "-- +goose StatementBegin")[1:] {
		block = strings.Split(block, "-- +goose StatementEnd")[0]
		if strings.Contains(block, "ALTER TABLE community_records VALIDATE CONSTRAINT community_records_status_check;") {
			validation = strings.TrimPrefix(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(block), "COMMIT;")), "BEGIN;")
			break
		}
	}
	require.NotEmpty(t, validation)
	// Keep the actual validation phase open to verify its locks allow concurrent application reads and writes.
	tx := f.adminDB.Begin()
	require.NoError(t, tx.Error)
	defer func() { require.NoError(t, tx.Rollback().Error) }()
	require.NoError(t, tx.Exec(validation).Error)
	readCtx, cancelRead := context.WithTimeout(f.actor(f.reader), 2*time.Second)
	defer cancelRead()
	records, _, err := f.store.RecallCommunities(readCtx, CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10})
	require.NoError(t, err, "constraint validation blocked current community reads")
	require.Len(t, records, 3)
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWrite()
	require.NoError(t, f.rls.WithSystemTx(writeCtx, f.appDB, func(writer *gorm.DB) error {
		return writer.Exec(`UPDATE community_records SET updated_at=updated_at WHERE team_id=?::uuid AND status='current'`, f.teamID).Error
	}), "constraint validation blocked community writes")
}
