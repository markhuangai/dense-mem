package service

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEvidenceOnlyUnitCanonicalHashRejectsMalformedEvidence(t *testing.T) {
	for _, value := range []any{func() {}, []any{"not an object"}} {
		_, err := CanonicalRequestBodyHash(value)
		require.Error(t, err)
	}
	nilObjects, err := canonicalRememberObjects(json.RawMessage("null"))
	require.NoError(t, err)
	require.Empty(t, nilObjects)
	nilHash, err := CanonicalRequestBodyHash(nil)
	require.NoError(t, err)
	emptyHash, err := CanonicalRequestBodyHash([]map[string]any{})
	require.NoError(t, err)
	require.Equal(t, nilHash, emptyHash)
}

func TestEvidenceOnlyUnitCanonicalHashNormalizesOnlyUnorderedProvenance(t *testing.T) {
	first := []map[string]any{{"content": "  exact café 🧭\n", "source_type": " document ", "source_key": " key ", "source_revision": " rev ", "labels": []string{"b", "a"}, "supersedes_evidence_ids": []string{"two", "one"}, "metadata": map[string]any{}}}
	second := []map[string]any{{"content": "  exact café 🧭\n", "source_type": "document", "source_key": "key", "source_revision": "rev", "labels": []string{"a", "b"}, "supersedes_evidence_ids": []string{"one", "two"}}}
	a, err := CanonicalRequestBodyHash(first)
	require.NoError(t, err)
	b, err := CanonicalRequestBodyHash(second)
	require.NoError(t, err)
	require.Equal(t, a, b)
	for _, field := range []string{"content", "source_revision", "source_key"} {
		changed := map[string]any{}
		for k, v := range second[0] {
			changed[k] = v
		}
		changed[field] = "changed"
		hash, err := CanonicalRequestBodyHash([]map[string]any{changed})
		require.NoError(t, err)
		require.NotEqual(t, a, hash)
	}
}
