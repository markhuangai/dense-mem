package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestConflictQueueRepositoryIsTeamScopedAndKeysetOrdered(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "conflict-queue", 3, "exact", "")
	teamA := createLedgerTeam(t, adminDB, rls, "conflict-queue-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "Queue A")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "Queue B")
	teamB := createLedgerTeam(t, adminDB, rls, "conflict-queue-team-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamB, "Queue C")
	ownerD := createLedgerProfile(t, adminDB, rls, teamB, "Queue D")
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)

	createQueueConflict := func(teamID, ownerOne, ownerTwo, label string) string {
		subject := createSemanticEntity(t, ctx, semantic, teamID, ownerOne, "project", label+" subject")
		objectA := createSemanticEntity(t, ctx, semantic, teamID, ownerOne, "product", label+" PostgreSQL")
		objectB := createSemanticEntity(t, ctx, semantic, teamID, ownerOne, "product", label+" GraphDB")
		commitPlacementRelationshipForConflictTest(t, ctx, ledger, teamID, ownerOne, label+"-a", label+"-a", label+" uses PostgreSQL.", subject.EntityID, objectA.EntityID, label+"-source-a")
		commitPlacementRelationshipForConflictTest(t, ctx, ledger, teamID, ownerTwo, label+"-b", label+"-b", label+" uses GraphDB.", subject.EntityID, objectB.EntityID, label+"-source-b")
		conflictID, _ := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerOne, subject.EntityID)
		return conflictID
	}

	conflictA := createQueueConflict(teamA, ownerA, ownerB, "queue-a")
	conflictA2 := createQueueConflict(teamA, ownerA, ownerB, "queue-a-second")
	conflictB := createQueueConflict(teamB, ownerC, ownerD, "queue-b")
	now := time.Now().UTC()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET status = 'overdue', next_review_at = ?, review_due_at = ?, lease_until = ?
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, now.Add(-time.Hour), now.Add(-2*time.Hour), now.Add(30*time.Minute), teamA, conflictA).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET status = 'open', next_review_at = ?, review_due_at = ?, lease_until = ?
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, now.Add(time.Hour), now.Add(time.Hour), now.Add(-time.Minute), teamA, conflictA2).Error
	}))
	var conflictVersion int
	var conflictPositionID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT version
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, teamA, conflictA).Row().Scan(&conflictVersion); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT position_id::text
			FROM relationship_conflict_positions
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
			ORDER BY position_id
		LIMIT 1
		`, teamA, conflictA).Row().Scan(&conflictPositionID)
	}))
	olderAttemptID := uuid.NewString()
	newerAttemptID := uuid.NewString()
	olderCompletedAt := now.Add(-2 * time.Minute)
	newerCompletedAt := now.Add(-time.Minute)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO relationship_conflict_ai_assessment_attempts (
				team_id, assessment_attempt_id, conflict_id, case_version,
				local_assessment_date, model, policy_version, status,
				failure_class, created_at, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, CURRENT_DATE - 1,
			          'queue-model-old', 'queue-policy', 'failed', 'provider_unavailable', $5, $5)
		`, teamA, olderAttemptID, conflictA, conflictVersion, olderCompletedAt).Error; err != nil {
			return err
		}
		err := tx.Exec(`
			INSERT INTO relationship_conflict_ai_assessment_attempts (
				team_id, assessment_attempt_id, conflict_id, case_version,
				local_assessment_date, model, policy_version, status,
				selected_position_id, confidence, created_at, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, CURRENT_DATE,
			          'queue-model-new', 'queue-policy', 'selected', $5::uuid, 0.9, $6, $6)
		`, teamA, newerAttemptID, conflictA, conflictVersion, conflictPositionID, newerCompletedAt).Error
		return err
	}))

	page, err := ledger.ListConflictQueue(ctx, domainConflictQueueQuery(teamA, "", 1))
	require.NoError(t, err)
	require.Equal(t, 1, page.Summary.OpenCount)
	require.Equal(t, 1, page.Summary.OverdueCount)
	require.Equal(t, 1, page.Summary.ActiveLeaseCount)
	require.Equal(t, 1, page.Summary.ExpiredLeaseCount)
	require.Len(t, page.Items, 1)
	require.Equal(t, conflictA, page.Items[0].ConflictID)
	require.Equal(t, "none", page.Items[0].LastFailureClass)
	require.Equal(t, "active", page.Items[0].LeaseState)
	require.NotEmpty(t, page.Items[0].Positions)
	require.LessOrEqual(t, len(page.Items[0].Positions), domain.ConflictQueueMaxPositions)
	for _, position := range page.Items[0].Positions {
		require.NotEmpty(t, position.Supporters)
		require.Positive(t, position.SupporterCount)
		require.LessOrEqual(t, len(position.Supporters), domain.ConflictQueueMaxSupporters)
	}
	require.NotNil(t, page.NextCursor)
	cursor, err := domain.DecodeConflictQueueCursor(*page.NextCursor)
	require.NoError(t, err)
	conflictA3 := createQueueConflict(teamA, ownerA, ownerB, "queue-a-inserted")
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET status = 'overdue', next_review_at = ?, review_due_at = ?, lease_until = NULL
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, now.Add(-30*time.Minute), now.Add(-time.Hour), teamA, conflictA3).Error
	}))
	overduePage, err := ledger.ListConflictQueue(ctx, domain.ConflictQueueQuery{TeamID: teamA, Status: "overdue", Limit: 25})
	require.NoError(t, err)
	require.Len(t, overduePage.Items, 2)
	require.ElementsMatch(t, []string{conflictA, conflictA3}, []string{overduePage.Items[0].ConflictID, overduePage.Items[1].ConflictID})
	for _, item := range overduePage.Items {
		require.Equal(t, "overdue", item.Status)
	}
	secondPage, err := ledger.ListConflictQueue(ctx, domain.ConflictQueueQuery{
		TeamID: teamA,
		Limit:  1,
		Cursor: cursor,
	})
	require.NoError(t, err)
	require.Len(t, secondPage.Items, 1)
	require.Equal(t, conflictA3, secondPage.Items[0].ConflictID)
	require.NotNil(t, secondPage.NextCursor)
	thirdCursor, err := domain.DecodeConflictQueueCursor(*secondPage.NextCursor)
	require.NoError(t, err)
	thirdPage, err := ledger.ListConflictQueue(ctx, domain.ConflictQueueQuery{TeamID: teamA, Limit: 1, Cursor: thirdCursor})
	require.NoError(t, err)
	require.Len(t, thirdPage.Items, 1)
	require.Equal(t, conflictA2, thirdPage.Items[0].ConflictID)
	require.Nil(t, thirdPage.NextCursor)
	_, err = ledger.ListConflictQueue(ctx, domain.ConflictQueueQuery{TeamID: teamB, Limit: 1, Cursor: cursor})
	require.ErrorIs(t, err, domain.ErrConflictQueueCursorScope)

	var queueIndexExists bool
	var summaryIndexes []string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM pg_index AS state
				JOIN pg_class AS index_class ON index_class.oid = state.indexrelid
				WHERE index_class.relname = 'relationship_conflict_queue_active_idx'
				  AND state.indisvalid
			)
		`).Scan(&queueIndexExists).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT index_class.relname
			FROM pg_index AS state
			JOIN pg_class AS index_class ON index_class.oid = state.indexrelid
			WHERE state.indisvalid
			  AND index_class.relname IN (
				'relationship_conflict_ai_assessment_events_failed_idx',
				'relationship_conflict_resolution_plans_applied_idx'
			)
			ORDER BY index_class.relname
		`).Scan(&summaryIndexes).Error
	}))
	require.True(t, queueIndexExists)
	require.ElementsMatch(t, []string{
		"relationship_conflict_ai_assessment_events_failed_idx",
		"relationship_conflict_resolution_plans_applied_idx",
	}, summaryIndexes)

	snapshot, err := ledger.CollectConflictQueueMetrics(ctx)
	require.NoError(t, err)
	teamCases := map[string]float64{}
	for _, metric := range snapshot.Cases {
		if metric.TeamID == teamA {
			teamCases[metric.Status] = metric.Value
		}
	}
	require.Equal(t, float64(1), teamCases["open"])
	require.Equal(t, float64(2), teamCases["overdue"])
	teamLeases := map[string]float64{}
	for _, metric := range snapshot.Leases {
		if metric.TeamID == teamA {
			teamLeases[metric.State] = metric.Value
		}
	}
	require.Equal(t, float64(1), teamLeases["idle"])
	require.Equal(t, float64(1), teamLeases["active"])
	require.Equal(t, float64(1), teamLeases["expired"])

	foreign, err := ledger.ListConflictQueue(ctx, domainConflictQueueQuery(teamB, "", 25))
	require.NoError(t, err)
	require.Equal(t, 1, foreign.Summary.OpenCount)
	require.Len(t, foreign.Items, 1)
	require.Equal(t, conflictB, foreign.Items[0].ConflictID)
	require.NotEqual(t, conflictB, page.Items[0].ConflictID)

	appSQL, err := appDB.DB()
	require.NoError(t, err)
	require.NoError(t, appSQL.Close())
	_, err = ledger.CollectConflictQueueMetrics(ctx)
	require.Error(t, err)
}

func TestConflictQueueHidesSealedPrivateGeneration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "conflict-queue-private-generation"))
	identityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	credential := createOwnedCredential(t, credentialRepo, teamID, identityID, "conflict-queue-private-generation", domain.CredentialBindingCredentialPrivate)
	ledger := NewLedgerRepository(appDB, rls)
	privateRepo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, privateRepo.Prepare(ctx))

	subjectID := uuid.New()
	conflictID := uuid.New()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, teamID.String()); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO entity_records (team_id, entity_id, entity_kind, space_id, space_generation)
			VALUES (?, ?, 'project', ?, ?)
		`, teamID, subjectID, credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_conflict_cases (
				team_id, conflict_id, semantic_scope_key, status, subject_entity_id,
				predicate_key, predicate_version, relationship_kind, current_cardinality,
				polarity, question, policy_version, review_due_at, next_review_at,
				review_ttl_days, timezone, space_id, space_generation
			) VALUES (?, ?, ?, 'open', ?, 'uses', 1, 'state', 'one', '+', ?, ?, now(), now(), 2, 'UTC', ?, ?)
		`, teamID, conflictID, "private-queue-generation:"+conflictID.String(), subjectID,
			"Which private value is current?", string(domain.ConflictPolicyVersion),
			credential.MemorySpaceID, credential.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		return nil
	}))

	page, err := ledger.ListConflictQueue(ctx, domainConflictQueueQuery(teamID.String(), "", 25))
	require.NoError(t, err)
	require.Equal(t, 1, page.Summary.OpenCount)
	require.Len(t, page.Items, 1)
	require.Equal(t, conflictID.String(), page.Items[0].ConflictID)
	require.Equal(t, "Which private value is current?", page.Items[0].Question)

	operation, created, err := privateRepo.RequestControlErasure(
		ctx,
		credential.MemorySpaceID,
		privateMemoryHash("conflict-queue-private-generation", credential.MemorySpaceID.String()),
		privateMemoryHash("erase-conflict-queue-private-generation", credential.MemorySpaceID.String()),
		"owner_request",
	)
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, operation)

	sealed, err := ledger.ListConflictQueue(ctx, domainConflictQueueQuery(teamID.String(), "", 25))
	require.NoError(t, err)
	require.Zero(t, sealed.Summary.OpenCount)
	require.Zero(t, sealed.Summary.OverdueCount)
	require.Zero(t, sealed.Summary.ActiveLeaseCount)
	require.Zero(t, sealed.Summary.ExpiredLeaseCount)
	require.Empty(t, sealed.Items)

	snapshot, err := ledger.CollectConflictQueueMetrics(ctx)
	require.NoError(t, err)
	for _, metric := range snapshot.Cases {
		require.NotEqual(t, teamID.String(), metric.TeamID)
	}
}

func domainConflictQueueQuery(teamID, status string, limit int) domain.ConflictQueueQuery {
	return domain.ConflictQueueQuery{TeamID: teamID, Status: status, Limit: limit}
}
