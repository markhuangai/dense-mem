package postgres

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMigrationRewritesStructuralReferencesWithoutChangingQuotes(t *testing.T) {
	original, next := uuid.NewString(), uuid.NewString()
	body := map[string]json.RawMessage{}
	body["evidence"], _ = json.Marshal([]map[string]any{{"evidence_id": original, "quote": original, "span_start": 0}})
	refs, _ := json.Marshal(map[string]string{original: next})
	require.NoError(t, applyPrivateMigrationReferences(&privateMigrationMove{Table: "relationship_observations", Replacements: map[string]json.RawMessage{"_historical_ids": refs}}, body))
	var evidence []map[string]any
	require.NoError(t, json.Unmarshal(body["evidence"], &evidence))
	require.Equal(t, next, evidence[0]["evidence_id"])
	require.Equal(t, original, evidence[0]["quote"])
	require.Equal(t, []string{next}, privateMigrationReferenceIDs(evidenceAsAny(t, body["evidence"])))
}
func evidenceAsAny(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var value any
	require.NoError(t, json.Unmarshal(raw, &value))
	return value
}
