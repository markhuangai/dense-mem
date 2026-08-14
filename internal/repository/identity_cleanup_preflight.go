package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

type IdentityCleanupPreflightRepository interface {
	ReadIdentityCleanupPreflight(ctx context.Context) (domain.IdentityCleanupPreflight, error)
}

type IdentityCleanupPreflightRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ IdentityCleanupPreflightRepository = (*IdentityCleanupPreflightRepositoryImpl)(nil)

func NewIdentityCleanupPreflightRepository(db *gorm.DB, rls postgres.RLSHelper) *IdentityCleanupPreflightRepositoryImpl {
	return &IdentityCleanupPreflightRepositoryImpl{db: db, rls: rls}
}

func (r *IdentityCleanupPreflightRepositoryImpl) ReadIdentityCleanupPreflight(ctx context.Context) (domain.IdentityCleanupPreflight, error) {
	if r == nil || r.db == nil || r.rls == nil {
		return domain.IdentityCleanupPreflight{}, fmt.Errorf("identity cleanup preflight repository is unavailable")
	}
	result := domain.IdentityCleanupPreflight{Blockers: make([]domain.IdentityCleanupBlocker, 0)}
	err := r.rls.WithSystemReadOnlyRepeatableTx(ctx, r.db, func(tx *gorm.DB) error {
		var tables bool
		if err := tx.Raw(`
			SELECT to_regclass('public.identity_compatibility_state') IS NOT NULL
			   AND to_regclass('public.actor_identities') IS NOT NULL
			   AND to_regclass('public.team_memberships') IS NOT NULL
			   AND to_regclass('public.credentials') IS NOT NULL
			   AND to_regclass('public.ownership_aliases') IS NOT NULL
		`).Scan(&tables).Error; err != nil {
			return err
		}
		if !tables {
			result.Blockers = append(result.Blockers, identityCleanupBlocker("identity_bridge_missing", "the additive identity bridge is not installed"))
			return nil
		}

		var backupCheckpoint, deploymentFingerprint string
		err := tx.Raw(`
			SELECT state, backup_checkpoint, deployment_fingerprint
			FROM identity_compatibility_state
			WHERE singleton = true
		`).Row().Scan(
			&result.BridgeState,
			&backupCheckpoint,
			&deploymentFingerprint,
		)
		if err == sql.ErrNoRows {
			result.Blockers = append(result.Blockers, identityCleanupBlocker("identity_bridge_state_missing", "identity bridge state is incomplete"))
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT
				(SELECT count(*) FROM team_profiles),
				(SELECT count(*) FROM actor_identities),
				(SELECT count(*) FROM team_memberships),
				(SELECT count(*) FROM credentials),
				(SELECT count(*) FROM ownership_aliases),
				(SELECT count(*) FROM team_profiles p
				 WHERE NOT EXISTS (
					 SELECT 1
					 FROM ownership_aliases a
					 WHERE a.team_id = p.team_id
					   AND a.legacy_owner_id = p.id
				 ))
		`).Row().Scan(
			&result.LegacyProfileCount,
			&result.IdentityCount,
			&result.MembershipCount,
			&result.CredentialCount,
			&result.AliasCount,
			&result.UnresolvedCount,
		); err != nil {
			return err
		}
		if result.BridgeState != "reconciled" && result.BridgeState != "cutover_ready" {
			result.Blockers = append(result.Blockers, identityCleanupBlocker("identity_bridge_not_reconciled", "the compatibility bridge has not completed reconciliation"))
		}
		if result.UnresolvedCount > 0 {
			result.Blockers = append(result.Blockers, identityCleanupBlocker("identity_reconciliation_pending", "unresolved legacy identity rows remain"))
		}
		if result.AliasCount < result.LegacyProfileCount {
			result.Blockers = append(result.Blockers, identityCleanupBlocker("ownership_aliases_incomplete", "permanent ownership aliases do not cover every legacy owner"))
		}
		if strings.TrimSpace(backupCheckpoint) == "" {
			result.Blockers = append(result.Blockers, identityCleanupBlocker("backup_checkpoint_missing", "a verified recovery checkpoint is required before cleanup"))
		}
		if strings.TrimSpace(deploymentFingerprint) == "" {
			result.Blockers = append(result.Blockers, identityCleanupBlocker("deployment_homogeneity_unproven", "all application replicas must report the compatible identity release"))
		}

		var marker bool
		if err := tx.Raw(`
			SELECT EXISTS (
				SELECT 1 FROM v2_compatibility_markers
				WHERE marker_kind = 'identity_cutover'
				  AND status = 'compatible'
			)
		`).Scan(&marker).Error; err != nil {
			return err
		}
		if !marker {
			result.Blockers = append(result.Blockers, identityCleanupBlocker("identity_cutover_marker_missing", "the compatible identity cutover marker is absent"))
		}
		return nil
	})
	if err != nil {
		return domain.IdentityCleanupPreflight{}, fmt.Errorf("identity cleanup preflight read failed: %w", err)
	}
	result.Ready = len(result.Blockers) == 0
	return result, nil
}

func identityCleanupBlocker(code, message string) domain.IdentityCleanupBlocker {
	return domain.IdentityCleanupBlocker{Code: code, Message: message}
}
