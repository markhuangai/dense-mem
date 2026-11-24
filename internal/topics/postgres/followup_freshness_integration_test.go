//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"gorm.io/gorm"
)

func TestOntologyAutomaticRetirementPreservesGroupingOverrides(t *testing.T) {
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	t.Setenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS", "1")
	for _, action := range []ontology.OverrideAction{ontology.KeepSeparate, ontology.GroupTogether} {
		t.Run(string(action), func(t *testing.T) {
			f := newHistoricalOntologyFixture(t)
			ctx := context.Background()
			migrator, err := storage.NewMigrator(f.admin)
			require.NoError(t, err)
			require.NoError(t, migrator.RunUpThroughEvidenceOnly(ctx))
			a := f.organizationEvidenceAt(t, 0, "Atlas uses PostgreSQL.", "2026-10-08T12:00:00Z", "")
			b := f.organizationEvidenceAt(t, 0, "Atlas uses PostgreSQL.", "2026-10-08T12:00:00Z", "")
			before := f.canonicalSnapshot(t)
			group := ontology.Record{ID: uuid.NewString(), Kind: ontology.EvidenceGroup, Group: &ontology.Group{Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
			override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: action, Members: group.Group.Members}, Sources: group.Sources}
			seedHistoricalRecords(t, f, "group", group)
			original := seedHistoricalRecords(t, f, "override", override)
			require.NoError(t, migrator.RunUp(ctx))

			page, err := f.store.ListRecords(ctx, f.team, ontology.EvidenceGroup, "", 20)
			require.NoError(t, err)
			require.Len(t, page.Records, 1)
			accepted := page.Records[0]
			require.True(t, accepted.Current)
			require.Equal(t, original[group.ID], accepted.Record)
			retired := accepted.Record
			retired.Retired = true
			result, err := f.store.PublishAutomatic(ctx, f.team, testPublication("retirement", page.Revision, ontology.Change{ExpectedVersion: accepted.Version, Record: retired}))
			require.NoError(t, err)
			head, err := f.store.GetRecord(ctx, f.team, group.ID, 0)
			require.NoError(t, err)
			require.True(t, head.Retired)
			require.Equal(t, accepted.Group, head.Group)
			require.Equal(t, accepted.Sources, head.Sources)

			regroup := group
			regroup.ID = uuid.NewString()
			_, err = f.store.PublishAutomatic(ctx, f.team, testPublication("regroup", result.Revision, ontology.Change{Record: regroup}))
			require.NoError(t, err)
			restored, err := f.store.GetRecord(ctx, f.team, regroup.ID, 0)
			require.NoError(t, err)
			require.True(t, restored.Current)
			require.Equal(t, group.Group, restored.Group)
			for _, record := range []ontology.Record{original[group.ID], original[override.ID]} {
				historical, err := f.store.GetRecord(ctx, f.team, record.ID, record.Version)
				require.NoError(t, err)
				require.Equal(t, record, historical.Record)
			}
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
}

func TestOntologyCurrentReadersDistinguishDependencyAndSourceChanges(t *testing.T) {
	for _, change := range []string{"retired dependency", "missing dependency", "withdrawn source"} {
		t.Run(change, func(t *testing.T) {
			f := newOrganizationFixture(t)
			source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
			topic := testTopic("database")
			assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: source, DefinitionID: topic.ID}, Sources: []ontology.SourceDependency{f.source(t, source)}}
			publication, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("initial", 0, ontology.Change{Record: topic}, ontology.Change{Record: assignment}))
			require.NoError(t, err)
			expected := "dependency_changed"
			switch change {
			case "retired dependency":
				topicView, err := f.store.GetRecord(context.Background(), f.team, topic.ID, 0)
				require.NoError(t, err)
				topicView.Record.Retired = true
				_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("retire", publication.Revision, ontology.Change{ExpectedVersion: topicView.Version, Record: topicView.Record}))
				require.NoError(t, err)
			case "missing dependency":
				require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
					var policy string
					if err := tx.Raw(`SELECT format('CREATE POLICY missing_dependency_fixture ON topic_record_heads AS RESTRICTIVE FOR SELECT TO ontology_app USING (record_id <> %L::uuid)', ?::text)`, topic.ID).Row().Scan(&policy); err != nil {
						return err
					}
					return tx.Exec(policy).Error
				}))
			case "withdrawn source":
				_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{source.ID}, Reason: "withdraw source", IdempotencyKey: "withdraw", RequestHash: testHash("withdraw")})
				require.NoError(t, err)
				expected = "source_changed"
			}
			view, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
			require.NoError(t, err)
			require.False(t, view.Current)
			require.Equal(t, expected, view.StaleReason)
			page, err := f.store.ListRecords(context.Background(), f.team, ontology.AssignmentKind, "", 20)
			require.NoError(t, err)
			require.Len(t, page.Records, 1)
			require.False(t, page.Records[0].Current)
			require.Equal(t, expected, page.Records[0].StaleReason)
		})
	}
}

func TestOntologyGroupingOverridesPreserveAssignmentFreshness(t *testing.T) {
	f := newOrganizationFixture(t)
	a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	b := f.organizationEvidence(t, 1, "Atlas uses Redis.", nil)
	topic, alternate := testTopic("database"), testTopic("alternate-database")
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: a, DefinitionID: topic.ID}, Sources: []ontology.SourceDependency{f.source(t, a)}}
	initial, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("assignment", 0, ontology.Change{Record: topic}, ontology.Change{Record: alternate}, ontology.Change{Record: assignment}))
	require.NoError(t, err)
	before := f.canonicalSnapshot(t)
	previous, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	separation := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.KeepSeparate, Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
	classification := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: alternate.ID, Members: []ontology.SourceHandle{a}}, Sources: assignment.Sources}
	pin := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: topic.ID}}
	for _, override := range []ontology.Record{separation, classification, pin} {
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(string(override.Override.Action), initial.Revision, ontology.Change{Record: override}))
		require.ErrorIs(t, err, ontology.ErrOverride)
		_, err = f.store.GetRecord(context.Background(), f.team, override.ID, 0)
		require.ErrorIs(t, err, ontology.ErrNotFound)
	}
	page, err := f.store.ListRecords(context.Background(), f.team, ontology.AssignmentKind, "", 20)
	require.NoError(t, err)
	require.Equal(t, initial.Revision, page.Revision)
	require.Len(t, page.Records, 1)
	require.True(t, page.Records[0].Current)
	require.Equal(t, previous.Record, page.Records[0].Record)
	topic.Definition.Label = "Renamed database"
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("rename-topic", initial.Revision, ontology.Change{ExpectedVersion: 1, Record: topic}))
	require.NoError(t, err)
	view, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.False(t, view.Current)
	require.Equal(t, "dependency_changed", view.StaleReason)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyMixedSeparationOverrideProtectsEvidenceGroups(t *testing.T) {
	f := newOrganizationFixture(t)
	a := f.organizationEvidenceAt(t, 0, "Atlas uses PostgreSQL.", "2026-10-08T12:00:00Z", "")
	b := f.organizationEvidenceAt(t, 0, "Atlas uses PostgreSQL.", "2026-10-08T12:00:00Z", "")
	unrelated := f.organizationEvidence(t, 1, "Independent context must remain separately classified.", nil)
	before := f.canonicalSnapshot(t)
	override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.KeepSeparate, Members: []ontology.SourceHandle{unrelated, a, b}}, Sources: []ontology.SourceDependency{f.source(t, unrelated), f.source(t, a), f.source(t, b)}}
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("mixed-separation", 0, ontology.Change{Record: override}))
	require.ErrorIs(t, err, ontology.ErrOverride)
	_, err = f.store.GetRecord(context.Background(), f.team, override.ID, 0)
	require.ErrorIs(t, err, ontology.ErrNotFound)
	group := ontology.Record{ID: uuid.NewString(), Kind: ontology.EvidenceGroup, Group: &ontology.Group{Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("equivalent-evidence", 0, ontology.Change{Record: group}))
	require.NoError(t, err)
	stored, err := f.store.GetRecord(context.Background(), f.team, group.ID, 0)
	require.NoError(t, err)
	require.True(t, stored.Current)
	require.ElementsMatch(t, []ontology.SourceHandle{a, b}, stored.Group.Members)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyCrossOwnerSpatialContextRemainsSeparate(t *testing.T) {
	f := newOrganizationFixture(t)
	a := f.organizationEvidenceAt(t, 0, "Atlas launched here today.", "2026-10-08T12:00:00Z", "UTC")
	b := f.organizationEvidenceAt(t, 1, "Atlas launched here today.", "2026-10-08T12:00:00Z", "UTC")
	ctx := context.Background()
	handles := []ontology.SourceHandle{a, b}
	sources, err := f.store.ReadSources(ctx, f.team, handles)
	require.NoError(t, err)
	require.Len(t, sources, 2)
	require.Equal(t, f.owners[0], sources[0].OwnerID)
	require.Equal(t, f.owners[1], sources[1].OwnerID)
	require.NotEqual(t, sources[0].OwnerID, sources[1].OwnerID)
	require.Equal(t, sources[0].State["created_at"], sources[1].State["created_at"])
	for _, source := range sources {
		require.JSONEq(t, `{"timezone":"UTC"}`, source.State["metadata"])
	}
	before := f.canonicalSnapshot(t)
	service, calls := organizationFixtureService(t, f, func(_, _ assessment.Item) bool { return true }, nil)
	result, err := service.Organize(ctx, f.team, ontology.OrganizationInput{OperationKey: "cross-owner-spatial", Sources: handles})
	require.NoError(t, err)
	require.NotNil(t, result.Publication)
	groups, err := f.store.ListRecords(ctx, f.team, ontology.EvidenceGroup, "", 20)
	require.NoError(t, err)
	require.Empty(t, groups.Records)
	assignments, err := f.store.ListRecords(ctx, f.team, ontology.AssignmentKind, "", 20)
	require.NoError(t, err)
	require.Len(t, assignments.Records, 2)
	count := calls.Load()
	replay, err := service.Organize(ctx, f.team, ontology.OrganizationInput{OperationKey: "cross-owner-spatial-replay", Sources: handles})
	require.NoError(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, count, calls.Load())
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyCrossOwnerMultipleRelativeDays(t *testing.T) {
	for _, testCase := range []struct {
		name, right string
		grouped     bool
	}{
		{"matching", "TODAY Atlas expanded after launching yesterday.", true},
		{"missing term", "Atlas launched yesterday.", false},
		{"mismatched term", "Atlas launched yesterday and expanded tomorrow.", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f := newOrganizationFixture(t)
			a := f.organizationEvidenceAt(t, 0, "Atlas launched yesterday and expanded today.", "2026-10-08T12:00:00Z", "UTC")
			b := f.organizationEvidenceAt(t, 1, testCase.right, "2026-10-08T12:00:00Z", "UTC")
			ctx := context.Background()
			handles := []ontology.SourceHandle{a, b}
			sources, err := f.store.ReadSources(ctx, f.team, handles)
			require.NoError(t, err)
			require.Len(t, sources, 2)
			require.Equal(t, f.owners[0], sources[0].OwnerID)
			require.Equal(t, f.owners[1], sources[1].OwnerID)
			require.NotEqual(t, sources[0].OwnerID, sources[1].OwnerID)
			require.Equal(t, sources[0].State["created_at"], sources[1].State["created_at"])
			for _, source := range sources {
				require.JSONEq(t, `{"timezone":"UTC"}`, source.State["metadata"])
			}
			before := f.canonicalSnapshot(t)
			service, calls := organizationFixtureService(t, f, func(_, _ assessment.Item) bool { return true }, nil)
			result, err := service.Organize(ctx, f.team, ontology.OrganizationInput{OperationKey: "multiple-relative-days", Sources: handles})
			require.NoError(t, err)
			require.NotNil(t, result.Publication)
			groups, err := f.store.ListRecords(ctx, f.team, ontology.EvidenceGroup, "", 20)
			require.NoError(t, err)
			if testCase.grouped {
				require.Len(t, groups.Records, 1)
				require.True(t, groups.Records[0].Current)
				require.ElementsMatch(t, handles, groups.Records[0].Group.Members)
			} else {
				require.Empty(t, groups.Records)
			}
			assignments, err := f.store.ListRecords(ctx, f.team, ontology.AssignmentKind, "", 20)
			require.NoError(t, err)
			require.Len(t, assignments.Records, 2)
			count := calls.Load()
			replay, err := service.Organize(ctx, f.team, ontology.OrganizationInput{OperationKey: "multiple-relative-days-replay", Sources: handles})
			require.NoError(t, err)
			require.True(t, replay.Existing)
			require.Equal(t, count, calls.Load())
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
}
