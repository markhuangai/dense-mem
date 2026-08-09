package repository

import (
	"context"
	"testing"
	"time"

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

	page, err := ledger.ListConflictQueue(ctx, domainConflictQueueQuery(teamA, "", 1))
	require.NoError(t, err)
	require.Equal(t, 1, page.Summary.OpenCount)
	require.Equal(t, 1, page.Summary.OverdueCount)
	require.Equal(t, 1, page.Summary.ActiveLeaseCount)
	require.Equal(t, 1, page.Summary.ExpiredLeaseCount)
	require.Len(t, page.Items, 1)
	require.Equal(t, conflictA, page.Items[0].ConflictID)
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
			ORDER BY indexname
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

func domainConflictQueueQuery(teamID, status string, limit int) domain.ConflictQueueQuery {
	return domain.ConflictQueueQuery{TeamID: teamID, Status: status, Limit: limit}
}
