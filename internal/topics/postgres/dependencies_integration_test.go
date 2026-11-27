//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	topiccontract "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologyTransitiveSourceFreshness(t *testing.T) {
	f := newOntologyFixture(t)
	definitionEvidence := f.evidence(t, 0, "Source for the database topic.")
	assignmentEvidence := f.evidence(t, 1, "Atlas uses PostgreSQL.")
	definitionHandle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: definitionEvidence.Evidence[0].FragmentID, Version: 1}
	handle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: assignmentEvidence.Evidence[0].FragmentID, Version: 1}
	topic := testTopic("database")
	topic.Sources = []topiccontract.SourceDependency{f.source(t, definitionHandle)}
	assignment := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.AssignmentKind, Assignment: &topiccontract.Assignment{Source: handle, DefinitionID: topic.ID}, Sources: []topiccontract.SourceDependency{f.source(t, handle)}}
	result, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("initial", 0, topiccontract.Change{Record: topic}, topiccontract.Change{Record: assignment}))
	require.NoError(t, err)
	view, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{definitionHandle.ID}, Reason: "definition support withdrawn", IdempotencyKey: "withdraw", RequestHash: testHash("withdraw")})
	require.NoError(t, err)
	view, err = f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current, "an accepted assignment stays current while its Topic is active")
	newEvidence := f.evidence(t, 1, "Atlas also uses SQLite.")
	newHandle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: newEvidence.Evidence[0].FragmentID, Version: 1}
	assignment.ID = uuid.NewString()
	assignment.Assignment = &topiccontract.Assignment{Source: newHandle, DefinitionID: topic.ID}
	assignment.Sources = []topiccontract.SourceDependency{f.source(t, newHandle)}
	_, err = f.store.PublishAutomatic(context.Background(), f.team, testPublication("stale-definition", result.Revision, topiccontract.Change{Record: assignment}))
	require.ErrorIs(t, err, topiccontract.ErrSourceStale)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 1)
}

func TestOntologyMismatchedSourceVersionDoesNotCommit(t *testing.T) {
	f := newOntologyFixture(t)
	evidence := f.evidence(t, 0, "Atlas uses PostgreSQL.")
	handle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: evidence.Evidence[0].FragmentID, Version: 1}
	topic := testTopic("database")
	wrong := handle
	wrong.Version++
	assignment := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.AssignmentKind, Assignment: &topiccontract.Assignment{Source: wrong, DefinitionID: topic.ID}, Sources: []topiccontract.SourceDependency{f.source(t, handle)}}
	before := f.canonicalSnapshot(t)
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("wrong-version", 0, topiccontract.Change{Record: topic}, topiccontract.Change{Record: assignment}))
	require.ErrorIs(t, err, topiccontract.ErrInvalid)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Empty(t, history)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyEnrichmentBoundRejectsAtomically(t *testing.T) {
	f := newOntologyFixture(t)
	ctx := f.actor(0, "manager")
	topic := testTopic("storage")
	seed, err := f.store.PublishManager(ctx, f.team, testPublication("seed", 0, topiccontract.Change{Record: topic}))
	require.NoError(t, err)
	revision := seed.Revision
	for start := 0; start <= topiccontract.MaxDependencyRecords; start += topiccontract.MaxChanges {
		var changes []topiccontract.Change
		for index := start; index < min(start+topiccontract.MaxChanges, topiccontract.MaxDependencyRecords+1); index++ {
			child := testTopic(fmt.Sprintf("child-%04d", index))
			child.Definition.ParentID = topic.ID
			changes = append(changes, topiccontract.Change{Record: child})
		}
		result, err := f.store.PublishManager(ctx, f.team, testPublication(uuid.NewString(), revision, changes...))
		require.NoError(t, err)
		revision = result.Revision
	}
	before := f.canonicalSnapshot(t)
	topic.Definition.Label = "Changed parent"
	_, err = f.store.PublishManager(ctx, f.team, testPublication("too-many-affected-children", revision, topiccontract.Change{Record: topic, ExpectedVersion: 1}))
	require.ErrorIs(t, err, topiccontract.ErrContextBound)
	view, err := f.store.GetRecord(ctx, f.team, topic.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	require.EqualValues(t, 1, view.Version)
	require.Equal(t, "storage", view.Definition.Label)
	page, err := f.store.ListRecords(ctx, f.team, topiccontract.Topic, "", 1)
	require.NoError(t, err)
	require.Equal(t, revision, page.Revision)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyDefinitionChangesValidateActiveChildren(t *testing.T) {
	f := newOntologyFixture(t)
	parent, child := testTopic("person"), testTopic("engineer")
	child.Definition.ParentID = parent.ID
	initial, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("parent", 0, topiccontract.Change{Record: parent}))
	require.NoError(t, err)
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("child", initial.Revision, topiccontract.Change{Record: child}))
	require.NoError(t, err)
	before := f.canonicalSnapshot(t)
	view, err := f.store.GetRecord(context.Background(), f.team, parent.ID, 0)
	require.NoError(t, err)
	retired := view.Record
	retired.Retired = true
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("retire-parent", 2, topiccontract.Change{ExpectedVersion: 1, Record: retired}))
	require.ErrorIs(t, err, topiccontract.ErrInvalid)
	_, err = f.store.Rollback(f.actor(0, "manager"), f.team, initial.ID, "rollback-parent", 2, "undo parent creation")
	require.ErrorIs(t, err, topiccontract.ErrInvalid)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 2)
	childView, err := f.store.GetRecord(context.Background(), f.team, child.ID, 0)
	require.NoError(t, err)
	require.True(t, childView.Current)
	childView.Record.Retired = true
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("retire-hierarchy", 2,
		topiccontract.Change{ExpectedVersion: 1, Record: retired}, topiccontract.Change{ExpectedVersion: 1, Record: childView.Record}))
	require.NoError(t, err)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyRollbackRefreshesDependencyVersions(t *testing.T) {
	f := newOntologyFixture(t)
	evidence := f.evidence(t, 0, "Atlas uses PostgreSQL.")
	handle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: evidence.Evidence[0].FragmentID, Version: 1}
	first, second, contextTopic := testTopic("storage"), testTopic("database"), testTopic("context")
	assignment := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.AssignmentKind,
		Assignment: &topiccontract.Assignment{Source: handle, DefinitionID: first.ID},
		Sources:    []topiccontract.SourceDependency{f.source(t, handle)}, Dependencies: []topiccontract.RevisionRef{{ID: contextTopic.ID, Version: 1}}}
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("initial", 0,
		topiccontract.Change{Record: first}, topiccontract.Change{Record: second}, topiccontract.Change{Record: contextTopic}, topiccontract.Change{Record: assignment}))
	require.NoError(t, err)
	before := f.canonicalSnapshot(t)
	assignment.Assignment.DefinitionID = second.ID
	changed, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("reclassify", 1, topiccontract.Change{ExpectedVersion: 1, Record: assignment}))
	require.NoError(t, err)
	first.Definition.Label, contextTopic.Definition.Label = "Storage systems", "Updated context"
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("revise-dependencies", changed.Revision,
		topiccontract.Change{ExpectedVersion: 1, Record: first}, topiccontract.Change{ExpectedVersion: 1, Record: contextTopic}))
	require.NoError(t, err)
	rollback, err := f.store.Rollback(f.actor(0, "manager"), f.team, changed.ID, "restore-classification", 3, "restore original classification")
	require.NoError(t, err)
	require.Equal(t, int64(4), rollback.Revision)
	view, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 0)
	require.NoError(t, err)
	require.True(t, view.Current)
	require.Equal(t, int64(3), view.Version)
	require.Equal(t, first.ID, view.Assignment.DefinitionID)
	require.ElementsMatch(t, []topiccontract.RevisionRef{{ID: first.ID, Version: 2}, {ID: contextTopic.ID, Version: 2}}, view.Dependencies)
	original, err := f.store.GetRecord(context.Background(), f.team, assignment.ID, 1)
	require.NoError(t, err)
	require.ElementsMatch(t, []topiccontract.RevisionRef{{ID: first.ID, Version: 1}, {ID: contextTopic.ID, Version: 1}}, original.Dependencies)
	replayed, err := f.store.Rollback(f.actor(0, "manager"), f.team, changed.ID, "restore-classification", 3, "restore original classification")
	require.NoError(t, err)
	require.True(t, replayed.Existing)
	require.Equal(t, rollback.ID, replayed.ID)
	require.Equal(t, before, f.canonicalSnapshot(t))
}
