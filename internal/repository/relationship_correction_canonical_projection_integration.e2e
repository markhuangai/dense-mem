package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRelationshipCorrectionPlannerUsesCanonicalNameForUniqueSubmitMatch(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-canonical-submit", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-canonical-submit")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Canonical Submit Owner")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Canonical Submit Wrong")
	canonicalObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	content := "Canonical Submit Owner works on Dense-Mem."
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-canonical-submit", content)
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "canonical-submit", SpanStart: 0, SpanEnd: len(content), Authority: "primary"},
	}).Relationship
	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{Name: "dense-mem", EntityKind: "project"}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(content)}},
		Reason:   "the object Entity spelling was normalized", IdempotencyKey: "canonical-submit",
	}

	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.Len(t, plan.Documents, 2)
	successorDocument := plan.Documents[len(plan.Documents)-1].DocumentText
	require.Contains(t, successorDocument, "object: Dense-Mem")
	require.NotContains(t, successorDocument, "object: dense-mem")

	result, err := semantic.CorrectRelationshipWithEmbeddings(ctx, input, relationshipCorrectionTestEmbeddings(plan))
	require.NoError(t, err)
	require.Equal(t, "completed", result.ProcessingState)
	require.Equal(t, canonicalObject.EntityID, loadRelationshipObjectEntity(t, ctx, appDB, rls, teamID, ownerID, result.Correction.SuccessorRelationshipID))
}
