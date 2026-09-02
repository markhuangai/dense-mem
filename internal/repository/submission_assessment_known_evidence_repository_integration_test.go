package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestSubmissionAssessmentKnownEvidenceCatalogEnforcesVisibilityAndEligibility(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "known-evidence-catalog-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "known-evidence-catalog-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "known-evidence-catalog-owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "known-evidence-catalog-team-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "known-evidence-catalog-owner-c")
	ledger := NewLedgerRepository(appDB, rls)
	repo := NewSemanticRepository(appDB, rls)

	shared := createSemanticIngest(t, ctx, ledger, teamA, ownerA, "known-shared", "shared known evidence")
	otherTeam := createSemanticIngest(t, ctx, ledger, teamC, ownerC, "known-other-team", "other team known evidence")

	privateSpace, err := NewMemorySpaceRepository(appDB, rls).EnsureProfilePrivate(ctx, uuid.MustParse(teamA), uuid.MustParse(ownerA))
	require.NoError(t, err)
	var privateGeneration int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, privateSpace.ID).Row().Scan(&privateGeneration)
	}))
	privateCtx := requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{{ID: privateSpace.ID, Kind: domain.MemorySpaceProfilePrivate}})
	private, err := createTestIngest(privateCtx, ledger, CreateIngestInput{
		TeamID: teamA, OwnerProfileID: ownerA, SpaceID: privateSpace.ID.String(), SpaceGeneration: privateGeneration,
		IdempotencyKey: "known-private", RequestHash: "known-private-hash",
		Evidence: []EvidenceInput{{Content: "private known evidence"}},
	})
	require.NoError(t, err)
	require.Len(t, private.Evidence, 1)

	quarantined, err := createTestIngest(ctx, ledger, CreateIngestInput{
		TeamID: teamA, OwnerProfileID: ownerA, IdempotencyKey: "known-quarantined", RequestHash: "known-quarantined-hash",
		Evidence: []EvidenceInput{{Content: "quarantined known evidence", InitialEvent: &SecurityEventDraft{
			EventKind: "deterministic_scan", Decision: "quarantine", Reason: "test quarantine",
		}}},
	})
	require.NoError(t, err)
	require.Len(t, quarantined.Evidence, 1)

	retracted := createSemanticIngest(t, ctx, ledger, teamA, ownerA, "known-retracted", "retracted known evidence")
	_, err = ledger.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID: teamA, OwnerProfileID: ownerA, EvidenceIDs: []string{retracted.Evidence[0].FragmentID},
		Reason: "known evidence no longer current", IdempotencyKey: "known-retract", RequestHash: "known-retract-hash",
	})
	require.NoError(t, err)

	requested := []string{shared.Evidence[0].FragmentID, private.Evidence[0].FragmentID, otherTeam.Evidence[0].FragmentID, quarantined.Evidence[0].FragmentID, retracted.Evidence[0].FragmentID}
	visibleToB, err := repo.ListSubmissionAssessmentKnownEvidence(ctx, SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamA, OwnerProfileID: ownerB, EvidenceIDs: requested,
	})
	require.NoError(t, err)
	require.Len(t, visibleToB.Evidence, 1)
	require.Equal(t, shared.Evidence[0].FragmentID, visibleToB.Evidence[0].EvidenceID)

	visibleToA, err := repo.ListSubmissionAssessmentKnownEvidence(privateCtx, SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamA, OwnerProfileID: ownerA, EvidenceIDs: []string{private.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, visibleToA.Evidence, 1)
	require.Equal(t, private.Evidence[0].FragmentID, visibleToA.Evidence[0].EvidenceID)
}
