package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/assertionservice"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

func TestAssertionStoreWriteBundleCreatesDynamicActiveProjection(t *testing.T) {
	client := &recordingClient{resultRecordsFor: assertionStoreRecords}
	store := NewAssertionStore(NewProfileScopeEnforcer(client))

	result, err := store.WriteBundle(context.Background(), "team-a", assertionStoreBundle(domain.AssertionStatusActive))

	require.NoError(t, err)
	require.Empty(t, result.Superseded)
	require.True(t, hasQuery(client.queries, "MERGE (predicate:Predicate"))
	require.True(t, hasQuery(client.queries, "MERGE (fragment)-[mention:MENTIONS"))
	require.True(t, hasQuery(client.queries, "[projection:$($relationshipType)]"))
	require.True(t, hasQuery(client.queries, "SUPPORTED_BY"))
	require.True(t, hasQuery(client.queries, "SUPERSEDED_BY"))

	projectionParams := paramsForQuery(t, client, "[projection:$($relationshipType)]")
	require.Equal(t, "WORKS_ON", projectionParams["relationshipType"])
	require.Equal(t, "entity:dense-mem", projectionParams["objectGraphKey"])
	properties, ok := projectionParams["properties"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "assertion-1", properties["assertion_id"])
	require.Equal(t, "team-a", properties["team_id"])
	require.Equal(t, "active", properties["status"])
	for i := range client.params {
		require.Equalf(t, "team-a", client.params[i]["profileId"], "query %d was not team scoped", i)
	}
}

func TestAssertionStoreSerializesOptionalTimesForNeo4j(t *testing.T) {
	t.Run("nil and zero values become null", func(t *testing.T) {
		bundle := assertionStoreBundle(domain.AssertionStatusActive)
		zero := time.Time{}
		bundle.Assertions[0].ValidTo = &zero
		client := &recordingClient{resultRecordsFor: assertionStoreRecords}

		_, err := NewAssertionStore(NewProfileScopeEnforcer(client)).WriteBundle(context.Background(), "team-a", bundle)

		require.NoError(t, err)
		assertionParams := paramsForQuery(t, client, "MERGE (assertion:Assertion")
		require.Nil(t, assertionParams["validFrom"])
		require.Nil(t, assertionParams["validTo"])
		require.Nil(t, assertionParams["recordedTo"])
		require.Nil(t, paramsForQuery(t, client, "MATCH (current:Assertion")["validFrom"])
	})

	t.Run("concrete values become UTC time values", func(t *testing.T) {
		bundle := assertionStoreBundle(domain.AssertionStatusActive)
		from := time.Date(2026, 7, 10, 12, 0, 0, 0, time.FixedZone("offset", 2*60*60))
		to := from.Add(time.Hour)
		recordedTo := to.Add(time.Hour)
		bundle.Assertions[0].ValidFrom = &from
		bundle.Assertions[0].ValidTo = &to
		bundle.Assertions[0].RecordedTo = &recordedTo
		client := &recordingClient{resultRecordsFor: assertionStoreRecords}

		_, err := NewAssertionStore(NewProfileScopeEnforcer(client)).WriteBundle(context.Background(), "team-a", bundle)

		require.NoError(t, err)
		assertionParams := paramsForQuery(t, client, "MERGE (assertion:Assertion")
		require.Equal(t, from.UTC(), assertionParams["validFrom"])
		require.Equal(t, to.UTC(), assertionParams["validTo"])
		require.Equal(t, recordedTo.UTC(), assertionParams["recordedTo"])
		require.Equal(t, from.UTC(), paramsForQuery(t, client, "MATCH (current:Assertion")["validFrom"])
		for _, key := range []string{"validFrom", "validTo", "recordedTo"} {
			require.IsType(t, time.Time{}, assertionParams[key])
		}
	})
}

func TestAssertionStoreWriteBundleKeepsInactiveAssertionsOffSemanticGraph(t *testing.T) {
	for _, status := range []domain.AssertionStatus{
		domain.AssertionStatusNeedsReview,
		domain.AssertionStatusQuarantined,
		domain.AssertionStatusRejected,
	} {
		t.Run(string(status), func(t *testing.T) {
			client := &recordingClient{}
			store := NewAssertionStore(NewProfileScopeEnforcer(client))

			_, err := store.WriteBundle(context.Background(), "team-a", assertionStoreBundle(status))

			require.NoError(t, err)
			require.True(t, hasQuery(client.queries, "SUPPORTED_BY"))
			require.True(t, hasQuery(client.queries, "DELETE projection"))
			require.False(t, hasQuery(client.queries, "MERGE (predicate:Predicate"))
			require.False(t, hasQuery(client.queries, "MENTIONS"))
			require.False(t, hasQuery(client.queries, "[projection:$($relationshipType)]"))
			require.False(t, hasQuery(client.queries, "SUPERSEDED_BY"))
		})
	}
}

func TestAssertionStoreRejectsPredicateRegistryConflict(t *testing.T) {
	client := &recordingClient{}
	store := NewAssertionStore(NewProfileScopeEnforcer(client))

	_, err := store.WriteBundle(context.Background(), "team-a", assertionStoreBundle(domain.AssertionStatusActive))

	require.ErrorContains(t, err, "conflicts with its registered relationship type or policy family")
	require.False(t, hasQuery(client.queries, "[projection:$($relationshipType)]"))
}

func TestAssertionStoreReadsTypedValueAndEvidence(t *testing.T) {
	row := assertionStoreRow(domain.AssertionStatusActive)
	row["object_entity_id"] = ""
	row["object_value_id"] = "value-1"
	row["value_type"] = "number"
	row["value"] = "42"
	row["value_display"] = "42 ms"
	row["value_unit"] = "ms"
	client := &recordingClient{resultRecordsFor: func(query string) []*neo4j.Record {
		if strings.Contains(query, "OPTIONAL MATCH (value:Value") {
			return []*neo4j.Record{neo4jRecord(row)}
		}
		return nil
	}}
	store := NewAssertionStore(NewProfileScopeEnforcer(client))

	got, err := store.GetAssertion(context.Background(), "team-a", "assertion-1")

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "team-a", got.ProfileID)
	require.Equal(t, "assertion-1", got.AssertionID)
	require.Equal(t, domain.AssertionTierValidatedClaim, got.Tier)
	require.Len(t, got.Evidence, 1)
	require.NotNil(t, got.ObjectValue)
	require.Equal(t, "42", got.ObjectValue.Value)
	require.Equal(t, "42 ms", got.ObjectValue.Display)
	require.Equal(t, "ms", got.ObjectValue.Unit)
	require.Equal(t, "assertion-1", paramsForQuery(t, client, "OPTIONAL MATCH (value:Value")["assertionId"])
}

func TestAssertionStoreLegacyRefChecksAndFinalizeCardinality(t *testing.T) {
	t.Run("missing refs are returned with type", func(t *testing.T) {
		client := &recordingClient{resultRecordsFor: func(query string) []*neo4j.Record {
			if strings.Contains(query, "RETURN legacyRef.type AS type") {
				return []*neo4j.Record{
					neo4jRecord(map[string]any{"type": "claim", "id": "claim-2"}),
					neo4jRecord(map[string]any{"type": "fragment", "id": "fragment-1"}),
				}
			}
			return nil
		}}
		store := NewAssertionStore(NewProfileScopeEnforcer(client))

		missing, err := store.MissingLegacyRefs(context.Background(), "team-a", []domain.LegacyMemoryRef{
			{Type: "fragment", ID: "fragment-1"},
			{Type: "claim", ID: "claim-2"},
		})

		require.NoError(t, err)
		require.Equal(t, []string{"claim:claim-2", "fragment:fragment-1"}, missing)
		require.Equal(t, "team-a", paramsForQuery(t, client, "RETURN legacyRef.type AS type")["profileId"])
	})

	t.Run("finalize fails the transaction when every link was not created", func(t *testing.T) {
		client := &recordingClient{resultRecordsFor: func(query string) []*neo4j.Record {
			switch {
			case strings.Contains(query, "OPTIONAL MATCH (value:Value"):
				return []*neo4j.Record{neo4jRecord(assertionStoreRow(domain.AssertionStatusNeedsReview))}
			case strings.Contains(query, "MERGE (predicate:Predicate"):
				return []*neo4j.Record{neo4jRecord(map[string]any{"predicate_key": "works_on"})}
			case strings.Contains(query, "RETURN count(decomposed) AS linked"):
				return []*neo4j.Record{neo4jRecord(map[string]any{"linked": int64(0)})}
			default:
				return nil
			}
		}}
		store := NewAssertionStore(NewProfileScopeEnforcer(client))

		_, _, err := store.FinalizeLegacyMigration(
			context.Background(),
			"team-a",
			[]assertionservice.StateUpdate{{AssertionID: "assertion-1", Tier: domain.AssertionTierFact, Status: domain.AssertionStatusActive}},
			[]domain.LegacyMemoryRef{{Type: "fragment", ID: "legacy-1"}},
			time.Now().UTC(),
		)

		require.ErrorContains(t, err, "linked 0 of 1 legacy decompositions")
		require.True(t, hasQuery(client.queries, "[projection:$($relationshipType)]"))
		require.True(t, hasQuery(client.queries, "DECOMPOSED_INTO"))
	})
}

func TestAssertionStorePropagatesScopeAndDriverErrors(t *testing.T) {
	_, err := NewAssertionStore(nil).WriteBundle(context.Background(), "team-a", assertionStoreBundle(domain.AssertionStatusActive))
	require.ErrorContains(t, err, "profile scope is required")

	client := &recordingClient{runErrFor: func(query string) error {
		if strings.Contains(query, "MERGE (entity:Entity") {
			return errors.New("neo4j unavailable")
		}
		return nil
	}}
	_, err = NewAssertionStore(NewProfileScopeEnforcer(client)).WriteBundle(context.Background(), "team-a", assertionStoreBundle(domain.AssertionStatusActive))
	require.ErrorContains(t, err, "neo4j unavailable")
}

func assertionStoreBundle(status domain.AssertionStatus) assertionservice.Bundle {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	entities := []domain.Entity{
		{
			EntityID: "mark", ProfileID: "team-a", CanonicalName: "Mark", NormalizedName: "mark", EntityType: "person",
			ResolutionStatus: domain.EntityResolutionCanonical, ResolutionConf: 0.98, FirstSeenAt: now, LastSeenAt: now,
		},
		{
			EntityID: "dense-mem", ProfileID: "team-a", CanonicalName: "Dense-Mem", NormalizedName: "dense mem", EntityType: "project",
			ResolutionStatus: domain.EntityResolutionCanonical, ResolutionConf: 0.97, FirstSeenAt: now, LastSeenAt: now,
		},
	}
	assertion := domain.Assertion{
		AssertionID: "assertion-1", ProfileID: "team-a", OwnerProfileID: "profile-a", SubjectEntityID: "mark",
		PredicateKey: "works_on", RelationshipType: "WORKS_ON", ObjectEntityID: "dense-mem",
		Tier: domain.AssertionTierValidatedClaim, Status: status, PolicyFamily: domain.AssertionPolicyVersioned,
		Polarity: domain.PolarityPlus, Modality: domain.ModalityAssertion, RecordedAt: now,
		ExtractConf: 0.95, ResolutionConf: 0.96, SourceQuality: 0.9, SupportCount: 1, SourceGroupCount: 1,
		Evidence:  []domain.EvidenceSpan{{FragmentID: "fragment-1", Start: 0, End: 4, SourceGroup: "source-1"}},
		Embedding: []float32{0.1, 0.2}, EmbeddingModel: "embedding-model", ExtractionModel: "reviewer-model",
		ExtractionVersion: "v2", VerifierModel: "verifier-model", PipelineRunID: "run-1",
		ProjectionVersion: assertionservice.ProjectionVersion, CreatedAt: now, UpdatedAt: now,
	}
	return assertionservice.Bundle{Entities: entities, Assertions: []domain.Assertion{assertion}}
}

func assertionStoreRecords(query string) []*neo4j.Record {
	if strings.Contains(query, "MERGE (predicate:Predicate") {
		return []*neo4j.Record{neo4jRecord(map[string]any{"predicate_key": "works_on"})}
	}
	return nil
}

func assertionStoreRow(status domain.AssertionStatus) map[string]any {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	evidence, _ := json.Marshal([]domain.EvidenceSpan{{FragmentID: "fragment-1", Start: 0, End: 4, SourceGroup: "source-1"}})
	return map[string]any{
		"assertion_id": "assertion-1", "owner_profile_id": "profile-a", "subject_entity_id": "mark",
		"predicate_key": "works_on", "relationship_type": "WORKS_ON", "object_entity_id": "dense-mem",
		"object_value_id": "", "tier": "validated_claim", "status": string(status), "policy_family": "versioned",
		"polarity": "+", "modality": "assertion", "recorded_at": now, "extract_conf": 0.95,
		"resolution_conf": 0.96, "source_quality": 0.9, "support_count": int64(1), "source_group_count": int64(1),
		"evidence_json": string(evidence), "embedding_model": "embedding-model", "extraction_model": "reviewer-model",
		"extraction_version": "v2", "verifier_model": "verifier-model", "pipeline_run_id": "run-1",
		"projection_version": assertionservice.ProjectionVersion, "created_at": now, "updated_at": now,
	}
}

func paramsForQuery(t *testing.T, client *recordingClient, queryPart string) map[string]any {
	t.Helper()
	for i, query := range client.queries {
		if strings.Contains(query, queryPart) {
			return client.params[i]
		}
	}
	t.Fatalf("query containing %q was not recorded", queryPart)
	return nil
}

func neo4jRecord(values map[string]any) *neo4j.Record {
	record := &neo4j.Record{Keys: make([]string, 0, len(values)), Values: make([]any, 0, len(values))}
	for key, value := range values {
		record.Keys = append(record.Keys, key)
		record.Values = append(record.Values, value)
	}
	return record
}
