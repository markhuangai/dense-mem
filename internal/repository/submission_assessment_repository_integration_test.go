package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSubmissionAssessmentPersistsOneRunAndAtomicallyCommitsEveryItem(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-commit-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-commit-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-commit")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	reused, existing, err := repo.PersistSubmissionAssessment(ctx, submissionAssessmentPersistInput(*claimed))
	require.NoError(t, err)
	assert.True(t, existing)
	assert.Equal(t, assessment.AssessmentID, reused.AssessmentID)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, false)
	committed, err := repo.CommitSubmissionAssessment(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, committed)
	assert.Equal(t, string(domain.SemanticReviewAccepted), committed.Status)
	assert.Len(t, committed.OutcomeIDs, 2)
	assert.Len(t, committed.EntityResolutionIDs, 4)
	assert.Len(t, committed.RelationshipResults, 2)
	assert.Len(t, committed.SearchDocuments, 4)

	var assessmentScope string
	var assessmentItemIsNull bool
	var runStatus string
	var completedItems, activeRelationships, verificationCount, entityResolutionCount, outcomeCount, evidenceDocumentCount, relationshipDocumentCount int64
	var actions []string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT assessment_scope, placement_item_id IS NULL
			FROM placement_assessments
			WHERE team_id = ?::uuid AND assessment_id = ?::uuid
		`, teamID, assessment.AssessmentID).Row().Scan(&assessmentScope, &assessmentItemIsNull); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status FROM placement_runs
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&runStatus); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM placement_items
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid AND status = 'completed'
		`, teamID, ingest.PlacementRunID).Scan(&completedItems).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM relationship_records
			WHERE team_id = ?::uuid AND status = 'active'
		`, teamID).Scan(&activeRelationships).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM verification_events
			WHERE team_id = ?::uuid AND assessment_id = ?::uuid
		`, teamID, assessment.AssessmentID).Scan(&verificationCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM entity_resolution_events
			WHERE team_id = ?::uuid AND assessment_id = ?::uuid
		`, teamID, assessment.AssessmentID).Scan(&entityResolutionCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM placement_outcomes
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
			  AND outcome_kind = 'submission_assessment_commit'
		`, teamID, ingest.PlacementRunID).Scan(&outcomeCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'evidence'
		`, teamID).Scan(&evidenceDocumentCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship'
		`, teamID).Scan(&relationshipDocumentCount).Error; err != nil {
			return err
		}
		rows, err := tx.Raw(`
			SELECT registration_action
			FROM predicate_registration_events
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
			ORDER BY relationship_ref ASC
		`, teamID, ingest.PlacementRunID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var action string
			if err := rows.Scan(&action); err != nil {
				return err
			}
			actions = append(actions, action)
		}
		return rows.Err()
	})
	require.NoError(t, err)
	assert.Equal(t, "submission", assessmentScope)
	assert.True(t, assessmentItemIsNull)
	assert.Equal(t, string(domain.PlacementRunCompleted), runStatus)
	assert.Equal(t, int64(2), completedItems)
	assert.Equal(t, int64(2), activeRelationships)
	assert.Equal(t, int64(2), verificationCount)
	assert.Equal(t, int64(4), entityResolutionCount)
	assert.Equal(t, int64(2), outcomeCount)
	assert.Equal(t, int64(2), evidenceDocumentCount)
	assert.Equal(t, int64(2), relationshipDocumentCount)
	assert.Equal(t, []string{"created", "reused"}, actions)

	err = rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE predicate_registration_events
			SET predicate_key = 'rewritten'
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Error
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append-only")
}

func TestSubmissionAssessmentEmbedsOutsideTransactionAndFencesCommit(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-inline-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-inline-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-inline-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-inline")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, false)

	plan, err := repo.PlanSubmissionAssessmentEmbeddings(ctx, input)
	require.NoError(t, err)
	require.Len(t, plan.Documents, 4)
	embeddings := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		embeddings = append(embeddings, InlineEmbeddingResult{
			DocumentHash: document.DocumentHash,
			Embedding:    []float32{1, 0, 0},
		})
	}
	committed, err := repo.CommitSubmissionAssessmentWithEmbeddings(ctx, input, embeddings)
	require.NoError(t, err)
	require.NotNil(t, committed)

	var pending, current, queuedJobs int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT COUNT(*) FILTER (WHERE search_state IN ('pending', 'failed')),
			       COUNT(*) FILTER (WHERE search_state = 'current')
			FROM search_documents
			WHERE team_id = ?::uuid
		`, teamID).Row().Scan(&pending, &current); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*) FROM embedding_jobs
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
		`, teamID, ownerID).Row().Scan(&queuedJobs)
	}))
	assert.Zero(t, pending)
	assert.Equal(t, int64(4), current)
	assert.Zero(t, queuedJobs)

	rollbackIngest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-inline-rollback")
	rollbackClaim, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, rollbackClaim)
	rollbackAssessment := persistSubmissionAssessment(t, ctx, repo, *rollbackClaim)
	rollbackInput := submissionAssessmentCommitFixture(*rollbackClaim, rollbackIngest, rollbackAssessment.AssessmentID, false)
	rollbackPlan, err := repo.PlanSubmissionAssessmentEmbeddings(ctx, rollbackInput)
	require.NoError(t, err)
	rollbackVectors := make([]InlineEmbeddingResult, 0, len(rollbackPlan.Documents))
	for index, document := range rollbackPlan.Documents {
		hash := document.DocumentHash
		if index == len(rollbackPlan.Documents)-1 {
			hash = "wrong-document-hash"
		}
		rollbackVectors = append(rollbackVectors, InlineEmbeddingResult{DocumentHash: hash, Embedding: []float32{1, 0, 0}})
	}
	var relationshipsBeforeRollback int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid`, teamID).Row().Scan(&relationshipsBeforeRollback)
	}))
	_, err = repo.CommitSubmissionAssessmentWithEmbeddings(ctx, rollbackInput, rollbackVectors)
	require.ErrorIs(t, err, ErrInlineEmbeddingPlanMismatch)
	var relationshipCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid`, teamID).Row().Scan(&relationshipCount)
	}))
	assert.Equal(t, relationshipsBeforeRollback, relationshipCount)
}

func TestSubmissionAssessmentCommitPersistsContiguousRelationshipSplits(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-split-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-split-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-split-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-split")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, false)

	second := input.RelationshipObservations[0]
	second.SplitIndex = 1
	second.Observation.Ref = "r:orion-vega#split:1"
	second.Observation.OriginalPredicate = "references"
	secondSupport := *second.Observation.Support
	second.Observation.Support = &secondSupport
	second.Observation.Support.SourceGroupKey = "submission-assessment:r:orion-vega:split:1"
	input.RelationshipObservations = append(input.RelationshipObservations, second)
	input.PredicateRegistrations = append(input.PredicateRegistrations, SubmissionPredicateRegistrationInput{
		RelationshipRef: second.Observation.Ref, PredicateKey: "references",
		SubjectKind: "concept", ObjectKind: "concept", RelationshipKind: "state", CurrentCardinality: "many",
	})

	plan, err := repo.PlanSubmissionAssessmentEmbeddings(ctx, input)
	require.NoError(t, err)
	inlineEmbeddings := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		inlineEmbeddings = append(inlineEmbeddings, InlineEmbeddingResult{
			DocumentHash: document.DocumentHash,
			Embedding:    []float32{1, 0, 0},
		})
	}
	committed, err := repo.CommitSubmissionAssessmentWithEmbeddings(ctx, input, inlineEmbeddings)
	require.NoError(t, err)
	require.Len(t, committed.RelationshipResults, 3)
	status, err := repo.GetPlacementRun(ctx, GetPlacementRunInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
	})
	require.NoError(t, err)

	var splitResult SubmissionRelationshipResult
	for _, result := range status.RelationshipResults {
		if result.RelationshipRef == "r:orion-vega" {
			splitResult = result
			break
		}
	}
	require.Equal(t, "stored", splitResult.Disposition)
	require.Len(t, splitResult.Splits, 2)
	assert.Equal(t, 0, splitResult.Splits[0].SplitIndex)
	assert.Equal(t, 1, splitResult.Splits[1].SplitIndex)
	assert.NotEqual(t, splitResult.Splits[0].RelationshipID, splitResult.Splits[1].RelationshipID)
}

func TestSubmissionAssessmentResponseRevisionIsAppendOnlyAndLatestForReplay(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-revision-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-revision-owner")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-revision")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)

	revisionJSON := json.RawMessage(`{"request_id":"submission-assessment-revision","security_signals":[],"entity_results":[],"relationship_results":[]}`)
	input := AppendSubmissionAssessmentRevisionInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
			PlacementRunID: ingest.PlacementRunID, WorkerID: "submission-worker", ExpectedAttempts: claimed.Attempts,
		},
		AssessmentID: assessment.AssessmentID, ProviderTurns: 2,
		InputTokens: 20, OutputTokens: 10, CandidateContextTokens: 5,
		NormalizedResponse: revisionJSON, ResponseHash: sha256Hex(string(revisionJSON)), ValidatedAt: time.Now().UTC(),
	}
	latest, existing, err := repo.AppendSubmissionAssessmentRevision(ctx, input)
	require.NoError(t, err)
	assert.False(t, existing)
	require.NotNil(t, latest)
	assert.Equal(t, 1, latest.RevisionNumber)
	assert.Equal(t, 2, latest.ProviderTurns)
	assert.JSONEq(t, string(revisionJSON), string(latest.NormalizedResponse))

	replayed, existing, err := repo.AppendSubmissionAssessmentRevision(ctx, input)
	require.NoError(t, err)
	assert.True(t, existing)
	assert.Equal(t, latest.ResponseHash, replayed.ResponseHash)

	err = rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE submission_assessment_response_revisions SET output_tokens = 11
			WHERE team_id = ?::uuid AND assessment_id = ?::uuid
		`, teamID, assessment.AssessmentID).Error
	})
	assert.Error(t, err)
	err = rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			DELETE FROM submission_assessment_response_revisions
			WHERE team_id = ?::uuid AND assessment_id = ?::uuid
		`, teamID, assessment.AssessmentID).Error
	})
	assert.Error(t, err)
}

func TestSubmissionAssessmentCommitRejectsChangedExactEntityConstraintAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-exact-entity-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-exact-entity-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-exact-entity-search", 3, "exact", "")
	semantic := NewSemanticRepository(appDB, rls)
	exactEntity := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "concept", "Exact Orion")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-exact-entity")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, false)
	input.EntityResolutions[0].Resolution.Action = string(domain.EntityResolutionReuse)
	input.EntityResolutions[0].Resolution.EntityID = exactEntity.EntityID
	input.EntityResolutions[0].Resolution.ExactEntityID = exactEntity.EntityID
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE entity_records SET status = 'retired' WHERE team_id = ?::uuid AND entity_id = ?::uuid`, teamID, exactEntity.EntityID).Error
	}))
	before := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)

	_, err = repo.CommitSubmissionAssessment(ctx, input)
	require.ErrorIs(t, err, ErrRememberExactReferenceStale)
	after := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)
	assert.Equal(t, before, after)
}

func TestSubmissionAssessmentCommitRejectsChangedExactPredicateConstraintAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-exact-predicate-stale-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-exact-predicate-stale-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-exact-predicate-stale-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-exact-predicate-stale")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, false)
	input.PredicateRegistrations = nil
	for index := range input.RelationshipObservations {
		input.RelationshipObservations[index].Observation.PredicateKey = "removed_exact_predicate"
		input.RelationshipObservations[index].Observation.PredicateVersion = 1
		input.RelationshipObservations[index].Observation.ExactPredicateKey = "removed_exact_predicate"
	}
	before := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)

	_, err = repo.CommitSubmissionAssessment(ctx, input)
	require.ErrorIs(t, err, ErrRememberExactReferenceStale)
	after := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)
	assert.Equal(t, before, after)
}

func TestSubmissionAssessmentRollsBackEverySemanticWriteWhenOneRelationshipFails(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-rollback-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-rollback-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-rollback")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, true)
	before := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)

	_, err = repo.CommitSubmissionAssessment(ctx, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolved relationship endpoint")

	after := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)
	assert.Equal(t, before, after)
}

func TestSubmissionAssessmentCommitValidatesConflictContextAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-conflict-context", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-conflict-context-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-conflict-context-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-conflict-context-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-conflict-context-owner-c")
	repo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	otherSubject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Other Project")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	commitPlacementRelationshipForConflictTest(
		t, ctx, repo, teamID, ownerA, "submission-context-seed-a",
		"submission-context-seed-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "submission-context-source-a",
	)
	commitPlacementRelationshipForConflictTest(
		t, ctx, repo, teamID, ownerB, "submission-context-seed-b",
		"submission-context-seed-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "submission-context-source-b",
	)
	conflictID, conflictVersion := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)

	_, freshInput := submissionAssessmentConflictCommitFixture(
		t, ctx, repo, teamID, ownerC, "submission-context-fresh-worker", "submission-context-fresh",
		"Dense-Mem uses PostgreSQL according to owner C.", subject.EntityID, postgres.EntityID,
		"submission-context-source-c", PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: conflictVersion},
	)
	committed, err := repo.CommitSubmissionAssessment(ctx, freshInput)
	require.NoError(t, err)
	require.Len(t, committed.RelationshipResults, 1)
	_, currentVersion := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	require.Greater(t, currentVersion, conflictVersion)

	mismatchedIngest, mismatchedInput := submissionAssessmentConflictCommitFixture(
		t, ctx, repo, teamID, ownerC, "submission-context-mismatched-worker", "submission-context-mismatched",
		"Other Project uses PostgreSQL according to owner C.", otherSubject.EntityID, postgres.EntityID,
		"submission-context-source-mismatched", PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: currentVersion},
	)
	beforeMismatch := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerA, mismatchedIngest.PlacementRunID)
	_, err = repo.CommitSubmissionAssessment(ctx, mismatchedInput)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflictContextStale), err)
	afterMismatch := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerA, mismatchedIngest.PlacementRunID)
	assert.Equal(t, beforeMismatch, afterMismatch)

	staleIngest, staleInput := submissionAssessmentConflictCommitFixture(
		t, ctx, repo, teamID, ownerC, "submission-context-stale-worker", "submission-context-stale",
		"Dense-Mem still uses PostgreSQL according to owner C.", subject.EntityID, postgres.EntityID,
		"submission-context-source-stale", PlacementConflictContextInput{ConflictID: conflictID, ExpectedVersion: conflictVersion},
	)
	before := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerA, staleIngest.PlacementRunID)

	_, err = repo.CommitSubmissionAssessment(ctx, staleInput)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflictContextStale), err)

	after := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerA, staleIngest.PlacementRunID)
	assert.Equal(t, before, after)
}

func TestRelationshipConflictContextValidationLocksConcurrentVersionChange(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-conflict-lock", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-conflict-lock-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-conflict-lock-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-conflict-lock-owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "GraphDB")
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "submission-context-lock-seed-a",
		"submission-context-lock-seed-a", "Dense-Mem uses PostgreSQL.", subject.EntityID, postgres.EntityID, "submission-context-lock-source-a",
	)
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "submission-context-lock-seed-b",
		"submission-context-lock-seed-b", "Dense-Mem uses GraphDB.", subject.EntityID, graphdb.EntityID, "submission-context-lock-source-b",
	)
	conflictID, conflictVersion := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)

	lockAcquired := make(chan struct{})
	releaseLock := make(chan struct{})
	validationDone := make(chan error, 1)
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseLock) })
	}
	defer release()
	go func() {
		validationDone <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
			if err := requireRelationshipConflictContextCurrent(ctx, tx, teamID, conflictID, conflictVersion); err != nil {
				return err
			}
			close(lockAcquired)
			<-releaseLock
			return nil
		})
	}()

	select {
	case <-lockAcquired:
	case err := <-validationDone:
		require.NoError(t, err)
		t.Fatal("conflict-context validation ended before its row lock could be observed")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for conflict-context row lock")
	}

	var competingVersion int
	lockErr := rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT version
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			FOR UPDATE NOWAIT
		`, teamID, conflictID).Row().Scan(&competingVersion)
	})
	require.Error(t, lockErr)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, lockErr, &pgErr)
	assert.Equal(t, "55P03", pgErr.Code)

	release()
	select {
	case err := <-validationDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for conflict-context validation to commit")
	}
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return bumpRelationshipConflictCaseVersion(ctx, tx, teamID, conflictID)
	}))
	_, advancedVersion := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	assert.Greater(t, advancedVersion, conflictVersion)
}

func TestSubmissionAssessmentCommitRequiresAssessmentForTheSameRun(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-scope-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-scope-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	first := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-scope-first")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	second := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-scope-second")
	wrongAssessment := persistSubmissionAssessment(t, ctx, repo, PlacementRun{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: second.IngestID, PlacementRunID: second.PlacementRunID,
	})
	input := submissionAssessmentCommitFixture(*claimed, first, wrongAssessment.AssessmentID, false)
	before := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, first.PlacementRunID)

	_, err = repo.CommitSubmissionAssessment(ctx, input)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSubmissionAssessmentScopeMismatch)

	after := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, first.PlacementRunID)
	assert.Equal(t, before, after)
}

func TestSubmissionAssessmentCommitRejectsDifferentProfileAndTeam(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-isolation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-other-owner")
	otherTeamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-other-team")
	otherTeamOwnerID := createLedgerProfile(t, adminDB, rls, otherTeamID, "submission-assessment-other-team-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-isolation-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-isolation")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, false)
	before := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)

	differentProfile := input
	differentProfile.OwnerProfileID = otherOwnerID
	_, err = repo.CommitSubmissionAssessment(ctx, differentProfile)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPlacementLeaseLost)
	afterDifferentProfile := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)
	assert.Equal(t, before, afterDifferentProfile)

	differentTeam := input
	differentTeam.TeamID = otherTeamID
	differentTeam.OwnerProfileID = otherTeamOwnerID
	_, err = repo.CommitSubmissionAssessment(ctx, differentTeam)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPlacementLeaseLost)
	afterDifferentTeam := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)
	assert.Equal(t, before, afterDifferentTeam)
}

func TestSubmissionAssessmentCommitReusesCompatiblePredicateAlias(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-alias-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-alias-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-alias-search", 3, "exact", "")
	semantic := NewSemanticRepository(appDB, rls)
	createSemanticEntity(t, ctx, semantic, teamID, ownerID, "concept", "Alias seed")
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_predicate_definitions (
			    team_id, predicate_key, version, aliases, allowed_subject_kinds,
			    allowed_object_kinds, relationship_kind, current_cardinality,
			    lifecycle_state, origin, metadata
			) VALUES (
			    ?::uuid, 'related_to', 1, ARRAY['links']::text[], ARRAY['concept']::text[],
			    ARRAY['concept']::text[], 'state', 'many', 'active', 'built_in', '{}'::jsonb
			)
		`, teamID).Error
	}))
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-alias")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, false)

	_, err = repo.CommitSubmissionAssessment(ctx, input)
	require.NoError(t, err)

	var actions, predicateKeys []string
	var registeredKeyCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT registration_action, predicate_key
			FROM predicate_registration_events
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
			ORDER BY relationship_ref ASC
		`, teamID, ingest.PlacementRunID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var action, predicateKey string
			if err := rows.Scan(&action, &predicateKey); err != nil {
				return err
			}
			actions = append(actions, action)
			predicateKeys = append(predicateKeys, predicateKey)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*) FROM team_predicate_definitions
			WHERE team_id = ?::uuid AND predicate_key = 'links'
		`, teamID).Scan(&registeredKeyCount).Error
	}))
	assert.Equal(t, []string{"reused", "reused"}, actions)
	assert.Equal(t, []string{"related_to", "related_to"}, predicateKeys)
	assert.Zero(t, registeredKeyCount)
}

func TestSubmissionAssessmentCommitPrefersExactPredicateKeyOverCanonical(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-exact-predicate-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-exact-predicate-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-exact-predicate-search", 3, "exact", "")
	semantic := NewSemanticRepository(appDB, rls)
	createSemanticEntity(t, ctx, semantic, teamID, ownerID, "concept", "Exact predicate seed")
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		for _, predicateKey := range []string{"Uses-DB", "uses_db"} {
			if err := tx.Exec(`
				INSERT INTO team_predicate_definitions (
				    team_id, predicate_key, version, aliases, allowed_subject_kinds,
				    allowed_object_kinds, relationship_kind, current_cardinality,
				    lifecycle_state, origin, metadata
				) VALUES (
				    ?::uuid, ?, 1, ARRAY[]::text[], ARRAY['concept']::text[],
				    ARRAY['concept']::text[], 'state', 'many', 'active', 'built_in', '{}'::jsonb
				)
			`, teamID, predicateKey).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-exact-predicate")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, false)
	for index := range input.PredicateRegistrations {
		input.PredicateRegistrations[index].PredicateKey = "Uses-DB"
	}

	_, err = repo.CommitSubmissionAssessment(ctx, input)
	require.NoError(t, err)

	var predicateKeys []string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT predicate_key
			FROM predicate_registration_events
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
			ORDER BY relationship_ref ASC
		`, teamID, ingest.PlacementRunID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var predicateKey string
			if err := rows.Scan(&predicateKey); err != nil {
				return err
			}
			predicateKeys = append(predicateKeys, predicateKey)
		}
		return rows.Err()
	}))
	assert.Equal(t, []string{"Uses-DB", "Uses-DB"}, predicateKeys)
}

func TestSubmissionTerminalStatusesTreatSupersededAsFailed(t *testing.T) {
	itemStatus, itemCategory, runStatus := submissionTerminalStatuses(string(domain.SemanticReviewSuperseded), "")
	assert.Equal(t, string(domain.PlacementRunFailed), itemStatus)
	assert.Equal(t, "failed", itemCategory)
	assert.Equal(t, string(domain.PlacementRunFailed), runStatus)
}

type submissionAssessmentSemanticCount struct {
	Entities        int64
	Predicates      int64
	Relationships   int64
	Verifications   int64
	Supports        int64
	Outcomes        int64
	SearchDocuments int64
}

func submissionAssessmentSemanticCounts(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls rLSHelper,
	teamID, ownerID, placementRunID string,
) submissionAssessmentSemanticCount {
	t.Helper()
	counts := submissionAssessmentSemanticCount{}
	err := rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		queries := []struct {
			query string
			dest  *int64
			args  []any
		}{
			{"SELECT COUNT(*) FROM entity_records WHERE team_id = ?::uuid", &counts.Entities, []any{teamID}},
			{"SELECT COUNT(*) FROM team_predicate_definitions WHERE team_id = ?::uuid", &counts.Predicates, []any{teamID}},
			{"SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid", &counts.Relationships, []any{teamID}},
			{"SELECT COUNT(*) FROM verification_events WHERE team_id = ?::uuid", &counts.Verifications, []any{teamID}},
			{"SELECT COUNT(*) FROM relationship_evidence_supports WHERE team_id = ?::uuid", &counts.Supports, []any{teamID}},
			{"SELECT COUNT(*) FROM placement_outcomes WHERE team_id = ?::uuid AND placement_run_id = ?::uuid", &counts.Outcomes, []any{teamID, placementRunID}},
			{"SELECT COUNT(*) FROM search_documents WHERE team_id = ?::uuid", &counts.SearchDocuments, []any{teamID}},
		}
		for _, query := range queries {
			if err := tx.Raw(query.query, query.args...).Scan(query.dest).Error; err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	return counts
}

func createSubmissionAssessmentIngest(t *testing.T, ctx context.Context, repo *LedgerRepositoryImpl, teamID, ownerID, idempotencyKey string) *CreateIngestResult {
	t.Helper()
	result, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: idempotencyKey,
		RequestHash:    sha256Hex(idempotencyKey),
		Evidence: []EvidenceInput{
			{Content: "Orion links Vega."},
			{Content: "Vega links Lyra."},
		},
	})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 2)
	require.Len(t, result.Items, 2)
	return result
}

func persistSubmissionAssessment(t *testing.T, ctx context.Context, repo *LedgerRepositoryImpl, run PlacementRun) *SubmissionAssessment {
	t.Helper()
	assessment, existing, err := repo.PersistSubmissionAssessment(ctx, submissionAssessmentPersistInput(run))
	require.NoError(t, err)
	assert.False(t, existing)
	return assessment
}

func submissionAssessmentPersistInput(run PlacementRun) PersistSubmissionAssessmentInput {
	response := json.RawMessage(`{"request_id":"submission-assessment","security_signals":[],"entity_results":[],"relationship_results":[]}`)
	return PersistSubmissionAssessmentInput{
		TeamID:                  run.TeamID,
		OwnerProfileID:          run.OwnerProfileID,
		IngestID:                run.IngestID,
		PlacementRunID:          run.PlacementRunID,
		RequestID:               "submission-assessment:" + run.PlacementRunID,
		AssessorContractVersion: domain.ContractVersion,
		Model:                   "submission-assessment-test",
		Tokenizer:               "o200k_base",
		InputTokens:             10,
		OutputTokens:            10,
		CandidateContextTokens:  10,
		NormalizedResponse:      response,
		ResponseHash:            "sha256:submission-assessment-test",
		ValidatedAt:             time.Now().UTC(),
	}
}

func submissionAssessmentConflictCommitFixture(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	workerID string,
	idempotencyKey string,
	content string,
	subjectEntityID string,
	objectEntityID string,
	sourceGroupKey string,
	conflictContext PlacementConflictContextInput,
) (*CreateIngestResult, CommitSubmissionAssessmentInput) {
	t.Helper()
	ingest := createSemanticIngest(t, ctx, repo, teamID, ownerID, idempotencyKey, content)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, workerID, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	threshold := 0.7
	confidence := 0.9
	input := CommitSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID, PlacementRunID: ingest.PlacementRunID,
			WorkerID: workerID, ExpectedAttempts: claimed.Attempts,
		},
		AssessmentID: assessment.AssessmentID,
		Items: []SubmissionAssessmentItemInput{{
			PlacementItemID: ingest.Items[0].PlacementItemID,
			FragmentID:      ingest.Evidence[0].FragmentID,
		}},
		EntityResolutions: []SubmissionAssessmentEntityResolutionInput{
			{
				PlacementItemID: ingest.Items[0].PlacementItemID,
				Resolution: PlacementEntityResolutionInput{
					MentionRef: "subject", Action: string(domain.EntityResolutionReuse), EntityID: subjectEntityID,
					FragmentID: ingest.Evidence[0].FragmentID, AssessmentID: assessment.AssessmentID,
				},
			},
			{
				PlacementItemID: ingest.Items[0].PlacementItemID,
				Resolution: PlacementEntityResolutionInput{
					MentionRef: "object", Action: string(domain.EntityResolutionReuse), EntityID: objectEntityID,
					FragmentID: ingest.Evidence[0].FragmentID, AssessmentID: assessment.AssessmentID,
				},
			},
		},
		RelationshipObservations: []SubmissionAssessmentRelationshipObservationInput{{
			PlacementItemID: ingest.Items[0].PlacementItemID,
			RelationshipRef: "relationship",
			SplitIndex:      0,
			Observation: PlacementRelationshipDecisionInput{
				Ref: "relationship", SubjectRef: "subject", OriginalPredicate: "primary database",
				PredicateKey: "primary_database", PredicateVersion: 1, ObjectRef: "object", Polarity: "+",
				AssessorAccepted: true,
				EvidenceVerdict:  string(domain.VerificationEntailed), Confidence: &confidence,
				Rationale: "The evidence explicitly states the relationship.", Model: "submission-assessment-test",
				ResponseHash: "sha256:submission-assessment-test", AssessmentID: assessment.AssessmentID,
				AssessmentPolicyVersion: "submission-assessment-test", ThresholdUsed: &threshold,
				GateResult: "meets_write_threshold", ConflictContext: &conflictContext,
				Support: &EvidenceSupportInput{
					FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: sourceGroupKey,
					SpanStart: 0, SpanEnd: len(content), Quote: content, Authority: "primary",
				},
			},
		}},
		RelationshipResults: []SubmissionRelationshipResultInput{{
			RelationshipRef: "relationship", Disposition: "stored",
		}},
		Payload: map[string]any{"assessor_contract": domain.ContractVersion},
	}
	return ingest, input
}

func submissionAssessmentCommitFixture(run PlacementRun, ingest *CreateIngestResult, assessmentID string, invalidSecondRelationship bool) CommitSubmissionAssessmentInput {
	threshold := 0.7
	items := []SubmissionAssessmentItemInput{
		{PlacementItemID: ingest.Items[0].PlacementItemID, FragmentID: ingest.Evidence[0].FragmentID},
		{PlacementItemID: ingest.Items[1].PlacementItemID, FragmentID: ingest.Evidence[1].FragmentID},
	}
	entity := func(item PlacementItem, fragment EvidenceFragment, ref, name string, start, end int) SubmissionAssessmentEntityResolutionInput {
		return SubmissionAssessmentEntityResolutionInput{
			PlacementItemID: item.PlacementItemID,
			Resolution: PlacementEntityResolutionInput{
				MentionRef: ref, Action: string(domain.EntityResolutionCreate), EntityKind: "concept", CanonicalName: name,
				FragmentID: fragment.FragmentID, SpanStart: &start, SpanEnd: &end, AssessmentID: assessmentID,
				IdentityContext: map[string]any{"source": "submission-assessment-test"},
			},
		}
	}
	observation := func(item PlacementItem, fragment EvidenceFragment, ref, subjectRef, objectRef, quote string, start, end int) SubmissionAssessmentRelationshipObservationInput {
		confidence := 0.9
		return SubmissionAssessmentRelationshipObservationInput{
			PlacementItemID: item.PlacementItemID,
			RelationshipRef: ref,
			SplitIndex:      0,
			Observation: PlacementRelationshipDecisionInput{
				Ref: ref, SubjectRef: subjectRef, OriginalPredicate: "links", ObjectRef: objectRef, Polarity: "+",
				AssessorAccepted: true,
				EvidenceVerdict:  string(domain.VerificationEntailed), Confidence: &confidence, Rationale: "The evidence explicitly states the link.",
				Model: "submission-assessment-test", ResponseHash: "sha256:submission-assessment-test",
				Support:      &EvidenceSupportInput{FragmentID: fragment.FragmentID, SourceGroupKey: "submission-assessment:" + ref, SpanStart: start, SpanEnd: end, Quote: quote, Authority: "primary"},
				AssessmentID: assessmentID, AssessmentPolicyVersion: "submission-assessment-test", ThresholdUsed: &threshold, GateResult: "meets_write_threshold",
			},
		}
	}
	secondObjectRef := "lyra"
	if invalidSecondRelationship {
		secondObjectRef = "missing"
	}
	return CommitSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: run.TeamID, OwnerProfileID: run.OwnerProfileID, IngestID: run.IngestID, PlacementRunID: run.PlacementRunID,
			WorkerID: "submission-worker", ExpectedAttempts: run.Attempts,
		},
		AssessmentID: assessmentID,
		Items:        items,
		EntityResolutions: []SubmissionAssessmentEntityResolutionInput{
			entity(ingest.Items[0], ingest.Evidence[0], "orion", "Orion", 0, 5),
			entity(ingest.Items[0], ingest.Evidence[0], "vega-first", "Vega", 12, 16),
			entity(ingest.Items[1], ingest.Evidence[1], "vega-second", "Vega", 0, 4),
			entity(ingest.Items[1], ingest.Evidence[1], "lyra", "Lyra", 11, 15),
		},
		RelationshipObservations: []SubmissionAssessmentRelationshipObservationInput{
			observation(ingest.Items[0], ingest.Evidence[0], "r:orion-vega", "orion", "vega-first", "Orion links Vega.", 0, 17),
			observation(ingest.Items[1], ingest.Evidence[1], "r:vega-lyra", "vega-second", secondObjectRef, "Vega links Lyra.", 0, 16),
		},
		PredicateRegistrations: []SubmissionPredicateRegistrationInput{
			{RelationshipRef: "r:orion-vega", PredicateKey: "links", SubjectKind: "concept", ObjectKind: "concept", RelationshipKind: "state", CurrentCardinality: "many"},
			{RelationshipRef: "r:vega-lyra", PredicateKey: "links", SubjectKind: "concept", ObjectKind: "concept", RelationshipKind: "state", CurrentCardinality: "many"},
		},
		RelationshipResults: []SubmissionRelationshipResultInput{
			{RelationshipRef: "r:orion-vega", Disposition: "stored"},
			{RelationshipRef: "r:vega-lyra", Disposition: "stored"},
		},
		Payload: map[string]any{"assessor_contract": domain.ContractVersion},
	}
}
