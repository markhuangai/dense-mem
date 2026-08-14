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
			   AND to_regclass('public.membership_grants') IS NOT NULL
			   AND to_regclass('public.identity_external_links') IS NOT NULL
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
				(SELECT count(*)
				 FROM (
					SELECT p.id
					FROM team_profiles p
					LEFT JOIN actor_identities ai
					  ON ai.id = p.id
					 AND ai.team_id = p.team_id
					LEFT JOIN team_memberships m
					  ON m.team_id = p.team_id
					 AND m.legacy_profile_id = p.id
					LEFT JOIN credentials c
					  ON c.team_id = p.team_id
					 AND c.legacy_profile_id = p.id
					WHERE NOT EXISTS (
						 SELECT 1
						 FROM ownership_aliases a
						 WHERE a.team_id = p.team_id
						   AND a.legacy_owner_id = p.id
					)
					OR ai.id IS NULL
					OR ai.active IS DISTINCT FROM (p.revoked_at IS NULL)
					OR m.id IS NULL
					OR m.status IS DISTINCT FROM (CASE WHEN p.revoked_at IS NULL THEN 'active' ELSE 'revoked' END)
					OR m.team_admin IS DISTINCT FROM (p.role = 'manager')
					OR NOT (
						 COALESCE(m.maximum_grants, ARRAY[]::text[]) @> COALESCE(p.scopes, ARRAY[]::text[])
						 AND COALESCE(p.scopes, ARRAY[]::text[]) @> COALESCE(m.maximum_grants, ARRAY[]::text[])
					)
					OR (
						 p.key_hash IS NOT NULL
						 AND p.key_prefix IS NOT NULL
						 AND (
							 c.id IS NULL
							 OR c.key_hash IS DISTINCT FROM p.key_hash
							 OR c.key_prefix IS DISTINCT FROM p.key_prefix
							 OR c.key_suffix IS DISTINCT FROM p.key_suffix
							 OR c.name IS DISTINCT FROM p.name
							 OR c.rate_limit IS DISTINCT FROM p.rate_limit
							 OR c.expires_at IS DISTINCT FROM p.expires_at
							 OR c.revoked_at IS DISTINCT FROM p.revoked_at
							 OR c.status IS DISTINCT FROM (
								 CASE
									 WHEN p.revoked_at IS NOT NULL THEN 'revoked'
									 WHEN p.expires_at IS NOT NULL AND p.expires_at <= NOW() THEN 'expired'
									 ELSE 'active'
								 END
							 )
							 OR NOT (
								 COALESCE(c.scopes, ARRAY[]::text[]) @> COALESCE(p.scopes, ARRAY[]::text[])
								 AND COALESCE(p.scopes, ARRAY[]::text[]) @> COALESCE(c.scopes, ARRAY[]::text[])
							 )
						 )
					)
						 UNION ALL
						 SELECT i.id
						 FROM sso_identities i
						 LEFT JOIN actor_identities ai ON ai.id = i.id
						 WHERE COALESCE(NULLIF(i.external_id, ''), NULLIF(i.subject, ''), '') <> ''
						   AND (
							 ai.id IS NULL
							 OR ai.provider IS DISTINCT FROM i.provider_id::text
							 OR ai.subject IS DISTINCT FROM i.subject
							 OR ai.display_name IS DISTINCT FROM COALESCE(i.display_name, '')
							 OR ai.active IS DISTINCT FROM i.active
							 OR NOT EXISTS (
							 SELECT 1
							 FROM identity_external_links l
							 WHERE l.identity_id = i.id
							   AND l.provider = i.provider_id::text
							   AND l.external_id = COALESCE(NULLIF(i.external_id, ''), NULLIF(i.subject, ''), '')
						   )
						   )
						 ) AS unresolved)
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

		var markerStatus string
		markerErr := tx.Raw(`
			SELECT status
			FROM v2_compatibility_markers
			WHERE marker_kind = 'identity_cutover'
			ORDER BY created_at DESC, marker_id DESC
			LIMIT 1
		`).Row().Scan(&markerStatus)
		if markerErr != nil && markerErr != sql.ErrNoRows {
			return markerErr
		}
		if markerErr == sql.ErrNoRows || markerStatus != "compatible" {
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
