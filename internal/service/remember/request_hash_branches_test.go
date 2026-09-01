package remember

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalRememberHashRejectsInvalidContractInputs(t *testing.T) {
	_, err := canonicalRequestBodyHashForContract(" ", nil, nil, nil)
	require.ErrorContains(t, err, "contract version is required")
	_, err = canonicalRequestBodyHashForContract("v2", func() {}, nil, nil)
	require.Error(t, err)
	_, err = canonicalRequestBodyHashForContract("v2", []any{"not an object"}, nil, nil)
	require.Error(t, err)
	_, err = canonicalRequestHashForVersion(RememberRequest{}, " ")
	require.ErrorContains(t, err, "contract version is required")

	hash, err := canonicalRequestBodyHashForContract(" v2 ", nil, nil, nil)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hash, "sha256:"))
	objects, err := canonicalRememberObjects([]map[string]any(nil))
	require.NoError(t, err)
	require.Empty(t, objects)
}

func TestCanonicalRememberHashNormalizesNestedSetsAndReferences(t *testing.T) {
	evidence := []map[string]any{{
		"source_type": " document ", "source": " notes ", "source_group": " group ", "authority": " primary ",
		"source_key": " key ", "source_revision": " rev ", "previous_source_revision": " previous ",
		"supersedes_evidence_ids": []any{" b ", "a "}, "labels": []any{" z ", "a "}, "metadata": map[string]any{},
	}}
	entities := []map[string]any{
		{"ref": " z ", "entity_kind": " concept ", "known_entity_id": " id-z "},
		{"ref": " a ", "entity_id": " id-a "},
	}
	relationships := []map[string]any{{
		"ref": " rel ", "polarity": " + ", "valid_from": " 2026-01-01 ", "valid_to": " 2026-01-02 ",
		"evidence_indices":  []any{2, 1},
		"subject":           map[string]any{"ref": " subject ", "entity_kind": " concept "},
		"predicate":         map[string]any{"proposed_key": " uses ", "known_predicate_key": " exact "},
		"object":            map[string]any{"entity": map[string]any{"ref": " object "}},
		"correction_target": map[string]any{"relationship_id": " relationship "},
		"conflict_context":  map[string]any{"conflict_id": " conflict "},
		"client_comment":    nil,
	}}
	hash, err := canonicalRequestBodyHashForContract("contract", evidence, entities, relationships)
	require.NoError(t, err)
	reorderedEntities := []map[string]any{entities[1], entities[0]}
	reorderedRelationships := []map[string]any{relationships[0]}
	reorderedRelationships[0]["evidence_indices"] = []any{1, 2}
	reorderedHash, err := canonicalRequestBodyHashForContract("contract", evidence, reorderedEntities, reorderedRelationships)
	require.NoError(t, err)
	require.Equal(t, hash, reorderedHash)

	valueRelationship := map[string]any{
		"ref": "value", "object": map[string]any{"value": map[string]any{"type": " string "}},
		"subject": map[string]any{}, "predicate": map[string]any{},
	}
	_, err = canonicalRequestBodyHashForContract("contract", evidence, nil, []map[string]any{valueRelationship})
	require.NoError(t, err)
}

func TestCanonicalRememberHashHelperBranches(t *testing.T) {
	var nilMap map[string]any
	canonicalRememberEntity(nilMap)
	canonicalRememberTrimString(nilMap, "field", true)
	item := map[string]any{
		"missing": nil, "nil_value": nil, "number": 1, "empty": " ", "keep_empty": " ",
	}
	canonicalRememberTrimString(item, "missing", true)
	canonicalRememberTrimString(item, "nil_value", true)
	canonicalRememberTrimString(item, "number", true)
	canonicalRememberTrimString(item, "empty", true)
	canonicalRememberTrimString(item, "keep_empty", false)
	require.NotContains(t, item, "empty")
	require.Equal(t, "", item["keep_empty"])

	item["strings"] = []any{" b ", "a "}
	canonicalRememberStringSet(item, "strings")
	require.Equal(t, []any{"a", "b"}, item["strings"])
	item["empty_set"] = []any{}
	canonicalRememberStringSet(item, "empty_set")
	item["wrong_set"] = "not-an-array"
	canonicalRememberStringSet(item, "wrong_set")
	item["scalars"] = []any{float64(2), "1"}
	canonicalRememberScalarSet(item, "scalars")
	item["wrong_scalars"] = "not-an-array"
	canonicalRememberScalarSet(item, "wrong_scalars")

	require.NotEmpty(t, canonicalRememberValueOrder(func() {}))
	require.True(t, canonicalRememberObjectOrder(map[string]any{"ref": "a"}, map[string]any{"ref": "b"}, "ref"))
	require.True(t, canonicalRememberObjectOrder(map[string]any{"ref": "a", "x": 1}, map[string]any{"ref": "a", "x": 2}, "ref"))
	require.Nil(t, canonicalRememberMap("not-an-object"))

	item["empty_map"] = map[string]any{}
	canonicalRememberDropEmptyMap(item, "empty_map")
	require.NotContains(t, item, "empty_map")
	item["nonempty_map"] = map[string]any{"value": true}
	canonicalRememberDropEmptyMap(item, "nonempty_map")
	item["nil_field"] = nil
	canonicalRememberDropNil(item, "nil_field")
	require.NotContains(t, item, "nil_field")

	value, err := canonicalRememberObjects(json.RawMessage("null"))
	require.NoError(t, err)
	require.Empty(t, value)
}
