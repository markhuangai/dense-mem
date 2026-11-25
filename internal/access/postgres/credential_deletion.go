package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	privacycontract "github.com/markhuangai/dense-mem/internal/privacy/contract"
)

type CredentialDeletionAuditInput = privacycontract.CredentialDeletionAuditInput

func (r *CredentialRepositoryImpl) deletionStore() privacycontract.CredentialDeletionStore {
	return r.deletion
}

func (r *CredentialRepositoryImpl) DeleteForTeam(ctx context.Context, teamID, id uuid.UUID) (int64, error) {
	deletion := r.deletionStore()
	if deletion == nil {
		return 0, fmt.Errorf("credential deletion store is unavailable")
	}
	now := time.Now().UTC()
	rows, err := deletion.RetireCredential(ctx, teamID, id, now, nil)
	if err != nil {
		return 0, err
	}
	if rows > 0 {
		if err := r.deactivateActorIfUnused(ctx, now, id); err != nil {
			return 0, fmt.Errorf("failed to reconcile deleted api credential actor: %w", err)
		}
	}
	return rows, nil
}

func (r *CredentialRepositoryImpl) DeleteForTeamWithAudit(ctx context.Context, teamID, id uuid.UUID, input privacycontract.CredentialDeletionAuditInput) (int64, error) {
	deletion := r.deletionStore()
	if deletion == nil {
		return 0, fmt.Errorf("credential deletion store is unavailable")
	}
	now := time.Now().UTC()
	rows, err := deletion.RetireCredential(ctx, teamID, id, now, &input)
	if err != nil {
		return 0, err
	}
	if rows > 0 {
		if err := r.deactivateActorIfUnused(ctx, now, id); err != nil {
			return 0, fmt.Errorf("failed to reconcile deleted api credential actor: %w", err)
		}
	}
	return rows, nil
}
