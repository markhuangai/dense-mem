//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologyTransitiveSourceFreshness(t *testing.T) {
	f := newOntologyFixture(t)
	definitionEvidence := f.evidence(t, 0, "Source for the database topic.")
	assignmentEvidence := f.evidence(t, 1, "Atlas uses PostgreSQL.")
	definitionHandle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: definitionEvidence.Evidence[0].FragmentID, Version: 1}
	handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: assignmentEvidence.Evidence[0].FragmentID, Version: 1}
	topic := testTopic("database")
	topic.Sources = []ontology.SourceDependency{f.source(t, definitionHandle)}
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: topic.ID}, Sources: []ontology.SourceDependency{f.source(t, handle)}}
	result, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("initial", 0, ontology.Change{Record: topic}, ontology.Change{Record: assignment}))
	require.NoError(t, err)
	view, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{definitionHandle.ID}, Reason: "definition support withdrawn", IdempotencyKey: "withdraw", RequestHash: testHash("withdraw")})
	require.NoError(t, err)
	view, err = f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.False(t, view.Current)
	assignment.ID = uuid.NewString()
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("stale-definition", result.Revision, ontology.Change{Record: assignment}))
	require.ErrorIs(t, err, ontology.ErrSourceStale)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 1)
}

func TestOntologyMismatchedSourceVersionDoesNotCommit(t *testing.T) {
	f := newOntologyFixture(t)
	evidence := f.evidence(t, 0, "Atlas uses PostgreSQL.")
	handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: evidence.Evidence[0].FragmentID, Version: 1}
	topic := testTopic("database")
	wrong := handle
	wrong.Version++
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: wrong, DefinitionID: topic.ID}, Sources: []ontology.SourceDependency{f.source(t, handle)}}
	before := f.canonicalSnapshot(t)
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("wrong-version", 0, ontology.Change{Record: topic}, ontology.Change{Record: assignment}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Empty(t, history)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyEnrichmentBoundRejectsAtomically(t *testing.T) {
	f := newOntologyFixture(t)
	seed, err := f.store.SeedDefinitions(context.Background(), f.team, ontology.SeedInput{OperationKey: "seed", Limit: 20})
	require.NoError(t, err)
	entity, err := f.knowledge.CreateEntity(context.Background(), knowledge.CreateEntityInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityKind: "person", CanonicalName: "Ada"})
	require.NoError(t, err)
	handle := ontology.SourceHandle{Kind: ontology.EntitySource, ID: entity.EntityID, Version: int64(entity.Version)}
	dependency := f.source(t, handle)
	classID := ontology.SeedDefinitionID(f.team, ontology.EntityClass, "person")
	revision := seed.Revision
	var changes []ontology.Change
	var lastID string
	for index := 0; index <= ontology.MaxMembers; index++ {
		lastID = uuid.NewString()
		changes = append(changes, ontology.Change{Record: ontology.Record{ID: lastID, Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: classID, Members: []ontology.SourceHandle{handle}}, Sources: []ontology.SourceDependency{dependency}}})
		if len(changes) == ontology.MaxChanges || index == ontology.MaxMembers {
			result, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), revision, changes...))
			require.NoError(t, err)
			revision = result.Revision
			changes = nil
		}
	}
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: handle, DefinitionID: classID}, Sources: []ontology.SourceDependency{dependency}}
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("too-many-overrides", revision, ontology.Change{Record: assignment}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 4)
	view, err := f.store.GetRecord(context.Background(), f.team, lastID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	_, err = f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.ErrorIs(t, err, ontology.ErrNotFound)
}

func TestOntologyDefinitionChangesValidateActiveChildren(t *testing.T) {
	f := newOntologyFixture(t)
	parent, child := testTopic("person"), testTopic("engineer")
	parent.Kind, child.Kind = ontology.EntityClass, ontology.EntityClass
	parent.Definition.BaseEntityKind, child.Definition.BaseEntityKind = "person", "person"
	child.Definition.ParentID = parent.ID
	initial, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("parent", 0, ontology.Change{Record: parent}))
	require.NoError(t, err)
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("child", initial.Revision, ontology.Change{Record: child}))
	require.NoError(t, err)
	before := f.canonicalSnapshot(t)
	view, err := f.store.GetRecord(context.Background(), f.team, parent.ID, 0)
	require.NoError(t, err)
	retired := view.Record
	retired.Retired = true
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("retire-parent", 2, ontology.Change{ExpectedVersion: 1, Record: retired}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	_, err = f.store.Rollback(f.actor(0, "manager"), f.team, initial.ID, "rollback-parent", 2, "undo parent creation")
	require.ErrorIs(t, err, ontology.ErrInvalid)
	parent.Definition.BaseEntityKind = "project"
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("change-parent-kind", 2, ontology.Change{ExpectedVersion: 1, Record: parent}))
	require.ErrorIs(t, err, ontology.ErrInvalid)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 2)
	childView, err := f.store.GetRecord(context.Background(), f.team, child.ID, 0)
	require.NoError(t, err)
	require.True(t, childView.Current)
	childView.Record.Retired = true
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("retire-hierarchy", 2,
		ontology.Change{ExpectedVersion: 1, Record: retired}, ontology.Change{ExpectedVersion: 1, Record: childView.Record}))
	require.NoError(t, err)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyRollbackRefreshesDependencyVersions(t *testing.T) {
	f := newOntologyFixture(t)
	evidence := f.evidence(t, 0, "Atlas uses PostgreSQL.")
	handle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: evidence.Evidence[0].FragmentID, Version: 1}
	first, second, contextTopic := testTopic("storage"), testTopic("database"), testTopic("context")
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind,
		Assignment: &ontology.Assignment{Source: handle, DefinitionID: first.ID},
		Sources:    []ontology.SourceDependency{f.source(t, handle)}, Dependencies: []ontology.RevisionRef{{ID: contextTopic.ID, Version: 1}}}
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("initial", 0,
		ontology.Change{Record: first}, ontology.Change{Record: second}, ontology.Change{Record: contextTopic}, ontology.Change{Record: assignment}))
	require.NoError(t, err)
	before := f.canonicalSnapshot(t)
	assignment.Assignment.DefinitionID = second.ID
	changed, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("reclassify", 1, ontology.Change{ExpectedVersion: 1, Record: assignment}))
	require.NoError(t, err)
	first.Definition.Label, contextTopic.Definition.Label = "Storage systems", "Updated context"
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("revise-dependencies", changed.Revision,
		ontology.Change{ExpectedVersion: 1, Record: first}, ontology.Change{ExpectedVersion: 1, Record: contextTopic}))
	require.NoError(t, err)
	rollback, err := f.store.Rollback(f.actor(0, "manager"), f.team, changed.ID, "restore-classification", 3, "restore original classification")
	require.NoError(t, err)
	require.Equal(t, int64(4), rollback.Revision)
	view, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	require.Equal(t, int64(3), view.Version)
	require.Equal(t, first.ID, view.Assignment.DefinitionID)
	require.ElementsMatch(t, []ontology.RevisionRef{{ID: first.ID, Version: 2}, {ID: contextTopic.ID, Version: 2}}, view.Dependencies)
	original, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 1)
	require.NoError(t, err)
	require.ElementsMatch(t, []ontology.RevisionRef{{ID: first.ID, Version: 1}, {ID: contextTopic.ID, Version: 1}}, original.Dependencies)
	replayed, err := f.store.Rollback(f.actor(0, "manager"), f.team, changed.ID, "restore-classification", 3, "restore original classification")
	require.NoError(t, err)
	require.True(t, replayed.Existing)
	require.Equal(t, rollback.ID, replayed.ID)
	require.Equal(t, before, f.canonicalSnapshot(t))
}
