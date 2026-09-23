//go:build integration

package postgres

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecallHypothesesRanksSourceEndpointAndLiteralMatches(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-hypothesis-context-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-hypothesis-context-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-hypothesis-context-reader")
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	semantic := newDreamFixtureStore(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Recall context subject")
	middle := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Recall context middle")
	sourceTarget := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Recall context source target")
	independent := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Recall context independent target")
	versionTarget := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Recall context versioned target")
	endpointMiddle := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Recall context endpoint middle")
	endpointTarget := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Recall context endpoint target")
	literalSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Recall context literal subject")
	literalMiddle := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Recall context literal middle")
	literalTarget := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Recall context literal target")
	literalRationaleTarget := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Recall context rationale target")

	sourceFirst := createActiveDreamRelationship(t, ctx, ledger, semantic, teamID, ownerID,
		"recall-hypothesis-context-source-first", "Dense-Mem uses Runtime for memory requests.",
		subject.EntityID, middle.EntityID, "recall-context-source-first")
	sourceSecond := createActiveDreamRelationship(t, ctx, ledger, semantic, teamID, ownerID,
		"recall-hypothesis-context-source-second", "Runtime stores memory in PostgreSQL.",
		middle.EntityID, sourceTarget.EntityID, "recall-context-source-second")
	endpointFirst := createActiveDreamRelationship(t, ctx, ledger, semantic, teamID, ownerID,
		"recall-hypothesis-context-endpoint-first", "Dense-Mem routes requests through a dispatcher.",
		subject.EntityID, endpointMiddle.EntityID, "recall-context-endpoint-first")
	endpointSecond := createActiveDreamRelationship(t, ctx, ledger, semantic, teamID, ownerID,
		"recall-hypothesis-context-endpoint-second", "The dispatcher counts processed requests.",
		endpointMiddle.EntityID, endpointTarget.EntityID, "recall-context-endpoint-second")
	literalFirst := createActiveDreamRelationship(t, ctx, ledger, semantic, teamID, ownerID,
		"recall-hypothesis-context-literal-first", "Delta signs message contents.",
		literalSubject.EntityID, literalMiddle.EntityID, "recall-context-literal-first")
	literalSecond := createActiveDreamRelationship(t, ctx, ledger, semantic, teamID, ownerID,
		"recall-hypothesis-context-literal-second", "Echo stores event logs.",
		literalMiddle.EntityID, literalTarget.EntityID, "recall-context-literal-second")

	runDate := time.Now().UTC().Format("2006-01-02")
	run, err := semantic.ClaimDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, InitiatedByProfileID: ownerID, RunDate: runDate,
		WindowKey: "manual:recall-hypothesis-context", LeaseToken: uuid.NewString(),
		LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)

	inputs, err := semantic.ListDreamInputs(ctx, DreamInputListInput{TeamID: teamID, Limit: 20})
	require.NoError(t, err)
	find := func(relationshipID string) DreamInput { return requireDreamInput(t, inputs, relationshipID) }
	typedValue, err := semantic.UpsertValue(ctx, knowledgepostgres.UpsertValueInput{
		TeamID: teamID, OwnerProfileID: ownerID, ValueType: "string",
		CanonicalValue: "recall-hypothesis-context-typed-endpoint", Display: "typed endpoint",
		NormalizationVersion: 1,
	})
	require.NoError(t, err)
	sourceProposal := evidenceGroundedDreamProposal(teamID, ownerID, run.RunID,
		find(sourceFirst.Relationship.RelationshipID), find(sourceSecond.Relationship.RelationshipID),
		subject.EntityID, independent.EntityID, "uses", "A context-derived hypothesis.")
	valueProposal := evidenceGroundedDreamProposal(teamID, ownerID, run.RunID,
		find(endpointFirst.Relationship.RelationshipID), find(endpointSecond.Relationship.RelationshipID),
		endpointTarget.EntityID, "", "released", "A hypothesis with a typed Value endpoint.")
	valueProposal.ObjectValueID = typedValue.ValueID
	endpointProposalA := evidenceGroundedDreamProposal(teamID, ownerID, run.RunID,
		find(endpointFirst.Relationship.RelationshipID), find(endpointSecond.Relationship.RelationshipID),
		subject.EntityID, endpointTarget.EntityID, "uses", "An endpoint-derived hypothesis about the target.")
	endpointProposalB := evidenceGroundedDreamProposal(teamID, otherOwnerID, run.RunID,
		find(endpointFirst.Relationship.RelationshipID), find(endpointSecond.Relationship.RelationshipID),
		sourceTarget.EntityID, independent.EntityID, "uses", "An endpoint-derived hypothesis from another owner.")
	literalProposal := evidenceGroundedDreamProposal(teamID, ownerID, run.RunID,
		find(literalFirst.Relationship.RelationshipID), find(literalSecond.Relationship.RelationshipID),
		literalSubject.EntityID, literalTarget.EntityID, "uses",
		"The unique fallback phrase appears in this hypothesis.")
	rationaleProposal := evidenceGroundedDreamProposal(teamID, ownerID, run.RunID,
		find(literalFirst.Relationship.RelationshipID), find(literalSecond.Relationship.RelationshipID),
		literalSubject.EntityID, literalRationaleTarget.EntityID, "uses",
		"A separate retained fallback hypothesis.")
	rationaleProposal.Rationale = "The unique fallback phrase appears only in rationale."
	endpointProposalB.SourceOwnerProfileIDs = []string{ownerID}
	endpointProposalB.CreatedByProfileID = otherOwnerID

	proposals := []UpsertHypothesisInput{sourceProposal, endpointProposalA, endpointProposalB, literalProposal, rationaleProposal, valueProposal}
	records := make([]*HypothesisRecord, 0, len(proposals))
	for _, proposal := range proposals {
		record, inserted, err := semantic.UpsertHypothesis(ctx, proposal)
		require.NoError(t, err)
		require.True(t, inserted)
		records = append(records, record)
	}
	endpointIDs := []string{records[1].HypothesisID, records[2].HypothesisID}
	sort.Strings(endpointIDs)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE hypotheses
			SET updated_at = TIMESTAMPTZ '2098-01-01 00:00:00+00'
			WHERE team_id = ?::uuid AND hypothesis_id IN (?::uuid, ?::uuid)
		`, teamID, endpointIDs[0], endpointIDs[1]).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE hypotheses
			SET updated_at = CASE hypothesis_id
				WHEN ?::uuid THEN TIMESTAMPTZ '2097-01-01 00:00:00+00'
				ELSE TIMESTAMPTZ '2099-01-01 00:00:00+00'
			END
			WHERE team_id = ?::uuid AND hypothesis_id IN (?::uuid, ?::uuid)
		`, records[3].HypothesisID, teamID, records[3].HypothesisID, records[4].HypothesisID).Error
	}))

	firstSourceEvidence := find(sourceFirst.Relationship.RelationshipID).Evidence[0]
	typedValueOnly, err := semantic.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID: teamID, Limit: 10, ValueIDs: []string{typedValue.ValueID},
	})
	require.NoError(t, err)
	require.Equal(t, []string{records[5].HypothesisID}, hypothesisIDs(typedValueOnly))

	fragmentOnly, err := semantic.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID:      teamID,
		Limit:       10,
		EvidenceIDs: []string{firstSourceEvidence.FragmentID},
	})
	require.NoError(t, err)
	require.Equal(t, []string{records[0].HypothesisID}, hypothesisIDs(fragmentOnly))

	input := RecallHypothesesInput{
		TeamID:          teamID,
		Query:           "unique fallback phrase",
		Limit:           10,
		EvidenceIDs:     []string{firstSourceEvidence.FragmentID},
		RelationshipIDs: []string{sourceFirst.Relationship.RelationshipID},
		EntityIDs:       []string{subject.EntityID, middle.EntityID, sourceTarget.EntityID},
	}
	recalled, err := semantic.RecallHypotheses(ctx, input)
	require.NoError(t, err)
	require.Len(t, recalled, 5)
	require.Equal(t, []string{
		records[0].HypothesisID,
		endpointIDs[0],
		endpointIDs[1],
		records[3].HypothesisID,
		records[4].HypothesisID,
	}, hypothesisIDs(recalled))

	literalFallback, err := semantic.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID: teamID, Query: input.Query, Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, []string{records[3].HypothesisID, records[4].HypothesisID}, hypothesisIDs(literalFallback))

	paraphrasedQuery := "What component handles Dense-Mem's incoming memory requests?"
	literalParaphrase, err := semantic.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID: teamID, Query: paraphrasedQuery, Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, literalParaphrase)
	paraphraseContext, err := semantic.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID:          teamID,
		Query:           paraphrasedQuery,
		Limit:           10,
		EvidenceIDs:     input.EvidenceIDs,
		RelationshipIDs: input.RelationshipIDs,
	})
	require.NoError(t, err)
	require.Equal(t, []string{records[0].HypothesisID}, hypothesisIDs(paraphraseContext))

	repeated, err := semantic.RecallHypotheses(ctx, input)
	require.NoError(t, err)
	require.Equal(t, hypothesisIDs(recalled), hypothesisIDs(repeated))

	limited, err := semantic.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID: teamID, Query: input.Query, Limit: 3,
		EvidenceIDs:     input.EvidenceIDs,
		RelationshipIDs: input.RelationshipIDs,
		EntityIDs:       input.EntityIDs,
	})
	require.NoError(t, err)
	require.Equal(t, hypothesisIDs(recalled[:3]), hypothesisIDs(limited))

	privateHypothesisID := insertPrivateRecallDecoy(t, ctx, adminDB, rls, teamID, otherOwnerID, subject.EntityID)
	privateDecoyRecall, err := semantic.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID: teamID, Query: "private-only decoy phrase", Limit: 10,
	})
	require.NoError(t, err)
	require.NotContains(t, hypothesisIDs(privateDecoyRecall), privateHypothesisID)
	require.Empty(t, privateDecoyRecall)

	contextOnly := RecallHypothesesInput{
		TeamID:          teamID,
		EvidenceIDs:     []string{firstSourceEvidence.FragmentID},
		RelationshipIDs: []string{sourceFirst.Relationship.RelationshipID},
	}
	beforeVersionChange, err := semantic.RecallHypotheses(ctx, contextOnly)
	require.NoError(t, err)
	require.Equal(t, []string{records[0].HypothesisID}, hypothesisIDs(beforeVersionChange))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET version = version + 1, updated_at = now()
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, sourceFirst.Relationship.RelationshipID).Error
	}))
	afterVersionChange, err := semantic.RecallHypotheses(ctx, contextOnly)
	require.NoError(t, err)
	require.Empty(t, afterVersionChange)

	updatedInputs, err := semantic.ListDreamInputs(ctx, DreamInputListInput{TeamID: teamID, Limit: 20})
	require.NoError(t, err)
	updatedSourceFirst := requireDreamInput(t, updatedInputs, sourceFirst.Relationship.RelationshipID)
	versionProposal := evidenceGroundedDreamProposal(teamID, ownerID, run.RunID,
		updatedSourceFirst, requireDreamInput(t, updatedInputs, sourceSecond.Relationship.RelationshipID),
		subject.EntityID, versionTarget.EntityID, "uses", "A hypothesis with current source versions.")
	versionRecord, inserted, err := semantic.UpsertHypothesis(ctx, versionProposal)
	require.NoError(t, err)
	require.True(t, inserted)
	currentVersionRecall, err := semantic.RecallHypotheses(ctx, contextOnly)
	require.NoError(t, err)
	require.Equal(t, []string{versionRecord.HypothesisID}, hypothesisIDs(currentVersionRecall))
	_, err = semantic.RetractEvidence(ctx, knowledgepostgres.RetractEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID,
		EvidenceIDs:    []string{firstSourceEvidence.FragmentID},
		Reason:         "recall context source evidence was retired",
		IdempotencyKey: "recall-hypothesis-context-retract-source",
		RequestHash:    "sha256:recall-hypothesis-context-retract-source",
	})
	require.NoError(t, err)
	afterRetirement, err := semantic.RecallHypotheses(ctx, contextOnly)
	require.NoError(t, err)
	require.Empty(t, afterRetirement)

	foreignTeamID := createLedgerTeam(t, adminDB, rls, "recall-hypothesis-context-foreign-team")
	foreignOwnerID := createLedgerProfile(t, adminDB, rls, foreignTeamID, "recall-hypothesis-context-foreign-owner")
	foreignSemantic := newDreamFixtureStore(appDB, rls)
	foreignLedger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	foreignSubject := createSemanticEntity(t, ctx, foreignSemantic, foreignTeamID, foreignOwnerID, "project", "Foreign context subject")
	foreignObject := createSemanticEntity(t, ctx, foreignSemantic, foreignTeamID, foreignOwnerID, "product", "Foreign context object")
	foreignSourceFirst := createActiveDreamRelationship(t, ctx, foreignLedger, foreignSemantic, foreignTeamID, foreignOwnerID,
		"recall-hypothesis-context-foreign-first", "Foreign team uses an isolated source.",
		foreignSubject.EntityID, foreignObject.EntityID, "foreign-context-first")
	foreignMiddle := createSemanticEntity(t, ctx, foreignSemantic, foreignTeamID, foreignOwnerID, "product", "Foreign context middle")
	foreignSourceSecond := createActiveDreamRelationship(t, ctx, foreignLedger, foreignSemantic, foreignTeamID, foreignOwnerID,
		"recall-hypothesis-context-foreign-second", "Foreign source remains isolated.",
		foreignObject.EntityID, foreignMiddle.EntityID, "foreign-context-second")
	foreignRun, err := foreignSemantic.ClaimDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: foreignTeamID, InitiatedByProfileID: foreignOwnerID, RunDate: runDate,
		WindowKey: "manual:recall-hypothesis-context-foreign", LeaseToken: uuid.NewString(),
		LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	foreignInputs, err := foreignSemantic.ListDreamInputs(ctx, DreamInputListInput{TeamID: foreignTeamID, Limit: 10})
	require.NoError(t, err)
	foreignProposal := evidenceGroundedDreamProposal(foreignTeamID, foreignOwnerID, foreignRun.RunID,
		requireDreamInput(t, foreignInputs, foreignSourceFirst.Relationship.RelationshipID),
		requireDreamInput(t, foreignInputs, foreignSourceSecond.Relationship.RelationshipID),
		foreignSubject.EntityID, foreignMiddle.EntityID, "uses", "A foreign team hypothesis.")
	_, inserted, err = foreignSemantic.UpsertHypothesis(ctx, foreignProposal)
	require.NoError(t, err)
	require.True(t, inserted)

	foreignEvidence := requireDreamInput(t, foreignInputs, foreignSourceFirst.Relationship.RelationshipID).Evidence[0]
	foreignContext, err := semantic.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID: teamID, Limit: 10,
		EvidenceIDs:     []string{foreignEvidence.FragmentID},
		RelationshipIDs: []string{foreignSourceFirst.Relationship.RelationshipID},
		EntityIDs:       []string{foreignSubject.EntityID, foreignObject.EntityID},
	})
	require.NoError(t, err)
	require.Empty(t, foreignContext)
}

func insertPrivateRecallDecoy(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls *storagepostgres.RLS,
	teamID, ownerID, subjectEntityID string,
) string {
	t.Helper()
	privateSpaceID := ""
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Raw(`
			INSERT INTO memory_spaces (team_id, kind, owner_profile_id)
			VALUES (?::uuid, 'profile_private', ?::uuid)
			RETURNING id::text
		`, teamID, ownerID).Row().Scan(&privateSpaceID)
	}))
	spaceGeneration := privateSpaceGeneration(t, ctx, db, rls, uuid.MustParse(privateSpaceID))
	ingestID, evidenceID, hypothesisID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	content := "private-only decoy phrase"
	contentHash := "sha256:" + uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
				source_summary, status, proposal, metadata, space_id, space_generation
			) VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?, 'private recall fixture',
			         'completed', '{}'::jsonb, '{}'::jsonb, ?::uuid, ?)
		`, teamID, ingestID, ownerID, "private-recall-"+ingestID, contentHash, privateSpaceID, spaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
				content, content_hash, source_type, authority, metadata, space_id, space_generation
			) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, 0, ?, ?, 'manual', 'primary',
			         '{}'::jsonb, ?::uuid, ?)
		`, teamID, evidenceID, ingestID, ownerID, content, contentHash, privateSpaceID, spaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO evidence_security_events (
				team_id, fragment_id, ingest_id, owner_profile_id, event_kind,
				decision, reason, metadata, space_id, space_generation
			) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, 'deterministic_scan',
			         'pass', 'private recall fixture passed security', '{}'::jsonb, ?::uuid, ?)
		`, teamID, evidenceID, ingestID, ownerID, privateSpaceID, spaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO hypotheses (
				team_id, hypothesis_id, created_by_profile_id, lane, status, statement,
				rationale, subject_entity_id, predicate_key, predicate_version,
				source_refs, source_versions, source_owner_profile_ids, generator_kind,
				generator_version, payload, space_id, space_generation
			) VALUES (?::uuid, ?::uuid, ?::uuid, 'evidence_discovery', 'proposed',
			         'private-only decoy phrase', 'private recall fixture', ?::uuid,
			         'uses', 1, '[]'::jsonb, '{}'::jsonb, ARRAY[]::uuid[],
			         'provider', 'fixture', '{}'::jsonb, ?::uuid, ?)
		`, teamID, hypothesisID, ownerID, subjectEntityID, privateSpaceID, spaceGeneration).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO hypothesis_evidence_derivation_sources (
				team_id, hypothesis_id, space_id, space_generation, evidence_id,
				fragment_id, source_group_key, span_start, span_end, quote, authority
			) VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?::uuid,
			         'private-recall-fixture', 0, ?, ?, 'primary')
		`, teamID, hypothesisID, privateSpaceID, spaceGeneration, evidenceID, evidenceID, len(content), content).Error
	}))
	return hypothesisID
}

func hypothesisIDs(records []HypothesisRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.HypothesisID)
	}
	return ids
}
