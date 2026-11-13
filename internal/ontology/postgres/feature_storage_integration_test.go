//go:build integration

package postgres

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFeatureStoragePostgresPreservesOrganizationDraftsAndIsolation(t *testing.T) {
	f := newHistoricalOntologyFixture(t)
	migrator, err := storage.NewMigrator(f.admin)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUpThroughEvidenceOnly(context.Background()))
	third := &domain.Credential{ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "third-owner", KeyHash: "synthetic-third", KeyPrefix: uuid.NewString()[:20], KeySuffix: "test", Scopes: []string{"read", "write"}}
	require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), third))
	f.credentials = append(f.credentials, third)
	f.owners = append(f.owners, third.ID.String())
	a := f.organizationEvidence(t, 0, "Exact storage Evidence A. café 🧭", nil)
	b := f.organizationEvidence(t, 1, "Exact storage Evidence B. 🧭", nil)
	topic := testTopic("feature-storage")
	assignment := func(source ontology.SourceHandle) ontology.Record {
		return ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: source, DefinitionID: topic.ID}, Sources: []ontology.SourceDependency{f.source(t, source)}}
	}
	left, right := assignment(a), assignment(b)
	group := ontology.Record{ID: uuid.NewString(), Kind: ontology.EvidenceGroup, Group: &ontology.Group{Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
	publication, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("all-features", 0, ontology.Change{Record: topic}, ontology.Change{Record: left}, ontology.Change{Record: right}, ontology.Change{Record: group}))
	require.NoError(t, err)
	pin := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: topic.ID}}
	publication, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("pin", publication.Revision, ontology.Change{Record: pin}))
	require.NoError(t, err)
	manager := managementService(f)
	input := managementInput(publication.Revision,
		ontology.ManagementEdit{Action: "define", Kind: ontology.Topic, Definition: &ontology.Definition{Key: "draft-feature", Label: "Exact draft label"}},
		ontology.ManagementEdit{Action: "group_together", Members: []ontology.SourceHandle{a, b}})
	draft, err := manager.SaveDraft(f.actor(0, "manager"), "", input)
	require.NoError(t, err)
	before := f.canonicalSnapshot(t)
	var holds string
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO private_memory_legal_holds(team_id,space_id,reason_code,actor_class) SELECT team_id,id,'feature-storage-fixture','control' FROM memory_spaces WHERE owner_membership_id=?::uuid`, f.credentials[0].MembershipID).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY id)::text,'[]') FROM private_memory_legal_holds row`).Row().Scan(&holds)
	}))
	ids := []string{topic.ID, left.ID, right.ID, group.ID, pin.ID}
	views := make([]ontology.RecordView, len(ids))
	for index, id := range ids {
		views[index], err = f.store.GetRecord(f.actor(2, "member"), f.team, id, 0)
		require.NoError(t, err)
	}
	readTopics := func() ontology.TopicReadResult {
		t.Helper()
		var result ontology.TopicReadResult
		require.NoError(t, f.rls.WithTeamReadOnlyRepeatableTx(f.actor(2, "member"), f.app, f.team, func(tx *gorm.DB) error {
			var err error
			result, err = f.store.ReadEvidenceTopics(f.actor(2, "member"), tx, f.team, ontology.TopicReadInput{TopicIDs: []string{topic.ID}})
			return err
		}))
		return result
	}
	topics := readTopics()
	require.Len(t, topics.Topics, 1)
	require.Len(t, topics.Topics[0].EvidenceIDs, 2)
	measure := func(label string) {
		t.Helper()
		samples := make([]time.Duration, 20)
		for index := range samples {
			start := time.Now()
			require.Equal(t, topics, readTopics())
			samples[index] = time.Since(start)
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		t.Logf("feature_storage_topic_fixture phase=%s samples=20 p50_us=%d p95_us=%d", label, samples[9].Microseconds(), samples[18].Microseconds())
	}
	measure("before")
	require.NoError(t, migrator.RunUp(context.Background()))
	require.NoError(t, f.admin.Exec(`GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO ontology_app`).Error)
	for index, id := range ids {
		actual, err := f.store.GetRecord(f.actor(2, "member"), f.team, id, 0)
		require.NoError(t, err)
		require.Equal(t, views[index], actual)
	}
	actualDraft, err := manager.GetDraft(f.actor(0, "manager"), draft.ID)
	require.NoError(t, err)
	require.Equal(t, draft, actualDraft)
	replay, err := manager.SaveDraft(f.actor(0, "manager"), "", input)
	require.NoError(t, err)
	require.Equal(t, draft, replay)
	for _, owner := range []int{1, 2} {
		_, err = manager.GetDraft(f.actor(owner, "manager"), draft.ID)
		require.ErrorIs(t, err, ontology.ErrNotFound)
	}
	measure("after")
	require.Equal(t, before, f.canonicalSnapshot(t))
	var retained string
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY id)::text,'[]') FROM private_memory_legal_holds row`).Row().Scan(&retained)
	}))
	require.JSONEq(t, holds, retained)
	other := &domain.Team{Name: "foreign-feature-team"}
	require.NoError(t, access.NewTeamRepository(f.admin, f.rls).Create(context.Background(), other))
	var visible int
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, other.ID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_revision_payloads WHERE record_id=?::uuid`, topic.ID).Row().Scan(&visible)
	}))
	require.Zero(t, visible)
	var privateVisible int
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_revision_payloads payload JOIN memory_spaces space ON space.id=payload.shared_space_id WHERE space.kind<>'team_shared'`).Row().Scan(&privateVisible)
	}))
	require.Zero(t, privateVisible)
}
