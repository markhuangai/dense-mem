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
