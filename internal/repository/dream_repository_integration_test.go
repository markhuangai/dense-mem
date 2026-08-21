package repository

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testDreamPathPredicateFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const changedTestDreamPathPredicateFingerprint = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

func TestStaleDreamSourceErrorMapsMissingRelationship(t *testing.T) {
	require.ErrorIs(t, staleDreamSourceError(sql.ErrNoRows, uuid.NewString()), ErrDreamSourceStale)
}

func TestDreamInputsIncludeExpiredPendingEvidence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-pending-input-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-pending-input-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	content := "Dense-Mem may use PostgreSQL after independent review."
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "dream-pending-input", content)
	assessment, _, err := ledgerRepo.PersistPlacementAssessment(ctx, placementAssessmentPersistInput(teamID, ownerID, ingest.Items[0]))
	require.NoError(t, err)
	threshold := 0.8
	pending := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:                  teamID,
		OwnerProfileID:          ownerID,
		IngestID:                ingest.IngestID,
		PlacementItemID:         ingest.Items[0].PlacementItemID,
		SubjectEntityID:         denseMem.EntityID,
		PredicateKey:            "uses",
		ObjectEntityID:          postgres.EntityID,
		EvidenceVerdict:         "insufficient",
		AssessmentID:            assessment.AssessmentID,
		AssessmentPolicyVersion: AssessmentPolicyVersion,
		ThresholdUsed:           &threshold,
		GateResult:              "below_write_threshold",
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "pending_observation",
			SpanStart:      0,
			SpanEnd:        len([]rune(content)),
			Authority:      "primary",
		},
	})
	require.Equal(t, "pending_evidence", pending.Relationship.Status)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO review_tasks (
			    team_id, owner_profile_id, ingest_id, placement_item_id,
			    relationship_id, observation_id, task_type, status, reason,
			    payload, dedupe_key, assessment_id
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    ?::uuid, ?::uuid, 'relationship_needs_review', 'expired', 'support_confidence',
			    '{}'::jsonb, '', ?::uuid
			)
		`, teamID, ownerID, ingest.IngestID, ingest.Items[0].PlacementItemID,
			pending.Relationship.RelationshipID, pending.ObservationID, assessment.AssessmentID).Error
	}))

	inputs, err := semanticRepo.ListDreamInputs(ctx, DreamInputListInput{TeamID: teamID, Limit: 10})
	require.NoError(t, err)
	input := requireDreamInput(t, inputs, pending.Relationship.RelationshipID)
	assert.Equal(t, "pending_evidence", input.Status)
	require.Len(t, input.Evidence, 1)
	assert.Equal(t, content, input.Evidence[0].Content)
	assert.Equal(t, pending.ObservationID, input.Evidence[0].ObservationID)
}

func TestDreamRepositoryPersistsEvidenceGroundedHypothesisAndPathAssessment(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-owner")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "other-dream-team")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, otherTeamID, "other-dream-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	app := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	runtime := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "Runtime")
	database := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	first := createActiveDreamRelationship(t, ctx, ledgerRepo, semanticRepo, teamID, ownerID,
		"dream-app-runtime", "Dense-Mem uses Runtime.", app.EntityID, runtime.EntityID, "dream:app-runtime")
	second := createActiveDreamRelationship(t, ctx, ledgerRepo, semanticRepo, teamID, ownerID,
		"dream-runtime-database", "Runtime uses PostgreSQL.", runtime.EntityID, database.EntityID, "dream:runtime-database")

	run, err := semanticRepo.ClaimDreamCycle(ctx, DreamCycleClaimInput{
		TeamID:               teamID,
		InitiatedByProfileID: ownerID,
		RunDate:              "2026-07-17",
		WindowKey:            "manual:dream-lifecycle",
		LeaseToken:           uuid.NewString(),
		LeaseUntil:           time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)
	require.True(t, run.Claimed)

	inputs, err := semanticRepo.ListDreamInputs(ctx, DreamInputListInput{TeamID: teamID, Limit: 10})
	require.NoError(t, err)
	firstInput := requireDreamInput(t, inputs, first.Relationship.RelationshipID)
	secondInput := requireDreamInput(t, inputs, second.Relationship.RelationshipID)
	require.Equal(t, "active", firstInput.Status)
	require.Equal(t, "active", secondInput.Status)
	require.Len(t, firstInput.Evidence, 1)
	require.Len(t, secondInput.Evidence, 1)

	proposal := evidenceGroundedDreamProposal(teamID, ownerID, run.RunID, firstInput, secondInput,
		app.EntityID, database.EntityID, "uses", "Dense-Mem may transitively depend on PostgreSQL.")
	missingSecondPremise := normalizeUpsertHypothesisInput(proposal)
	missingSecondPremise.Derivations = append([]DreamDerivationSource(nil), proposal.Derivations...)
	missingSecondPremise.Derivations[1].PremisePosition = 1
	require.ErrorContains(t, validateUpsertHypothesisInput(missingSecondPremise, false), "cover both premise positions")
	path := DreamPathEvaluationInput{
		FirstRelationshipID:         firstInput.RelationshipID,
		FirstRelationshipVersion:    firstInput.Version,
		SecondRelationshipID:        secondInput.RelationshipID,
		SecondRelationshipVersion:   secondInput.Version,
		AllowedPredicateFingerprint: testDreamPathPredicateFingerprint,
	}
	persisted, err := semanticRepo.PersistDreamGeneration(ctx, DreamGenerationPersistInput{
		TeamID:             teamID,
		CreatedByProfileID: ownerID,
		RunID:              run.RunID,
		LeaseToken:         run.LeaseToken,
		ProviderModel:      "test-provider",
		Proposals:          []UpsertHypothesisInput{proposal},
		EvaluatedPaths:     []DreamPathEvaluationInput{path},
	})
	require.NoError(t, err)
	require.Equal(t, 1, persisted.Created)
	require.Zero(t, persisted.Rejected)

	var hiddenDerivations, hiddenEvaluations int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, otherTeamID, otherOwnerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT count(*)
			FROM hypothesis_derivation_sources
			WHERE team_id = ?::uuid
		`, teamID).Scan(&hiddenDerivations).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)
			FROM dream_path_evaluations
			WHERE team_id = ?::uuid
		`, teamID).Scan(&hiddenEvaluations).Error
	}))
	assert.Zero(t, hiddenDerivations)
	assert.Zero(t, hiddenEvaluations)

	available, err := semanticRepo.ListAvailableDreamTargets(ctx, teamID, []DreamTargetCandidate{
		{
			PathRef:         "path_existing",
			PredicateRef:    "predicate_existing",
			SubjectEntityID: app.EntityID,
			PredicateKey:    "uses",
			ObjectEntityID:  runtime.EntityID,
		},
		{
			PathRef:         "path_hypothesis",
			PredicateRef:    "predicate_hypothesis",
			SubjectEntityID: app.EntityID,
			PredicateKey:    "uses",
			ObjectEntityID:  database.EntityID,
		},
		{
			PathRef:         "path_available_first",
			PredicateRef:    "predicate_available_first",
			SubjectEntityID: app.EntityID,
			PredicateKey:    "enables",
			ObjectEntityID:  database.EntityID,
		},
		{
			PathRef:         "path_available_second",
			PredicateRef:    "predicate_available_second",
			SubjectEntityID: app.EntityID,
			PredicateKey:    "enables",
			ObjectEntityID:  database.EntityID,
		},
	})
	require.NoError(t, err)
	require.Equal(t, []DreamTargetCandidate{
		{
			PathRef:         "path_available_first",
			PredicateRef:    "predicate_available_first",
			SubjectEntityID: app.EntityID,
			PredicateKey:    "enables",
			ObjectEntityID:  database.EntityID,
		},
		{
			PathRef:         "path_available_second",
			PredicateRef:    "predicate_available_second",
			SubjectEntityID: app.EntityID,
			PredicateKey:    "enables",
			ObjectEntityID:  database.EntityID,
		},
	}, available, "one set query preserves every available path/predicate pair")

	records, _, err := semanticRepo.ListHypotheses(ctx, ListHypothesesInput{TeamID: teamID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 1)
	record := records[0]
	require.Equal(t, ownerID, record.CreatedByProfileID)
	require.Len(t, record.Derivations, 2)
	assert.Equal(t, firstInput.Evidence[0].Content, record.Derivations[0].Quote)
	assert.Equal(t, secondInput.Evidence[0].Content, record.Derivations[1].Quote)
	assert.Equal(t, firstInput.RelationshipID, record.Derivations[0].RelationshipID)
	assert.Equal(t, secondInput.RelationshipID, record.Derivations[1].RelationshipID)

	loaded, err := semanticRepo.GetHypothesis(ctx, GetHypothesisInput{TeamID: teamID, HypothesisID: record.HypothesisID})
	require.NoError(t, err)
	require.Len(t, loaded.Derivations, 2)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE hypotheses
			SET target_identity = NULL
			WHERE team_id = ?::uuid
			  AND hypothesis_id = ?::uuid
		`, teamID, record.HypothesisID).Error
	}))
	available, err = semanticRepo.ListAvailableDreamTargets(ctx, teamID, []DreamTargetCandidate{{
		PathRef:         "path_legacy_hypothesis",
		PredicateRef:    "predicate_legacy_hypothesis",
		SubjectEntityID: app.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  database.EntityID,
	}})
	require.NoError(t, err)
	assert.Empty(t, available, "a legacy hypothesis without target identity still blocks the exact target")
	recalled, err := semanticRepo.RecallHypotheses(ctx, RecallHypothesesInput{TeamID: teamID, Query: "Dense-Mem", Limit: 10})
	require.NoError(t, err)
	require.Len(t, recalled, 1, "a current exact derivation is recallable")
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO relationship_support_decision_events (
			    team_id, support_id, relationship_id, owner_profile_id,
			    actor_profile_id, decision, reason, metadata
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    'revoke', 'recall derivation regression', '{}'::jsonb
			)
		`, teamID, record.Derivations[0].SupportID, firstInput.RelationshipID, ownerID, ownerID).Error
	}))
	recalled, err = semanticRepo.RecallHypotheses(ctx, RecallHypothesesInput{TeamID: teamID, Query: "Dense-Mem", Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, recalled, "revoking an exact cited support hides the hypothesis from recall")

	unassessed, err := semanticRepo.ListUnassessedDreamPaths(ctx, teamID, []DreamPathEvaluationInput{path})
	require.NoError(t, err)
	require.Empty(t, unassessed, "the exact relationship-version pair is evaluated once")
	predicateChangedPath := path
	predicateChangedPath.AllowedPredicateFingerprint = changedTestDreamPathPredicateFingerprint
	unassessed, err = semanticRepo.ListUnassessedDreamPaths(ctx, teamID, []DreamPathEvaluationInput{predicateChangedPath})
	require.NoError(t, err)
	require.Equal(t, []DreamPathEvaluationInput{predicateChangedPath}, unassessed, "a predicate allowlist change permits reassessment")

	_, err = semanticRepo.UpdateHypothesisStatus(ctx, UpdateHypothesisStatusInput{
		TeamID:         teamID,
		ActorProfileID: ownerID,
		HypothesisID:   record.HypothesisID,
		Status:         "rejected",
		Decision:       "reject",
	})
	require.NoError(t, err)
	available, err = semanticRepo.ListAvailableDreamTargets(ctx, teamID, []DreamTargetCandidate{{
		PathRef:         "path_1",
		PredicateRef:    "predicate_uses",
		SubjectEntityID: app.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  database.EntityID,
	}})
	require.NoError(t, err)
	assert.Empty(t, available, "a rejected hypothesis still blocks the exact target")

	pending := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "dream-pending-target", "Dense-Mem may work on PostgreSQL.").IngestID,
		SubjectEntityID: app.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  database.EntityID,
		EvidenceVerdict: "insufficient",
	})
	require.Equal(t, "pending_evidence", pending.Relationship.Status)
	available, err = semanticRepo.ListAvailableDreamTargets(ctx, teamID, []DreamTargetCandidate{{
		PathRef:         "path_2",
		PredicateRef:    "predicate_works_on",
		SubjectEntityID: app.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  database.EntityID,
	}})
	require.NoError(t, err)
	assert.Empty(t, available, "a pending relationship still blocks the exact target")

	require.NoError(t, semanticRepo.CompleteDreamCycle(ctx, DreamCycleCompleteInput{
		TeamID:               teamID,
		InitiatedByProfileID: ownerID,
		RunID:                run.RunID,
		LeaseToken:           run.LeaseToken,
		Status:               "completed",
		InputCount:           len(inputs),
		CreatedHypotheses:    1,
	}))

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records
			SET version = version + 1,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, firstInput.RelationshipID).Error
	}))
	path.FirstRelationshipVersion++
	unassessed, err = semanticRepo.ListUnassessedDreamPaths(ctx, teamID, []DreamPathEvaluationInput{path})
	require.NoError(t, err)
	require.Equal(t, []DreamPathEvaluationInput{path}, unassessed, "a source version change permits reassessment")
}

func TestDreamRepositoryPersistsTeamScopedPredicateHypothesis(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-team-predicate-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "dream-team-predicate-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	middle := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "Runtime")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	first := createActiveDreamRelationship(t, ctx, ledgerRepo, semanticRepo, teamID, ownerID,
		"dream-team-predicate-first", "Dense-Mem uses Runtime.", subject.EntityID, middle.EntityID, "dream:team-predicate-first")
	second := createActiveDreamRelationship(t, ctx, ledgerRepo, semanticRepo, teamID, ownerID,
		"dream-team-predicate-second", "Runtime uses PostgreSQL.", middle.EntityID, object.EntityID, "dream:team-predicate-second")

	predicate, err := semanticRepo.EnsureSemanticReviewPredicateCandidate(ctx, EnsureSemanticPredicateCandidateInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		Predicate:        "depends transitively on",
		RelationshipKind: "state",
		SubjectKind:      "project",
		ObjectKind:       "product",
		Origin:           "provider_generated",
	})
	require.NoError(t, err)
	require.Equal(t, "depends_transitively_on", predicate.PredicateKey)

	targetPredicates, err := semanticRepo.ListDreamTargetPredicates(ctx, teamID)
	require.NoError(t, err)
	require.Contains(t, targetPredicates, DreamTargetPredicate{
		PredicateKey:        predicate.PredicateKey,
		Version:             predicate.Version,
		AllowedSubjectKinds: []string{"project"},
		AllowedObjectKinds:  []string{"product"},
		RelationshipKind:    "state",
		CurrentCardinality:  "many",
	})

	var globalPredicateCount int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)::int
			FROM predicate_definitions
			WHERE predicate_key = ?
			  AND version = ?
		`, predicate.PredicateKey, predicate.Version).Scan(&globalPredicateCount).Error
	}))
	require.Zero(t, globalPredicateCount, "the regression requires a team-only predicate")

	run, err := semanticRepo.ClaimDreamCycle(ctx, DreamCycleClaimInput{
		TeamID:               teamID,
		InitiatedByProfileID: ownerID,
		RunDate:              "2026-08-05",
		WindowKey:            "manual:team-predicate",
		LeaseToken:           uuid.NewString(),
		LeaseUntil:           time.Now().UTC().Add(time.Minute),
	})
	require.NoError(t, err)

	inputs, err := semanticRepo.ListDreamInputs(ctx, DreamInputListInput{TeamID: teamID, Limit: 10})
	require.NoError(t, err)
	firstInput := requireDreamInput(t, inputs, first.Relationship.RelationshipID)
	secondInput := requireDreamInput(t, inputs, second.Relationship.RelationshipID)
	proposal := evidenceGroundedDreamProposal(
		teamID,
		ownerID,
		run.RunID,
		firstInput,
		secondInput,
		subject.EntityID,
		object.EntityID,
		predicate.PredicateKey,
		"Dense-Mem may depend transitively on PostgreSQL.",
	)
	proposal.PredicateVersion = predicate.Version

	persisted, err := semanticRepo.PersistDreamGeneration(ctx, DreamGenerationPersistInput{
		TeamID:             teamID,
		CreatedByProfileID: ownerID,
		RunID:              run.RunID,
		LeaseToken:         run.LeaseToken,
		ProviderModel:      "test-provider",
		Proposals:          []UpsertHypothesisInput{proposal},
		EvaluatedPaths: []DreamPathEvaluationInput{{
			FirstRelationshipID:         firstInput.RelationshipID,
			FirstRelationshipVersion:    firstInput.Version,
			SecondRelationshipID:        secondInput.RelationshipID,
			SecondRelationshipVersion:   secondInput.Version,
			AllowedPredicateFingerprint: testDreamPathPredicateFingerprint,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, persisted.Created)
	require.Zero(t, persisted.Rejected)

	records, _, err := semanticRepo.ListHypotheses(ctx, ListHypothesesInput{TeamID: teamID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, predicate.PredicateKey, records[0].PredicateKey)
	assert.Equal(t, predicate.Version, records[0].PredicateVersion)
}

func TestScheduledDreamRecoveryFencesExpiredLease(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "dream-recovery-team")
	semanticRepo := NewSemanticRepository(appDB, rls)
	scheduledFor := time.Now().UTC().Truncate(time.Minute)
	firstLeaseToken := uuid.NewString()
	claimed, err := semanticRepo.ClaimScheduledDreamCycle(ctx, DreamCycleClaimInput{
		TeamID:       teamID,
		RunDate:      scheduledFor.Format("2006-01-02"),
		WindowKey:    scheduledFor.Format("2006-01-02"),
		ScheduledFor: &scheduledFor,
		LeaseToken:   firstLeaseToken,
		LeaseUntil:   time.Now().UTC().Add(-time.Minute),
	})
	require.NoError(t, err)
	require.True(t, claimed.Claimed)
	stalePersistence := DreamGenerationPersistInput{
		TeamID:        teamID,
		RunID:         claimed.RunID,
		LeaseToken:    firstLeaseToken,
		ProviderModel: "test-provider",
		EvaluatedPaths: []DreamPathEvaluationInput{{
			FirstRelationshipID:         uuid.NewString(),
			FirstRelationshipVersion:    1,
			SecondRelationshipID:        uuid.NewString(),
			SecondRelationshipVersion:   1,
			AllowedPredicateFingerprint: testDreamPathPredicateFingerprint,
		}},
	}
	_, err = semanticRepo.PersistScheduledDreamGeneration(ctx, stalePersistence)
	require.ErrorIs(t, err, ErrDreamCycleLeaseLost, "an expired worker must not persist output before recovery")

	recovered, err := semanticRepo.ClaimRecoverableScheduledDreamCycle(ctx, DreamCycleRecoveryClaimInput{
		TeamID:      teamID,
		LeaseToken:  uuid.NewString(),
		LeaseUntil:  time.Now().UTC().Add(time.Minute),
		MaxAttempts: 3,
	})
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.True(t, recovered.Claimed)
	require.Equal(t, claimed.RunID, recovered.RunID)
	require.Equal(t, 2, recovered.AttemptCount)
	require.NotEqual(t, firstLeaseToken, recovered.LeaseToken)
	_, err = semanticRepo.PersistScheduledDreamGeneration(ctx, stalePersistence)
	require.ErrorIs(t, err, ErrDreamCycleLeaseLost, "a reclaimed lease must fence stale provider output")

	err = semanticRepo.CompleteScheduledDreamCycle(ctx, DreamCycleCompleteInput{
		TeamID:     teamID,
		RunID:      claimed.RunID,
		LeaseToken: firstLeaseToken,
		Status:     "completed",
	})
	require.ErrorIs(t, err, ErrDreamCycleLeaseLost, "the expired worker must not complete a reclaimed run")
	require.NoError(t, semanticRepo.CompleteScheduledDreamCycle(ctx, DreamCycleCompleteInput{
		TeamID:     teamID,
		RunID:      recovered.RunID,
		LeaseToken: recovered.LeaseToken,
		Status:     "completed",
		OutcomeSummary: map[string]int{
			"eligible_relationships": 0,
		},
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
		return seedTeamPredicateDefinitions(ctx, tx, teamID)
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
		Statement:        "Dense-Mem may work on PostgreSQL after independent evidence.",
		Rationale:        "Team ownership is the behavior under test; evidence-grounded proposals use the provider path.",
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     "works_on",
		PredicateVersion: 1,
		ObjectEntityID:   postgres.EntityID,
		SourceRefs: []map[string]any{{
			"type": "candidate_relationship",
			"id":   candidate.Relationship.RelationshipID,
		}},
		SourceVersions:        map[string]int{candidate.Relationship.RelationshipID: candidate.Relationship.Version},
		SourceOwnerProfileIDs: []string{creatorID},
		ContentHash:           "sha256:scheduled-team-hypothesis",
		GeneratorKind:         "evaluation_seed",
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

	var visibleEvents int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, actorID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM hypothesis_feedback_events
			WHERE team_id = ?::uuid
			  AND hypothesis_id = ?::uuid
		`, teamID, hypothesis.HypothesisID).Scan(&visibleEvents).Error
	}))
	require.Equal(t, 1, visibleEvents)

	var hiddenEvents int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, otherTeamID, otherActorID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM hypothesis_feedback_events
			WHERE team_id = ?::uuid
			  AND hypothesis_id = ?::uuid
		`, teamID, hypothesis.HypothesisID).Scan(&hiddenEvents).Error
	}))
	require.Zero(t, hiddenEvents)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, actorID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO hypothesis_feedback_events (
				team_id, hypothesis_id, actor_profile_id, decision
			) VALUES (?::uuid, ?::uuid, ?::uuid, 'reinforce')
		`, teamID, hypothesis.HypothesisID, creatorID).Error
	})
	require.Error(t, err)

	for _, mutation := range []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "creator",
			query: `UPDATE hypotheses SET created_by_profile_id = ?::uuid WHERE team_id = ?::uuid AND hypothesis_id = ?::uuid`,
			args:  []any{creatorID, teamID, hypothesis.HypothesisID},
		},
		{
			name:  "canonical hypothesis",
			query: `UPDATE hypotheses SET canonical_hypothesis_id = hypothesis_id WHERE team_id = ?::uuid AND hypothesis_id = ?::uuid`,
			args:  []any{teamID, hypothesis.HypothesisID},
		},
		{
			name:  "content hash",
			query: `UPDATE hypotheses SET content_hash = 'sha256:tampered' WHERE team_id = ?::uuid AND hypothesis_id = ?::uuid`,
			args:  []any{teamID, hypothesis.HypothesisID},
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			err := rls.WithTeamProfileTx(ctx, appDB, teamID, actorID, func(tx *gorm.DB) error {
				return tx.Exec(mutation.query, mutation.args...).Error
			})
			require.ErrorContains(t, err, "hypothesis provenance columns are immutable")
		})
	}

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

func TestUpdateHypothesisStatusValidationBindsDecisionToStatus(t *testing.T) {
	input := UpdateHypothesisStatusInput{
		TeamID:         uuid.NewString(),
		ActorProfileID: uuid.NewString(),
		HypothesisID:   uuid.NewString(),
	}
	for _, tc := range []struct {
		name     string
		status   string
		decision string
		wantErr  string
	}{
		{name: "reject", status: "rejected", decision: "reject"},
		{name: "stale", status: "stale", decision: "stale"},
		{name: "reinforce", status: "reinforced", decision: "reinforce"},
		{name: "contradicting", status: "rejected", decision: "reinforce", wantErr: `decision "reinforce" requires status "reinforced"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input.Status = tc.status
			input.Decision = tc.decision
			err := validateUpdateHypothesisStatusInput(input)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func createActiveDreamRelationship(
	t *testing.T,
	ctx context.Context,
	ledgerRepo *LedgerRepositoryImpl,
	semanticRepo *SemanticRepositoryImpl,
	teamID string,
	ownerID string,
	idempotencyKey string,
	content string,
	subjectEntityID string,
	objectEntityID string,
	sourceGroupKey string,
) *RelationshipDecisionResult {
	t.Helper()
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, idempotencyKey, content)
	result := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subjectEntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  objectEntityID,
		EvidenceVerdict: "entailed",
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: sourceGroupKey,
			SpanStart:      0,
			SpanEnd:        len([]rune(content)),
			Authority:      "primary",
		},
	})
	require.Equal(t, "active", result.Relationship.Status)
	require.NotEmpty(t, result.SupportID)
	return result
}

func requireDreamInput(t *testing.T, inputs []DreamInput, relationshipID string) DreamInput {
	t.Helper()
	for _, input := range inputs {
		if input.RelationshipID == relationshipID {
			return input
		}
	}
	t.Fatalf("missing dream input %s in %+v", relationshipID, inputs)
	return DreamInput{}
}

func evidenceGroundedDreamProposal(
	teamID string,
	ownerID string,
	runID string,
	first DreamInput,
	second DreamInput,
	subjectEntityID string,
	objectEntityID string,
	predicateKey string,
	statement string,
) UpsertHypothesisInput {
	firstEvidence := first.Evidence[0]
	secondEvidence := second.Evidence[0]
	return UpsertHypothesisInput{
		TeamID:             teamID,
		CreatedByProfileID: ownerID,
		RunID:              runID,
		Statement:          statement,
		Rationale:          "The provider joined the two supplied relationship premises without adding evidence.",
		SubjectEntityID:    subjectEntityID,
		PredicateKey:       predicateKey,
		PredicateVersion:   1,
		ObjectEntityID:     objectEntityID,
		SourceRefs: []map[string]any{
			{"type": "relationship", "id": first.RelationshipID},
			{"type": "relationship", "id": second.RelationshipID},
		},
		SourceVersions: map[string]int{
			first.RelationshipID:  first.Version,
			second.RelationshipID: second.Version,
		},
		SourceOwnerProfileIDs: []string{ownerID},
		ContentHash:           "sha256:" + uuid.NewString(),
		GeneratorKind:         "provider",
		GeneratorVersion:      "test-provider",
		Derivations: []DreamDerivationSource{
			dreamDerivationFromEvidence(1, first, firstEvidence),
			dreamDerivationFromEvidence(2, second, secondEvidence),
		},
	}
}

func dreamDerivationFromEvidence(position int, input DreamInput, evidence DreamEvidence) DreamDerivationSource {
	return DreamDerivationSource{
		PremisePosition:     position,
		RelationshipID:      input.RelationshipID,
		RelationshipVersion: input.Version,
		SupportID:           evidence.SupportID,
		ObservationID:       evidence.ObservationID,
		FragmentID:          evidence.FragmentID,
		SourceID:            evidence.SourceID,
		SourceRevisionID:    evidence.SourceRevisionID,
		SourceGroupKey:      evidence.SourceGroupKey,
		SpanStart:           evidence.SpanStart,
		SpanEnd:             evidence.SpanEnd,
		Quote:               evidence.Content,
		Authority:           evidence.Authority,
	}
}
