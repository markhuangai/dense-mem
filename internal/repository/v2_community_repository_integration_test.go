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

func TestV2CommunityRepositorySnapshotLifecycleAndRecallExpansion(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createV2LedgerTeam(t, adminDB, rls, "community-team-a")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamA, "community-owner-a")
	teamC := createV2LedgerTeam(t, adminDB, rls, "community-team-c")
	ownerC := createV2LedgerProfile(t, adminDB, rls, teamC, "community-owner-c")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)
	searchRepo := NewV2SearchRepository(appDB, rls)

	mark := createV2SemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "person", "Mark Huang")
	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "product", "PostgreSQL")
	otherTeamProject := createV2SemanticEntity(t, ctx, semanticRepo, teamC, ownerC, "project", "Other Project")
	otherTeamPostgres := createV2SemanticEntity(t, ctx, semanticRepo, teamC, ownerC, "product", "PostgreSQL")

	workIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"community-work-source", "The maintainer works on the memory service.")
	work := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        workIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     workIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:community-work",
			SpanStart:      0,
			SpanEnd:        len("The maintainer works on the memory service."),
			Authority:      "primary",
		},
	})

	usesIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"community-uses-source", "Dense-Mem depends on the durable database.")
	uses := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        usesIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     usesIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:community-uses",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem depends on the durable database."),
			Authority:      "primary",
		},
	})

	candidateIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"community-candidate-source", "Mark may use PostgreSQL.")
	candidate := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        candidateIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		EvidenceVerdict: "insufficient",
	})
	require.NotNil(t, candidate.Relationship)

	otherTeamIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamC, ownerC,
		"community-other-team-source", "Other Project uses PostgreSQL.")
	otherTeamRelationship := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamC,
		OwnerProfileID:  ownerC,
		IngestID:        otherTeamIngest.IngestID,
		SubjectEntityID: otherTeamProject.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  otherTeamPostgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     otherTeamIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:community-other",
			SpanStart:      0,
			SpanEnd:        len("Other Project uses PostgreSQL."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, otherTeamRelationship.Relationship)

	inputs, err := semanticRepo.ListV2CommunityInputs(ctx, V2CommunityInputListInput{
		TeamID: teamA,
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, inputs, 2)
	require.NotNil(t, findV2CommunityInput(inputs, work.Relationship.RelationshipID))
	require.NotNil(t, findV2CommunityInput(inputs, uses.Relationship.RelationshipID))
	require.Nil(t, findV2CommunityInput(inputs, candidate.Relationship.RelationshipID))
	require.Nil(t, findV2CommunityInput(inputs, otherTeamRelationship.Relationship.RelationshipID))

	run, err := semanticRepo.ClaimV2CommunityRun(ctx, V2CommunityRunClaimInput{
		TeamID:     teamA,
		WindowKey:  "community-window-1",
		LeaseUntil: time.Now().UTC().Add(time.Minute),
		MaxNodes:   10,
		MaxEdges:   10,
	})
	require.NoError(t, err)
	require.True(t, run.Claimed)
	duplicate, err := semanticRepo.ClaimV2CommunityRun(ctx, V2CommunityRunClaimInput{
		TeamID:     teamA,
		WindowKey:  "community-window-1",
		LeaseUntil: time.Now().UTC().Add(time.Minute),
		MaxNodes:   10,
		MaxEdges:   10,
	})
	require.NoError(t, err)
	require.False(t, duplicate.Claimed)
	require.Equal(t, run.RunID, duplicate.RunID)

	_, err = searchRepo.UpsertSearchDocument(ctx, V2UpsertSearchDocumentInput{
		TeamID:         teamA,
		OwnerProfileID: ownerA,
		SourceKind:     "evidence",
		SourceID:       usesIngest.Evidence[0].FragmentID,
		SourceVersion:  1,
		DocumentText:   "Dense-Mem depends on the durable database.",
	})
	require.NoError(t, err)

	publishInput := V2CommunitySnapshotPublishInput{
		TeamID:            teamA,
		RunID:             run.RunID,
		SourceFingerprint: "sha256:community-window-1",
		SourceSnapshot: []map[string]any{
			v2CommunitySourceSnapshot(work.Relationship),
			v2CommunitySourceSnapshot(uses.Relationship),
		},
		NodeCount: 3,
		EdgeCount: 2,
		Communities: []V2CommunityPublishRecord{{
			CommunityID:       uuid.NewString(),
			Ordinal:           0,
			Summary:           "Community groups Dense-Mem and PostgreSQL operational facts.",
			SummaryVersion:    "community-deterministic-v1",
			MemberCount:       3,
			SourceCount:       2,
			TopEntities:       []string{"Dense-Mem", "PostgreSQL"},
			TopPredicates:     []string{"uses", "works_on"},
			SourceFingerprint: "sha256:community-window-1",
			Memberships: []V2CommunityMembershipInput{
				{EntityID: mark.EntityID, Rank: 0, MembershipScore: 1, SourceCount: 1},
				{EntityID: denseMem.EntityID, Rank: 1, MembershipScore: 1, SourceCount: 2},
				{EntityID: postgres.EntityID, Rank: 2, MembershipScore: 1, SourceCount: 1},
			},
			Sources: []V2CommunitySourceInput{
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
	require.NoError(t, semanticRepo.PublishV2CommunitySnapshot(ctx, publishInput))

	list, err := semanticRepo.ListV2Communities(ctx, V2CommunityListInput{
		TeamID: teamA,
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "current", list[0].Status)
	otherTeamList, err := semanticRepo.ListV2Communities(ctx, V2CommunityListInput{
		TeamID: teamC,
		Limit:  10,
	})
	require.NoError(t, err)
	assert.Empty(t, otherTeamList)

	discovery, err := semanticRepo.RecallV2CommunityDiscovery(ctx, V2CommunityDiscoveryInput{
		TeamID: teamA,
		Query:  "PostgreSQL",
		Limit:  1,
	})
	require.NoError(t, err)
	require.Len(t, discovery, 1)
	assert.Equal(t, uses.Relationship.RelationshipID, discovery[0].Relationship.RelationshipID)
	assert.Contains(t, discovery[0].EvidenceIDs, usesIngest.Evidence[0].FragmentID)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET version = version + 1
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamA, uses.Relationship.RelationshipID).Error
	}))
	staleCount, err := semanticRepo.RefreshV2CommunityStaleness(ctx, V2CommunityStalenessInput{
		TeamID: teamA,
		Limit:  10,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, staleCount)
	current, err := semanticRepo.ListV2Communities(ctx, V2CommunityListInput{
		TeamID: teamA,
		Limit:  10,
	})
	require.NoError(t, err)
	assert.Empty(t, current)

	discoveryAfterStale, err := semanticRepo.RecallV2CommunityDiscovery(ctx, V2CommunityDiscoveryInput{
		TeamID: teamA,
		Query:  "PostgreSQL",
		Limit:  1,
	})
	require.NoError(t, err)
	assert.Empty(t, discoveryAfterStale)

	secondRun, err := semanticRepo.ClaimV2CommunityRun(ctx, V2CommunityRunClaimInput{
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
	err = semanticRepo.PublishV2CommunitySnapshot(ctx, stalePublish)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2CommunitySourceStale), err)
}

func findV2CommunityInput(inputs []V2CommunityInput, relationshipID string) *V2CommunityInput {
	for i := range inputs {
		if inputs[i].RelationshipID == relationshipID {
			return &inputs[i]
		}
	}
	return nil
}

func v2CommunitySourceSnapshot(relationship *V2RelationshipRecord) map[string]any {
	return map[string]any{
		"relationship_id": relationship.RelationshipID,
		"version":         relationship.Version,
	}
}
