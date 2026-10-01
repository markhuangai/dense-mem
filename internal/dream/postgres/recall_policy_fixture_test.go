//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type hypothesisRecallFixture struct {
	store                    *Store
	adminDB                  *gorm.DB
	rls                      *storagepostgres.RLS
	counters                 *dreamPolicyBenchmarkCounters
	teamID                   string
	ownerID                  string
	evidenceID               string
	entityID                 string
	valueID                  string
	graphRelationshipID      string
	graphOtherRelationshipID string
	graphFragmentID          string
	objectID                 string
}

func hypothesisRecallFixtureID(index int) string {
	return fmt.Sprintf("48900000-0000-4000-8000-%012x", index)
}

func hypothesisRecallFixtureIDs(indices ...int) []string {
	ids := make([]string, len(indices))
	for i, index := range indices {
		ids[i] = hypothesisRecallFixtureID(index)
	}
	return ids
}

func newHypothesisRecallFixture(t testing.TB) *hypothesisRecallFixture {
	t.Helper()
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "hypothesis-recall-policy")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "hypothesis-recall-policy-owner")
	semantic := newDreamFixtureStore(appDB, rls)
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	matched := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Matched endpoint")
	other := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Other endpoint")
	middle := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Graph middle")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Graph object")
	first := createActiveDreamRelationship(t, ctx, ledger, semantic, teamID, ownerID,
		"hypothesis-recall-first", "Other endpoint uses Graph middle.", other.EntityID, middle.EntityID, "recall:first")
	second := createActiveDreamRelationship(t, ctx, ledger, semantic, teamID, ownerID,
		"hypothesis-recall-second", "Graph middle uses Graph object.", middle.EntityID, object.EntityID, "recall:second")
	inputs, err := semantic.ListDreamInputs(ctx, DreamInputListInput{TeamID: teamID, Limit: 10})
	require.NoError(t, err)
	firstInput := requireDreamInput(t, inputs, first.Relationship.RelationshipID)
	secondInput := requireDreamInput(t, inputs, second.Relationship.RelationshipID)
	value, err := semantic.UpsertValue(ctx, knowledgepostgres.UpsertValueInput{
		TeamID: teamID, OwnerProfileID: ownerID, ValueType: "string",
		CanonicalValue: "recall-policy-value", Display: "Recall policy value", NormalizationVersion: 1,
	})
	require.NoError(t, err)
	const content = "Exact evidence for the hypothesis recall policy fixture."
	createEvidence := func(key, text string) knowledgepostgres.EvidenceFragment {
		result, err := ledger.CreateIngestForTest(ctx, knowledgepostgres.CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key, RequestHash: sha256Hex(text),
			Evidence: []knowledgepostgres.EvidenceInput{{
				Content: text, InitialEvent: &knowledgepostgres.SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
			}},
		})
		require.NoError(t, err)
		return requireTestEvidenceFragment(t, result)
	}
	matchedEvidence := createEvidence("recall-policy-matched", content)
	otherEvidence := createEvidence("recall-policy-other", content+" Other source.")
	fixture := &hypothesisRecallFixture{
		adminDB: adminDB, rls: rls, counters: &dreamPolicyBenchmarkCounters{},
		teamID: teamID, ownerID: ownerID, evidenceID: matchedEvidence.FragmentID,
		entityID: matched.EntityID, valueID: value.ValueID,
		graphRelationshipID: firstInput.RelationshipID, graphOtherRelationshipID: secondInput.RelationshipID,
		graphFragmentID: firstInput.Evidence[0].FragmentID, objectID: object.EntityID,
	}
	fixture.store = NewStore(dreamPolicyCountedDB(appDB, fixture.counters), rls)
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		seed := func(index, mask int, status, canonicalID string) error {
			subjectID := other.EntityID
			if mask&2 != 0 {
				subjectID = matched.EntityID
			}
			statement, rationale := "Unmatched statement.", "Unmatched rationale."
			if mask&4 != 0 {
				statement = "statement needle"
			}
			if mask&8 != 0 {
				rationale = "rationale needle"
			}
			updated := baseTime
			if index == 1 {
				updated = updated.Add(time.Hour)
			}
			if index == 8 {
				updated = updated.Add(24 * time.Hour)
			}
			id := hypothesisRecallFixtureID(index)
			if err := insertHypothesisRecallFixtureRow(ctx, tx, teamID, ownerID, id,
				"evidence_discovery", status, canonicalID, statement, rationale, subjectID, object.EntityID, "", updated); err != nil {
				return err
			}
			fragment := otherEvidence
			if mask&1 != 0 {
				fragment = matchedEvidence
			}
			return insertHypothesisEvidenceDerivations(ctx, tx, teamID, id, []EvidenceDerivationSource{{
				EvidenceID: fragment.FragmentID, FragmentID: fragment.FragmentID,
				SourceID: fragment.SourceID, SourceRevisionID: fragment.SourceRevisionID,
				SourceGroupKey: "recall-policy", SpanStart: 0, SpanEnd: len([]rune(fragment.Content)),
				Quote: fragment.Content, Authority: fragment.Authority,
			}})
		}
		for mask := 0; mask < 16; mask++ {
			status := "proposed"
			if mask == 15 {
				status = "reinforced"
			}
			if err := seed(mask, mask, status, ""); err != nil {
				return err
			}
		}
		for _, index := range []int{32, 33, 34, 35, 36, 37} {
			mask := 0
			if index < 34 {
				mask = 1
			}
			if err := seed(index, mask, "proposed", ""); err != nil {
				return err
			}
		}
		for i, status := range []string{"proposed", "stale", "rejected", "submitted"} {
			canonicalID := ""
			if i == 0 {
				canonicalID = hypothesisRecallFixtureID(1)
			}
			if err := seed(50+i, 15, status, canonicalID); err != nil {
				return err
			}
		}
		graphID := hypothesisRecallFixtureID(40)
		if err := insertHypothesisRecallFixtureRow(ctx, tx, teamID, ownerID, graphID,
			"graph", "proposed", "", "Graph-derived hypothesis.", "Graph rationale.",
			other.EntityID, object.EntityID, "", baseTime); err != nil {
			return err
		}
		if err := insertHypothesisDerivations(ctx, tx, teamID, graphID, []DreamDerivationSource{
			dreamDerivationFromEvidence(1, firstInput, firstInput.Evidence[0]),
			dreamDerivationFromEvidence(2, secondInput, secondInput.Evidence[0]),
		}); err != nil {
			return err
		}
		valueID := hypothesisRecallFixtureID(41)
		if err := insertHypothesisRecallFixtureRow(ctx, tx, teamID, ownerID, valueID,
			"evidence_discovery", "proposed", "", "Typed Value hypothesis.", "Value rationale.",
			other.EntityID, "", value.ValueID, baseTime); err != nil {
			return err
		}
		return insertHypothesisEvidenceDerivations(ctx, tx, teamID, valueID, []EvidenceDerivationSource{{
			EvidenceID: otherEvidence.FragmentID, FragmentID: otherEvidence.FragmentID,
			SourceID: otherEvidence.SourceID, SourceRevisionID: otherEvidence.SourceRevisionID,
			SourceGroupKey: "recall-policy", SpanEnd: len([]rune(otherEvidence.Content)),
			Quote: otherEvidence.Content, Authority: otherEvidence.Authority,
		}})
	}))
	return fixture
}

func insertHypothesisRecallFixtureRow(
	ctx context.Context, tx *gorm.DB, teamID, ownerID, id, lane, status, canonicalID,
	statement, rationale, subjectID, objectID, valueID string, updated time.Time,
) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO hypotheses (
			team_id, hypothesis_id, created_by_profile_id, lane, status, canonical_hypothesis_id,
			statement, rationale, subject_entity_id, predicate_key, predicate_version,
			object_entity_id, object_value_id, source_refs, source_versions, source_owner_profile_ids,
			generator_kind, generator_version, content_hash, payload, created_at, updated_at, space_id, space_generation
		) VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?, NULLIF(?, '')::uuid,
			?, ?, ?::uuid, 'uses', 1, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid,
			'[]'::jsonb, '{}'::jsonb, ARRAY[?::uuid], 'provider', 'recall-policy-fixture',
			?, '{}'::jsonb, ?, ?, dense_mem_team_shared_space(?::uuid), dense_mem_team_shared_generation(?::uuid))
	`, teamID, id, ownerID, lane, status, canonicalID, statement, rationale, subjectID,
		objectID, valueID, ownerID, "sha256:"+id, updated, updated, teamID, teamID).Error
}

func (f *hypothesisRecallFixture) mixedInput() RecallHypothesesInput {
	return RecallHypothesesInput{
		TeamID: f.teamID, Query: "needle", Limit: 20,
		EvidenceIDs: []string{f.evidenceID}, EntityIDs: []string{f.entityID},
	}
}

func cappedHypothesisRecallIDs(id string, matchingLast bool) []string {
	ids := make([]string, 201)
	for i := range ids {
		ids[i] = uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("recall-context-%d", i))).String()
	}
	index := 199
	if matchingLast {
		index = 200
	}
	ids[index] = id
	return ids
}
