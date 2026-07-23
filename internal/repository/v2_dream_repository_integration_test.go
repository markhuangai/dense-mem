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

func TestV2DreamRepositoryCandidateSafeHypothesisLifecycle(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "dream-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "dream-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	mark := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Mark Huang")
	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")

	candidateIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"dream candidate source", "Dense-Mem may use PostgreSQL.")
	candidate := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        candidateIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		EvidenceVerdict: "insufficient",
	})
	require.NotNil(t, candidate.Relationship)

	activeIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"dream active source", "Mark Huang works on Dense-Mem.")
	active := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        activeIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     activeIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:dream-active",
			SpanStart:      0,
			SpanEnd:        len("Mark Huang works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, active.Relationship)

	run, err := semanticRepo.ClaimV2DreamCycle(ctx, V2DreamCycleClaimInput{
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

	inputs, err := semanticRepo.ListV2DreamInputs(ctx, V2DreamInputListInput{
		TeamID: teamID,
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, inputs, 2)
	assertV2DreamInput(t, inputs, candidate.Relationship.RelationshipID, "candidate", "pending_evidence")
	assertV2DreamInput(t, inputs, active.Relationship.RelationshipID, "validated_claim", "active")

	proposal := V2UpsertHypothesisInput{
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
		GeneratorVersion:      "test-v2-dream",
		Payload:               map[string]any{"source_tier": "candidate"},
	}
	record, inserted, err := semanticRepo.UpsertV2Hypothesis(ctx, proposal)
	require.NoError(t, err)
	require.True(t, inserted)
	require.NotEmpty(t, record.HypothesisID)
	assert.Equal(t, "proposed", record.Status)

	record, inserted, err = semanticRepo.UpsertV2Hypothesis(ctx, proposal)
	require.NoError(t, err)
	require.False(t, inserted)
	assert.Equal(t, "reinforced", record.Status)

	recall, err := semanticRepo.RecallV2Hypotheses(ctx, V2RecallHypothesesInput{
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
	staleCount, err := semanticRepo.RefreshV2HypothesisStaleness(ctx, V2RefreshHypothesisStalenessInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, staleCount)
	stale, _, err := semanticRepo.ListV2Hypotheses(ctx, V2ListHypothesesInput{
		TeamID: teamID,
		Status: "stale",
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, record.HypothesisID, stale[0].HypothesisID)

	_, _, err = semanticRepo.UpsertV2Hypothesis(ctx, staleProposal)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2DreamSourceStale), err)

	exactActive := V2UpsertHypothesisInput{
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
		GeneratorVersion:      "test-v2-dream",
		Payload:               map[string]any{"source_tier": "validated_claim"},
	}
	_, _, err = semanticRepo.UpsertV2Hypothesis(ctx, exactActive)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2DreamExactRelationshipExists), err)
}

func assertV2DreamInput(t *testing.T, inputs []V2DreamInput, relationshipID, tier, status string) {
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
