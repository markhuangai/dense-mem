package contract

import (
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

func TestOntologyClassificationOverrideRequiresCompatibleEntityKind(t *testing.T) {
	snapshot := policySnapshot("person", EntitySource, "")
	class := Record{ID: uuid.NewString(), Version: 1, Kind: EntityClass, Definition: &Definition{Key: "project", Label: "Project", BaseEntityKind: "project"}}
	for _, record := range []Record{
		{ID: uuid.NewString(), Kind: AssignmentKind, Assignment: &Assignment{Source: snapshot.SourceHandle, DefinitionID: class.ID}, Sources: []SourceDependency{sourceDependency(t, snapshot)}},
		{ID: uuid.NewString(), Kind: OverrideKind, Override: &Override{Action: SetClassification, DefinitionID: class.ID, Members: []SourceHandle{snapshot.SourceHandle}}, Sources: []SourceDependency{sourceDependency(t, snapshot)}},
	} {
		_, err := PreparePublication(policyCatalog(t, class), map[string]SourceSnapshot{SourceKey(snapshot.SourceHandle): snapshot}, publicationFor(record), false)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestOntologyEnrichedDependenciesAreBounded(t *testing.T) {
	snapshot := policySnapshot("person", EntitySource, "")
	class := Record{ID: uuid.NewString(), Version: 1, Kind: EntityClass, Definition: &Definition{Key: "person", Label: "Person", BaseEntityKind: "person"}}
	catalog := policyCatalog(t, class)
	snapshots := map[string]SourceSnapshot{SourceKey(snapshot.SourceHandle): snapshot}
	for i := 0; i < MaxMembers+1; i++ {
		override := Record{ID: uuid.NewString(), Version: 1, Kind: OverrideKind, Override: &Override{Action: SetClassification, DefinitionID: class.ID, Members: []SourceHandle{snapshot.SourceHandle}}, Sources: []SourceDependency{sourceDependency(t, snapshot)}}
		override.Dependencies = dependenciesFor(override, catalog)
		fingerprint, err := RecordFingerprint(override, snapshots, catalog)
		require.NoError(t, err)
		override.Fingerprint = fingerprint
		catalog[override.ID] = override
	}
	assignment := Record{ID: uuid.NewString(), Kind: AssignmentKind, Assignment: &Assignment{Source: snapshot.SourceHandle, DefinitionID: class.ID}, Sources: []SourceDependency{sourceDependency(t, snapshot)}}
	_, err := PreparePublication(catalog, snapshots, publicationFor(assignment), true)
	require.ErrorIs(t, err, ErrInvalid)
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
