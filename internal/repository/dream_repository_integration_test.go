package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
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

	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"dream candidate source", "Dense-Mem may use PostgreSQL.")
	candidate := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		EvidenceVerdict: "insufficient",
	})
	require.NotNil(t, candidate.Relationship)

	run, err := semanticRepo.ClaimDreamCycle(ctx, DreamCycleClaimInput{
		TeamID:               teamID,
		InitiatedByProfileID: ownerID,
		RunDate:              "2026-07-17",
		WindowKey:            "manual:dream-lifecycle",
		LeaseUntil:           time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	require.True(t, run.Claimed)

	inputs, err := semanticRepo.ListDreamInputs(ctx, DreamInputListInput{TeamID: teamID, Limit: 10})
	require.NoError(t, err)
	assertDreamInput(t, inputs, candidate.Relationship.RelationshipID, "pending_evidence")

	proposal := UpsertHypothesisInput{
		TeamID:             teamID,
		CreatedByProfileID: ownerID,
		RunID:              run.RunID,
		Statement:          "Dense-Mem may use PostgreSQL.",
		Rationale:          "The candidate needs independent evidence before semantic commitment.",
		SubjectEntityID:    denseMem.EntityID,
		PredicateKey:       "uses",
		PredicateVersion:   1,
		ObjectEntityID:     postgres.EntityID,
		SourceRefs: []map[string]any{{
			"type": "candidate_relationship",
			"id":   candidate.Relationship.RelationshipID,
		}},
		SourceVersions:        map[string]int{candidate.Relationship.RelationshipID: candidate.Relationship.Version},
		SourceOwnerProfileIDs: []string{ownerID},
		ContentHash:           "sha256:dream-candidate-postgres",
		GeneratorKind:         "test",
		GeneratorVersion:      "test-dream",
	}
	record, inserted, err := semanticRepo.UpsertHypothesis(ctx, proposal)
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, ownerID, record.CreatedByProfileID)

	record, inserted, err = semanticRepo.UpsertHypothesis(ctx, proposal)
	require.NoError(t, err)
	require.False(t, inserted)
	require.Equal(t, "reinforced", record.Status)

	require.NoError(t, semanticRepo.CompleteDreamCycle(ctx, DreamCycleCompleteInput{
		TeamID:               teamID,
		InitiatedByProfileID: ownerID,
		RunID:                run.RunID,
		Status:               "completed",
		InputCount:           len(inputs),
		CreatedHypotheses:    1,
	}))

	recalled, err := semanticRepo.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID: teamID,
		Query:  "PostgreSQL",
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, recalled, 1)
	require.Equal(t, record.HypothesisID, recalled[0].HypothesisID)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET version = version + 1,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, candidate.Relationship.RelationshipID).Error
	}))

	recalled, err = semanticRepo.RecallHypotheses(ctx, RecallHypothesesInput{
		TeamID: teamID,
		Query:  "PostgreSQL",
		Limit:  10,
	})
	require.NoError(t, err)
	require.Empty(t, recalled)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var status string
		if err := tx.Raw(`
			SELECT status
			FROM hypotheses
			WHERE team_id = ?::uuid
			  AND hypothesis_id = ?::uuid
		`, teamID, record.HypothesisID).Row().Scan(&status); err != nil {
			return err
		}
		assert.Equal(t, "reinforced", status)
		return nil
	}))
}

func TestScheduledDreamsAreTeamOwnedAndFeedbackIsActorAudited(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "scheduled-dream-team")
	creatorID := createLedgerProfile(t, adminDB, rls, teamID, "dream-creator")
	endpointOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-endpoint-owner")
	actorID := createLedgerProfile(t, adminDB, rls, teamID, "dream-reviewer")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "other-dream-team")
	otherActorID := createLedgerProfile(t, adminDB, rls, otherTeamID, "other-reviewer")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return ensureSemanticRefs(ctx, tx, teamID, actorID)
	}))

	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, creatorID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, endpointOwnerID, "product", "PostgreSQL")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, creatorID,
		"scheduled dream source", "Dense-Mem may use PostgreSQL.")
	candidate := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  creatorID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		EvidenceVerdict: "insufficient",
	})
	require.NotNil(t, candidate.Relationship)

	claim := DreamCycleClaimInput{
		TeamID:     teamID,
		RunDate:    "2026-07-31",
		WindowKey:  "2026-07-31",
		LeaseUntil: time.Now().UTC().Add(time.Minute),
	}
	results := make([]*DreamCycleRun, 2)
	errs := make([]error, 2)
	var group sync.WaitGroup
	for i := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index], errs[index] = semanticRepo.ClaimScheduledDreamCycle(ctx, claim)
		}(i)
	}
	group.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Equal(t, results[0].RunID, results[1].RunID)
	require.NotEqual(t, results[0].Claimed, results[1].Claimed)

	var runID string
	for _, result := range results {
		if result.Claimed {
			runID = result.RunID
		}
	}
	require.NotEmpty(t, runID)

	hypothesis, inserted, err := semanticRepo.UpsertScheduledHypothesis(ctx, UpsertHypothesisInput{
		TeamID:           teamID,
		RunID:            runID,
		Statement:        "Dense-Mem may use PostgreSQL after independent evidence.",
		Rationale:        "Team-visible candidate relationship needs confirmation.",
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     "uses",
		PredicateVersion: 1,
		ObjectEntityID:   postgres.EntityID,
		SourceRefs: []map[string]any{{
			"type": "candidate_relationship",
			"id":   candidate.Relationship.RelationshipID,
		}},
		SourceVersions:        map[string]int{candidate.Relationship.RelationshipID: candidate.Relationship.Version},
		SourceOwnerProfileIDs: []string{creatorID},
		ContentHash:           "sha256:scheduled-team-hypothesis",
		GeneratorKind:         "test",
		GeneratorVersion:      "test-dream",
	})
	require.NoError(t, err)
	require.True(t, inserted)
	require.Empty(t, hypothesis.CreatedByProfileID)

	updated, err := semanticRepo.UpdateHypothesisStatus(ctx, UpdateHypothesisStatusInput{
		TeamID:            teamID,
		ActorProfileID:    actorID,
		HypothesisID:      hypothesis.HypothesisID,
		Status:            "reinforced",
		Decision:          "reinforce",
		InvalidatedReason: "reviewed by a different team member",
	})
	require.NoError(t, err)
	require.Equal(t, "reinforced", updated.Status)

	_, err = semanticRepo.UpdateHypothesisStatus(ctx, UpdateHypothesisStatusInput{
		TeamID:         teamID,
		ActorProfileID: otherActorID,
		HypothesisID:   hypothesis.HypothesisID,
		Status:         "rejected",
		Decision:       "reject",
	})
	require.Error(t, err)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var (
			createdBy string
			initiator string
			status    string
			events    int
		)
		if err := tx.Raw(`
			SELECT COALESCE(created_by_profile_id::text, ''), status
			FROM hypotheses
			WHERE team_id = ?::uuid AND hypothesis_id = ?::uuid
		`, teamID, hypothesis.HypothesisID).Row().Scan(&createdBy, &status); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COALESCE(initiated_by_profile_id::text, '')
			FROM dream_cycle_runs
			WHERE team_id = ?::uuid AND run_id = ?::uuid
		`, teamID, runID).Row().Scan(&initiator); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT count(*)
			FROM hypothesis_feedback_events
			WHERE team_id = ?::uuid
			  AND hypothesis_id = ?::uuid
			  AND actor_profile_id = ?::uuid
			  AND decision = 'reinforce'
		`, teamID, hypothesis.HypothesisID, actorID).Scan(&events).Error; err != nil {
			return err
		}
		assert.Empty(t, createdBy)
		assert.Empty(t, initiator)
		assert.Equal(t, "reinforced", status)
		assert.Equal(t, 1, events)
		return nil
	}))

	missed, err := semanticRepo.RecordMissedScheduledDreamCycle(ctx, DreamCycleClaimInput{
		TeamID:    teamID,
		RunDate:   "2026-08-01",
		WindowKey: "2026-08-01",
	})
	require.NoError(t, err)
	require.True(t, missed.Claimed)
	require.Equal(t, "missed", missed.Status)
}

func TestDreamListSortValidation(t *testing.T) {
	input := normalizeListHypothesesInput(ListHypothesesInput{
		TeamID:    uuid.NewString(),
		Sort:      " CREATED_AT ",
		Direction: " ASC ",
	})
	require.NoError(t, validateListHypothesesInput(input))
	assert.Equal(t, "created_at ASC", hypothesisListOrder(input.Sort, input.Direction))

	for _, invalid := range []ListHypothesesInput{
		{TeamID: uuid.NewString(), Sort: "updated_at; DROP TABLE hypotheses", Direction: "asc"},
		{TeamID: uuid.NewString(), Sort: "updated_at", Direction: "desc NULLS FIRST"},
		{TeamID: uuid.NewString(), Sort: "last_evaluated_at", Direction: "desc"},
	} {
		normalized := normalizeListHypothesesInput(invalid)
		require.Error(t, validateListHypothesesInput(normalized))
	}
}

func assertDreamInput(t *testing.T, inputs []DreamInput, relationshipID, status string) {
	t.Helper()
	for _, input := range inputs {
		if input.RelationshipID == relationshipID {
			assert.Equal(t, status, input.Status)
			return
		}
	}
	t.Fatalf("missing dream input %s in %+v", relationshipID, inputs)
}
