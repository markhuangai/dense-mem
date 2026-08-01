package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *SSORepositoryImpl) DirectoryAuthorityActive(ctx context.Context, providerID uuid.UUID) (bool, error) {
	var active bool
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		var err error
		active, err = directoryAuthorityActiveTx(tx, providerID)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("failed to check directory authority: %w", err)
	}
	return active, nil
}

func (r *SSORepositoryImpl) DirectoryTeamProfileEntitled(ctx context.Context, profileID, providerID, identityID, teamID uuid.UUID, groupID string) (bool, error) {
	if profileID == uuid.Nil || providerID == uuid.Nil || identityID == uuid.Nil || teamID == uuid.Nil || strings.TrimSpace(groupID) == "" {
		return false, nil
	}
	var entitled bool
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM team_profiles p
				JOIN teams t ON t.id = p.team_id
				JOIN sso_directory_connectors c
					ON c.provider_id = p.sso_provider_id
					AND c.status = 'active'
				JOIN sso_directory_users u
					ON u.connector_id = c.id
					AND u.identity_id = p.sso_identity_id
					AND u.active = true
				JOIN sso_directory_groups g
					ON g.connector_id = c.id
					AND g.active = true
					AND (CASE WHEN g.external_id <> '' THEN g.external_id ELSE g.id::text END) = p.sso_group_id
				JOIN sso_directory_group_memberships gm
					ON gm.connector_id = c.id
					AND gm.group_id = g.id
					AND gm.user_id = u.id
				JOIN sso_group_mappings m
					ON m.provider_id = p.sso_provider_id
					AND m.team_id = p.team_id
					AND m.group_id = (CASE WHEN g.external_id <> '' THEN g.external_id ELSE g.id::text END)
					AND m.enabled = true
					AND m.retired_at IS NULL
				WHERE p.id = $1
					AND p.sso_provider_id = $2
					AND p.sso_identity_id = $3
					AND p.team_id = $4
					AND p.sso_group_id = $5
					AND p.auth_source = 'sso'
					AND p.sso_entitlement_status = 'active'
					AND p.revoked_at IS NULL
					AND t.status = 'active'
					AND t.deleted_at IS NULL
			)
		`, profileID, providerID, identityID, teamID, strings.TrimSpace(groupID)).Row().Scan(&entitled)
	})
	if err != nil {
		return false, fmt.Errorf("failed to check directory team profile entitlement: %w", err)
	}
	return entitled, nil
}

func (r *SSORepositoryImpl) UpsertIdentity(ctx context.Context, identity *domain.SSOIdentity) error {
	now := time.Now().UTC()
	if identity.LastLoginAt == nil {
		identity.LastLoginAt = &now
	}
	identity.ExternalID = strings.TrimSpace(identity.ExternalID)
	if identity.ExternalID == "" {
		identity.ExternalID = identity.Subject
	}
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		directoryAuthority, err := directoryAuthorityActiveTx(tx, identity.ProviderID)
		if err != nil {
			return err
		}
		externalMatch, externalFound, err := findDirectoryIdentityIDTx(tx, identity.ProviderID, "external_id", identity.ExternalID)
		if err != nil {
			return err
		}
		subjectMatch, subjectFound, err := findDirectoryIdentityIDTx(tx, identity.ProviderID, "subject", identity.Subject)
		if err != nil {
			return err
		}
		if externalFound && subjectFound && externalMatch != subjectMatch {
			return ErrSSOIdentityConflict
		}
		if externalFound {
			identity.ID = externalMatch
		} else if subjectFound {
			identity.ID = subjectMatch
		}
		if directoryAuthority {
			if identity.ID == uuid.Nil {
				return ErrDirectoryIdentityNotProvisioned
			}
			active, err := directoryIdentityActiveTx(tx, identity.ID)
			if err != nil {
				return err
			}
			if !active {
				return ErrDirectoryIdentityNotProvisioned
			}
		}
		if !externalFound && !subjectFound {
			if identity.ID == uuid.Nil {
				identity.ID = uuid.New()
			}
			return tx.Raw(`
				INSERT INTO sso_identities (
					id, provider_id, subject, external_id, email, display_name, active,
					last_login_at, last_entitlement_check_at, created_at, updated_at
				) VALUES ($1, $2, $3, $4, $5, $6, true, $7, $8, $9, $9)
				RETURNING id, active, created_at, updated_at
			`, identity.ID, identity.ProviderID, identity.Subject, identity.ExternalID, identity.Email, identity.DisplayName, identity.LastLoginAt, identity.LastEntitlementCheckAt, now).Row().Scan(&identity.ID, &identity.Active, &identity.CreatedAt, &identity.UpdatedAt)
		}
		return tx.Raw(`
			UPDATE sso_identities
			SET subject = $1,
			    external_id = $2,
			    email = $3,
			    display_name = $4,
			    active = CASE WHEN $10 THEN active ELSE true END,
			    last_login_at = $5,
			    last_entitlement_check_at = $6,
			    updated_at = $7
			WHERE id = $8 AND provider_id = $9
			RETURNING id, active, created_at, updated_at
		`, identity.Subject, identity.ExternalID, identity.Email, identity.DisplayName, identity.LastLoginAt, identity.LastEntitlementCheckAt, now, identity.ID, identity.ProviderID, directoryAuthority).Row().Scan(&identity.ID, &identity.Active, &identity.CreatedAt, &identity.UpdatedAt)
	})
	if err != nil {
		return fmt.Errorf("failed to upsert sso identity: %w", err)
	}
	return nil
}

func directoryAuthorityActiveTx(tx *gorm.DB, providerID uuid.UUID) (bool, error) {
	var active bool
	if err := tx.Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM sso_directory_connectors
			WHERE provider_id = $1 AND status = 'active'
		)
	`, providerID).Row().Scan(&active); err != nil {
		return false, err
	}
	return active, nil
}

func directoryIdentityActiveTx(tx *gorm.DB, identityID uuid.UUID) (bool, error) {
	var active bool
	if err := tx.Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM sso_directory_users u
			JOIN sso_directory_connectors c ON c.id = u.connector_id
			WHERE u.identity_id = $1
			  AND u.active = true
			  AND c.status = 'active'
		)
	`, identityID).Row().Scan(&active); err != nil {
		return false, err
	}
	return active, nil
}

func (r *SSORepositoryImpl) GetIdentity(ctx context.Context, id uuid.UUID) (*domain.SSOIdentity, error) {
	return r.getIdentity(ctx, `WHERE id = $1`, id)
}

func (r *SSORepositoryImpl) GetIdentityByProviderSubject(ctx context.Context, providerID uuid.UUID, subject string) (*domain.SSOIdentity, error) {
	return r.getIdentity(ctx, `WHERE provider_id = $1 AND subject = $2`, providerID, subject)
}

func (r *SSORepositoryImpl) getIdentity(ctx context.Context, where string, args ...any) (*domain.SSOIdentity, error) {
	var identity *domain.SSOIdentity
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id, provider_id, subject, external_id, email, display_name, active, last_login_at, last_entitlement_check_at, created_at, updated_at
			FROM sso_identities `+where, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanSSOIdentity(rows)
		if err != nil {
			return err
		}
		identity = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get sso identity: %w", err)
	}
	return identity, nil
}
