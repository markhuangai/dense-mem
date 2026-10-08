package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func policyID(key string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("ontology-test:"+key)).String()
}
func topicRecord(key string) Record {
	return Record{ID: policyID(key), Kind: Topic, Definition: &Definition{Key: key, Label: key}}
}
func publicationFor(records ...Record) Publication {
	input := Publication{OperationKey: "test-operation", Reason: "organization correction"}
	for _, record := range records {
		input.Changes = append(input.Changes, Change{Record: record})
	}
	return input
}
func policySnapshot(key string, kind SourceKind, meaning string) SourceSnapshot {
	return SourceSnapshot{SourceHandle: SourceHandle{Kind: kind, ID: policyID(key), Version: 1}, TeamID: policyID("team"), SpaceID: policyID("space"), Generation: 1, OwnerID: policyID("owner"), Eligible: true, EntityKind: "person", State: map[string]string{"content": meaning}, MeaningKey: meaning}
}
func sourceDependency(t *testing.T, snapshot SourceSnapshot) SourceDependency {
	t.Helper()
	fingerprint, err := SourceFingerprint(snapshot)
	require.NoError(t, err)
	return SourceDependency{SourceHandle: snapshot.SourceHandle, Fingerprint: fingerprint}
}

func policyCatalog(t *testing.T, records ...Record) map[string]Record {
	t.Helper()
	catalog := map[string]Record{}
	for _, record := range records {
		catalog[record.ID] = record
	}
	for _, record := range records {
		fingerprint, err := RecordFingerprint(record, nil, catalog)
		require.NoError(t, err)
		record.Fingerprint = fingerprint
		catalog[record.ID] = record
	}
	return catalog
}

func TestOntologyClosedPublicationSchema(t *testing.T) {
	valid := publicationFor(topicRecord("databases"))
	encoded, err := json.Marshal(valid)
	require.NoError(t, err)
	decoded, err := DecodePublication(strings.NewReader(string(encoded)))
	require.NoError(t, err)
	require.Equal(t, valid, decoded)
	for name, input := range map[string]string{
		"unknown":   strings.Replace(string(encoded), "{", "{\"team_id\":\"replacement\",", 1),
		"duplicate": strings.Replace(string(encoded), "{", "{\"reason\":\"first\",", 1),
		"trailing":  string(encoded) + " {}",
		"oversized": strings.Repeat(" ", MaxPublicationBytes+1),
		"null":      "null",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodePublication(strings.NewReader(input))
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestOntologyRecordValidation(t *testing.T) {
	for name, alter := range map[string]func(*Record){
		"identity":           func(r *Record) { r.ID = "not-uuid" },
		"unknown kind":       func(r *Record) { r.Kind = "fact" },
		"extra body":         func(r *Record) { r.Group = &Group{} },
		"missing body":       func(r *Record) { r.Definition = nil },
		"duplicate alias":    func(r *Record) { r.Definition.Aliases = []string{"DB", " db "} },
		"empty key":          func(r *Record) { r.Definition.Key = " " },
		"parent":             func(r *Record) { r.Definition.ParentID = "bad" },
		"invalid base kind":  func(r *Record) { r.Kind = EntityClass; r.Definition.BaseEntityKind = "database" },
		"base kind on topic": func(r *Record) { r.Definition.BaseEntityKind = "person" },
		"self dependency":    func(r *Record) { r.Dependencies = []RevisionRef{{ID: r.ID, Version: 1}} },
		"source hash": func(r *Record) {
			r.Sources = []SourceDependency{{SourceHandle: SourceHandle{Kind: EvidenceSource, ID: policyID("e"), Version: 1}, Fingerprint: "sha256:invalid"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			record := topicRecord("db")
			alter(&record)
			require.ErrorIs(t, ValidateRecord(record), ErrInvalid)
		})
	}
	first := topicRecord("first")
	duplicate := publicationFor(first, first)
	require.ErrorIs(t, ValidatePublication(duplicate), ErrInvalid)
	validPredicate := SourceHandle{Kind: PredicateSource, ID: "uses", Version: 1}
	require.NoError(t, ValidateSourceHandle(validPredicate))
	for _, handle := range []SourceHandle{{Kind: "value", ID: policyID("e"), Version: 1}, {Kind: EntitySource, ID: "person", Version: 1}, {Kind: PredicateSource, Version: 1}, {Kind: EvidenceSource, ID: policyID("e")}} {
		require.ErrorIs(t, ValidateSourceHandle(handle), ErrInvalid)
	}
}

func TestOntologyDefinitionHierarchyAndCollisions(t *testing.T) {
	parent, child := topicRecord("engineering"), topicRecord("databases")
	child.Definition.ParentID = parent.ID
	prepared, err := PreparePublication(nil, nil, publicationFor(parent, child), false)
	require.NoError(t, err)
	require.Len(t, prepared, 2)
	for _, record := range prepared {
		if record.ID == child.ID {
			require.Contains(t, record.Dependencies, RevisionRef{ID: parent.ID, Version: 1})
		}
	}
	parent.Definition.ParentID = child.ID
	_, err = PreparePublication(nil, nil, publicationFor(parent, child), false)
	require.ErrorIs(t, err, ErrInvalid)
	parent.Definition.ParentID = ""
	child.Definition.ParentID = policyID("missing")
	_, err = PreparePublication(nil, nil, publicationFor(parent, child), false)
	require.ErrorIs(t, err, ErrInvalid)
	child.Definition.ParentID = ""
	child.Definition.Aliases = []string{" ENGINEERING "}
	_, err = PreparePublication(nil, nil, publicationFor(parent, child), false)
	require.ErrorIs(t, err, ErrInvalid)
	child.Definition.Aliases = nil
	child.Kind = EntityClass
	child.Definition.BaseEntityKind = "person"
	child.Definition.ParentID = parent.ID
	_, err = PreparePublication(nil, nil, publicationFor(parent, child), false)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestOntologyPublicationRejectsStaleSourcesAndPartialChanges(t *testing.T) {
	snapshot := policySnapshot("entity", EntitySource, "Ada")
	definition := topicRecord("person")
	definition.Kind = EntityClass
	definition.Definition.BaseEntityKind = "person"
	definition.Version = 1
	assignment := Record{ID: policyID("assignment"), Kind: AssignmentKind, Assignment: &Assignment{Source: snapshot.SourceHandle, DefinitionID: definition.ID}, Sources: []SourceDependency{sourceDependency(t, snapshot)}, Dependencies: []RevisionRef{{ID: definition.ID, Version: 1}}}
	catalog := policyCatalog(t, definition)
	snapshots := map[string]SourceSnapshot{SourceKey(snapshot.SourceHandle): snapshot}
	prepared, err := PreparePublication(catalog, snapshots, publicationFor(assignment), true)
	require.NoError(t, err)
	require.Len(t, prepared, 1)
	snapshot.State["content"] = "new identity context"
	snapshots[SourceKey(snapshot.SourceHandle)] = snapshot
	prepared, err = PreparePublication(catalog, snapshots, publicationFor(topicRecord("valid"), assignment), true)
	require.ErrorIs(t, err, ErrSourceStale)
	require.Nil(t, prepared)
	assignment.Assignment.Source.Kind = EvidenceSource
	_, err = PreparePublication(catalog, snapshots, publicationFor(assignment), true)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestOntologyGroupingKeepsDistinctContext(t *testing.T) {
	for _, kind := range []Kind{EvidenceGroup, RelationshipGroup} {
		sourceKind := EvidenceSource
		if kind == RelationshipGroup {
			sourceKind = RelationshipSource
		}
		first := policySnapshot("first", sourceKind, "Alice|uses|PostgreSQL|+|2026|production")
		second := policySnapshot("second", sourceKind, first.MeaningKey)
		group := Record{ID: policyID("group"), Kind: kind, Group: &Group{Members: []SourceHandle{first.SourceHandle, second.SourceHandle}}, Sources: []SourceDependency{sourceDependency(t, first), sourceDependency(t, second)}}
		snapshots := map[string]SourceSnapshot{SourceKey(first.SourceHandle): first, SourceKey(second.SourceHandle): second}
		_, err := PreparePublication(nil, snapshots, publicationFor(group), true)
		require.NoError(t, err)
		for _, meaning := range []string{"Bob|uses|PostgreSQL|+|2026|production", "Alice|uses|PostgreSQL|-|2026|production", "Alice|uses|PostgreSQL|+|2025|production", "Alice|uses|PostgreSQL|+|2026|staging"} {
			second.MeaningKey = meaning
			snapshots[SourceKey(second.SourceHandle)] = second
			group.Sources[1] = sourceDependency(t, second)
			_, err := PreparePublication(nil, snapshots, publicationFor(group), true)
			require.ErrorIs(t, err, ErrInvalid)
		}
	}
}

func TestOntologyManagerOverridesSurviveReprocessing(t *testing.T) {
	definition := topicRecord("person")
	definition.Kind = EntityClass
	definition.Definition.BaseEntityKind = "person"
	definition.Version = 1
	pin := Record{ID: policyID("pin"), Kind: OverrideKind, Override: &Override{Action: PinDefinition, TargetID: definition.ID}, Version: 1}
	catalog := policyCatalog(t, definition, pin)
	changed := definition
	copy := *definition.Definition
	changed.Definition = &copy
	changed.Definition.Label = "Renamed person"
	input := publicationFor(changed)
	input.Changes[0].ExpectedVersion = 1
	_, err := PreparePublication(catalog, nil, input, true)
	require.ErrorIs(t, err, ErrOverride)
	_, err = PreparePublication(catalog, nil, input, false)
	require.NoError(t, err)
	retired := pin
	retired.Retired = true
	_, err = PreparePublication(catalog, nil, Publication{OperationKey: "unpin", Reason: "remove pin", Changes: []Change{{ExpectedVersion: 1, Record: retired}, {ExpectedVersion: 1, Record: changed}}}, false)
	require.NoError(t, err)
	_, err = PreparePublication(nil, nil, publicationFor(pin), true)
	require.ErrorIs(t, err, ErrOverride)
	first := policySnapshot("a", EvidenceSource, "same complete meaning")
	second := policySnapshot("b", EvidenceSource, first.MeaningKey)
	separation := Record{ID: policyID("separation"), Version: 1, Kind: OverrideKind, Override: &Override{Action: KeepSeparate, Members: []SourceHandle{first.SourceHandle, second.SourceHandle}}}
	first.Version = 2
	second.Version = 2
	group := Record{ID: policyID("joined"), Kind: EvidenceGroup, Group: &Group{Members: []SourceHandle{first.SourceHandle, second.SourceHandle}}, Sources: []SourceDependency{sourceDependency(t, first), sourceDependency(t, second)}}
	_, err = PreparePublication(map[string]Record{separation.ID: separation}, map[string]SourceSnapshot{SourceKey(first.SourceHandle): first, SourceKey(second.SourceHandle): second}, publicationFor(group), true)
	require.ErrorIs(t, err, ErrOverride)
	group.Version = 1
	retiredGroup := group
	retiredGroup.Retired = true
	retirement := Publication{OperationKey: "retire-separated-group", Reason: "honor manager separation", Changes: []Change{{ExpectedVersion: group.Version, Record: retiredGroup}}}
	_, err = PreparePublication(map[string]Record{separation.ID: separation, group.ID: group}, nil, retirement, true)
	require.NoError(t, err)
	grouping := separation
	grouping.Override = &Override{Action: GroupTogether, Members: separation.Override.Members}
	_, err = PreparePublication(map[string]Record{grouping.ID: grouping, group.ID: group}, nil, retirement, true)
	require.ErrorIs(t, err, ErrOverride)
}

func TestOntologyFingerprintsAreStableAndSelective(t *testing.T) {
	first := policySnapshot("a", EvidenceSource, "same")
	second := policySnapshot("b", EvidenceSource, "same")
	group := Record{ID: policyID("group"), Kind: EvidenceGroup, Group: &Group{Members: []SourceHandle{first.SourceHandle, second.SourceHandle}}, Sources: []SourceDependency{sourceDependency(t, first), sourceDependency(t, second)}}
	snapshots := map[string]SourceSnapshot{SourceKey(first.SourceHandle): first, SourceKey(second.SourceHandle): second}
	one, err := RecordFingerprint(group, snapshots, nil)
	require.NoError(t, err)
	group.Group.Members[0], group.Group.Members[1] = group.Group.Members[1], group.Group.Members[0]
	group.Sources[0], group.Sources[1] = group.Sources[1], group.Sources[0]
	two, err := RecordFingerprint(group, snapshots, map[string]Record{policyID("unrelated"): topicRecord("unrelated")})
	require.NoError(t, err)
	require.Equal(t, one, two)
	second.State["qualification"] = "changed"
	snapshots[SourceKey(second.SourceHandle)] = second
	three, err := RecordFingerprint(group, snapshots, nil)
	require.NoError(t, err)
	require.NotEqual(t, one, three)
	first.Eligible = false
	snapshots[SourceKey(first.SourceHandle)] = first
	_, err = RecordFingerprint(group, snapshots, nil)
	require.ErrorIs(t, err, ErrSourceStale)
	group.Retired = true
	retired, err := RecordFingerprint(group, nil, nil)
	require.NoError(t, err)
	group.Fingerprint = retired
	again, err := RecordFingerprint(group, nil, nil)
	require.NoError(t, err)
	require.Equal(t, retired, again)
}

func TestOntologyOverrideApplicabilityFollowsActionAndRecordKind(t *testing.T) {
	first, second := policySnapshot("a", EvidenceSource, "same"), policySnapshot("b", EvidenceSource, "same")
	topic := topicRecord("topic")
	assignment := Record{ID: policyID("assignment"), Kind: AssignmentKind, Assignment: &Assignment{Source: first.SourceHandle, DefinitionID: topic.ID}, Sources: []SourceDependency{sourceDependency(t, first)}}
	group := Record{ID: policyID("group"), Kind: EvidenceGroup, Group: &Group{Members: []SourceHandle{first.SourceHandle, second.SourceHandle}}}
	separation := Record{ID: policyID("separation"), Kind: OverrideKind, Override: &Override{Action: KeepSeparate, Members: group.Group.Members}}
	classification := Record{ID: policyID("classification"), Kind: OverrideKind, Override: &Override{Action: SetClassification, Members: []SourceHandle{first.SourceHandle}, DefinitionID: topic.ID}}
	pin := Record{ID: policyID("pin"), Kind: OverrideKind, Override: &Override{Action: PinDefinition, TargetID: topic.ID}}
	catalog := map[string]Record{separation.ID: separation, classification.ID: classification, pin.ID: pin}
	require.Equal(t, []Record{classification}, ApplicableOverrides(assignment, catalog))
	require.Equal(t, []Record{separation}, ApplicableOverrides(group, catalog))
	require.Equal(t, []Record{pin}, ApplicableOverrides(topic, catalog))
	snapshots := map[string]SourceSnapshot{SourceKey(first.SourceHandle): first}
	one, err := RecordFingerprint(assignment, snapshots, map[string]Record{topic.ID: topic})
	require.NoError(t, err)
	two, err := RecordFingerprint(assignment, snapshots, map[string]Record{topic.ID: topic, separation.ID: separation})
	require.NoError(t, err)
	require.Equal(t, one, two)
}

func TestOntologyPublicationEnforcesAggregateBounds(t *testing.T) {
	input := publicationFor(topicRecord("escaped"))
	input.Changes[0].Record.Definition.Description = strings.Repeat("\x01", MaxPublicationBytes)
	require.ErrorIs(t, ValidatePublication(input), ErrInvalid)
	input = Publication{OperationKey: "bounded", Reason: "aggregate dependencies"}
	for i := 0; i < 5; i++ {
		record := topicRecord(uuid.NewString())
		for j := 0; j < MaxMembers; j++ {
			record.Dependencies = append(record.Dependencies, RevisionRef{ID: uuid.NewString(), Version: 1})
		}
		input.Changes = append(input.Changes, Change{Record: record})
	}
	require.ErrorIs(t, ValidatePublication(input), ErrInvalid)
}

func TestOntologySeedingPreservesExistingKinds(t *testing.T) {
	predicate := policySnapshot("predicate", PredicateSource, "")
	predicate.ID = "uses"
	first, err := SeedRecords(predicate.TeamID, []SourceSnapshot{predicate})
	require.NoError(t, err)
	require.Len(t, first, 9)
	second, err := SeedRecords(predicate.TeamID, []SourceSnapshot{predicate})
	require.NoError(t, err)
	require.Equal(t, first, second)
	for _, record := range first {
		require.NoError(t, ValidateRecord(record))
	}
	predicate.Eligible = false
	_, err = SeedRecords(predicate.TeamID, []SourceSnapshot{predicate})
	require.ErrorIs(t, err, ErrSourceStale)
}
