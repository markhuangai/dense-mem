//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypg "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspg "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type topicProjectionFixture struct {
	*communityOwnershipFixture
	ontology   *ontologypg.Store
	service    communityapp.Service
	topics     map[string]string
	space      string
	generation int64
	reader     string
	windowID   string
}

type topicFixtureConfig struct{}

func (topicFixtureConfig) CommunityDetectionRuntimeConfig(context.Context) (domain.CommunityDetectionRuntimeConfig, error) {
	return domain.CommunityDetectionRuntimeConfig{Enabled: true}, nil
}
func (topicFixtureConfig) OntologyMaintenanceRuntimeConfig(context.Context) (domain.OntologyMaintenanceConfig, error) {
	return domain.OntologyMaintenanceConfig{Enabled: true}, nil
}

func newTopicProjectionFixture(t testing.TB) *topicProjectionFixture {
	t.Helper()
	base := newCommunityOwnershipFixture(t)
	f := &topicProjectionFixture{communityOwnershipFixture: base, ontology: ontologypg.NewStore(base.appDB, base.rls), topics: map[string]string{}}
	require.NoError(t, base.rls.WithSystemTx(context.Background(), base.adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, f.teamID).Row().Scan(&f.space, &f.generation)
	}))
	f.reader = createLedgerProfile(t, f.adminDB, f.rls, f.teamID, "topic-reader-c")
	f.store = NewStore(f.appDB, f.rls).WithTopics(f.ontology.NewTopicCatalogReader(), f.ontology.NewTopicMembershipReader(), f.ontology.NewTopicProjectionAdmission(), f.ontology.NewTopicProjectionRelease())
	f.service = communityapp.New(communityapp.Dependencies{Store: f.store, AppConfig: topicFixtureConfig{}})
	f.setMode(t, true)
	config := settings.NewAppConfigService(settingspg.NewAppConfigRepository(f.appDB, f.rls), nil)
	_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyEnabled: "true", domain.AppConfigOntologyModel: "fixture-model", domain.AppConfigOntologyStartTime: "00:00"}, "control", "", "")
	require.NoError(t, err)
	_, err = config.UpdateGeneralSettings(context.Background(), map[string]string{domain.AppConfigTimezone: "UTC"}, "control", "", "")
	require.NoError(t, err)
	policy, err := config.OntologyMaintenanceRuntimeConfig(context.Background())
	require.NoError(t, err)
	window, err := f.ontology.EnsureMaintenanceWindow(context.Background(), policy, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, window)
	f.windowID = window.ID
	return f
}

func (f *topicProjectionFixture) setMode(t testing.TB, enabled bool) {
	t.Helper()
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO app_config(key,value) VALUES('COMMUNITY_DETECTION_ENABLED','true'),
			('ONTOLOGY_MAINTENANCE_ENABLED',?) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, fmt.Sprint(enabled)).Error
	}))
}

func (f *topicProjectionFixture) actor(owner string) context.Context {
	return requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: uuid.MustParse(f.teamID), OwnerID: uuid.MustParse(owner), Role: "member", Grants: []string{"read", "write"},
		AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.MustParse(f.space), Kind: domain.MemorySpaceTeamShared, Generation: f.generation}}})
}

func (f *topicProjectionFixture) publish(t testing.TB, changes ...ontology.Change) {
	t.Helper()
	page, err := f.ontology.ListRecords(context.Background(), f.teamID, "", "", 1)
	require.NoError(t, err)
	_, err = f.ontology.PublishAutomatic(context.Background(), f.teamID, ontology.Publication{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision, Reason: "synthetic topic projection fixture", Changes: changes})
	require.NoError(t, err)
}

func (f *topicProjectionFixture) topic(t testing.TB, key string, handles []ontology.SourceHandle, dependencies ...ontology.SourceDependency) string {
	t.Helper()
	id := uuid.NewString()
	f.publish(t, ontology.Change{Record: ontology.Record{ID: id, Kind: ontology.Topic, Sources: dependencies, Definition: &ontology.Definition{Key: key, Label: key + " tools", Description: "Community snapshot marker"}}})
	f.topics[key] = id
	f.assignTopic(t, id, handles)
	return id
}

func (f *topicProjectionFixture) assignTopic(t testing.TB, id string, handles []ontology.SourceHandle) {
	t.Helper()
	for offset := 0; offset < len(handles); offset += 32 {
		batch := handles[offset:min(offset+32, len(handles))]
		snapshots, err := f.ontology.ReadSources(context.Background(), f.teamID, batch)
		require.NoError(t, err)
		byHandle := map[ontology.SourceHandle]ontology.SourceSnapshot{}
		for _, snapshot := range snapshots {
			byHandle[snapshot.SourceHandle] = snapshot
		}
		changes := []ontology.Change{}
		for _, handle := range batch {
			fingerprint, err := ontology.SourceFingerprint(byHandle[handle])
			require.NoError(t, err)
			changes = append(changes, ontology.Change{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: id},
				Sources: []ontology.SourceDependency{{SourceHandle: handle, Fingerprint: fingerprint}}}})
		}
		f.publish(t, changes...)
	}
}

func (f *topicProjectionFixture) seedCohort(t testing.TB) {
	t.Helper()
	for _, topic := range evalharness.CommunityTopicCohort() {
		handles := []ontology.SourceHandle{}
		for _, index := range topic.SourceIndices {
			if index%2 == 0 {
				handles = append(handles, ontology.SourceHandle{Kind: ontology.RelationshipSource, ID: f.sources[index].RelationshipID, Version: int64(f.sources[index].RelationshipVersion)})
			} else {
				handles = append(handles, ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: f.evidenceIDs[index], Version: 1})
			}
		}
		f.topic(t, topic.Key, handles)
	}
}

func (f *topicProjectionFixture) drain(t testing.TB, maxTurns int) int {
	t.Helper()
	for turn := 0; turn < maxTurns; turn++ {
		progress, err := f.service.RunProjectionTurn(context.Background())
		require.NoError(t, err)
		if !progress {
			return turn
		}
	}
	t.Fatalf("topic projection did not drain within %d turns", maxTurns)
	return 0
}

func (f *topicProjectionFixture) retract(t testing.TB, index int) {
	t.Helper()
	owner := f.ownerA
	if index%2 == 1 {
		owner = f.ownerB
	}
	_, err := knowledge.NewStore(f.appDB, f.rls, knowledge.ConflictRuntimeConfig{}).RetractEvidence(f.actor(owner), knowledge.RetractEvidenceInput{
		TeamID: f.teamID, OwnerProfileID: owner, EvidenceIDs: []string{f.evidenceIDs[index]}, Reason: "topic freshness regression", IdempotencyKey: uuid.NewString(), RequestHash: "sha256:" + fmt.Sprintf("%064d", index+1)})
	require.NoError(t, err)
}

func (f *topicProjectionFixture) currentIDs(t testing.TB) map[string]string {
	t.Helper()
	result := map[string]string{}
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`SELECT topic_id::text,community_id::text FROM community_records WHERE team_id=?::uuid AND status='current' AND topic_id IS NOT NULL`, f.teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var topic, id string
			if err := rows.Scan(&topic, &id); err != nil {
				return err
			}
			result[topic] = id
		}
		return rows.Err()
	}))
	return result
}

func (f *topicProjectionFixture) expireLease(t testing.TB, workID string) {
	t.Helper()
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE community_topic_work SET lease_until=? WHERE community_id=?::uuid`, time.Now().Add(-time.Minute), workID).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE ontology_maintenance_teams AS state SET lease_until=? FROM community_topic_work AS work WHERE work.community_id=?::uuid AND state.lease_token=work.lease_token`, time.Now().Add(-time.Minute), workID).Error
	}))
}

func (f *topicProjectionFixture) canonicalSnapshot(t testing.TB) map[string]string {
	t.Helper()
	result := map[string]string{}
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		for _, table := range []string{"entity_records", "entity_names", "value_records", "knowledge_ingests", "evidence_fragments", "relationship_records", "relationship_evidence_supports", "relationship_support_decision_events", "evidence_lifecycle_events"} {
			var digest string
			if err := tx.Raw("SELECT md5(COALESCE(string_agg(body,',' ORDER BY body),'')) FROM (SELECT to_jsonb(record)::text AS body FROM "+table+" AS record WHERE team_id=?::uuid) AS rows", f.teamID).Row().Scan(&digest); err != nil {
				return err
			}
			result[table] = digest
		}
		return nil
	}))
	return result
}
