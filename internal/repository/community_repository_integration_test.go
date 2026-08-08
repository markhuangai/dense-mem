package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCommunityRepositorySnapshotLifecycleAndRecallExpansion(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "community-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "community-owner-a")
	teamC := createLedgerTeam(t, adminDB, rls, "community-team-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "community-owner-c")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)
	insertSearchTestContract(t, adminDB, rls, "community-search", 3, "exact", "")

	mark := createSemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "person", "Mark Huang")
	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "product", "PostgreSQL")
	otherTeamProject := createSemanticEntity(t, ctx, semanticRepo, teamC, ownerC, "project", "Other Project")
	otherTeamPostgres := createSemanticEntity(t, ctx, semanticRepo, teamC, ownerC, "product", "PostgreSQL")

	workIngest := createSemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"community-work-source", "The maintainer works on the memory service.")
	work := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        workIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     workIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:community-work",
			SpanStart:      0,
			SpanEnd:        len("The maintainer works on the memory service."),
			Authority:      "primary",
		},
	})

	usesIngest := createSemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"community-uses-source", "Dense-Mem depends on the durable database.")
	uses := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        usesIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     usesIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:community-uses",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem depends on the durable database."),
			Authority:      "primary",
		},
	})

	candidateIngest := createSemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"community-candidate-source", "Mark may use PostgreSQL.")
	candidate := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        candidateIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		EvidenceVerdict: "insufficient",
	})
	require.NotNil(t, candidate.Relationship)

	otherTeamIngest := createSemanticIngest(t, ctx, ledgerRepo, teamC, ownerC,
		"community-other-team-source", "Other Project uses PostgreSQL.")
	otherTeamRelationship := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamC,
		OwnerProfileID:  ownerC,
		IngestID:        otherTeamIngest.IngestID,
		SubjectEntityID: otherTeamProject.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  otherTeamPostgres.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     otherTeamIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:community-other",
			SpanStart:      0,
			SpanEnd:        len("Other Project uses PostgreSQL."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, otherTeamRelationship.Relationship)

	inputs, err := semanticRepo.ListCommunityInputs(ctx, CommunityInputListInput{
		TeamID: teamA,
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, inputs, 2)
	require.NotNil(t, findCommunityInput(inputs, work.Relationship.RelationshipID))
	require.NotNil(t, findCommunityInput(inputs, uses.Relationship.RelationshipID))
	require.Nil(t, findCommunityInput(inputs, candidate.Relationship.RelationshipID))
	require.Nil(t, findCommunityInput(inputs, otherTeamRelationship.Relationship.RelationshipID))

	run, err := semanticRepo.ClaimCommunityRun(ctx, CommunityRunClaimInput{
		TeamID:     teamA,
		WindowKey:  "community-window-1",
		LeaseUntil: time.Now().UTC().Add(time.Minute),
		MaxNodes:   10,
		MaxEdges:   10,
	})
	require.NoError(t, err)
	require.True(t, run.Claimed)
	duplicate, err := semanticRepo.ClaimCommunityRun(ctx, CommunityRunClaimInput{
		TeamID:     teamA,
		WindowKey:  "community-window-1",
		LeaseUntil: time.Now().UTC().Add(time.Minute),
		MaxNodes:   10,
		MaxEdges:   10,
	})
	require.NoError(t, err)
	require.False(t, duplicate.Claimed)
	require.Equal(t, run.RunID, duplicate.RunID)

	_, err = searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamA,
		OwnerProfileID: ownerA,
		SourceKind:     "evidence",
		SourceID:       usesIngest.Evidence[0].FragmentID,
		SourceVersion:  1,
		DocumentText:   "Dense-Mem depends on the durable database.",
	})
	require.NoError(t, err)

	publishInput := CommunitySnapshotPublishInput{
		TeamID:            teamA,
		RunID:             run.RunID,
		SourceFingerprint: "sha256:community-window-1",
		SourceSnapshot: []map[string]any{
			communitySourceSnapshot(work.Relationship),
			communitySourceSnapshot(uses.Relationship),
		},
		NodeCount: 3,
		EdgeCount: 2,
		Communities: []CommunityPublishRecord{{
			CommunityID:       uuid.NewString(),
			Ordinal:           0,
			Summary:           "Community groups Dense-Mem and PostgreSQL operational facts.",
			SummaryVersion:    "community-deterministic-v1",
			MemberCount:       3,
			SourceCount:       2,
			TopEntities:       []string{"Dense-Mem", "PostgreSQL"},
			TopPredicates:     []string{"uses", "works_on"},
			SourceFingerprint: "sha256:community-window-1",
			Memberships: []CommunityMembershipInput{
				{EntityID: mark.EntityID, Rank: 0, MembershipScore: 1, SourceCount: 1},
				{EntityID: denseMem.EntityID, Rank: 1, MembershipScore: 1, SourceCount: 2},
				{EntityID: postgres.EntityID, Rank: 2, MembershipScore: 1, SourceCount: 1},
			},
			Sources: []CommunitySourceInput{
				{
					RelationshipID:      work.Relationship.RelationshipID,
					OwnerProfileID:      ownerA,
					RelationshipVersion: work.Relationship.Version,
					SourceRank:          0,
				},
				{
					RelationshipID:      uses.Relationship.RelationshipID,
					OwnerProfileID:      ownerA,
					RelationshipVersion: uses.Relationship.Version,
					SourceRank:          1,
				},
			},
		}},
	}
	require.NoError(t, semanticRepo.PublishCommunitySnapshot(ctx, publishInput))

	list, err := semanticRepo.ListCommunities(ctx, CommunityListInput{
		TeamID: teamA,
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "current", list[0].Status)
	otherTeamList, err := semanticRepo.ListCommunities(ctx, CommunityListInput{
		TeamID: teamC,
		Limit:  10,
	})
	require.NoError(t, err)
	assert.Empty(t, otherTeamList)

	recalledCommunities, err := semanticRepo.RecallCommunities(ctx, CommunityRecallInput{
		TeamID:            teamA,
		Query:             "PostgreSQL",
		Limit:             10,
		RelationshipLimit: 1,
	})
	require.NoError(t, err)
	require.Len(t, recalledCommunities, 1)
	assert.NotEmpty(t, recalledCommunities[0].TopEntities)
	require.Len(t, recalledCommunities[0].Relationships, 1)
	assert.Equal(t, teamA, recalledCommunities[0].Relationships[0].TeamID)
	assert.Contains(t, []string{work.Relationship.RelationshipID, uses.Relationship.RelationshipID}, recalledCommunities[0].Relationships[0].RelationshipID)

	discovery, err := semanticRepo.RecallCommunityDiscovery(ctx, CommunityDiscoveryInput{
		TeamID: teamA,
		Query:  "PostgreSQL",
		Limit:  1,
	})
	require.NoError(t, err)
	require.Len(t, discovery, 1)
	assert.Contains(t, []string{work.Relationship.RelationshipID, uses.Relationship.RelationshipID}, discovery[0].Relationship.RelationshipID)
	assert.NotEmpty(t, discovery[0].EvidenceIDs)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET version = version + 1
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamA, uses.Relationship.RelationshipID).Error
	}))
	staleCount, err := semanticRepo.RefreshCommunityStaleness(ctx, CommunityStalenessInput{
		TeamID: teamA,
		Limit:  10,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, staleCount)
	current, err := semanticRepo.ListCommunities(ctx, CommunityListInput{
		TeamID: teamA,
		Limit:  10,
	})
	require.NoError(t, err)
	assert.Empty(t, current)

	discoveryAfterStale, err := semanticRepo.RecallCommunityDiscovery(ctx, CommunityDiscoveryInput{
		TeamID: teamA,
		Query:  "PostgreSQL",
		Limit:  1,
	})
	require.NoError(t, err)
	assert.Empty(t, discoveryAfterStale)

	secondRun, err := semanticRepo.ClaimCommunityRun(ctx, CommunityRunClaimInput{
		TeamID:     teamA,
		WindowKey:  "community-window-2",
		LeaseUntil: time.Now().UTC().Add(time.Minute),
		MaxNodes:   10,
		MaxEdges:   10,
	})
	require.NoError(t, err)
	require.True(t, secondRun.Claimed)
	stalePublish := publishInput
	stalePublish.RunID = secondRun.RunID
	err = semanticRepo.PublishCommunitySnapshot(ctx, stalePublish)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCommunitySourceStale), err)
}

func findCommunityInput(inputs []CommunityInput, relationshipID string) *CommunityInput {
	for i := range inputs {
		if inputs[i].RelationshipID == relationshipID {
			return &inputs[i]
		}
	}
	return nil
}

func communitySourceSnapshot(relationship *RelationshipRecord) map[string]any {
	return map[string]any{
		"relationship_id": relationship.RelationshipID,
		"version":         relationship.Version,
	}
}
