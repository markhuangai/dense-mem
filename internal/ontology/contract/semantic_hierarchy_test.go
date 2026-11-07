package contract

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func semanticDefinition(key string, kind Kind, parent string, relation ParentRelation) Record {
	record := topicRecord(key)
	record.Kind = kind
	record.Definition.ParentID, record.Definition.ParentRelation = parent, relation
	if kind == EntityClass {
		record.Definition.BaseEntityKind = "person"
	}
	return record
}

func TestOntologySemanticHierarchyLegacyCompatibility(t *testing.T) {
	const childID = "00000000-0000-4000-8000-000000000001"
	const parentID = "00000000-0000-4000-8000-000000000002"
	const actorID = "00000000-0000-4000-8000-000000000009"
	const legacy = `{"key":"person","label":"Person","description":"The person class","aliases":["human"],"parent_id":"00000000-0000-4000-8000-000000000002","base_entity_kind":"person"}`
	var definition Definition
	require.NoError(t, json.Unmarshal([]byte(legacy), &definition))
	require.Empty(t, definition.ParentRelation)
	body, err := json.Marshal(definition)
	require.NoError(t, err)
	require.Equal(t, legacy, string(body))
	child := Record{ID: childID, Version: 3, Kind: EntityClass, Definition: &definition}
	parent := semanticDefinition("parent", EntityClass, "", "")
	parent.ID, parent.Version = parentID, 2
	catalog := map[string]Record{parent.ID: parent}
	fingerprint, err := RecordFingerprint(child, nil, catalog)
	require.NoError(t, err)
	require.Equal(t, "sha256:33b828cd907530a582d5ef6bc8bfffbdf56376ad214898be81865f27f4f7ac7b", fingerprint)
	input := Publication{OperationKey: "legacy-publication", ExpectedRevision: 7, Reason: "Legacy compatibility", Changes: []Change{{ExpectedVersion: 3, Record: child}}}
	hash, err := RequestHash(input, "manager", actorID)
	require.NoError(t, err)
	require.Equal(t, "sha256:9b591f643cee0d49a4e856f527cf01728b60ad827ba3fdb6bbd11869723616ca", hash)
	definition.ParentRelation = SubclassOf
	semantic, err := RecordFingerprint(child, nil, catalog)
	require.NoError(t, err)
	require.NotEqual(t, fingerprint, semantic)
	semanticHash, err := RequestHash(input, "manager", actorID)
	require.NoError(t, err)
	require.NotEqual(t, hash, semanticHash)
	require.Equal(t, "ontology-source-v1", FingerprintVersion)
}

func TestOntologySemanticHierarchyRelationValidation(t *testing.T) {
	for _, kind := range []Kind{EntityClass, PredicateConcept, Topic} {
		for _, relation := range []ParentRelation{"", SubclassOf, SubpropertyOf, "organizational", "SUBCLASS_OF"} {
			for _, withParent := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/parent=%t", kind, relation, withParent), func(t *testing.T) {
					parent := ""
					if withParent {
						parent = policyID("parent")
					}
					record := semanticDefinition("child", kind, parent, relation)
					valid := relation == "" || withParent && ((kind == EntityClass && relation == SubclassOf) || (kind == PredicateConcept && relation == SubpropertyOf))
					err := ValidateRecord(record)
					if valid {
						require.NoError(t, err)
						body, err := json.Marshal(publicationFor(record))
						require.NoError(t, err)
						_, err = DecodePublication(strings.NewReader(string(body)))
						require.NoError(t, err)
					} else {
						require.ErrorIs(t, err, ErrInvalid)
						var rejection *PublicationRejection
						require.ErrorAs(t, err, &rejection)
						require.NotEmpty(t, rejection.Reason)
					}
				})
			}
		}
	}
}

func TestOntologySemanticHierarchyBoundsAndReasons(t *testing.T) {
	for _, kind := range []Kind{EntityClass, PredicateConcept} {
		relation := SubclassOf
		if kind == PredicateConcept {
			relation = SubpropertyOf
		}
		catalog := map[string]Record{}
		parent := ""
		for index := 0; index <= MaxParentDepth; index++ {
			record := semanticDefinition(fmt.Sprintf("%s-%d", kind, index), kind, parent, "")
			if parent != "" {
				record.Definition.ParentRelation = relation
			}
			catalog[record.ID], parent = record, record.ID
		}
		require.NoError(t, ValidateDefinitions(catalog))
		tooDeep := semanticDefinition(string(kind)+"-deep", kind, parent, relation)
		catalog[tooDeep.ID] = tooDeep
		var rejection *PublicationRejection
		require.ErrorAs(t, ValidateDefinitions(catalog), &rejection)
		require.Equal(t, "hierarchy_depth", rejection.Reason)
		delete(catalog, tooDeep.ID)
		root := catalog[policyID(fmt.Sprintf("%s-0", kind))]
		root.Definition.ParentID, root.Definition.ParentRelation = root.ID, relation
		require.ErrorAs(t, ValidateDefinitions(map[string]Record{root.ID: root}), &rejection)
		require.Equal(t, "hierarchy_cycle", rejection.Reason)
	}
	parent := semanticDefinition("parent", EntityClass, "", "")
	parent.Definition.BaseEntityKind = "project"
	child := semanticDefinition("child", EntityClass, parent.ID, SubclassOf)
	var rejection *PublicationRejection
	require.ErrorAs(t, ValidateDefinitions(map[string]Record{parent.ID: parent, child.ID: child}), &rejection)
	require.Equal(t, "class_parent_kind", rejection.Reason)
	require.Equal(t, "person", rejection.Expected)
	require.Equal(t, "project", rejection.Observed)
}

func TestOntologySemanticHierarchyInvalidatesOnlyDependents(t *testing.T) {
	root := semanticDefinition("root", EntityClass, "", "")
	child := semanticDefinition("child", EntityClass, root.ID, "")
	unrelated := semanticDefinition("unrelated", EntityClass, "", "")
	prepared, err := PreparePublication(nil, nil, publicationFor(root, child, unrelated), false)
	require.NoError(t, err)
	catalog := map[string]Record{}
	for _, record := range prepared {
		catalog[record.ID] = record
	}
	before := catalog[child.ID]
	updated := before
	definition := *updated.Definition
	definition.ParentRelation = SubclassOf
	updated.Definition = &definition
	input := Publication{OperationKey: "declare-semantics", Reason: "Declare an explicit class relation", Changes: []Change{{ExpectedVersion: 1, Record: updated}}}
	changed, err := PreparePublication(catalog, nil, input, false)
	require.NoError(t, err)
	require.NotEqual(t, before.Fingerprint, changed[0].Fingerprint)
	require.Contains(t, changed[0].Dependencies, RevisionRef{ID: root.ID, Version: 1})
	catalog[child.ID] = changed[0]
	fingerprint, err := RecordFingerprint(catalog[unrelated.ID], nil, catalog)
	require.NoError(t, err)
	require.Equal(t, catalog[unrelated.ID].Fingerprint, fingerprint)
}
