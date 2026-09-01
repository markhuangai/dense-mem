//go:build integration

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

func TestPrivateMemoryErasureCleansSynchronousAssessmentAfterAttemptOrder(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-erasure-synchronous-assessment"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "synchronous-assessment", domain.CredentialBindingCredentialPrivate)
	ingestID := seedPrivateMemoryIngest(t, adminDB, rls, teamID, target.ID, target.MemorySpaceID, "private synchronous assessment")

	ledger := NewLedgerRepository(appDB, rls)
	require.NoError(t, ledger.RecordRememberAttempt(ctx, RememberAttemptRecordInput{
		TeamID: teamID.String(), OwnerProfileID: target.ID.String(), AttemptID: ingestID.String(),
		SpaceID: target.MemorySpaceID.String(), SpaceGeneration: target.MemorySpaceGeneration,
		IdempotencyKey: "private-synchronous-assessment", RequestHash: "private-synchronous-assessment-hash",
		ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "completed",
		PublicResult: map[string]any{},
	}))

	assessmentID := uuid.New()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO semantic_assessments (
				team_id, semantic_assessment_id, attempt_id, owner_profile_id,
				response_history, provider_turns, model, tokenizer, response_hash
			) VALUES (?, ?, ?, ?, '[]'::jsonb, 0, '', '', '')
		`, teamID, assessmentID, ingestID, target.ID).Error
	}))

	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))
	operation, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: target.ID, CredentialID: target.ID,
		IdempotencyScopeHash: privateMemoryHash("synchronous-assessment", target.ID.String()),
		RequestHash:          privateMemoryHash("erase-synchronous-assessment", target.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	claim, err := repo.ClaimNext(ctx, "synchronous-assessment-worker", time.Minute)
	require.NoError(t, err)
	require.Equal(t, operation.ID, claim.ID)
	completed, err := repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)
	require.EqualValues(t, 1, completed.DeletedCounts["semantic_assessments"])

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, fixture := range []struct {
			table, column string
			id            uuid.UUID
		}{
			{table: "semantic_assessments", column: "semantic_assessment_id", id: assessmentID},
			{table: "remember_attempts", column: "attempt_id", id: ingestID},
			{table: "knowledge_ingests", column: "ingest_id", id: ingestID},
		} {
			var count int64
			if err := tx.Raw("SELECT COUNT(*) FROM "+fixture.table+" WHERE team_id = ? AND "+fixture.column+" = ?", teamID, fixture.id).Scan(&count).Error; err != nil {
				return err
			}
			require.Zero(t, count, fixture.table+" rows remain after private erasure")
		}
		return nil
	}))
}
