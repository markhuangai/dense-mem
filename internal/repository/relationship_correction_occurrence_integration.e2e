package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRelationshipCorrectionPreservesOccurrenceSupport(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-occurrence", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-occurrence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "relationship-correction-occurrence-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	canonicalContent := "The canonical evidence uses the original wording."
	canonical := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-occurrence-canonical", canonicalContent)
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	occurrenceID, occurrenceIngestID := uuid.NewString(), uuid.NewString()
	occurrenceContent := "The submitted occurrence uses a paraphrased wording."
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
				status, proposal, metadata, space_id, space_generation
			) VALUES (?, ?::uuid, ?::uuid, ?, ?, 'completed', '{}'::jsonb, '{}'::jsonb, ?::uuid, ?)
		`, teamID, occurrenceIngestID, ownerID, "relationship-correction-occurrence-ingest", sha256Hex(occurrenceContent), spaceID, generation).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO evidence_occurrences (
				team_id, occurrence_id, canonical_fragment_id, canonical_owner_profile_id,
				ingest_id, owner_profile_id, space_id, space_generation, evidence_index,
				content, content_hash, source_type, authority, source_ref, labels, metadata
			) VALUES (?, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, 0,
			          ?, ?, 'manual', 'primary', '', ARRAY[]::text[], '{}'::jsonb)
		`, teamID, occurrenceID, canonical.Evidence[0].FragmentID, ownerID,
			occurrenceIngestID, ownerID, spaceID, generation, occurrenceContent, sha256Hex(occurrenceContent)).Error
	}))

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Correction occurrence subject")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Correction occurrence wrong")
	correctObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Correction occurrence correct")
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: occurrenceIngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: canonical.Evidence[0].FragmentID, OccurrenceID: occurrenceID,
			OccurrenceOwnerProfileID: ownerID, EvidenceOwnerProfileID: ownerID,
			SourceGroupKey: "relationship-correction-occurrence", SpanStart: 0,
			SpanEnd: len(occurrenceContent), Quote: occurrenceContent, Authority: "primary",
		},
	}).Relationship
	require.NotNil(t, original)

	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: canonical.Evidence[0].FragmentID, Start: 0, End: len(occurrenceContent)}},
		Reason:   "preserve the submitted occurrence while correcting the object", IdempotencyKey: "relationship-correction-occurrence",
	}
	result, err := correctRelationshipWithTestEmbeddings(ctx, semantic, input)
	require.NoError(t, err)
	require.Equal(t, "completed", result.ProcessingState)
	require.NotNil(t, result.Correction)

	var successorOccurrenceID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT occurrence_id::text
			FROM relationship_evidence_supports
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, result.Correction.SuccessorRelationshipID).Row().Scan(&successorOccurrenceID)
	}))
	require.Equal(t, occurrenceID, successorOccurrenceID)

	trace, err := semantic.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID: teamID, RelationshipID: result.Correction.SuccessorRelationshipID,
	})
	require.NoError(t, err)
	require.Len(t, trace.EvidenceFragments, 1)
	require.Equal(t, occurrenceID, trace.EvidenceFragments[0].OccurrenceID)
	require.Equal(t, occurrenceContent, trace.EvidenceFragments[0].Content)
}
