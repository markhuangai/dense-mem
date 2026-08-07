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

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestPlacementResolutionSelectPredicateRequeuesOwnerScopedReview(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-resolution-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "person", "Mark")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerA,
		"placement-resolution-predicate", "Mark works on Dense-Mem.")
	unknown := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:            teamID,
		OwnerProfileID:    ownerA,
		IngestID:          ingest.IngestID,
		PlacementItemID:   ingest.Items[0].PlacementItemID,
		SubjectEntityID:   subject.EntityID,
		OriginalPredicate: "works on",
		PredicateKey:      "unknown_predicate",
		PredicateVersion:  1,
		ObjectEntityID:    object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:predicate-resolution",
			SpanStart:      0,
			SpanEnd:        len("Mark works on Dense-Mem."),
			Quote:          "Mark works on Dense-Mem.",
			Authority:      "primary",
		},
	})
	require.NotEmpty(t, unknown.ReviewTaskID)
	require.NotEmpty(t, unknown.ObservationID)

	result, err := ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerA,
		Action:               "select_predicate",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		ObservationID:        unknown.ObservationID,
		PredicateKey:         "works_on",
		PredicateVersion:     1,
		IdempotencyKey:       "select-predicate-1",
	})
	require.NoError(t, err)
	require.Equal(t, "queued", result.Status)
	require.Equal(t, 60, result.CheckAfterSeconds)
	require.NotEmpty(t, result.DecisionID)

	retry, err := ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerA,
		Action:               "select_predicate",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		ObservationID:        unknown.ObservationID,
		PredicateKey:         "works_on",
		PredicateVersion:     1,
		IdempotencyKey:       "select-predicate-1",
	})
	require.NoError(t, err)
	require.True(t, retry.Existing)
	require.Equal(t, result.DecisionID, retry.DecisionID)

	_, err = ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerB,
		Action:               "select_predicate",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		ObservationID:        unknown.ObservationID,
		PredicateKey:         "works_on",
		PredicateVersion:     1,
		IdempotencyKey:       "owner-b-select-predicate",
	})
	require.ErrorIs(t, err, ErrPlacementResolutionNotFound)

	var taskStatus, resolvedPredicate, runStatus, itemStatus, itemCategory, itemAction string
	var resolvedVersion int
	var outcomeCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT status, resolution->>'predicate_key',
			       COALESCE((resolution->>'predicate_version')::int, 0)
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND review_task_id = ?::uuid
		`, teamID, unknown.ReviewTaskID).Row().Scan(&taskStatus, &resolvedPredicate, &resolvedVersion))
		require.NoError(t, tx.Raw(`
			SELECT run.status, item.status, item.category, item.result->>'action'
			FROM placement_runs AS run
			JOIN placement_items AS item
			  ON item.team_id = run.team_id
			 AND item.placement_run_id = run.placement_run_id
			WHERE run.team_id = ?::uuid
			  AND run.placement_run_id = ?::uuid
			  AND item.placement_item_id = ?::uuid
		`, teamID, ingest.PlacementRunID, ingest.Items[0].PlacementItemID).Row().Scan(
			&runStatus, &itemStatus, &itemCategory, &itemAction,
		))
		return tx.Raw(`
			SELECT COUNT(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND idempotency_key = 'select-predicate-1'
		`, teamID, ownerA).Scan(&outcomeCount).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "resolved", taskStatus)
	assert.Equal(t, "works_on", resolvedPredicate)
	assert.Equal(t, 1, resolvedVersion)
	assert.Equal(t, "queued", runStatus)
	assert.Equal(t, "queued", itemStatus)
	assert.Equal(t, "pending", itemCategory)
	assert.Equal(t, "select_predicate", itemAction)
	assert.Equal(t, int64(1), outcomeCount)
}

func TestPlacementResolutionSelectEntityResolvesOnlyMatchingV24AssessmentTask(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-v24-entity-selection-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "other-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	selected := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Mark")
	expiredCandidate := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Marcus")
	otherMentionCandidate := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement-v24-entity-selection", "Mark works on Dense-Mem.")
	assessment, _, err := ledgerRepo.PersistPlacementAssessment(ctx, placementAssessmentPersistInput(teamID, ownerID, ingest.Items[0]))
	require.NoError(t, err)
	validTaskID := uuid.NewString()
	expiredTaskID := uuid.NewString()
	otherTaskID := uuid.NewString()
	dependentRelationshipTaskID := uuid.NewString()
	conflictTaskID := uuid.NewString()

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO review_tasks (
			    team_id, review_task_id, owner_profile_id, ingest_id, placement_item_id,
			    task_type, status, reason, payload, dedupe_key, assessment_id, expires_at
			) VALUES
			    (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			     'identity_needs_review', 'open', 'ambiguous_identity',
			     jsonb_build_object(
			         'semantic_kind', 'identity',
			         'mention_ref', 'subject',
			         'options', jsonb_build_array(jsonb_build_object('entity_id', ?::text))
			     ), '', ?::uuid, now() + interval '1 hour'),
			    (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			     'identity_needs_review', 'open', 'ambiguous_identity',
			     jsonb_build_object(
			         'semantic_kind', 'identity',
			         'mention_ref', 'subject',
			         'options', jsonb_build_array(jsonb_build_object('entity_id', ?::text))
			     ), '', ?::uuid, now() - interval '1 minute'),
			    (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			     'identity_needs_review', 'open', 'ambiguous_identity',
			     jsonb_build_object(
			         'semantic_kind', 'identity',
			         'mention_ref', 'object',
			         'options', jsonb_build_array(jsonb_build_object('entity_id', ?::text))
			     ), '', ?::uuid, now() + interval '1 hour'),
			    (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			     'identity_needs_review', 'open', 'identity_needs_review',
			     jsonb_build_object(
			         'semantic_kind', 'identity',
			         'relationship_ref', 'works-on',
			         'subject_ref', 'subject',
			         'object_ref', 'object',
			         'options', jsonb_build_array(jsonb_build_object('entity_id', ?::text))
			     ), '', ?::uuid, now() + interval '1 hour')
			`,
			teamID, validTaskID, ownerID, ingest.IngestID, ingest.Items[0].PlacementItemID, selected.EntityID, assessment.AssessmentID,
			teamID, expiredTaskID, ownerID, ingest.IngestID, ingest.Items[0].PlacementItemID, expiredCandidate.EntityID, assessment.AssessmentID,
			teamID, otherTaskID, ownerID, ingest.IngestID, ingest.Items[0].PlacementItemID, otherMentionCandidate.EntityID, assessment.AssessmentID,
			teamID, dependentRelationshipTaskID, ownerID, ingest.IngestID, ingest.Items[0].PlacementItemID, selected.EntityID, assessment.AssessmentID,
		).Error
	}))
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO review_tasks (
			    team_id, review_task_id, owner_profile_id, ingest_id, placement_item_id,
			    task_type, status, reason, payload, dedupe_key
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    'relationship_needs_review', 'open', 'relationship_identity_valid_to_conflict',
			    '{"legacy_conflict":true}'::jsonb, ''
			)
		`, teamID, conflictTaskID, ownerID, ingest.IngestID, ingest.Items[0].PlacementItemID).Error
	}))

	_, err = ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               "select_entity",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		EntityRef:            "subject",
		CandidateEntityID:    expiredCandidate.EntityID,
		IdempotencyKey:       "v24-expired-entity-option",
	})
	require.ErrorIs(t, err, ErrPlacementResolutionInvalidState)

	_, err = ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       otherOwnerID,
		Action:               "select_entity",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		EntityRef:            "subject",
		CandidateEntityID:    selected.EntityID,
		IdempotencyKey:       "v24-cross-owner-entity-option",
	})
	require.ErrorIs(t, err, ErrPlacementResolutionNotFound)

	result, err := ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               "select_entity",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		EntityRef:            "subject",
		CandidateEntityID:    selected.EntityID,
		IdempotencyKey:       "v24-select-entity-option",
	})
	require.NoError(t, err)
	assert.Equal(t, "queued", result.Status)

	statuses := map[string]string{}
	resolutions := map[string]string{}
	var assessmentCount int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		rows, queryErr := tx.Raw(`
			SELECT review_task_id::text, status, COALESCE(resolution->>'candidate_entity_id', '')
			FROM review_tasks
			WHERE team_id = ?::uuid
			  AND review_task_id IN (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid)
		`, teamID, validTaskID, expiredTaskID, otherTaskID, dependentRelationshipTaskID, conflictTaskID).Rows()
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var taskID, status, candidateID string
			if scanErr := rows.Scan(&taskID, &status, &candidateID); scanErr != nil {
				return scanErr
			}
			statuses[taskID] = status
			resolutions[taskID] = candidateID
		}
		if rows.Err() != nil {
			return rows.Err()
		}
		return tx.Raw(`
			SELECT count(*) FROM placement_assessments
			WHERE team_id = ?::uuid AND placement_item_id = ?::uuid
		`, teamID, ingest.Items[0].PlacementItemID).Row().Scan(&assessmentCount)
	}))
	assert.Equal(t, "resolved", statuses[validTaskID])
	assert.Equal(t, selected.EntityID, resolutions[validTaskID])
	assert.Equal(t, "open", statuses[expiredTaskID])
	assert.Equal(t, "open", statuses[otherTaskID])
	assert.Equal(t, "resolved", statuses[dependentRelationshipTaskID])
	assert.Equal(t, "open", statuses[conflictTaskID])
	assert.Equal(t, 1, assessmentCount)
}

func TestPlacementResolutionSelectEntityValidatesDependentAssessmentTaskOptions(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-v24-dependent-entity-selection-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "other-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement-v24-dependent-entity-selection", "Mark works on Dense-Mem.")
	assessment, _, err := ledgerRepo.PersistPlacementAssessment(ctx, placementAssessmentPersistInput(teamID, ownerID, ingest.Items[0]))
	require.NoError(t, err)
	allowedID := uuid.NewString()
	rejectedID := uuid.NewString()
	taskID := uuid.NewString()

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO review_tasks (
			    team_id, review_task_id, owner_profile_id, ingest_id, placement_item_id,
			    task_type, status, reason, payload, dedupe_key, assessment_id, expires_at
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    'identity_needs_review', 'open', 'identity_needs_review',
			    jsonb_build_object(
			        'semantic_kind', 'identity',
			        'relationship_ref', 'works-on',
			        'subject_ref', 'subject',
			        'options', jsonb_build_array(jsonb_build_object('entity_id', ?::text))
			    ), '', ?::uuid, now() + interval '1 hour'
			)
		`, teamID, taskID, ownerID, ingest.IngestID, ingest.Items[0].PlacementItemID, allowedID, assessment.AssessmentID).Error
	}))

	_, err = ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               "select_entity",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		EntityRef:            "subject",
		CandidateEntityID:    rejectedID,
		IdempotencyKey:       "v24-dependent-rejected-entity-option",
	})
	require.ErrorIs(t, err, ErrPlacementResolutionInvalidState)

	_, err = ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       otherOwnerID,
		Action:               "select_entity",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		EntityRef:            "subject",
		CandidateEntityID:    allowedID,
		IdempotencyKey:       "v24-dependent-cross-owner-entity-option",
	})
	require.ErrorIs(t, err, ErrPlacementResolutionNotFound)

	result, err := ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               "select_entity",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		EntityRef:            "subject",
		CandidateEntityID:    allowedID,
		IdempotencyKey:       "v24-dependent-allowed-entity-option",
	})
	require.NoError(t, err)
	assert.Equal(t, "queued", result.Status)

	var status, selectedID string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status, COALESCE(resolution->>'candidate_entity_id', '')
			FROM review_tasks
			WHERE team_id = ?::uuid AND review_task_id = ?::uuid
		`, teamID, taskID).Row().Scan(&status, &selectedID)
	}))
	assert.Equal(t, "resolved", status)
	assert.Equal(t, allowedID, selectedID)
}

func TestPlacementResolutionSelectPredicateResolvesOnlyMatchingV24AssessmentTask(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-v24-predicate-selection-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "other-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Mark")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement-v24-predicate-selection", "Mark works on Dense-Mem.")
	unknown := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:            teamID,
		OwnerProfileID:    ownerID,
		IngestID:          ingest.IngestID,
		PlacementItemID:   ingest.Items[0].PlacementItemID,
		SubjectEntityID:   subject.EntityID,
		OriginalPredicate: "works on",
		PredicateKey:      "unknown_predicate",
		PredicateVersion:  1,
		ObjectEntityID:    object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:v24-predicate-selection",
			SpanStart:      0,
			SpanEnd:        len("Mark works on Dense-Mem."),
			Quote:          "Mark works on Dense-Mem.",
			Authority:      "primary",
		},
	})
	assessment, _, err := ledgerRepo.PersistPlacementAssessment(ctx, placementAssessmentPersistInput(teamID, ownerID, ingest.Items[0]))
	require.NoError(t, err)
	identityTaskID := uuid.NewString()

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE review_tasks
			SET assessment_id = ?::uuid,
			    expires_at = now() + interval '1 hour',
			    payload = jsonb_build_object(
			        'semantic_kind', 'predicate',
			        'relationship_ref', 'works-on',
			        'options', jsonb_build_array(jsonb_build_object('predicate_key', 'works_on', 'version', 1))
			    )
			WHERE team_id = ?::uuid AND review_task_id = ?::uuid
		`, assessment.AssessmentID, teamID, unknown.ReviewTaskID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO review_tasks (
			    team_id, review_task_id, owner_profile_id, ingest_id, placement_item_id,
			    task_type, status, reason, payload, dedupe_key, assessment_id, expires_at
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    'identity_needs_review', 'open', 'ambiguous_identity',
			    jsonb_build_object(
			        'semantic_kind', 'identity',
			        'mention_ref', 'subject',
			        'options', jsonb_build_array(jsonb_build_object('entity_id', ?::text))
			    ), '', ?::uuid, now() + interval '1 hour'
			)
		`, teamID, identityTaskID, ownerID, ingest.IngestID, ingest.Items[0].PlacementItemID, subject.EntityID, assessment.AssessmentID).Error
	}))

	_, err = ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       otherOwnerID,
		Action:               "select_predicate",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		ObservationID:        unknown.ObservationID,
		PredicateKey:         "works_on",
		PredicateVersion:     1,
		IdempotencyKey:       "v24-cross-owner-predicate-option",
	})
	require.ErrorIs(t, err, ErrPlacementResolutionNotFound)

	result, err := ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               "select_predicate",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		ObservationID:        unknown.ObservationID,
		PredicateKey:         "works_on",
		PredicateVersion:     1,
		IdempotencyKey:       "v24-select-predicate-option",
	})
	require.NoError(t, err)
	assert.Equal(t, "queued", result.Status)

	var predicateStatus, identityStatus, predicateKey string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status, COALESCE(resolution->>'predicate_key', '')
			FROM review_tasks
			WHERE team_id = ?::uuid AND review_task_id = ?::uuid
		`, teamID, unknown.ReviewTaskID).Row().Scan(&predicateStatus, &predicateKey); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status FROM review_tasks
			WHERE team_id = ?::uuid AND review_task_id = ?::uuid
		`, teamID, identityTaskID).Row().Scan(&identityStatus)
	}))
	assert.Equal(t, "resolved", predicateStatus)
	assert.Equal(t, "works_on", predicateKey)
	assert.Equal(t, "open", identityStatus)
}

func TestPlacementResolutionRejectIsTerminalAndIdempotent(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-reject-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement-resolution-reject", "This unsupported memory should be rejected.")

	first, err := ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               "reject",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		Message:              "Evidence does not support durable memory.",
		IdempotencyKey:       "reject-1",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", first.Status)
	require.NotNil(t, first.FirstDisposition)
	assert.Equal(t, string(domain.PlacementRunCompleted), first.FirstDisposition.Status)

	retry, err := ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               "reject",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		Message:              "Evidence does not support durable memory.",
		IdempotencyKey:       "reject-1",
	})
	require.NoError(t, err)
	require.True(t, retry.Existing)
	require.Equal(t, first.DecisionID, retry.DecisionID)
	assert.Nil(t, retry.FirstDisposition)

	_, err = ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               "reject",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		Message:              "different reason",
		IdempotencyKey:       "reject-1",
	})
	require.ErrorIs(t, err, ErrIdempotencyConflict)

	var runStatus, itemStatus, itemCategory, outcomeStatus string
	var firstDispositionCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT run.status, item.status, item.category
			FROM placement_runs AS run
			JOIN placement_items AS item
			  ON item.team_id = run.team_id
			 AND item.placement_run_id = run.placement_run_id
			WHERE run.team_id = ?::uuid
			  AND run.placement_run_id = ?::uuid
			  AND item.placement_item_id = ?::uuid
		`, teamID, ingest.PlacementRunID, ingest.Items[0].PlacementItemID).Row().Scan(
			&runStatus, &itemStatus, &itemCategory,
		))
		if err := tx.Raw(`
			SELECT status
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND outcome_id = ?::uuid
		`, teamID, first.DecisionID).Scan(&outcomeStatus).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND outcome_kind = 'telemetry_first_disposition'
			  AND status = 'completed'
		`, teamID, ingest.PlacementRunID).Scan(&firstDispositionCount).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "completed", runStatus)
	assert.Equal(t, "completed", itemStatus)
	assert.Equal(t, "candidate", itemCategory)
	assert.Equal(t, "rejected", outcomeStatus)
	assert.Equal(t, int64(1), firstDispositionCount)
}

func TestPlacementResolutionReleaseQuarantineRequeuesGuarded(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-release-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)

	ingest, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "placement-resolution-release",
		Status:         "quarantined",
		Evidence: []EvidenceInput{{
			Content: "Quarantined evidence pending manager review.",
			InitialEvent: &SecurityEventDraft{
				EventKind: "deterministic_scan",
				Decision:  "quarantine",
				Reason:    "deterministic quarantine",
			},
		}},
	})
	require.NoError(t, err)

	result, err := ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		ActorRole:            "manager",
		Action:               "release_quarantine",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		Message:              "Manager reviewed and approved guarded processing.",
		IdempotencyKey:       "release-1",
	})
	require.NoError(t, err)
	require.Equal(t, "guarded", result.Status)

	var runStatus, itemStatus, itemCategory, quarantineStatus string
	var releaseEventCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`
			SELECT run.status, item.status, item.category
			FROM placement_runs AS run
			JOIN placement_items AS item
			  ON item.team_id = run.team_id
			 AND item.placement_run_id = run.placement_run_id
			WHERE run.team_id = ?::uuid
			  AND run.placement_run_id = ?::uuid
			  AND item.placement_item_id = ?::uuid
		`, teamID, ingest.PlacementRunID, ingest.Items[0].PlacementItemID).Row().Scan(
			&runStatus, &itemStatus, &itemCategory,
		))
		require.NoError(t, tx.Raw(`
			SELECT status
			FROM evidence_quarantines
			WHERE team_id = ?::uuid
			  AND fragment_id = ?::uuid
		`, teamID, ingest.Evidence[0].FragmentID).Scan(&quarantineStatus).Error)
		return tx.Raw(`
			SELECT COUNT(*)
			FROM evidence_security_events
			WHERE team_id = ?::uuid
			  AND fragment_id = ?::uuid
			  AND event_kind = 'quarantine_release'
			  AND decision = 'released'
		`, teamID, ingest.Evidence[0].FragmentID).Scan(&releaseEventCount).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "guarded", runStatus)
	assert.Equal(t, "queued", itemStatus)
	assert.Equal(t, "pending", itemCategory)
	assert.Equal(t, "released", quarantineStatus)
	assert.Equal(t, int64(1), releaseEventCount)
}

func TestPlacementResolutionQuarantineRejectsTerminalRetryWithoutResettingExpiry(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-quarantine-retry-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)

	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement-resolution-quarantine-retry", "Evidence to quarantine after manual review.")
	input := ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               "accept",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		Evidence: []EvidenceInput{{
			Content: "Manual review confirmed this evidence must remain quarantined.",
			InitialEvent: &SecurityEventDraft{
				EventKind: "deterministic_scan",
				Decision:  "quarantine",
				Reason:    "manual review retained quarantine",
			},
		}},
		IdempotencyKey: "quarantine-retry-1",
	}
	first, err := ledgerRepo.ResolvePlacementReview(ctx, input)
	require.NoError(t, err)
	require.Equal(t, string(domain.PlacementRunQuarantined), first.Status)

	var firstCompletedAt, firstExpiry time.Time
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT completed_at, quarantine_expires_at
			FROM placement_runs
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&firstCompletedAt, &firstExpiry)
	}))
	require.WithinDuration(t, firstCompletedAt.Add(24*time.Hour), firstExpiry, time.Second)

	retry := input
	retry.PlacementItemVersion++
	retry.IdempotencyKey = "quarantine-retry-2"
	_, err = ledgerRepo.ResolvePlacementReview(ctx, retry)
	require.ErrorIs(t, err, ErrPlacementResolutionInvalidState)

	var finalStatus string
	var finalCompletedAt, finalExpiry time.Time
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status, completed_at, quarantine_expires_at
			FROM placement_runs
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&finalStatus, &finalCompletedAt, &finalExpiry)
	}))
	assert.Equal(t, string(domain.PlacementRunQuarantined), finalStatus)
	assert.Equal(t, firstCompletedAt, finalCompletedAt)
	assert.Equal(t, firstExpiry, finalExpiry)
}

func TestPlacementResolutionBlocksProcessingState(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-processing-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement-resolution-processing", "Processing item.")
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE placement_runs
			SET status = 'processing',
			    worker_id = 'worker',
			    attempts = 1,
			    lease_until = now() + interval '1 hour'
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Error
	}))

	_, err := ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerID,
		Action:               "reject",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		Message:              "reject while processing",
		IdempotencyKey:       "processing-1",
	})
	require.True(t, errors.Is(err, ErrPlacementResolutionInvalidState), err)
}
