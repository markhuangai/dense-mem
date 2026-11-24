package contract

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestOntologyRejectsMismatchedMemberVersions(t *testing.T) {
	first, second := policySnapshot("first", EvidenceSource, "same"), policySnapshot("second", EvidenceSource, "same")
	topic := topicRecord("topic")
	topic.Version = 1
	catalog := policyCatalog(t, topic)
	snapshots := map[string]SourceSnapshot{SourceKey(first.SourceHandle): first, SourceKey(second.SourceHandle): second}
	wrong := first.SourceHandle
	wrong.Version++
	for _, record := range []Record{
		{ID: uuid.NewString(), Kind: AssignmentKind, Assignment: &Assignment{Source: wrong, DefinitionID: topic.ID}, Sources: []SourceDependency{sourceDependency(t, first)}},
		{ID: uuid.NewString(), Kind: EvidenceGroup, Group: &Group{Members: []SourceHandle{wrong, second.SourceHandle}}, Sources: []SourceDependency{sourceDependency(t, first), sourceDependency(t, second)}},
		{ID: uuid.NewString(), Kind: OverrideKind, Override: &Override{Action: KeepSeparate, Members: []SourceHandle{wrong, second.SourceHandle}}, Sources: []SourceDependency{sourceDependency(t, first), sourceDependency(t, second)}},
	} {
		_, err := PreparePublication(catalog, snapshots, publicationFor(record), false)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestOntologyEnrichedDependenciesAreBounded(t *testing.T) {
	snapshot := policySnapshot("evidence", EvidenceSource, "")
	topic := topicRecord("people")
	topic.Version = 1
	snapshots := map[string]SourceSnapshot{SourceKey(snapshot.SourceHandle): snapshot}
	assignment := Record{ID: uuid.NewString(), Kind: AssignmentKind, Assignment: &Assignment{Source: snapshot.SourceHandle, DefinitionID: topic.ID}, Sources: []SourceDependency{sourceDependency(t, snapshot)}}
	topics := []Record{topic}
	var dependencies []RevisionRef
	for i := range MaxMembers {
		dependency := topicRecord(fmt.Sprintf("related-%d", i))
		dependency.Version = 1
		topics = append(topics, dependency)
		dependencies = append(dependencies, RevisionRef{ID: dependency.ID, Version: dependency.Version})
	}
	catalog := policyCatalog(t, topics...)
	assignment.Dependencies = dependencies[:MaxMembers-1]
	prepared, err := PreparePublication(catalog, snapshots, publicationFor(assignment), true)
	require.NoError(t, err)
	require.Len(t, prepared[0].Dependencies, MaxMembers)
	require.Contains(t, prepared[0].Dependencies, RevisionRef{ID: topic.ID, Version: topic.Version})

	assignment.Dependencies = dependencies
	input := publicationFor(assignment)
	require.NoError(t, ValidatePublication(input))
	prepared, err = PreparePublication(catalog, snapshots, input, true)
	require.ErrorIs(t, err, ErrInvalid)
	require.Nil(t, prepared)
}

func TestOntologyDependencyTraversalRejectsCyclesAndMissingSources(t *testing.T) {
	first, second := topicRecord("first"), topicRecord("second")
	first.Version = 1
	second.Version = 1
	first.Dependencies = []RevisionRef{{ID: second.ID, Version: 1}}
	second.Dependencies = []RevisionRef{{ID: first.ID, Version: 1}}
	_, err := DependencyRecords([]Record{first}, map[string]Record{first.ID: first, second.ID: second})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = DependencyRecords([]Record{first}, map[string]Record{first.ID: first})
	require.ErrorIs(t, err, ErrSourceStale)
	snapshot := policySnapshot("evidence", EvidenceSource, "original")
	second.Dependencies = nil
	second.Sources = []SourceDependency{sourceDependency(t, snapshot)}
	catalog := map[string]Record{second.ID: second}
	fingerprint, err := RecordFingerprint(second, map[string]SourceSnapshot{SourceKey(snapshot.SourceHandle): snapshot}, catalog)
	require.NoError(t, err)
	second.Fingerprint = fingerprint
	catalog[second.ID] = second
	snapshot.State["content"] = "changed"
	err = CheckDependencies([]Record{first}, map[string]SourceSnapshot{SourceKey(snapshot.SourceHandle): snapshot}, catalog)
	require.ErrorIs(t, err, ErrSourceStale)
}
