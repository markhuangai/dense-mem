package recallservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAssertionSearcherReturnsTypedPathsAndDeduplicatedFrontier(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	reader := &sequenceRecallScopedReader{rowSets: [][]map[string]any{
		{
			assertionSearchRow("assertion-1", "mark", "person", "entity:dense-mem", "dense-mem", "project", "Dense-Mem", now),
			assertionValueSearchRow(now),
			{"assertion_id": ""},
		},
		{
			{
				"from_entity_id": "mark", "direction": "outgoing", "relationship_type": "DEMOED", "assertion_id": "assertion-3",
				"tier": "validated_claim", "neighbor_key": "entity:conference", "neighbor_id": "conference", "neighbor_type": "event", "neighbor_name": "Demo Day",
			},
			{
				"from_entity_id": "mark", "direction": "outgoing", "relationship_type": "DEMOED", "assertion_id": "assertion-3",
				"tier": "validated_claim", "neighbor_key": "entity:conference", "neighbor_id": "conference", "neighbor_type": "event", "neighbor_name": "Demo Day",
			},
			{"from_entity_id": "", "assertion_id": "ignored"},
		},
	}}
	searcher := NewAssertionSearcher(reader)

	got, err := searcher.SearchActive(context.Background(), "team-a", "Dense/Mem", []float32{0.1, 0.2}, 2, &now, &now)

	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "assertion-1", got[0].Assertion.AssertionID)
	require.Equal(t, "WORKS_ON", got[0].Path.Edges[0].Relationship)
	require.Equal(t, []string{"fragment-1"}, got[0].Path.Edges[0].EvidenceIDs)
	require.Len(t, got[0].Frontier, 1)
	require.Equal(t, "assertion-3", got[0].Frontier[0].AssertionID)
	require.NotNil(t, got[1].Assertion.ObjectValue)
	require.Equal(t, "42", got[1].Assertion.ObjectValue.Value)
	require.Equal(t, "42 ms", got[1].Assertion.ObjectValue.Display)
	require.Equal(t, "ms", got[1].Assertion.ObjectValue.Unit)
	require.Equal(t, "Dense Mem", reader.params[0]["searchQuery"])
	require.Equal(t, int64(4), reader.params[0]["candidateLimit"])
	require.Equal(t, &now, reader.params[0]["validAt"])
	require.Equal(t, []string{"assertion-1", "assertion-2"}, reader.params[1]["assertionIds"])
	require.NotContains(t, reader.params[1]["entityIds"], "value-1")
	require.Contains(t, reader.queries[0], "node.status = 'active'")
	require.Contains(t, reader.queries[0], "END AS object_value")
	require.Contains(t, reader.queries[1], "CALL {")
	require.Contains(t, reader.queries[1], "RETURN from_entity_id")
}

func TestAssertionSearcherValidationEmptyAndErrorPaths(t *testing.T) {
	_, err := NewAssertionSearcher(nil).SearchActive(context.Background(), "team-a", "query", []float32{1}, 1, nil, nil)
	require.ErrorContains(t, err, "reader is required")
	_, err = (*assertionSearcher)(nil).SearchActive(context.Background(), "team-a", "query", []float32{1}, 1, nil, nil)
	require.ErrorContains(t, err, "reader is required")

	reader := &sequenceRecallScopedReader{}
	searcher := NewAssertionSearcher(reader)
	for _, input := range []struct {
		profile   string
		query     string
		embedding []float32
	}{{query: "query", embedding: []float32{1}}, {profile: "team-a", embedding: []float32{1}}, {profile: "team-a", query: "query"}} {
		got, callErr := searcher.SearchActive(context.Background(), input.profile, input.query, input.embedding, 1, nil, nil)
		require.NoError(t, callErr)
		require.Empty(t, got)
	}
	require.Empty(t, reader.queries)

	reader = &sequenceRecallScopedReader{errs: []error{errors.New("search failed")}}
	_, err = NewAssertionSearcher(reader).SearchActive(context.Background(), "team-a", "query", []float32{1}, 1, nil, nil)
	require.ErrorContains(t, err, "search failed")

	reader = &sequenceRecallScopedReader{rowSets: [][]map[string]any{{assertionSearchRow("assertion-1", "mark", "person", "entity:dense-mem", "dense-mem", "project", "Dense-Mem", time.Now())}}, errs: []error{nil, errors.New("frontier failed")}}
	_, err = NewAssertionSearcher(reader).SearchActive(context.Background(), "team-a", "query", []float32{1}, 0, nil, nil)
	require.ErrorContains(t, err, "frontier failed")
	require.Equal(t, int64(DefaultLimit), reader.params[0]["limit"])

	bad := assertionSearchRow("assertion-1", "mark", "person", "entity:dense-mem", "dense-mem", "project", "Dense-Mem", time.Now())
	bad["evidence_json"] = `{`
	reader = &sequenceRecallScopedReader{rowSets: [][]map[string]any{{bad}, {}}}
	results, err := NewAssertionSearcher(reader).SearchActive(context.Background(), "team-a", "query", []float32{1}, 1, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Empty(t, results[0].Assertion.Evidence)
	require.Equal(t, []string{"fragment-1"}, results[0].Path.Edges[0].EvidenceIDs)

	require.Equal(t, 2, minInt(2, 3))
	require.Equal(t, 2, minInt(3, 2))
}

func assertionSearchRow(assertionID, subjectID, subjectType, objectKey, objectID, objectType, objectName string, now time.Time) map[string]any {
	return map[string]any{
		"assertion_id": assertionID, "owner_profile_id": "profile-a", "subject_entity_id": subjectID, "predicate_key": "works_on",
		"relationship_type": "WORKS_ON", "object_entity_id": objectID, "tier": "fact", "status": "active", "policy_family": "versioned",
		"polarity": "+", "modality": "assertion", "recorded_at": now, "created_at": now, "updated_at": now,
		"extract_conf": 0.9, "resolution_conf": 0.9, "source_quality": 0.8, "support_count": int64(1), "source_group_count": int64(1),
		"evidence_json": `[{"fragment_id":"fragment-1","start":0,"end":4,"source_group":"source-1"}]`,
		"subject_id":    subjectID, "subject_name": "Mark", "subject_type": subjectType,
		"object_key": objectKey, "object_id": objectID, "object_name": objectName, "object_type": objectType,
		"evidence_ids": []string{"fragment-1"}, "score": 0.91,
	}
}

func assertionValueSearchRow(now time.Time) map[string]any {
	row := assertionSearchRow("assertion-2", "latency", "metric", "value:value-1", "value-1", "value:number", "42 ms", now)
	row["object_entity_id"] = ""
	row["object_value_id"] = "value-1"
	row["object_value"] = "42"
	row["object_display"] = "42 ms"
	row["object_unit"] = "ms"
	return row
}
