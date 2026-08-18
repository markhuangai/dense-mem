package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/postgrescompat"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *DirectoryIdentityRepositoryImpl) ApplyDirectoryReconcilePlan(ctx context.Context, plan domain.DirectoryReconcilePlan) error {
	return r.applyDirectoryReconcilePlan(ctx, plan, false)
}

func (r *DirectoryIdentityRepositoryImpl) ActivateDirectoryConnector(ctx context.Context, plan domain.DirectoryReconcilePlan) error {
	return r.applyDirectoryReconcilePlan(ctx, plan, true)
}

func (r *DirectoryIdentityRepositoryImpl) applyDirectoryReconcilePlan(ctx context.Context, plan domain.DirectoryReconcilePlan, activate bool) error {
	if plan.ConnectorID == uuid.Nil || plan.ProviderID == uuid.Nil {
		return fmt.Errorf("directory reconcile plan requires connector and provider IDs")
	}
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		connector, err := getDirectoryConnectorTx(tx, plan.ConnectorID, true)
		if err != nil {
			return err
		}
		if connector == nil || connector.ProviderID != plan.ProviderID {
			return gorm.ErrRecordNotFound
		}
		if plan.ReconcileVersion > 0 && connector.ReconcileVersion != plan.ReconcileVersion {
			return ErrDirectoryReconcileStale
		}
		if activate {
			if connector.Status != domain.DirectoryConnectorObserve {
				return fmt.Errorf("directory connector must be observing before activation")
			}
			now := time.Now().UTC()
			if err := tx.Exec(`
				UPDATE sso_directory_connectors
				SET status = 'active',
				    last_activation_at = $1,
				    reconcile_version = reconcile_version + 1,
				    updated_at = $1
				WHERE id = $2
			`, now, plan.ConnectorID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`
					UPDATE sso_identities i
				SET active = u.active,
				    updated_at = $1
				FROM sso_directory_users u
				WHERE u.connector_id = $2
				  AND i.id = u.identity_id
				  AND i.provider_id = $3
				`, now, plan.ConnectorID, plan.ProviderID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`
					UPDATE actor_identities actor
					SET active = identity.active,
					    updated_at = $1
					FROM sso_identities identity
					WHERE identity.provider_id = $2
					  AND actor.id = identity.id
				`, now, plan.ProviderID).Error; err != nil {
				return err
			}
			connector.Status = domain.DirectoryConnectorActive
			connector.ReconcileVersion++
		}
		if connector.Status != domain.DirectoryConnectorActive {
			return fmt.Errorf("directory connector is not active")
		}
		now := time.Now().UTC()
		if len(plan.RestoreDirectoryTeamIDs) > 0 {
			if err := tx.Exec(`
				UPDATE teams
				SET status = 'active', updated_at = $1
				WHERE id = ANY($2::uuid[])
				  AND directory_managed = true
				  AND directory_connector_id = $3
			`, now, postgrescompat.Array(directoryUUIDStrings(plan.RestoreDirectoryTeamIDs)), plan.ConnectorID).Error; err != nil {
				return err
			}
		}
		for _, action := range plan.Bindings {
			if action.GroupID == uuid.Nil || action.TeamID == uuid.Nil || action.GroupExternalID == "" {
				return fmt.Errorf("directory binding action is incomplete")
			}
			if action.CreateTeam {
				if err := tx.Exec(`
					INSERT INTO teams (
						id, name, description, metadata, config, status,
						directory_connector_id, directory_group_id, directory_managed, created_at, updated_at
					) VALUES ($1, $2, '', '{}'::jsonb, '{}'::jsonb, 'active', $3, $4, true, $5, $5)
				`, action.TeamID, action.TeamName, plan.ConnectorID, action.GroupID, now).Error; err != nil {
					return err
				}
			}
			if err := tx.Exec(`
				INSERT INTO sso_directory_group_bindings (
					connector_id, group_id, team_id, origin, scopes, role, created_at, updated_at
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
				ON CONFLICT (connector_id, group_id) DO UPDATE
				SET team_id = EXCLUDED.team_id,
				    origin = EXCLUDED.origin,
				    scopes = EXCLUDED.scopes,
				    role = EXCLUDED.role,
				    updated_at = EXCLUDED.updated_at
			`, plan.ConnectorID, action.GroupID, action.TeamID, action.Origin,
				postgrescompat.Array(action.Entitlement.Scopes), action.Entitlement.Role, now).Error; err != nil {
				return err
			}
			if err := tx.Exec(`
				INSERT INTO sso_group_mappings (
					id, provider_id, team_id, group_id, group_name, scopes, role, enabled, origin, retired_at, created_at, updated_at
				) VALUES (gen_random_uuid(), $1, $2, $3, '', $4, $5, true, 'directory', NULL, $6, $6)
				ON CONFLICT (provider_id, team_id, group_id) DO UPDATE
				SET scopes = EXCLUDED.scopes,
				    role = EXCLUDED.role,
				    enabled = true,
				    origin = 'directory',
				    retired_at = NULL,
				    updated_at = EXCLUDED.updated_at
				WHERE sso_group_mappings.origin = 'directory'
			`, plan.ProviderID, action.TeamID, action.GroupExternalID,
				postgrescompat.Array(action.Entitlement.Scopes), action.Entitlement.Role, now).Error; err != nil {
				return err
			}
		}
		if len(plan.DisableDirectoryGroupIDs) > 0 {
			if err := tx.Exec(`
				UPDATE sso_group_mappings
				SET enabled = false,
				    retired_at = COALESCE(retired_at, $1),
				    updated_at = $1
				WHERE provider_id = $2
				  AND origin = 'directory'
				  AND group_id = ANY($3::text[])
			`, now, plan.ProviderID, postgrescompat.Array(uniqueDirectoryStrings(plan.DisableDirectoryGroupIDs))).Error; err != nil {
				return err
			}
		}
		if err := replaceDirectoryIssuesTx(tx, plan.ConnectorID, plan.Issues, now); err != nil {
			return err
		}
		for _, grant := range plan.ProfileGrants {
			if err := upsertDirectoryTeamProfileTx(ctx, tx, plan.ProviderID, grant, now); err != nil {
				return err
			}
		}
		if err := revokeMissingDirectoryProfilesTx(ctx, tx, plan.ProviderID, plan.DirectoryIdentityIDs, plan.ProfileGrants, now); err != nil {
			return err
		}
		if len(plan.ArchiveDirectoryTeamIDs) > 0 {
			if err := tx.Exec(`
				UPDATE teams
				SET status = 'archived', updated_at = $1
				WHERE id = ANY($2::uuid[])
				  AND directory_managed = true
				  AND directory_connector_id = $3
			`, now, postgrescompat.Array(directoryUUIDStrings(plan.ArchiveDirectoryTeamIDs)), plan.ConnectorID).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to apply directory reconcile plan: %w", err)
	}
	return nil
}

func (r *DirectoryIdentityRepositoryImpl) AdoptDirectoryTeam(ctx context.Context, connectorID, groupID, teamID uuid.UUID) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		connector, err := getDirectoryConnectorTx(tx, connectorID, true)
		if err != nil {
			return err
		}
		if connector == nil {
			return gorm.ErrRecordNotFound
		}
		var boundTeamID uuid.UUID
		if err := tx.Raw(`
			SELECT team_id
			FROM sso_directory_group_bindings
			WHERE connector_id = $1 AND group_id = $2
			FOR UPDATE
		`, connectorID, groupID).Row().Scan(&boundTeamID); err != nil {
			return err
		}
		if boundTeamID != teamID {
			return fmt.Errorf("directory group is not bound to the requested team")
		}
		now := time.Now().UTC()
		res := tx.Exec(`
			UPDATE teams
			SET directory_managed = false,
			    directory_connector_id = NULL,
			    directory_group_id = NULL,
			    updated_at = $1
			WHERE id = $2
			  AND directory_managed = true
			  AND directory_connector_id = $3
			  AND directory_group_id = $4
		`, now, teamID, connectorID, groupID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("directory team cannot be adopted")
		}
		if err := tx.Exec(`
			UPDATE sso_directory_group_bindings
			SET origin = 'adopted', updated_at = $1
			WHERE connector_id = $2 AND group_id = $3
		`, now, connectorID, groupID).Error; err != nil {
			return err
		}
		return bumpDirectoryConnectorReconcileVersionTx(tx, connectorID, now)
	})
	if err != nil {
		return fmt.Errorf("failed to adopt directory team: %w", err)
	}
	return nil
}

func getDirectoryConnectorTx(tx *gorm.DB, connectorID uuid.UUID, forUpdate ...bool) (*domain.DirectoryConnector, error) {
	lockClause := ""
	if len(forUpdate) > 0 && forUpdate[0] {
		lockClause = " FOR UPDATE"
	}
	rows, err := tx.Raw(`
		SELECT id, provider_id, status, group_pattern, role_entitlements::text, max_auto_teams,
		       credential_version, bearer_token_hash, oauth_client_id, oauth_client_secret_hash,
		       last_activation_at, reconcile_version, created_at, updated_at
		FROM sso_directory_connectors
		WHERE id = $1
	`+lockClause, connectorID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanDirectoryConnector(rows)
}

func listDirectoryUsersTx(tx *gorm.DB, connectorID uuid.UUID) ([]domain.DirectoryUser, error) {
	items := make([]domain.DirectoryUser, 0)
	rows, err := tx.Raw(`
		SELECT id, connector_id, external_id, user_name, email, display_name, active, identity_id, created_at, updated_at
		FROM sso_directory_users
		WHERE connector_id = $1
		ORDER BY lower(user_name), id
	`, connectorID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanDirectoryUser(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func listDirectoryGroupsTx(tx *gorm.DB, connectorID uuid.UUID) ([]domain.DirectoryGroup, error) {
	items := make([]domain.DirectoryGroup, 0)
	rows, err := tx.Raw(`
		SELECT id, connector_id, external_id, display_name, active, created_at, updated_at
		FROM sso_directory_groups
		WHERE connector_id = $1
		ORDER BY lower(display_name), id
	`, connectorID).Rows()
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		item, err := scanDirectoryGroup(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := populateDirectoryGroupMembersTx(tx, connectorID, items); err != nil {
		return nil, err
	}
	return items, nil
}

func populateDirectoryGroupMembersTx(tx *gorm.DB, connectorID uuid.UUID, groups []domain.DirectoryGroup) error {
	if len(groups) == 0 {
		return nil
	}
	groupIDs := make([]uuid.UUID, 0, len(groups))
	membersByGroup := make(map[uuid.UUID][]domain.DirectoryUser, len(groups))
	for _, group := range groups {
		groupIDs = append(groupIDs, group.ID)
		membersByGroup[group.ID] = []domain.DirectoryUser{}
	}
	rows, err := tx.Raw(`
		SELECT m.group_id,
		       u.id, u.connector_id, u.external_id, u.user_name, u.email, u.display_name,
		       u.active, u.identity_id, u.created_at, u.updated_at
		FROM sso_directory_group_memberships m
		JOIN sso_directory_users u
		  ON u.connector_id = m.connector_id AND u.id = m.user_id
		WHERE m.connector_id = $1
		  AND m.group_id = ANY($2::uuid[])
		ORDER BY m.group_id, lower(u.user_name), u.id
	`, connectorID, postgrescompat.Array(groupIDs)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var groupID uuid.UUID
		var user domain.DirectoryUser
		if err := rows.Scan(
			&groupID,
			&user.ID,
			&user.ConnectorID,
			&user.ExternalID,
			&user.UserName,
			&user.Email,
			&user.DisplayName,
			&user.Active,
			&user.IdentityID,
			&user.CreatedAt,
			&user.UpdatedAt,
		); err != nil {
			return err
		}
		membersByGroup[groupID] = append(membersByGroup[groupID], user)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range groups {
		groups[index].Members = membersByGroup[groups[index].ID]
	}
	return nil
}

func listDirectoryBindingsTx(tx *gorm.DB, connectorID uuid.UUID) ([]domain.DirectoryGroupBinding, error) {
	items := make([]domain.DirectoryGroupBinding, 0)
	rows, err := tx.Raw(`
		SELECT connector_id, group_id, team_id, origin, scopes, role, created_at, updated_at
		FROM sso_directory_group_bindings
		WHERE connector_id = $1
		ORDER BY group_id
	`, connectorID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.DirectoryGroupBinding
		var origin string
		if err := rows.Scan(
			&item.ConnectorID,
			&item.GroupID,
			&item.TeamID,
			&origin,
			postgrescompat.Array(&item.Scopes),
			&item.Role,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.Origin = domain.DirectoryBindingOrigin(origin)
		items = append(items, item)
	}
	return items, rows.Err()
}

func listManualDirectoryMappingsTx(tx *gorm.DB, providerID uuid.UUID) ([]domain.SSOGroupMapping, error) {
	items := make([]domain.SSOGroupMapping, 0)
	rows, err := tx.Raw(`
		SELECT m.id, m.provider_id, m.team_id, t.name, m.group_id, m.group_name, m.scopes, m.role,
		       m.enabled, m.origin, m.retired_at, m.created_at, m.updated_at
		FROM sso_group_mappings m
		JOIN teams t ON t.id = m.team_id
		WHERE m.provider_id = $1
		  AND m.origin = 'manual'
		  AND m.enabled = true
		  AND m.retired_at IS NULL
		  AND t.status = 'active'
		  AND t.deleted_at IS NULL
		ORDER BY m.group_id, m.id
	`, providerID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.SSOGroupMapping
		if err := rows.Scan(
			&item.ID,
			&item.ProviderID,
			&item.TeamID,
			&item.TeamName,
			&item.GroupID,
			&item.GroupName,
			postgrescompat.Array(&item.Scopes),
			&item.Role,
			&item.Enabled,
			&item.Origin,
			&item.RetiredAt,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func listDirectoryTeamsTx(tx *gorm.DB) ([]domain.DirectoryTeam, error) {
	items := make([]domain.DirectoryTeam, 0)
	rows, err := tx.Raw(`
		SELECT id, name, status, directory_managed, directory_connector_id::text, directory_group_id::text
		FROM teams
		WHERE deleted_at IS NULL
		ORDER BY lower(name), id
	`).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.DirectoryTeam
		var connectorID, groupID sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &item.Status, &item.DirectoryManaged, &connectorID, &groupID); err != nil {
			return nil, err
		}
		if connectorID.Valid && connectorID.String != "" {
			parsed, err := uuid.Parse(connectorID.String)
			if err != nil {
				return nil, err
			}
			item.DirectoryConnectorID = &parsed
		}
		if groupID.Valid && groupID.String != "" {
			parsed, err := uuid.Parse(groupID.String)
			if err != nil {
				return nil, err
			}
			item.DirectoryGroupID = &parsed
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func replaceDirectoryIssuesTx(tx *gorm.DB, connectorID uuid.UUID, issues []domain.DirectoryIssue, now time.Time) error {
	if err := tx.Exec(`
		UPDATE sso_directory_issues
		SET active = false, updated_at = $1
		WHERE connector_id = $2 AND active = true
	`, now, connectorID).Error; err != nil {
		return err
	}
	for _, issue := range issues {
		if strings.TrimSpace(issue.Key) == "" {
			return fmt.Errorf("directory issue key is required")
		}
		if err := tx.Exec(`
			INSERT INTO sso_directory_issues (
				id, connector_id, group_id, issue_key, kind, detail, active, created_at, updated_at
			) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, true, $6, $6)
			ON CONFLICT (connector_id, issue_key) DO UPDATE
			SET group_id = EXCLUDED.group_id,
			    kind = EXCLUDED.kind,
			    detail = EXCLUDED.detail,
			    active = true,
			    updated_at = EXCLUDED.updated_at
		`, connectorID, issue.GroupID, issue.Key, issue.Kind, issue.Detail, now).Error; err != nil {
			return err
		}
	}
	return nil
}

func upsertDirectoryTeamProfileTx(ctx context.Context, tx *gorm.DB, providerID uuid.UUID, grant domain.DirectoryProfileGrant, now time.Time) error {
	if grant.IdentityID == uuid.Nil || grant.TeamID == uuid.Nil || strings.TrimSpace(grant.Subject) == "" {
		return fmt.Errorf("directory membership grant is incomplete")
	}
	if err := setActiveSSOTeamMutationScope(ctx, tx, grant.TeamID.String()); err != nil {
		return err
	}
	_, _, _, err := upsertCanonicalSSOMembershipTx(tx, canonicalSSOMembershipInput{
		IdentityID: grant.IdentityID, ProviderID: providerID, TeamID: grant.TeamID,
		Scopes: grant.Entitlement.Scopes, Role: grant.Entitlement.Role,
		GroupID: grant.GroupExternalID, MembershipName: directoryMembershipName(grant.Email, grant.DisplayName, grant.IdentityID),
		LastEntitlementCheckedAt: &now, Now: now,
	})
	return err
}

func revokeMissingDirectoryProfilesTx(ctx context.Context, tx *gorm.DB, providerID uuid.UUID, identityIDs []uuid.UUID, grants []domain.DirectoryProfileGrant, now time.Time) error {
	identityIDs = uniqueDirectoryUUIDs(identityIDs)
	if len(identityIDs) == 0 {
		return nil
	}
	desired := make(map[string]struct{}, len(grants))
	for _, grant := range grants {
		desired[directoryProfileGrantKey(grant.IdentityID, grant.TeamID)] = struct{}{}
	}
	rows, err := tx.Raw(`
		SELECT id, team_id, actor_identity_id
		FROM team_memberships
		WHERE sso_provider_id = $1
		  AND actor_identity_id = ANY($2::uuid[])
		  AND status = 'active'
	`, providerID, postgrescompat.Array(directoryUUIDStrings(identityIDs))).Rows()
	if err != nil {
		return err
	}
	type profileRef struct {
		id, teamID, identityID uuid.UUID
	}
	stale := make([]profileRef, 0)
	for rows.Next() {
		var item profileRef
		if err := rows.Scan(&item.id, &item.teamID, &item.identityID); err != nil {
			_ = rows.Close()
			return err
		}
		if _, found := desired[directoryProfileGrantKey(item.identityID, item.teamID)]; !found {
			stale = append(stale, item)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range stale {
		if err := setActiveSSOTeamMutationScope(ctx, tx, item.teamID.String()); err != nil {
			return err
		}
		if err := tx.Exec(`
				UPDATE team_memberships
				SET status = 'revoked',
				    sso_entitlement_status = 'denied',
				    sso_last_entitlement_checked_at = $1,
				    updated_at = $1
				WHERE id = $2 AND team_id = $3 AND status = 'active'
			`, now, item.id, item.teamID).Error; err != nil {
			return err
		}
	}
	return nil
}

func directoryProfileGrantKey(identityID, teamID uuid.UUID) string {
	return identityID.String() + ":" + teamID.String()
}

func directoryMembershipName(email, displayName string, identityID uuid.UUID) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = strings.TrimSpace(email)
	}
	if name == "" {
		name = "directory"
	}
	suffix := " (" + identityID.String() + ")"
	limit := 100 - len([]rune(suffix))
	characters := []rune(name)
	if len(characters) > limit {
		name = string(characters[:limit])
	}
	return name + suffix
}

func directoryUUIDStrings(values []uuid.UUID) []string {
	values = uniqueDirectoryUUIDs(values)
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.String())
	}
	return result
}

func uniqueDirectoryStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
