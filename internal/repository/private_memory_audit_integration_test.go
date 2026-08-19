package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestPrivateMemorySSOCredentialRevocationAuditFailureRollsBackDisable(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "sso-delete-audit-rollback"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "audit-rollback", domain.CredentialBindingSharedOnly)
	repo := NewPrivateMemoryRepository(appDB, rls)

	_, created, err := repo.DisableSSOCredential(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: ownerID, CredentialID: target.ID,
		IdempotencyScopeHash: privateMemoryHash("audit-rollback", target.ID.String()),
		RequestHash:          privateMemoryHash("audit-rollback-request", target.ID.String()),
		ReasonCode:           "credential_deleted",
		CredentialRevocationAudit: &PrivateMemoryCredentialRevocationAudit{
			ActorRole: "member",
			ClientIP:  "not-an-ip",
		},
	})
	require.Error(t, err)
	require.False(t, created)

	var status string
	var operationCount, auditCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT status FROM credentials WHERE id = ?`, target.ID).Row().Scan(&status); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM private_memory_erasure_operations WHERE idempotency_scope_hash = ?`, privateMemoryHash("audit-rollback", target.ID.String())).Row().Scan(&operationCount); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*)
			FROM audit_log
			WHERE team_id = ? AND entity_type = 'api_key' AND entity_id = ? AND operation = 'REVOKE'
		`, teamID, target.ID.String()).Row().Scan(&auditCount)
	}))
	require.Equal(t, "active", status)
	require.Zero(t, operationCount)
	require.Zero(t, auditCount)
}
