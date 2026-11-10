package contract

import (
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestEvidenceOnlyUnitOntologyRetainsTopicHierarchyAndDirectEvidenceMembership(t *testing.T) {
	parent, child, evidence, assignment := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	source := SourceSnapshot{SourceHandle: SourceHandle{Kind: EvidenceSource, ID: evidence, Version: 1}, Eligible: true, State: map[string]string{"content_hash": "unchanged"}}
	fingerprint, err := SourceFingerprint(source)
	require.NoError(t, err)
	publication := Publication{OperationKey: "topic-publication", Reason: "Organize Evidence", Changes: []Change{
		{Record: Record{ID: parent, Kind: Topic, Definition: &Definition{Key: "operations", Label: "Operations"}}},
		{Record: Record{ID: child, Kind: Topic, Definition: &Definition{Key: "timeouts", Label: "Timeouts", ParentID: parent}}},
		{Record: Record{ID: assignment, Kind: AssignmentKind, Assignment: &Assignment{Source: source.SourceHandle, DefinitionID: child}, Sources: []SourceDependency{{SourceHandle: source.SourceHandle, Fingerprint: fingerprint}}}},
	}}
	prepared, err := PreparePublication(map[string]Record{}, map[string]SourceSnapshot{SourceKey(source.SourceHandle): source}, publication, false)
	require.NoError(t, err)
	require.Len(t, prepared, 3)
	require.Equal(t, child, prepared[2].Assignment.DefinitionID)
	require.Equal(t, parent, prepared[1].Definition.ParentID)
	for _, kind := range []SourceKind{"entity", "predicate", "relationship"} {
		require.Error(t, ValidateSourceHandle(SourceHandle{Kind: kind, ID: evidence, Version: 1}))
	}
	publication.Changes[0].Record.Definition.ParentID = child
	_, err = PreparePublication(map[string]Record{}, map[string]SourceSnapshot{SourceKey(source.SourceHandle): source}, publication, false)
	require.Error(t, err)
}

func TestEvidenceOnlyUnitMigrationFingerprintDoesNotRefreshAcceptedSourceState(t *testing.T) {
	source := SourceSnapshot{SourceHandle: SourceHandle{Kind: EvidenceSource, ID: uuid.NewString(), Version: 1}, Eligible: true, State: map[string]string{"content_hash": "original"}}
	hash, err := SourceFingerprint(source)
	require.NoError(t, err)
	record := Record{ID: uuid.NewString(), Kind: Topic, Definition: &Definition{Key: "topic", Label: "Topic"}, Sources: []SourceDependency{{SourceHandle: source.SourceHandle, Fingerprint: hash}}}
	expected, err := RecordFingerprint(record, map[string]SourceSnapshot{SourceKey(source.SourceHandle): source}, map[string]Record{})
	require.NoError(t, err)
	retained, err := RecordFingerprintWithAcceptedSources(record, map[string]Record{})
	require.NoError(t, err)
	require.Equal(t, expected, retained)
	source.State = map[string]string{"content_hash": "changed"}
	require.ErrorIs(t, CheckSourceDependencies(record, map[string]SourceSnapshot{SourceKey(source.SourceHandle): source}), ErrSourceStale)
	retained, err = RecordFingerprintWithAcceptedSources(record, map[string]Record{})
	require.NoError(t, err)
	require.Equal(t, expected, retained)
	record.Sources[0].Fingerprint = "sha256:" + strings.Repeat("0", 64)
	changed, err := RecordFingerprintWithAcceptedSources(record, map[string]Record{})
	require.NoError(t, err)
	require.NotEqual(t, expected, changed)
}
