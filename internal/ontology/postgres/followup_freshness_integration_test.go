//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyAutomaticRetirementPreservesGroupingOverrides(t *testing.T) {
	for _, action := range []ontology.OverrideAction{ontology.KeepSeparate, ontology.GroupTogether} {
		t.Run(string(action), func(t *testing.T) {
			f := newOrganizationFixture(t)
			a := f.organizationEvidenceAt(t, 0, "Atlas uses PostgreSQL.", "2026-10-08T12:00:00Z", "")
			b := f.organizationEvidenceAt(t, 0, "Atlas uses PostgreSQL.", "2026-10-08T12:00:00Z", "")
			before := f.canonicalSnapshot(t)
			group := ontology.Record{ID: uuid.NewString(), Kind: ontology.EvidenceGroup, Group: &ontology.Group{Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
			original, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("group", 0, ontology.Change{Record: group}))
			require.NoError(t, err)
			accepted, err := f.store.GetRecord(context.Background(), f.team, group.ID, 0)
			require.NoError(t, err)
			override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: action, Members: group.Group.Members}, Sources: group.Sources}
			correction, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("override", original.Revision, ontology.Change{Record: override}))
			require.NoError(t, err)
			retired := accepted.Record
			retired.Retired = true
			result, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("retirement", correction.Revision, ontology.Change{ExpectedVersion: accepted.Version, Record: retired}))
			if action == ontology.GroupTogether {
				require.ErrorIs(t, err, ontology.ErrOverride)
			} else {
				require.NoError(t, err)
				head, err := f.store.GetRecord(context.Background(), f.team, group.ID, 0)
				require.NoError(t, err)
				require.True(t, head.Retired)
				require.Equal(t, accepted.Group, head.Group)
				require.Equal(t, accepted.Sources, head.Sources)
				regroup := group
				regroup.ID = uuid.NewString()
				_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("regroup", result.Revision, ontology.Change{Record: regroup}))
				require.ErrorIs(t, err, ontology.ErrOverride)
			}
			historical, err := f.store.GetRecord(context.Background(), f.team, group.ID, accepted.Version)
			require.NoError(t, err)
			require.Equal(t, accepted.Record, historical.Record)
			history, err := f.store.History(context.Background(), f.team, 0, 20)
			require.NoError(t, err)
			count := 2
			if action == ontology.KeepSeparate {
				count = 3
			}
			require.Len(t, history, count)
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
					if err := tx.Raw(`SELECT format('CREATE POLICY missing_dependency_fixture ON ontology_record_heads AS RESTRICTIVE FOR SELECT TO ontology_app USING (record_id <> %L::uuid)', ?::text)`, topic.ID).Row().Scan(&policy); err != nil {
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
	result, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("separate", initial.Revision, ontology.Change{Record: separation}))
	require.NoError(t, err)
	view, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	require.Equal(t, previous.Fingerprint, view.Fingerprint)
	page, err := f.store.ListRecords(context.Background(), f.team, ontology.AssignmentKind, "", 20)
	require.NoError(t, err)
	require.True(t, page.Records[0].Current)
	classification := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: alternate.ID, Members: []ontology.SourceHandle{a}}, Sources: assignment.Sources}
	result, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("classify", result.Revision, ontology.Change{Record: classification}))
	require.NoError(t, err)
	view, err = f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.False(t, view.Current)
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("ignore-classification", result.Revision, ontology.Change{ExpectedVersion: previous.Version, Record: previous.Record}))
	require.ErrorIs(t, err, ontology.ErrOverride)
	pin := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: topic.ID}}
	result, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("pin", result.Revision, ontology.Change{Record: pin}))
	require.NoError(t, err)
	topic.Definition.Label = "Renamed database"
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("ignore-pin", result.Revision, ontology.Change{ExpectedVersion: 1, Record: topic}))
	require.ErrorIs(t, err, ontology.ErrOverride)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyMixedSeparationOverrideProtectsEvidenceGroups(t *testing.T) {
	f := newOrganizationFixture(t)
	a := f.organizationEvidenceAt(t, 0, "Atlas uses PostgreSQL.", "2026-10-08T12:00:00Z", "")
	b := f.organizationEvidenceAt(t, 0, "Atlas uses PostgreSQL.", "2026-10-08T12:00:00Z", "")
	relationship := maintenanceRelationship(t, f)
	before := f.canonicalSnapshot(t)
	override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.KeepSeparate, Members: []ontology.SourceHandle{relationship, a, b}}, Sources: []ontology.SourceDependency{f.source(t, relationship), f.source(t, a), f.source(t, b)}}
	publication, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("mixed-separation", 0, ontology.Change{Record: override}))
	require.NoError(t, err)
	stored, err := f.store.GetRecord(context.Background(), f.team, override.ID, 0)
	require.NoError(t, err)
	require.Equal(t, relationship, stored.Override.Members[0])
	group := ontology.Record{ID: uuid.NewString(), Kind: ontology.EvidenceGroup, Group: &ontology.Group{Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("contradict-mixed-separation", publication.Revision, ontology.Change{Record: group}))
	require.ErrorIs(t, err, ontology.ErrOverride)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyCurrentReadersReportCanonicalFingerprintAndVersionChanges(t *testing.T) {
	for _, change := range []string{"name", "version"} {
		t.Run(change, func(t *testing.T) {
			f := newOrganizationFixture(t)
			entity, err := f.knowledge.CreateEntity(context.Background(), knowledge.CreateEntityInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityKind: "project", CanonicalName: "Atlas"})
			require.NoError(t, err)
			handle := ontology.SourceHandle{Kind: ontology.EntitySource, ID: entity.EntityID, Version: int64(entity.Version)}
			class := testTopic("project-class")
			class.Kind = ontology.EntityClass
			class.Definition.BaseEntityKind = "project"
			assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: class.ID}, Sources: []ontology.SourceDependency{f.source(t, handle)}}
			_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("entity-assignment", 0, ontology.Change{Record: class}, ontology.Change{Record: assignment}))
			require.NoError(t, err)
			if change == "name" {
				_, err = f.knowledge.AddEntityName(context.Background(), knowledge.AddEntityNameInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityID: entity.EntityID, DisplayName: "Atlas alias", NameKind: "alias"})
				require.NoError(t, err)
			} else {
				require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
					return tx.Exec(`UPDATE entity_records SET version=version+1 WHERE team_id=?::uuid AND entity_id=?::uuid`, f.team, entity.EntityID).Error
				}))
			}
			view, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
			require.NoError(t, err)
			require.False(t, view.Current)
			require.Equal(t, "source_changed", view.StaleReason)
			page, err := f.store.ListRecords(context.Background(), f.team, ontology.AssignmentKind, "", 20)
			require.NoError(t, err)
			require.Len(t, page.Records, 1)
			require.False(t, page.Records[0].Current)
			require.Equal(t, "source_changed", page.Records[0].StaleReason)
		})
	}
}
