package memoryservice

import "testing"

func TestV2ReviewSourceProposalWithTrustedConflictContextsCopiesByRefAndShape(t *testing.T) {
	stored := map[string]any{
		"relationship_hints": []map[string]any{
			{
				"proposal_id": "rel-postgres",
				"subject_ref": "project",
				"predicate":   "Primary Database",
				"object_ref":  "postgres",
				"conflict_context": map[string]any{
					"conflict_id":      "00000000-0000-0000-0000-000000000101",
					"expected_version": 3,
				},
			},
			{
				"proposal_id": "rel-region",
				"subject_ref": "project",
				"predicate":   "deployment_region",
				"object_value": map[string]any{
					"type":  "string",
					"value": "us-east-1",
				},
				"conflict_context": map[string]any{
					"conflict_id":      "00000000-0000-0000-0000-000000000102",
					"expected_version": 4,
				},
			},
		},
	}
	contexts := v2ReviewSourceConflictContexts(stored)
	if len(contexts) != 2 {
		t.Fatalf("contexts = %#v", contexts)
	}
	if contexts[0].PredicateKey != "primary_database" || contexts[1].ObjectValueKey == "" {
		t.Fatalf("normalized contexts = %#v", contexts)
	}

	provider := map[string]any{
		"relationships": []map[string]any{
			{
				"proposal_id": "rel-postgres",
				"subject_ref": "project",
				"predicate":   "primary_database",
				"object_ref":  "postgres",
			},
			{
				"subject_ref": "project",
				"predicate":   "deployment_region",
				"object_value": map[string]any{
					"type":  "string",
					"value": "us-east-1",
				},
			},
		},
	}

	out := v2ReviewSourceProposalWithTrustedConflictContexts(provider, contexts)
	relationships := v2PlacementReviewObjectArray(out, "relationships")
	first, ok := v2PlacementReviewConflictContext(relationships[0])
	if !ok || first.ConflictID != "00000000-0000-0000-0000-000000000101" || first.ExpectedVersion != 3 {
		t.Fatalf("first conflict context = %#v ok=%v", first, ok)
	}
	second, ok := v2PlacementReviewConflictContext(relationships[1])
	if !ok || second.ConflictID != "00000000-0000-0000-0000-000000000102" || second.ExpectedVersion != 4 {
		t.Fatalf("second conflict context = %#v ok=%v", second, ok)
	}
}

func TestV2ReviewSourceMatchConflictContextFallbackAndAmbiguity(t *testing.T) {
	contexts := []v2ReviewSourceConflictContext{
		{
			Ref:          "relationship:0",
			SubjectRef:   "project",
			PredicateKey: "primary_database",
			ObjectRef:    "postgres",
		},
	}
	index, ok := v2ReviewSourceMatchConflictContext(map[string]any{
		"subject_ref": "other",
		"predicate":   "other",
		"object_ref":  "other",
	}, contexts, map[int]struct{}{}, 1)
	if !ok || index != 0 {
		t.Fatalf("fallback match index=%d ok=%v", index, ok)
	}

	ambiguous := []v2ReviewSourceConflictContext{
		{SubjectRef: "project", PredicateKey: "primary_database", ObjectRef: "postgres"},
		{SubjectRef: "project", PredicateKey: "primary_database", ObjectRef: "postgres"},
	}
	_, ok = v2ReviewSourceMatchConflictContext(map[string]any{
		"subject_ref": "project",
		"predicate":   "primary_database",
		"object_ref":  "postgres",
	}, ambiguous, map[int]struct{}{}, 2)
	if ok {
		t.Fatalf("ambiguous conflict context matched")
	}

	if v2ReviewSourceConflictContextMatches(
		v2ReviewSourceConflictContext{SubjectRef: "project", PredicateKey: "primary_database", ObjectRef: "postgres"},
		v2ReviewSourceConflictContext{SubjectRef: "project", PredicateKey: "primary_database", ObjectRef: "graphdb"},
	) {
		t.Fatalf("mismatched object_ref matched")
	}
}
