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

func (r *SSORepositoryImpl) DirectoryMembershipEntitled(ctx context.Context, providerID, identityID, teamID uuid.UUID, groupID string) (bool, error) {
	if providerID == uuid.Nil || identityID == uuid.Nil || teamID == uuid.Nil || strings.TrimSpace(groupID) == "" {
		return false, nil
	}
	var entitled bool
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM team_memberships membership
				JOIN actor_identities actor ON actor.id = membership.actor_identity_id
				JOIN teams t ON t.id = membership.team_id
				JOIN sso_directory_connectors c
					ON c.provider_id = membership.sso_provider_id
					AND c.status = 'active'
				JOIN sso_directory_users u
					ON u.connector_id = c.id
					AND u.identity_id = membership.actor_identity_id
					AND u.active = true
				JOIN sso_directory_groups g
					ON g.connector_id = c.id
					AND g.active = true
					AND (CASE WHEN g.external_id <> '' THEN g.external_id ELSE g.id::text END) = membership.sso_group_id
				JOIN sso_directory_group_memberships gm
					ON gm.connector_id = c.id
					AND gm.group_id = g.id
					AND gm.user_id = u.id
				JOIN sso_group_mappings m
					ON m.provider_id = membership.sso_provider_id
					AND m.team_id = membership.team_id
					AND m.group_id = (CASE WHEN g.external_id <> '' THEN g.external_id ELSE g.id::text END)
					AND m.enabled = true
					AND m.retired_at IS NULL
				WHERE membership.sso_provider_id = $1
					AND membership.actor_identity_id = $2
					AND membership.team_id = $3
					AND membership.sso_group_id = $4
					AND membership.sso_entitlement_status = 'active'
					AND membership.status = 'active'
					AND actor.active = true
					AND t.status = 'active'
					AND t.deleted_at IS NULL
			)
		`, providerID, identityID, teamID, strings.TrimSpace(groupID)).Row().Scan(&entitled)
	})
	if err != nil {
		return false, fmt.Errorf("failed to check directory team membership entitlement: %w", err)
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
			if err := tx.Raw(`
				INSERT INTO sso_identities (
					id, provider_id, subject, external_id, email, display_name, active,
					last_login_at, last_entitlement_check_at, created_at, updated_at
				) VALUES ($1, $2, $3, $4, $5, $6, true, $7, $8, $9, $9)
				RETURNING id, active, created_at, updated_at
			`, identity.ID, identity.ProviderID, identity.Subject, identity.ExternalID, identity.Email, identity.DisplayName, identity.LastLoginAt, identity.LastEntitlementCheckAt, now).Row().Scan(&identity.ID, &identity.Active, &identity.CreatedAt, &identity.UpdatedAt); err != nil {
				return err
			}
		} else if err := tx.Raw(`
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
		`, identity.Subject, identity.ExternalID, identity.Email, identity.DisplayName, identity.LastLoginAt, identity.LastEntitlementCheckAt, now, identity.ID, identity.ProviderID, directoryAuthority).Row().Scan(&identity.ID, &identity.Active, &identity.CreatedAt, &identity.UpdatedAt); err != nil {
			return err
		}
		return upsertCanonicalSSOIdentityTx(tx, *identity)
	})
	if err != nil {
		return fmt.Errorf("failed to upsert sso identity: %w", err)
	}
	return nil
}

func upsertCanonicalSSOIdentityTx(tx *gorm.DB, identity domain.SSOIdentity) error {
	provider := identity.ProviderID.String()
	externalID := strings.TrimSpace(identity.ExternalID)
	if externalID == "" {
		externalID = strings.TrimSpace(identity.Subject)
	}
	if err := tx.Exec(`
		INSERT INTO actor_identities (
			id, kind, team_id, provider, subject, display_name, active, created_at, updated_at
		) VALUES ($1, 'human', NULL, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET
			kind = CASE WHEN actor_identities.kind = 'api_client' THEN actor_identities.kind ELSE 'human' END,
			team_id = CASE WHEN actor_identities.kind = 'api_client' THEN actor_identities.team_id ELSE NULL END,
			provider = EXCLUDED.provider,
			subject = EXCLUDED.subject,
			display_name = EXCLUDED.display_name,
			active = EXCLUDED.active,
			updated_at = EXCLUDED.updated_at
	`, identity.ID, provider, identity.Subject, identity.DisplayName, identity.Active, identity.CreatedAt, identity.UpdatedAt).Error; err != nil {
		return err
	}
	if err := tx.Exec(`
		DELETE FROM identity_external_links
		WHERE identity_id = $1 AND provider = $2 AND external_id <> $3
	`, identity.ID, provider, externalID).Error; err != nil {
		return err
	}
	if externalID == "" {
		return nil
	}
	return tx.Exec(`
		INSERT INTO identity_external_links (identity_id, provider, external_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (provider, external_id) DO UPDATE SET identity_id = EXCLUDED.identity_id
	`, identity.ID, provider, externalID).Error
}

func syncCanonicalSSOIdentityByIDTx(tx *gorm.DB, identityID uuid.UUID) error {
	rows, err := tx.Raw(`
		SELECT id, provider_id, subject, external_id, email, display_name, active,
		       last_login_at, last_entitlement_check_at, created_at, updated_at
		FROM sso_identities
		WHERE id = $1
	`, identityID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		return rows.Err()
	}
	identity, err := scanSSOIdentity(rows)
	if err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return upsertCanonicalSSOIdentityTx(tx, *identity)
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
