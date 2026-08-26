package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The planner must enforce mutation ownership before rendering provider input.
// Actor B shares A's team; actor C is in another team, and neither may plan A's correction.
func TestPlanRelationshipCorrectionEmbeddingsRejectsForeignProfile(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-planner-owner", 3, "exact", "")
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "relationship-correction-planner-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "relationship-correction-planner-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "relationship-correction-planner-owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "relationship-correction-planner-team-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "relationship-correction-planner-owner-c")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamA, ownerA, "person", "Planner Owner")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamA, ownerA, "project", "Wrong Project")
	correctObject := createSemanticEntity(t, ctx, semantic, teamA, ownerA, "project", "Correct Project")
	ingest := createSemanticIngest(t, ctx, ledger, teamA, ownerA, "relationship-correction-planner-source", "Planner Owner works on Correct Project.")
	relationship := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamA, OwnerProfileID: ownerA, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "planner-owner", SpanStart: 0, SpanEnd: len(ingest.Evidence[0].Content), Authority: "primary"},
	}).Relationship
	require.NotNil(t, relationship)

	for _, actor := range []struct {
		name    string
		teamID  string
		profile string
	}{
		{name: "same-team actor B", teamID: teamA, profile: ownerB},
		{name: "cross-team actor C", teamID: teamC, profile: ownerC},
	} {
		t.Run(actor.name, func(t *testing.T) {
			plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, CorrectRelationshipInput{
				TeamID: actor.teamID, OwnerProfileID: actor.profile, Action: "submit", RelationshipID: relationship.RelationshipID,
				ExpectedVersion: relationship.Version,
				Patch:           RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}},
				Supports:        []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(ingest.Evidence[0].Content)}},
				Reason:          "foreign profile must not plan owner correction", IdempotencyKey: "foreign-planner-" + actor.name,
			})
			require.NoError(t, err)
			require.Empty(t, plan.Documents)
		})
	}
}
