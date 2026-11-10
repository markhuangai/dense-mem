package service

import (
	"encoding/json"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEvidenceOnlyUnitRememberRejectsRetiredFieldsAndBindsV3Hash(t *testing.T) {
	raw := `{"target_space":"team_shared","idempotency_key":"key","evidence":[{"content":"Exact original Evidence."}]}`
	var request RememberRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &request))
	original, err := CanonicalRequestHash(request)
	require.NoError(t, err)
	request.IdempotencyKey = "another-key"
	same, err := CanonicalRequestHash(request)
	require.NoError(t, err)
	require.Equal(t, original, same)
	request.TargetSpace = domain.MemorySpacePrivate
	private, err := CanonicalRequestHash(request)
	require.NoError(t, err)
	require.NotEqual(t, original, private)
	request.TargetSpace = domain.MemorySpaceTeamShared
	request.Evidence[0].Content = "Changed Evidence."
	changed, err := CanonicalRequestHash(request)
	require.NoError(t, err)
	require.NotEqual(t, original, changed)
	var object map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &object))
	for _, field := range []string{"relationships", "entities", "values", "predicates", "proposal", "known_relationship_ids"} {
		t.Run(field, func(t *testing.T) {
			candidate := map[string]any{}
			for key, value := range object {
				candidate[key] = value
			}
			candidate[field] = []any{}
			body, err := json.Marshal(candidate)
			require.NoError(t, err)
			var invalid RememberRequest
			require.Error(t, json.Unmarshal(body, &invalid))
		})
	}
	require.Equal(t, "dense-mem.v3.0.0", domain.ContractVersion)
}
