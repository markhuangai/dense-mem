package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRelationshipConflictResolutionRejectsStaleSearchGenerationBeforeLifecycleCommit(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-resolution-stale-generation", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-resolution-stale-generation-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-resolution-stale-generation-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-resolution-stale-generation-owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Conflict generation fence")
	firstObject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "First generation value")
	secondObject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Second generation value")
	first := commitPlacementRelationshipForConflictTest(t, ctx, ledgerRepo, teamID, ownerA, "worker-generation-a", "generation-fence-a", "Generation fence chooses first.", subject.EntityID, firstObject.EntityID, "generation-source-a")
	second := commitPlacementRelationshipForConflictTest(t, ctx, ledgerRepo, teamID, ownerB, "worker-generation-b", "generation-fence-b", "Generation fence chooses second.", subject.EntityID, secondObject.EntityID, "generation-source-b")

	conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	reviewNow := time.Now().UTC()
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE relationship_conflict_cases SET review_due_at = ?, next_review_at = ? WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, reviewNow.Add(-time.Minute), reviewNow.Add(-time.Minute), teamID, conflictID).Error
	}))
	reviewed := reviewConflictCaseForTest(t, ctx, ledgerRepo, teamID, "worker-generation-review", conflictID, reviewNow)
	require.Equal(t, ConflictReviewOutcomeOverdue, reviewed.Outcome)

	reservation, dossier, reserved, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{TeamID: teamID, ConflictID: conflictID, ReviewRunID: uuid.NewString(), WorkerID: "worker-generation-assessment", LocalAssessmentDate: reviewNow, Model: "test-model", PolicyVersion: domain.ConflictOverduePolicyVersion})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, dossier)
	require.NotEmpty(t, dossier.Positions)
	selectedPositionID := dossier.Positions[0].PositionID
	confidence := 0.95
	_, err = ledgerRepo.CompleteOverdueConflictAssessment(ctx, CompleteOverdueConflictAssessmentInput{TeamID: teamID, ConflictID: conflictID, AssessmentAttemptID: reservation.AssessmentAttemptID, CaseVersion: reservation.CaseVersion, ReviewRunID: uuid.NewString(), Decision: "selected", SelectedPositionID: selectedPositionID, Confidence: &confidence, ResponseHash: "sha256:generation-fence"})
	require.NoError(t, err)
	plan, err := ledgerRepo.PlanRelationshipConflictResolution(ctx, RelationshipConflictResolutionInput{TeamID: teamID, ConflictID: conflictID, ReviewRunID: uuid.NewString(), WorkerID: "worker-generation-commit", ExpectedCaseVersion: reservation.CaseVersion, PreferredPositionID: selectedPositionID, AssessmentAttemptID: reservation.AssessmentAttemptID, Method: "ai", Now: reviewNow})
	require.NoError(t, err)
	require.False(t, plan.Stale)
	require.NotEmpty(t, plan.Documents)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE search_index_generations SET activation_state = 'deprecated' WHERE search_index_generation_id = ?::uuid`, plan.Fence.SearchIndexGenerationID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO search_index_generations (search_index_generation_id, generation, embedding_contract_id, embedding_dimensions, ann_strategy, operator_class, indexed_expression, physical_index_name, hnsw_m, hnsw_ef_construction, query_ef_search, exact_max_rows, candidate_limit, allow_exact_fallback, activation_state, activated_at, metadata)
			SELECT gen_random_uuid(), generation + 1, embedding_contract_id, embedding_dimensions, ann_strategy, operator_class, indexed_expression, physical_index_name, hnsw_m, hnsw_ef_construction, query_ef_search, exact_max_rows, candidate_limit, allow_exact_fallback, 'active', now(), metadata
			FROM search_index_generations WHERE search_index_generation_id = ?::uuid
		`, plan.Fence.SearchIndexGenerationID).Error
	}))
	embeddings := conflictResolutionTestEmbeddings(plan)
	committed, err := ledgerRepo.CommitRelationshipConflictResolution(ctx, CommitRelationshipConflictResolutionInput{Plan: *plan, Embeddings: embeddings})
	require.NoError(t, err)
	assert.True(t, committed.Stale)
	assert.False(t, committed.Resolved)

	var conflictStatus, firstStatus, secondStatus string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT status FROM relationship_conflict_cases WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, teamID, conflictID).Row().Scan(&conflictStatus); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT status FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, first.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&firstStatus); err != nil {
			return err
		}
		return tx.Raw(`SELECT status FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, second.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&secondStatus)
	}))
	assert.Equal(t, "overdue", conflictStatus)
	assert.Equal(t, "active", firstStatus)
	assert.Equal(t, "active", secondStatus)
}

func TestRelationshipConflictResolutionFencesStaleCaseVersionBeforeWrites(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-resolution-stale-case-version", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "conflict-resolution-stale-case-version-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "conflict-resolution-stale-case-version-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "conflict-resolution-stale-case-version-owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "project", "Conflict case version fence")
	firstObject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "First case version value")
	secondObject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerA, "product", "Second case version value")
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-case-version-a",
		"case-version-fence-a", "The first case version value is current.", subject.EntityID, firstObject.EntityID, "source-group-case-version-a",
	)
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledgerRepo, teamID, ownerB, "worker-case-version-b",
		"case-version-fence-b", "The second case version value is current.", subject.EntityID, secondObject.EntityID, "source-group-case-version-b",
	)

	conflictID, expectedVersion := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	reviewNow := time.Now().UTC()
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET review_due_at = ?, next_review_at = ?
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, reviewNow.Add(-time.Minute), reviewNow.Add(-time.Minute), teamID, conflictID).Error
	}))
	reviewed := reviewConflictCaseForTest(t, ctx, ledgerRepo, teamID, "worker-case-version-review", conflictID, reviewNow)
	require.Equal(t, ConflictReviewOutcomeOverdue, reviewed.Outcome)
	reservation, dossier, reserved, err := ledgerRepo.ReserveOverdueConflictAssessment(ctx, ReserveOverdueConflictAssessmentInput{
		TeamID: teamID, ConflictID: conflictID, ReviewRunID: uuid.NewString(), WorkerID: "worker-case-version-assessment",
		LocalAssessmentDate: reviewNow, Model: "test-model", PolicyVersion: domain.ConflictOverduePolicyVersion,
	})
	require.NoError(t, err)
	require.True(t, reserved)
	require.NotNil(t, dossier)
	require.NotEmpty(t, dossier.Positions)
	selectedPositionID := dossier.Positions[0].PositionID
	confidence := 0.95
	_, err = ledgerRepo.CompleteOverdueConflictAssessment(ctx, CompleteOverdueConflictAssessmentInput{
		TeamID: teamID, ConflictID: conflictID, AssessmentAttemptID: reservation.AssessmentAttemptID,
		CaseVersion: reservation.CaseVersion, ReviewRunID: uuid.NewString(), Decision: "selected",
		SelectedPositionID: selectedPositionID, Confidence: &confidence, ResponseHash: "sha256:case-version-fence",
	})
	require.NoError(t, err)

	resolution := RelationshipConflictResolutionInput{
		TeamID: teamID, ConflictID: conflictID, ReviewRunID: uuid.NewString(), WorkerID: "worker-case-version-commit",
		ExpectedCaseVersion: expectedVersion, PreferredPositionID: selectedPositionID,
		AssessmentAttemptID: reservation.AssessmentAttemptID, Method: "ai", Now: reviewNow,
	}
	plan, err := ledgerRepo.PlanRelationshipConflictResolution(ctx, resolution)
	require.NoError(t, err)
	require.False(t, plan.Stale)
	require.NotEmpty(t, plan.Documents)

	// Add a new support through the placement path so the persisted conflict
	// snapshot version advances after the plan has been prepared.
	commitPlacementRelationshipForConflictTestWithOptions(
		t, ctx, ledgerRepo, teamID, ownerA, "worker-case-version-support",
		"case-version-fence-support", "The first case version value has another supporting source.",
		subject.EntityID, firstObject.EntityID, "source-group-case-version-support", conflictTestRelationshipOptions{authority: "secondary"},
	)
	var currentVersion int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT version FROM relationship_conflict_cases WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, teamID, conflictID).Row().Scan(&currentVersion)
	}))
	require.Greater(t, currentVersion, expectedVersion)

	stalePlan, err := ledgerRepo.PlanRelationshipConflictResolution(ctx, resolution)
	require.NoError(t, err)
	require.True(t, stalePlan.Stale)

	relationshipIDs := make([]string, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		relationshipIDs = append(relationshipIDs, document.RelationshipID)
	}
	before := captureConflictResolutionStateSnapshot(t, ctx, adminDB, rls, teamID, conflictID, relationshipIDs)
	committed, err := ledgerRepo.CommitRelationshipConflictResolution(ctx, CommitRelationshipConflictResolutionInput{
		Plan: *plan, Embeddings: conflictResolutionTestEmbeddings(plan),
	})
	require.NoError(t, err)
	require.True(t, committed.Stale)
	require.False(t, committed.Resolved)
	after := captureConflictResolutionStateSnapshot(t, ctx, adminDB, rls, teamID, conflictID, relationshipIDs)
	assert.Equal(t, before, after)
}

func TestRelationshipConflictResolutionPreflightCountsNonForegroundDocuments(t *testing.T) {
	fixture := NewOversizedConflictServiceFixture(t)
	ctx := context.Background()
	reviewed, err := fixture.Ledger.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
		TeamID: fixture.TeamID, WorkerID: fixture.WorkerID, ReviewRunID: fixture.ReviewRunID,
		ConflictID: fixture.ConflictID, Now: fixture.ReviewNow,
	})
	require.NoError(t, err)
	require.NotNil(t, reviewed)
	require.Equal(t, ConflictReviewOutcomeResolve, reviewed.Outcome)
	require.NotNil(t, reviewed.Resolution)

	projectionGenerationID := uuid.NewString()
	require.NoError(t, fixture.rls.WithSystemTx(ctx, fixture.adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO search_projection_generations (
			    team_id, projection_generation_id, source_kind, generation,
			    projection_format_version, state, eligible_count, projected_count,
			    current_vector_count, failed_job_count, last_error, activated_at
			) VALUES (
			    ?::uuid, ?::uuid, 'relationship', 1, 2,
			    'current', ?, ?, ?, 0, '', now()
			)
		`, fixture.TeamID, projectionGenerationID, len(fixture.RelationshipIDs), len(fixture.RelationshipIDs), len(fixture.RelationshipIDs)).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'current', embedding = '[1,0,0]'::vector,
			    embedding_updated_at = now(), embedding_error = '',
			    projection_format_version = 2, projection_generation_id = ?::uuid
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
			  AND source_id = ANY(?::uuid[])
		`, projectionGenerationID, fixture.TeamID, pq.Array(fixture.RelationshipIDs)).Error
	}))

	plan, err := fixture.Ledger.PlanRelationshipConflictResolution(ctx, *reviewed.Resolution)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.True(t, plan.Pending)
	assert.True(t, plan.PendingTransitioned)
	assert.Empty(t, plan.Documents)
}

func TestOversizedConflictRefreshesStaleMembershipBeforeDeferral(t *testing.T) {
	fixture := NewOversizedConflictServiceFixture(t)
	ctx := context.Background()
	loserID := fixture.RelationshipIDs[1]
	winnerIDs := fixture.RelationshipIDs[2:]
	require.NoError(t, fixture.rls.WithSystemTx(ctx, fixture.adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE relationship_records
			SET status = 'superseded', support_count = 0
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, fixture.TeamID, loserID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'pending', embedding = NULL,
			    embedding_updated_at = NULL, embedding_error = ''
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
			  AND source_id = ANY(?::uuid[])
		`, fixture.TeamID, pq.Array(winnerIDs)).Error
	}))

	result, err := fixture.Ledger.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
		TeamID: fixture.TeamID, WorkerID: fixture.WorkerID, ReviewRunID: fixture.ReviewRunID,
		ConflictID: fixture.ConflictID, Now: fixture.ReviewNow,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, ConflictReviewOutcomeNoop, result.Outcome)
	assert.Equal(t, domain.ConflictReviewStageDismissedNoConflict, result.Stage)
	assert.False(t, result.ResolutionPending)

	var status string
	require.NoError(t, fixture.rls.WithSystemTx(ctx, fixture.adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, fixture.TeamID, fixture.ConflictID).Row().Scan(&status)
	}))
	assert.Equal(t, "dismissed", status)
}

func TestRelationshipConflictResolutionRetainsAssignmentsForDeduplicatedRenderedDocuments(t *testing.T) {
	fixture := NewOversizedConflictServiceFixture(t)
	ctx := context.Background()
	reviewNow := fixture.ReviewNow.Add(-24 * time.Hour)
	require.NoError(t, fixture.rls.WithSystemTx(ctx, fixture.adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE relationship_records
			SET valid_from = ?
			WHERE team_id = ?::uuid AND relationship_id = ANY(?::uuid[])
		`, reviewNow, fixture.TeamID, pq.Array(fixture.RelationshipIDs)).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'pending', embedding = NULL,
			    embedding_updated_at = NULL, embedding_error = '', document_hash = ''
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
			  AND source_id = ANY(?::uuid[])
		`, fixture.TeamID, pq.Array(fixture.RelationshipIDs)).Error
	}))

	reviewed, err := fixture.Ledger.ReviewRelationshipConflictCase(ctx, ReviewRelationshipConflictCaseInput{
		TeamID: fixture.TeamID, WorkerID: fixture.WorkerID, ReviewRunID: fixture.ReviewRunID,
		ConflictID: fixture.ConflictID, Now: fixture.ReviewNow,
	})
	require.NoError(t, err)
	require.NotNil(t, reviewed)
	require.Equal(t, ConflictReviewOutcomeResolve, reviewed.Outcome)
	require.NotNil(t, reviewed.Resolution)

	plan, err := fixture.Ledger.PlanRelationshipConflictResolution(ctx, *reviewed.Resolution)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.False(t, plan.Pending)
	assert.Len(t, plan.Documents, domain.MaxEmbeddingBatchDocuments+1)
	assert.Equal(t, 1, uniqueConflictResolutionDocumentCount(plan.Documents))
	committed, err := fixture.Ledger.CommitRelationshipConflictResolution(ctx, CommitRelationshipConflictResolutionInput{
		Plan: *plan, Embeddings: conflictResolutionTestEmbeddings(plan),
	})
	require.NoError(t, err)
	require.NotNil(t, committed)
	assert.True(t, committed.Resolved)
	winnerIDs := make([]string, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		winnerIDs = append(winnerIDs, document.RelationshipID)
	}
	var currentWinnerCount int64
	require.NoError(t, fixture.rls.WithSystemTx(ctx, fixture.adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'relationship'
			  AND source_id = ANY(?::uuid[])
			  AND search_state = 'current'
			  AND embedding IS NOT NULL
		`, fixture.TeamID, pq.Array(winnerIDs)).Row().Scan(&currentWinnerCount)
	}))
	assert.Equal(t, int64(len(winnerIDs)), currentWinnerCount)
}

type conflictResolutionStateSnapshot struct {
	CaseState           string
	CaseAttempts        int
	CaseLeaseWorkerID   string
	CaseLeaseUntil      *time.Time
	RelationshipState   string
	SearchDocumentState string
	EmbeddingJobState   string
}

func captureConflictResolutionStateSnapshot(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	teamID string,
	conflictID string,
	relationshipIDs []string,
) conflictResolutionStateSnapshot {
	t.Helper()
	var snapshot conflictResolutionStateSnapshot
	var leaseUntil sql.NullTime
	require.NotEmpty(t, relationshipIDs)
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status || ':' || version::text || ':' || COALESCE(resolution_reason, '') || ':' || next_review_at::text || ':' || review_due_at::text,
			       attempts, COALESCE(lease_worker_id, ''), lease_until
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, teamID, conflictID).Row().Scan(&snapshot.CaseState, &snapshot.CaseAttempts, &snapshot.CaseLeaseWorkerID, &leaseUntil); err != nil {
			return err
		}
		if leaseUntil.Valid {
			snapshot.CaseLeaseUntil = &leaseUntil.Time
		}
		if err := tx.Raw(`
			SELECT COALESCE(string_agg(status || ':' || version::text || ':' || support_count::text || ':' || space_id::text || ':' || space_generation::text, ',' ORDER BY relationship_id::text), '')
			FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ANY(?::uuid[])
		`, teamID, pq.Array(relationshipIDs)).Row().Scan(&snapshot.RelationshipState); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COALESCE(string_agg(search_state || ':' || document_hash || ':' || embedding_dimensions::text || ':' || source_version::text || ':' || document_version::text || ':' || (embedding IS NOT NULL)::text, ',' ORDER BY source_id::text), '')
			FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ANY(?::uuid[])
		`, teamID, pq.Array(relationshipIDs)).Row().Scan(&snapshot.SearchDocumentState); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COALESCE(string_agg(status || ':' || source_version::text || ':' || attempts::text || ':' || COALESCE(error, ''), ',' ORDER BY embedding_job_id::text), '')
			FROM embedding_jobs
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ANY(?::uuid[])
		`, teamID, pq.Array(relationshipIDs)).Row().Scan(&snapshot.EmbeddingJobState)
	}))
	return snapshot
}

func conflictResolutionTestEmbeddings(plan *RelationshipConflictResolutionPlan) []RelationshipConflictResolutionEmbedding {
	seen := map[string]struct{}{}
	values := make([]RelationshipConflictResolutionEmbedding, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		if _, exists := seen[document.DocumentHash]; exists {
			continue
		}
		seen[document.DocumentHash] = struct{}{}
		vector := make([]float32, plan.Fence.EmbeddingDimensions)
		vector[0] = 1
		values = append(values, RelationshipConflictResolutionEmbedding{DocumentHash: document.DocumentHash, Embedding: vector})
	}
	return values
}
