//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	topiccontract "github.com/markhuangai/dense-mem/internal/topics/contract"
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
	b := f.organizationEvidence(t, 0, "Exact storage Evidence A. café 🧭", nil)
	otherOwner := f.organizationEvidence(t, 1, "Exact storage Evidence B. 🧭", nil)
	topic := testTopic("feature-storage")
	assignment := func(source topiccontract.SourceHandle) topiccontract.Record {
		return topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.AssignmentKind, Assignment: &topiccontract.Assignment{Source: source, DefinitionID: topic.ID}, Sources: []topiccontract.SourceDependency{f.source(t, source)}}
	}
	left, right, peer := assignment(a), assignment(b), assignment(otherOwner)
	group := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.EvidenceGroup, Group: &topiccontract.Group{Members: []topiccontract.SourceHandle{a, b}}, Sources: []topiccontract.SourceDependency{f.source(t, a), f.source(t, b)}}
	seedHistoricalRecords(t, f, "all-features", topic, left, right, peer, group)
	pin := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.OverrideKind, Override: &topiccontract.Override{Action: topiccontract.PinDefinition, TargetID: topic.ID}}
	original := seedHistoricalRecords(t, f, "pin", pin)
	originalReceipt := seedHistoricalAssessment(t, f, a)
	manager := managementService(f)
	input := managementInput(2,
		topiccontract.ManagementEdit{Action: "define", Kind: topiccontract.Topic, Definition: &topiccontract.Definition{Key: "draft-feature", Label: "Exact draft label"}},
		topiccontract.ManagementEdit{Action: "group_together", Members: []topiccontract.SourceHandle{a, b}})
	draft := topiccontract.ManagementDraft{ID: uuid.NewString(), Version: 1, State: "open", BaseRevision: input.ExpectedRevision, Reason: input.Reason, Edits: input.Edits, UpdatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	hash, err := topiccontract.ManagementRequestHash("save", "", input)
	require.NoError(t, err)
	draftTopic := testTopic("draft-feature")
	draftTopic.Definition = input.Edits[0].Definition
	draftGroup := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.OverrideKind, Override: &topiccontract.Override{Action: topiccontract.GroupTogether, Members: []topiccontract.SourceHandle{a, b}}}
	body, err := json.Marshal(storedManagementDraft{Draft: draft, Prepared: testPublication(input.OperationKey, input.ExpectedRevision, topiccontract.Change{Record: draftTopic}, topiccontract.Change{Record: draftGroup})})
	require.NoError(t, err)
	require.NoError(t, f.rls.WithTeamProfileTx(f.actor(0, "manager"), f.app, f.team, f.owners[0], func(tx *gorm.DB) error {
		var stored bool
		err := tx.Raw(`SELECT dense_mem_persist_ontology_draft(?,?,?,?,?,?,0,?::jsonb)`, f.team, f.space, f.generation, f.owners[0], input.OperationKey, hash, string(body)).Row().Scan(&stored)
		if err != nil {
			return err
		}
		require.True(t, stored)
		return nil
	}))
	before := f.canonicalSnapshot(t)
	var holds string
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO private_memory_legal_holds(team_id,space_id,reason_code,actor_class) SELECT team_id,id,'feature-storage-fixture','control' FROM memory_spaces WHERE owner_membership_id=?::uuid`, f.credentials[0].MembershipID).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY id)::text,'[]') FROM private_memory_legal_holds row`).Row().Scan(&holds)
	}))
	ids := []string{topic.ID, left.ID, right.ID, peer.ID, group.ID, pin.ID}
	historical := true
	readTopics := func() topiccontract.TopicReadResult {
		t.Helper()
		var result topiccontract.TopicReadResult
		ctx := f.actor(2, "member")
		if historical {
			require.NoError(t, f.rls.WithTeamTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
				if err := tx.Exec(`CREATE TEMP VIEW topic_record_heads WITH (security_invoker=true) AS
					SELECT h.*, (r.body#>>'{assignment,source,id}')::uuid AS evidence_id, (r.body#>>'{assignment,definition_id}')::uuid AS topic_id
					FROM ontology_record_heads h JOIN ontology_revision_records r USING(team_id,shared_space_id,space_generation,record_id,version);
					CREATE TEMP VIEW topic_revision_records WITH (security_invoker=true) AS SELECT * FROM ontology_revision_records;
					CREATE TEMP VIEW topic_source_dependencies WITH (security_invoker=true) AS SELECT * FROM ontology_source_dependencies;`).Error; err != nil {
					return err
				}
				var err error
				result, err = f.store.ReadEvidenceTopics(ctx, tx, f.team, topiccontract.TopicReadInput{TopicIDs: []string{topic.ID}})
				if err != nil {
					return err
				}
				return tx.Exec("DROP VIEW pg_temp.topic_record_heads, pg_temp.topic_revision_records, pg_temp.topic_source_dependencies").Error
			}))
		} else {
			require.NoError(t, f.rls.WithTeamReadOnlyRepeatableTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
				var err error
				result, err = f.store.ReadEvidenceTopics(ctx, tx, f.team, topiccontract.TopicReadInput{TopicIDs: []string{topic.ID}})
				return err
			}))
		}
		return result
	}
	topics := readTopics()
	require.Len(t, topics.Topics, 1)
	require.Len(t, topics.Topics[0].EvidenceIDs, 3)
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
	historical = false
	organizer, calls := organizationFixtureServiceWithIdentity(t, f, nil, nil, "historical-fixture-model", assessor.DefaultSemanticAssessmentLimits())
	recalled, err := organizer.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: originalReceipt.OperationKey, Sources: []topiccontract.SourceHandle{a}})
	require.NoError(t, err)
	require.True(t, recalled.Existing)
	require.Equal(t, originalReceipt.ID, recalled.AssessmentID)
	require.Equal(t, originalReceipt.Result.Outcomes, recalled.Outcomes)
	require.Zero(t, calls.Load())

	for _, id := range ids {
		actual, err := f.store.GetRecord(f.actor(2, "member"), f.team, id, 0)
		require.NoError(t, err)
		require.Equal(t, original[id], actual.Record)
	}
	actualDraft, err := manager.Draft(f.actor(0, "manager"), draft.ID)
	require.NoError(t, err)
	require.Equal(t, draft, actualDraft)
	replay, err := manager.SaveDraft(f.actor(0, "manager"), "", input)
	require.NoError(t, err)
	require.Equal(t, draft, replay)
	for _, owner := range []int{1, 2} {
		_, err = manager.Draft(f.actor(owner, "manager"), draft.ID)
		require.ErrorIs(t, err, topiccontract.ErrNotFound)
	}
	measure("after")
	catalogPage, err := manager.Records(f.actor(0, "manager"), topiccontract.Topic, "", 20)
	require.NoError(t, err)
	renamed := *topic.Definition
	renamed.Label = "Editable after historic pin"
	edit, err := manager.SaveDraft(f.actor(0, "manager"), "", managementInput(catalogPage.Revision, topiccontract.ManagementEdit{Action: "define", RecordID: topic.ID, ExpectedVersion: 1, Kind: topiccontract.Topic, Definition: &renamed}))
	require.NoError(t, err)
	_, err = manager.CommandDraft(f.actor(0, "manager"), edit.ID, "publish", managementCommand(edit))
	require.NoError(t, err)
	retainedPin, err := manager.Record(f.actor(0, "manager"), pin.ID, 1)
	require.NoError(t, err)
	require.Equal(t, original[pin.ID], retainedPin.Record)

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
		return tx.Raw(`SELECT count(*) FROM topic_revision_payloads WHERE record_id=?::uuid`, topic.ID).Row().Scan(&visible)
	}))
	require.Zero(t, visible)
	var privateVisible int
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM topic_revision_payloads payload JOIN memory_spaces space ON space.id=payload.shared_space_id WHERE space.kind<>'team_shared'`).Row().Scan(&privateVisible)
	}))
	require.Zero(t, privateVisible)
}
