//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

func TestDreamPolicyOwnershipPreservesGraphAndFeedback(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-policy-ownership")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-policy-owner")
	reviewerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-policy-reviewer")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "other-dream-policy-team")
	otherID := createLedgerProfile(t, adminDB, rls, otherTeamID, "other-dream-policy-reviewer")
	semantic := newDreamFixtureStore(appDB, rls)
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Policy owner")
	middle := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Dream source")
	sourceObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Source target")
	firstTarget := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "First hypothesis target")
	secondTarget := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "Second hypothesis target")
	first := createActiveDreamRelationship(t, ctx, ledger, semantic, teamID, ownerID,
		"dream-policy-first", "Policy owner uses Dream source.", subject.EntityID, middle.EntityID, "source:first")
	second := createActiveDreamRelationship(t, ctx, ledger, semantic, teamID, ownerID,
		"dream-policy-second", "Dream source uses Source target.", middle.EntityID, sourceObject.EntityID, "source:second")
	run, err := semantic.ClaimDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, InitiatedByProfileID: ownerID, RunDate: "2026-09-29",
		WindowKey: "manual:dream-policy-ownership", LeaseToken: uuid.NewString(),
		LeaseUntil: time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	inputs, err := semantic.ListDreamInputs(ctx, DreamInputListInput{TeamID: teamID, Limit: 10})
	require.NoError(t, err)
	firstInput := requireDreamInput(t, inputs, first.Relationship.RelationshipID)
	secondInput := requireDreamInput(t, inputs, second.Relationship.RelationshipID)
	proposal := evidenceGroundedDreamProposal(teamID, ownerID, run.RunID, firstInput, secondInput,
		subject.EntityID, firstTarget.EntityID, "uses", "Policy owner may use First hypothesis target.")
	missingPremise := proposal
	missingPremise.Derivations = append([]DreamDerivationSource(nil), proposal.Derivations...)
	missingPremise.Derivations[1].PremisePosition = 1
	_, _, err = semantic.UpsertHypothesis(ctx, missingPremise)
	require.ErrorContains(t, err, "dream derivations must cover both premise positions")
	records, _, err := semantic.ListHypotheses(ctx, ListHypothesesInput{TeamID: teamID, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, records)

	record, inserted, err := semantic.UpsertHypothesis(ctx, proposal)
	require.NoError(t, err)
	require.True(t, inserted)
	stored, err := semantic.GetHypothesis(ctx, GetHypothesisInput{TeamID: teamID, HypothesisID: record.HypothesisID})
	require.NoError(t, err)
	require.Len(t, stored.Derivations, 2)
	for _, step := range []struct{ decision, status string }{
		{"reject", "rejected"}, {"stale", "stale"}, {"reinforce", "reinforced"},
	} {
		updated, err := semantic.UpdateHypothesisStatus(ctx, UpdateHypothesisStatusInput{
			TeamID: teamID, ActorProfileID: reviewerID, HypothesisID: record.HypothesisID,
			Status: step.status, Decision: step.decision,
		})
		require.NoError(t, err)
		require.Equal(t, step.status, updated.Status)
	}
	_, err = semantic.UpdateHypothesisStatus(ctx, UpdateHypothesisStatusInput{
		TeamID: teamID, ActorProfileID: reviewerID, HypothesisID: record.HypothesisID,
		Status: "rejected", Decision: "reinforce",
	})
	require.ErrorContains(t, err, `decision "reinforce" requires status "reinforced"`)
	_, err = semantic.UpdateHypothesisStatus(ctx, UpdateHypothesisStatusInput{
		TeamID: teamID, ActorProfileID: otherID, HypothesisID: record.HypothesisID,
		Status: "rejected", Decision: "reject",
	})
	require.Error(t, err)
	var events int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, reviewerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM hypothesis_feedback_events WHERE team_id = ?::uuid AND hypothesis_id = ?::uuid`,
			teamID, record.HypothesisID).Scan(&events).Error
	}))
	require.Equal(t, 3, events)

	secondProposal := evidenceGroundedDreamProposal(teamID, ownerID, run.RunID, firstInput, secondInput,
		subject.EntityID, secondTarget.EntityID, "uses", "Policy owner may use Second hypothesis target.")
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE relationship_records SET version = version + 1 WHERE team_id = ?::uuid AND relationship_id = ?::uuid`,
			teamID, firstInput.RelationshipID).Error
	}))
	_, _, err = semantic.UpsertHypothesis(ctx, secondProposal)
	require.ErrorIs(t, err, ErrDreamSourceStale)
	records, _, err = semantic.ListHypotheses(ctx, ListHypothesesInput{TeamID: teamID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "reinforced", records[0].Status)
	t.Log("result_signature=graph:one|feedback:3|source_stale")
}
