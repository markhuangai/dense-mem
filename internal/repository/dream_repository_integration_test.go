package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDreamRepositoryCandidateSafeHypothesisLifecycle(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	mark := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Mark Huang")
	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")

	candidateIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"dream candidate source", "Dense-Mem may use PostgreSQL.")
	candidate := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        candidateIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		EvidenceVerdict: "insufficient",
	})
	require.NotNil(t, candidate.Relationship)

	activeIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"dream active source", "Mark Huang works on Dense-Mem.")
	active := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        activeIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     activeIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:dream-active",
			SpanStart:      0,
			SpanEnd:        len("Mark Huang works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, active.Relationship)

	run, err := semanticRepo.ClaimDreamCycle(ctx, DreamCycleClaimInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		RunDate:        "2026-07-17",
		WindowKey:      "dream-lifecycle",
		LeaseUntil:     time.Now().UTC().Add(time.Minute),
		SourceSnapshot: []map[string]any{{
			"relationship_id": candidate.Relationship.RelationshipID,
			"version":         candidate.Relationship.Version,
		}},
	})
	require.NoError(t, err)
	require.True(t, run.Claimed)

	inputs, err := semanticRepo.ListDreamInputs(ctx, DreamInputListInput{
		TeamID: teamID,
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, inputs, 2)
	assertDreamInput(t, inputs, candidate.Relationship.RelationshipID, "candidate", "pending_evidence")
	assertDreamInput(t, inputs, active.Relationship.RelationshipID, "validated_claim", "active")

	proposal := UpsertHypothesisInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		RunID:            run.RunID,
		Statement:        "Dense-Mem may use PostgreSQL.",
		Rationale:        "The candidate needs independent evidence before semantic commitment.",
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     "uses",
		PredicateVersion: 1,
		ObjectEntityID:   postgres.EntityID,
		SourceRefs: []map[string]any{{
			"type": "candidate_relationship",
			"id":   candidate.Relationship.RelationshipID,
		}},
		SourceVersions: map[string]int{
			candidate.Relationship.RelationshipID: candidate.Relationship.Version,
		},
		SourceOwnerProfileIDs: []string{ownerID},
		ContentHash:           "sha256:dream-candidate-postgres",
		GeneratorKind:         "test",
		GeneratorVersion:      "test-dream",
		Payload:               map[string]any{"source_tier": "candidate"},
	}
	record, inserted, err := semanticRepo.UpsertHypothesis(ctx, proposal)
	require.NoError(t, err)
	require.True(t, inserted)
	require.NotEmpty(t, record.HypothesisID)
	assert.Equal(t, "proposed", record.Status)

	record, inserted, err = semanticRepo.UpsertHypothesis(ctx, proposal)
	require.NoError(t, err)
	require.False(t, inserted)
	assert.Equal(t, "reinforced", record.Status)

	recall, err := semanticRepo.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID: teamID,
		Query:  "PostgreSQL",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, recall, 1)
	assert.Equal(t, record.HypothesisID, recall[0].HypothesisID)

	staleProposal := proposal
	staleProposal.ContentHash = "sha256:dream-candidate-stale"
	staleProposal.Statement = "Dense-Mem may use PostgreSQL after the source changes."
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET version = version + 1
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, candidate.Relationship.RelationshipID).Error
	}))
	staleCount, err := semanticRepo.RefreshHypothesisStaleness(ctx, RefreshHypothesisStalenessInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, staleCount)
	stale, _, err := semanticRepo.ListHypotheses(ctx, ListHypothesesInput{
		TeamID: teamID,
		Status: "stale",
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, record.HypothesisID, stale[0].HypothesisID)

	_, _, err = semanticRepo.UpsertHypothesis(ctx, staleProposal)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDreamSourceStale), err)

	exactActive := UpsertHypothesisInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		RunID:            run.RunID,
		Statement:        "Mark Huang may work on Dense-Mem.",
		Rationale:        "This duplicates an already active relationship.",
		SubjectEntityID:  mark.EntityID,
		PredicateKey:     "works_on",
		PredicateVersion: 1,
		ObjectEntityID:   denseMem.EntityID,
		SourceRefs: []map[string]any{{
			"type": "relationship",
			"id":   active.Relationship.RelationshipID,
		}},
		SourceVersions: map[string]int{
			active.Relationship.RelationshipID: active.Relationship.Version,
		},
		SourceOwnerProfileIDs: []string{ownerID},
		ContentHash:           "sha256:dream-exact-active",
		GeneratorKind:         "test",
		GeneratorVersion:      "test-dream",
		Payload:               map[string]any{"source_tier": "validated_claim"},
	}
	_, _, err = semanticRepo.UpsertHypothesis(ctx, exactActive)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDreamExactRelationshipExists), err)
}

func assertDreamInput(t *testing.T, inputs []DreamInput, relationshipID, tier, status string) {
	t.Helper()
	for _, input := range inputs {
		if input.RelationshipID == relationshipID {
			assert.Equal(t, tier, input.Tier)
			assert.Equal(t, status, input.Status)
			return
		}
	}
	t.Fatalf("missing dream input %s in %+v", relationshipID, inputs)
}
