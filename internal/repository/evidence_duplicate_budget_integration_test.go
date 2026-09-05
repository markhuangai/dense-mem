package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

func TestRememberDuplicateBudgetAllocationUsesScopedPostgresCandidates(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-budget-selection", 2, "exact", "")
	teamA := createLedgerTeam(t, adminDB, rls, "duplicate-budget-selection-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "duplicate-budget-selection-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "duplicate-budget-selection-owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "duplicate-budget-selection-team-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "duplicate-budget-selection-owner-c")
	sharedSpaceID, sharedGeneration := duplicateTeamSharedSpace(t, adminDB, rls, teamA)
	repo := NewLedgerRepository(appDB, rls)

	// These candidates are intentionally large enough that the complete set
	// cannot fit the configured context sub-budget while each item remains
	// individually admissible.
	for index := 0; index < 3; index++ {
		candidate := duplicateRememberInput(teamA, ownerB, fmt.Sprintf("duplicate-budget-selection-shared-%02d", index), strings.Repeat(fmt.Sprintf("shared candidate %02d ", index), 1000), false)
		candidate.SpaceID, candidate.SpaceGeneration = sharedSpaceID, sharedGeneration
		commitDuplicateFixture(t, ctx, repo, candidate)
	}
	privateSpaceID, privateGeneration := duplicatePrivateSpace(t, adminDB, rls, teamA, ownerB)
	private := duplicateRememberInput(teamA, ownerB, "duplicate-budget-selection-private", strings.Repeat("private candidate ", 1000), false)
	private.SpaceID, private.SpaceGeneration = privateSpaceID, privateGeneration
	commitDuplicateFixture(t, ctx, repo, private)
	foreign := duplicateRememberInput(teamC, ownerC, "duplicate-budget-selection-foreign", strings.Repeat("foreign candidate ", 1000), false)
	commitDuplicateFixture(t, ctx, repo, foreign)

	query := duplicateRememberInput(teamA, ownerA, "duplicate-budget-selection-query", "short submitted note", false)
	query.SpaceID, query.SpaceGeneration = sharedSpaceID, sharedGeneration
	resolved, err := resolveDuplicateFixture(t, ctx, repo, query)
	require.NoError(t, err)
	require.Len(t, resolved.Candidates, 1)
	require.Len(t, resolved.Candidates[0].Candidates, 3)
	for _, candidate := range resolved.Candidates[0].Candidates {
		require.Equal(t, teamA, candidateOwnerTeam(t, adminDB, rls, candidate.FragmentID))
		require.Equal(t, ownerB, candidateOwnerProfile(t, adminDB, rls, candidate.FragmentID))
		require.NotEqual(t, private.Evidence[0].FragmentID, candidate.FragmentID)
		require.NotEqual(t, foreign.Evidence[0].FragmentID, candidate.FragmentID)
	}

	toAssessorCandidates := make([]assessor.SemanticAssessmentEvidenceEquivalenceCandidate, 0, len(resolved.Candidates[0].Candidates))
	for _, candidate := range resolved.Candidates[0].Candidates {
		prepared := assessor.PrepareSemanticAssessmentEvidence(assessor.SemanticReviewEvidence{EvidenceID: candidate.FragmentID, Content: candidate.Content})
		toAssessorCandidates = append(toAssessorCandidates, assessor.SemanticAssessmentEvidenceEquivalenceCandidate{
			EvidenceID: prepared.EvidenceID, Content: prepared.Content, BoundaryText: prepared.BoundaryText,
			BoundaryRefs: prepared.BoundaryRefs, BoundaryPrefix: prepared.BoundaryPrefix,
		})
	}
	request := assessor.SemanticAssessmentRequest{
		RequestID: "duplicate-budget-selection",
		TeamID:    teamA,
		Evidence: []assessor.SemanticReviewEvidence{{
			EvidenceID: "evidence:0", Content: query.Evidence[0].Content,
		}},
		EvidenceEquivalenceCandidates: []assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup{{
			EvidenceID: "evidence:0", Candidates: toAssessorCandidates,
		}},
	}
	baseRequest := request
	baseRequest.EvidenceEquivalenceCandidates = []assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup{{EvidenceID: "evidence:0"}}
	_, _, err = assessor.CountSemanticAssessmentRequestTokens(baseRequest, assessor.DefaultSemanticAssessmentLimits())
	require.NoError(t, err)
	firstRequest := request
	firstRequest.EvidenceEquivalenceCandidates = []assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup{{
		EvidenceID: "evidence:0", Candidates: toAssessorCandidates[:1],
	}}
	_, firstContextTokens, err := assessor.CountSemanticAssessmentRequestTokens(firstRequest, assessor.DefaultSemanticAssessmentLimits())
	require.NoError(t, err)
	require.Positive(t, firstContextTokens)
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1_000_000
	limits.MaxCandidateContextTokens = firstContextTokens
	prepared, validationErrors := assessor.PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, validationErrors)
	require.True(t, prepared.CandidateContextTruncated)
	require.Equal(t, len(toAssessorCandidates)-1, prepared.CandidateContextOmittedCandidates)
	require.Len(t, prepared.EvidenceEquivalenceCandidates[0].Candidates, 1)
	require.Equal(t, toAssessorCandidates[0].EvidenceID, prepared.EvidenceEquivalenceCandidates[0].Candidates[0].EvidenceID)
}

func candidateOwnerProfile(t *testing.T, db *gorm.DB, rls rLSHelper, fragmentID string) string {
	t.Helper()
	var ownerID string
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT owner_profile_id::text FROM evidence_fragments WHERE fragment_id = ?::uuid`, fragmentID).Row().Scan(&ownerID)
	}))
	return ownerID
}
