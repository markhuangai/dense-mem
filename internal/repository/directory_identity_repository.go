package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type DirectoryIdentityRepository interface {
	CreateDirectoryConnector(ctx context.Context, connector *domain.DirectoryConnector) error
	GetDirectoryConnector(ctx context.Context, id uuid.UUID) (*domain.DirectoryConnector, error)
	GetDirectoryConnectorByProviderID(ctx context.Context, providerID uuid.UUID) (*domain.DirectoryConnector, error)
	GetDirectoryConnectorByOAuthClientID(ctx context.Context, clientID string) (*domain.DirectoryConnector, error)
	ListDirectoryConnectors(ctx context.Context) ([]*domain.DirectoryConnector, error)
	UpdateDirectoryConnector(ctx context.Context, connector *domain.DirectoryConnector) error
	SetDirectoryConnectorStatus(ctx context.Context, id uuid.UUID, status domain.DirectoryConnectorStatus, activatedAt *time.Time) error
	SetDirectoryCredentials(ctx context.Context, id uuid.UUID, credentialVersion int, bearerHash, oauthClientID, oauthSecretHash string) error

	CreateDirectoryOAuthToken(ctx context.Context, connectorID uuid.UUID, tokenHash string, expiresAt time.Time) error
	GetDirectoryOAuthToken(ctx context.Context, connectorID uuid.UUID, tokenHash string) (bool, error)
	DeleteExpiredDirectoryOAuthTokens(ctx context.Context, now time.Time) error

	UpsertDirectoryUser(ctx context.Context, user domain.DirectoryUser) (*domain.DirectoryUser, error)
	GetDirectoryUser(ctx context.Context, connectorID, userID uuid.UUID) (*domain.DirectoryUser, error)
	ListDirectoryUsers(ctx context.Context, connectorID uuid.UUID) ([]*domain.DirectoryUser, error)
	UpsertDirectoryGroup(ctx context.Context, group domain.DirectoryGroup) (*domain.DirectoryGroup, error)
	UpsertDirectoryGroupWithMembers(ctx context.Context, group domain.DirectoryGroup, memberIDs []uuid.UUID) (*domain.DirectoryGroup, error)
	GetDirectoryGroup(ctx context.Context, connectorID, groupID uuid.UUID) (*domain.DirectoryGroup, error)
	ListDirectoryGroups(ctx context.Context, connectorID uuid.UUID) ([]*domain.DirectoryGroup, error)
	ReplaceDirectoryGroupMembers(ctx context.Context, connectorID, groupID uuid.UUID, memberIDs []uuid.UUID) error

	DirectoryConnectorSnapshot(ctx context.Context, connectorID uuid.UUID) (*domain.DirectoryConnectorSnapshot, error)
	ApplyDirectoryReconcilePlan(ctx context.Context, plan domain.DirectoryReconcilePlan) error
	ActivateDirectoryConnector(ctx context.Context, plan domain.DirectoryReconcilePlan) error
	AdoptDirectoryTeam(ctx context.Context, connectorID, groupID, teamID uuid.UUID) error
}

type DirectoryIdentityRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ DirectoryIdentityRepository = (*DirectoryIdentityRepositoryImpl)(nil)

func NewDirectoryIdentityRepository(db *gorm.DB, rls postgres.RLSHelper) *DirectoryIdentityRepositoryImpl {
	return &DirectoryIdentityRepositoryImpl{db: db, rls: rls}
}

func (r *DirectoryIdentityRepositoryImpl) CreateDirectoryConnector(ctx context.Context, connector *domain.DirectoryConnector) error {
	if connector == nil {
		return fmt.Errorf("directory connector is required")
	}
	if connector.ID == uuid.Nil {
		connector.ID = uuid.New()
	}
	payload, err := marshalDirectoryRoleEntitlements(connector.RoleEntitlements)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	connector.CreatedAt = now
	connector.UpdatedAt = now
	err = r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_directory_connectors (
				id, provider_id, status, group_pattern, role_entitlements, max_auto_teams,
				credential_version, bearer_token_hash, oauth_client_id, oauth_client_secret_hash,
				last_activation_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12, $12)
		`, connector.ID, connector.ProviderID, connector.Status, connector.GroupPattern, payload,
			connector.MaxAutoTeams, connector.CredentialVersion, connector.BearerTokenHash,
			connector.OAuthClientID, connector.OAuthClientSecretHash, connector.LastActivationAt, now).Error
	})
	if err != nil {
		return fmt.Errorf("failed to create directory connector: %w", err)
	}
	return nil
}

func (r *DirectoryIdentityRepositoryImpl) GetDirectoryConnector(ctx context.Context, id uuid.UUID) (*domain.DirectoryConnector, error) {
	return r.getDirectoryConnector(ctx, "WHERE id = $1", id)
}

func (r *DirectoryIdentityRepositoryImpl) GetDirectoryConnectorByProviderID(ctx context.Context, providerID uuid.UUID) (*domain.DirectoryConnector, error) {
	return r.getDirectoryConnector(ctx, "WHERE provider_id = $1", providerID)
}

func (r *DirectoryIdentityRepositoryImpl) GetDirectoryConnectorByOAuthClientID(ctx context.Context, clientID string) (*domain.DirectoryConnector, error) {
	return r.getDirectoryConnector(ctx, "WHERE oauth_client_id = $1", strings.TrimSpace(clientID))
}

func (r *DirectoryIdentityRepositoryImpl) getDirectoryConnector(ctx context.Context, where string, args ...any) (*domain.DirectoryConnector, error) {
	var connector *domain.DirectoryConnector
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id, provider_id, status, group_pattern, role_entitlements::text, max_auto_teams,
			       credential_version, bearer_token_hash, oauth_client_id, oauth_client_secret_hash,
			       last_activation_at, created_at, updated_at
			FROM sso_directory_connectors `+where, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		item, err := scanDirectoryConnector(rows)
		if err != nil {
			return err
		}
		connector = item
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get directory connector: %w", err)
	}
	return connector, nil
}

func (r *DirectoryIdentityRepositoryImpl) ListDirectoryConnectors(ctx context.Context) ([]*domain.DirectoryConnector, error) {
	items := make([]*domain.DirectoryConnector, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id, provider_id, status, group_pattern, role_entitlements::text, max_auto_teams,
			       credential_version, bearer_token_hash, oauth_client_id, oauth_client_secret_hash,
			       last_activation_at, created_at, updated_at
			FROM sso_directory_connectors
			ORDER BY created_at ASC, id ASC
		`).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanDirectoryConnector(rows)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list directory connectors: %w", err)
	}
	return items, nil
}

func (r *DirectoryIdentityRepositoryImpl) UpdateDirectoryConnector(ctx context.Context, connector *domain.DirectoryConnector) error {
	if connector == nil {
		return fmt.Errorf("directory connector is required")
	}
	payload, err := marshalDirectoryRoleEntitlements(connector.RoleEntitlements)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	err = r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE sso_directory_connectors
			SET group_pattern = $1,
			    role_entitlements = $2::jsonb,
			    max_auto_teams = $3,
			    updated_at = $4
			WHERE id = $5
		`, connector.GroupPattern, payload, connector.MaxAutoTeams, now, connector.ID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update directory connector: %w", err)
	}
	connector.UpdatedAt = now
	return nil
}

func (r *DirectoryIdentityRepositoryImpl) SetDirectoryConnectorStatus(ctx context.Context, id uuid.UUID, status domain.DirectoryConnectorStatus, activatedAt *time.Time) error {
	now := time.Now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE sso_directory_connectors
			SET status = $1,
			    last_activation_at = CASE WHEN $2::timestamptz IS NULL THEN last_activation_at ELSE $2::timestamptz END,
			    updated_at = $3
			WHERE id = $4
		`, status, activatedAt, now, id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update directory connector status: %w", err)
	}
	return nil
}

func (r *DirectoryIdentityRepositoryImpl) SetDirectoryCredentials(ctx context.Context, id uuid.UUID, credentialVersion int, bearerHash, oauthClientID, oauthSecretHash string) error {
	now := time.Now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE sso_directory_connectors
			SET credential_version = $1,
			    bearer_token_hash = $2,
			    oauth_client_id = $3,
			    oauth_client_secret_hash = $4,
			    updated_at = $5
			WHERE id = $6
		`, credentialVersion, bearerHash, oauthClientID, oauthSecretHash, now, id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Exec(`DELETE FROM sso_directory_oauth_tokens WHERE connector_id = $1`, id).Error
	})
	if err != nil {
		return fmt.Errorf("failed to rotate directory connector credentials: %w", err)
	}
	return nil
}

func (r *DirectoryIdentityRepositoryImpl) CreateDirectoryOAuthToken(ctx context.Context, connectorID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_directory_oauth_tokens (token_hash, connector_id, expires_at)
			VALUES ($1, $2, $3)
		`, tokenHash, connectorID, expiresAt).Error
	})
	if err != nil {
		return fmt.Errorf("failed to create directory oauth token: %w", err)
	}
	return nil
}

func (r *DirectoryIdentityRepositoryImpl) GetDirectoryOAuthToken(ctx context.Context, connectorID uuid.UUID, tokenHash string) (bool, error) {
	var found bool
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT EXISTS (
				SELECT 1 FROM sso_directory_oauth_tokens
				WHERE connector_id = $1 AND token_hash = $2 AND expires_at > NOW()
			)
		`, connectorID, tokenHash).Row().Scan(&found)
	})
	if err != nil {
		return false, fmt.Errorf("failed to get directory oauth token: %w", err)
	}
	return found, nil
}

func (r *DirectoryIdentityRepositoryImpl) DeleteExpiredDirectoryOAuthTokens(ctx context.Context, now time.Time) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM sso_directory_oauth_tokens WHERE expires_at <= $1`, now).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete expired directory oauth tokens: %w", err)
	}
	return nil
}

func marshalDirectoryRoleEntitlements(values map[string]domain.DirectoryRoleEntitlement) (string, error) {
	payload, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("failed to encode directory role entitlements: %w", err)
	}
	return string(payload), nil
}

func scanDirectoryConnector(rows *sql.Rows) (*domain.DirectoryConnector, error) {
	var item domain.DirectoryConnector
	var status string
	var entitlementsJSON string
	if err := rows.Scan(
		&item.ID,
		&item.ProviderID,
		&status,
		&item.GroupPattern,
		&entitlementsJSON,
		&item.MaxAutoTeams,
		&item.CredentialVersion,
		&item.BearerTokenHash,
		&item.OAuthClientID,
		&item.OAuthClientSecretHash,
		&item.LastActivationAt,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(entitlementsJSON), &item.RoleEntitlements); err != nil {
		return nil, fmt.Errorf("failed to decode directory role entitlements: %w", err)
	}
	item.Status = domain.DirectoryConnectorStatus(status)
	return &item, nil
}

func (r *DirectoryIdentityRepositoryImpl) UpsertDirectoryUser(ctx context.Context, user domain.DirectoryUser) (*domain.DirectoryUser, error) {
	user.ExternalID = strings.TrimSpace(user.ExternalID)
	user.UserName = strings.TrimSpace(user.UserName)
	user.Email = strings.TrimSpace(user.Email)
	user.DisplayName = strings.TrimSpace(user.DisplayName)
	if user.ConnectorID == uuid.Nil || user.UserName == "" {
		return nil, fmt.Errorf("directory connector ID and userName are required")
	}
	if user.ID == uuid.Nil {
		user.ID = uuid.New()
	}
	var stored *domain.DirectoryUser
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		existing, err := findDirectoryUserTx(tx, user.ConnectorID, user.ID, user.ExternalID, user.UserName)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.ExternalID != "" && user.ExternalID != "" && existing.ExternalID != user.ExternalID {
				return fmt.Errorf("directory user external_id is immutable")
			}
			user.ID = existing.ID
			if user.ExternalID == "" {
				user.ExternalID = existing.ExternalID
			}
		}
		providerID, err := directoryConnectorProviderIDTx(tx, user.ConnectorID)
		if err != nil {
			return err
		}
		subject := user.ExternalID
		if subject == "" {
			subject = "scim:" + user.ID.String()
		}
		identityID, err := upsertDirectoryIdentityTx(tx, providerID, user.ExternalID, subject, user.Email, user.DisplayName, user.Active)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		rows, err := tx.Raw(`
			INSERT INTO sso_directory_users (
				id, connector_id, external_id, user_name, email, display_name, active, identity_id, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
			ON CONFLICT (id) DO UPDATE
			SET external_id = EXCLUDED.external_id,
			    user_name = EXCLUDED.user_name,
			    email = EXCLUDED.email,
			    display_name = EXCLUDED.display_name,
			    active = EXCLUDED.active,
			    identity_id = EXCLUDED.identity_id,
			    updated_at = EXCLUDED.updated_at
			WHERE sso_directory_users.connector_id = EXCLUDED.connector_id
			RETURNING id, connector_id, external_id, user_name, email, display_name, active, identity_id, created_at, updated_at
		`, user.ID, user.ConnectorID, user.ExternalID, user.UserName, user.Email, user.DisplayName, user.Active, identityID, now).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return gorm.ErrRecordNotFound
		}
		item, err := scanDirectoryUser(rows)
		if err != nil {
			return err
		}
		stored = item
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upsert directory user: %w", err)
	}
	return stored, nil
}

func (r *DirectoryIdentityRepositoryImpl) GetDirectoryUser(ctx context.Context, connectorID, userID uuid.UUID) (*domain.DirectoryUser, error) {
	var item *domain.DirectoryUser
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id, connector_id, external_id, user_name, email, display_name, active, identity_id, created_at, updated_at
			FROM sso_directory_users
			WHERE connector_id = $1 AND id = $2
		`, connectorID, userID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanDirectoryUser(rows)
		if err != nil {
			return err
		}
		item = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get directory user: %w", err)
	}
	return item, nil
}

func (r *DirectoryIdentityRepositoryImpl) ListDirectoryUsers(ctx context.Context, connectorID uuid.UUID) ([]*domain.DirectoryUser, error) {
	items := make([]*domain.DirectoryUser, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id, connector_id, external_id, user_name, email, display_name, active, identity_id, created_at, updated_at
			FROM sso_directory_users
			WHERE connector_id = $1
			ORDER BY lower(user_name), id
		`, connectorID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanDirectoryUser(rows)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list directory users: %w", err)
	}
	return items, nil
}

func (r *DirectoryIdentityRepositoryImpl) UpsertDirectoryGroup(ctx context.Context, group domain.DirectoryGroup) (*domain.DirectoryGroup, error) {
	group.ExternalID = strings.TrimSpace(group.ExternalID)
	group.DisplayName = strings.TrimSpace(group.DisplayName)
	if group.ConnectorID == uuid.Nil || group.DisplayName == "" {
		return nil, fmt.Errorf("directory connector ID and group displayName are required")
	}
	if group.ID == uuid.Nil {
		group.ID = uuid.New()
	}
	var stored *domain.DirectoryGroup
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		item, err := upsertDirectoryGroupTx(tx, group)
		if err != nil {
			return err
		}
		stored = item
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upsert directory group: %w", err)
	}
	return stored, nil
}

func (r *DirectoryIdentityRepositoryImpl) UpsertDirectoryGroupWithMembers(ctx context.Context, group domain.DirectoryGroup, memberIDs []uuid.UUID) (*domain.DirectoryGroup, error) {
	group.ExternalID = strings.TrimSpace(group.ExternalID)
	group.DisplayName = strings.TrimSpace(group.DisplayName)
	if group.ConnectorID == uuid.Nil || group.DisplayName == "" {
		return nil, fmt.Errorf("directory connector ID and group displayName are required")
	}
	if group.ID == uuid.Nil {
		group.ID = uuid.New()
	}
	var stored *domain.DirectoryGroup
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		item, err := upsertDirectoryGroupTx(tx, group)
		if err != nil {
			return err
		}
		if err := replaceDirectoryGroupMembersTx(tx, item.ConnectorID, item.ID, memberIDs); err != nil {
			return err
		}
		stored = item
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upsert directory group with members: %w", err)
	}
	return stored, nil
}

func upsertDirectoryGroupTx(tx *gorm.DB, group domain.DirectoryGroup) (*domain.DirectoryGroup, error) {
	existing, err := findDirectoryGroupTx(tx, group.ConnectorID, group.ID, group.ExternalID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.ExternalID != "" && group.ExternalID != "" && existing.ExternalID != group.ExternalID {
			return nil, fmt.Errorf("directory group external_id is immutable")
		}
		group.ID = existing.ID
		if group.ExternalID == "" {
			group.ExternalID = existing.ExternalID
		}
	}
	now := time.Now().UTC()
	rows, err := tx.Raw(`
		INSERT INTO sso_directory_groups (
			id, connector_id, external_id, display_name, active, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $6)
		ON CONFLICT (id) DO UPDATE
		SET external_id = EXCLUDED.external_id,
		    display_name = EXCLUDED.display_name,
		    active = EXCLUDED.active,
		    updated_at = EXCLUDED.updated_at
		WHERE sso_directory_groups.connector_id = EXCLUDED.connector_id
		RETURNING id, connector_id, external_id, display_name, active, created_at, updated_at
	`, group.ID, group.ConnectorID, group.ExternalID, group.DisplayName, group.Active, now).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, gorm.ErrRecordNotFound
	}
	item, err := scanDirectoryGroup(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return item, nil
}

func (r *DirectoryIdentityRepositoryImpl) GetDirectoryGroup(ctx context.Context, connectorID, groupID uuid.UUID) (*domain.DirectoryGroup, error) {
	var item *domain.DirectoryGroup
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		group, err := getDirectoryGroupTx(tx, connectorID, groupID)
		if err != nil {
			return err
		}
		item = group
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get directory group: %w", err)
	}
	return item, nil
}

func (r *DirectoryIdentityRepositoryImpl) ListDirectoryGroups(ctx context.Context, connectorID uuid.UUID) ([]*domain.DirectoryGroup, error) {
	items := make([]*domain.DirectoryGroup, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		groups, err := listDirectoryGroupsTx(tx, connectorID)
		if err != nil {
			return err
		}
		for index := range groups {
			item := groups[index]
			items = append(items, &item)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list directory groups: %w", err)
	}
	return items, nil
}

func (r *DirectoryIdentityRepositoryImpl) ReplaceDirectoryGroupMembers(ctx context.Context, connectorID, groupID uuid.UUID, memberIDs []uuid.UUID) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return replaceDirectoryGroupMembersTx(tx, connectorID, groupID, memberIDs)
	})
	if err != nil {
		return fmt.Errorf("failed to replace directory group members: %w", err)
	}
	return nil
}

func replaceDirectoryGroupMembersTx(tx *gorm.DB, connectorID, groupID uuid.UUID, memberIDs []uuid.UUID) error {
	memberIDs = uniqueDirectoryUUIDs(memberIDs)
	if _, err := getDirectoryGroupTx(tx, connectorID, groupID); err != nil {
		return err
	}
	if len(memberIDs) > 0 {
		var count int
		if err := tx.Raw(`
			SELECT count(*)
			FROM sso_directory_users
			WHERE connector_id = $1 AND id = ANY($2::uuid[])
		`, connectorID, pq.Array(memberIDs)).Row().Scan(&count); err != nil {
			return err
		}
		if count != len(memberIDs) {
			return fmt.Errorf("directory group members must belong to the connector")
		}
	}
	if err := tx.Exec(`
		DELETE FROM sso_directory_group_memberships
		WHERE connector_id = $1 AND group_id = $2
	`, connectorID, groupID).Error; err != nil {
		return err
	}
	for _, userID := range memberIDs {
		if err := tx.Exec(`
			INSERT INTO sso_directory_group_memberships (connector_id, group_id, user_id)
			VALUES ($1, $2, $3)
		`, connectorID, groupID, userID).Error; err != nil {
			return err
		}
	}
	return nil
}

func findDirectoryUserTx(tx *gorm.DB, connectorID, id uuid.UUID, externalID, userName string) (*domain.DirectoryUser, error) {
	where := ""
	args := []any{connectorID}
	switch {
	case id != uuid.Nil:
		where = "AND id = $2"
		args = append(args, id)
	case externalID != "":
		where = "AND external_id = $2"
		args = append(args, externalID)
	default:
		where = "AND lower(user_name) = lower($2)"
		args = append(args, userName)
	}
	rows, err := tx.Raw(`
		SELECT id, connector_id, external_id, user_name, email, display_name, active, identity_id, created_at, updated_at
		FROM sso_directory_users
		WHERE connector_id = $1 `+where+` FOR UPDATE
	`, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanDirectoryUser(rows)
}

func findDirectoryGroupTx(tx *gorm.DB, connectorID, id uuid.UUID, externalID string) (*domain.DirectoryGroup, error) {
	where := ""
	args := []any{connectorID}
	if id != uuid.Nil {
		where = "AND id = $2"
		args = append(args, id)
	} else if externalID != "" {
		where = "AND external_id = $2"
		args = append(args, externalID)
	} else {
		return nil, nil
	}
	rows, err := tx.Raw(`
		SELECT id, connector_id, external_id, display_name, active, created_at, updated_at
		FROM sso_directory_groups
		WHERE connector_id = $1 `+where+` FOR UPDATE
	`, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanDirectoryGroup(rows)
}

func getDirectoryGroupTx(tx *gorm.DB, connectorID, groupID uuid.UUID) (*domain.DirectoryGroup, error) {
	rows, err := tx.Raw(`
		SELECT id, connector_id, external_id, display_name, active, created_at, updated_at
		FROM sso_directory_groups
		WHERE connector_id = $1 AND id = $2
	`, connectorID, groupID).Rows()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		_ = rows.Close()
		return nil, rows.Err()
	}
	item, err := scanDirectoryGroup(rows)
	if err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	members, err := listDirectoryGroupMembersTx(tx, connectorID, groupID)
	if err != nil {
		return nil, err
	}
	item.Members = members
	return item, nil
}

func listDirectoryGroupMembersTx(tx *gorm.DB, connectorID, groupID uuid.UUID) ([]domain.DirectoryUser, error) {
	items := make([]domain.DirectoryUser, 0)
	rows, err := tx.Raw(`
		SELECT u.id, u.connector_id, u.external_id, u.user_name, u.email, u.display_name, u.active, u.identity_id, u.created_at, u.updated_at
		FROM sso_directory_group_memberships m
		JOIN sso_directory_users u
		  ON u.connector_id = m.connector_id AND u.id = m.user_id
		WHERE m.connector_id = $1 AND m.group_id = $2
		ORDER BY lower(u.user_name), u.id
	`, connectorID, groupID).Rows()
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

func directoryConnectorProviderIDTx(tx *gorm.DB, connectorID uuid.UUID) (uuid.UUID, error) {
	var providerID uuid.UUID
	if err := tx.Raw(`SELECT provider_id FROM sso_directory_connectors WHERE id = $1`, connectorID).Row().Scan(&providerID); err != nil {
		return uuid.Nil, err
	}
	return providerID, nil
}

func upsertDirectoryIdentityTx(tx *gorm.DB, providerID uuid.UUID, externalID, subject, email, displayName string, active bool) (uuid.UUID, error) {
	var externalMatch, subjectMatch uuid.UUID
	var found bool
	if externalID != "" {
		var err error
		externalMatch, found, err = findDirectoryIdentityIDTx(tx, providerID, "external_id", externalID)
		if err != nil {
			return uuid.Nil, err
		}
		if !found {
			externalMatch = uuid.Nil
		}
	}
	var err error
	subjectMatch, found, err = findDirectoryIdentityIDTx(tx, providerID, "subject", subject)
	if err != nil {
		return uuid.Nil, err
	}
	if !found {
		subjectMatch = uuid.Nil
	}
	if externalMatch != uuid.Nil && subjectMatch != uuid.Nil && externalMatch != subjectMatch {
		return uuid.Nil, fmt.Errorf("directory identity subject conflicts with a different external identity")
	}
	id := externalMatch
	if id == uuid.Nil {
		id = subjectMatch
	}
	now := time.Now().UTC()
	if id == uuid.Nil {
		id = uuid.New()
		if err := tx.Exec(`
			INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name, active, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		`, id, providerID, subject, externalID, email, displayName, active, now).Error; err != nil {
			return uuid.Nil, err
		}
		return id, nil
	}
	if err := tx.Exec(`
		UPDATE sso_identities
		SET subject = $1,
		    external_id = CASE WHEN $2 = '' THEN external_id ELSE $2 END,
		    email = $3,
		    display_name = $4,
		    active = $5,
		    updated_at = $6
		WHERE id = $7 AND provider_id = $8
	`, subject, externalID, email, displayName, active, now, id, providerID).Error; err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func findDirectoryIdentityIDTx(tx *gorm.DB, providerID uuid.UUID, column, value string) (uuid.UUID, bool, error) {
	var id uuid.UUID
	rows, err := tx.Raw(`
		SELECT id
		FROM sso_identities
		WHERE provider_id = $1 AND `+column+` = $2
		FOR UPDATE
	`, providerID, value).Rows()
	if err != nil {
		return uuid.Nil, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return uuid.Nil, false, rows.Err()
	}
	if err := rows.Scan(&id); err != nil {
		return uuid.Nil, false, err
	}
	return id, true, rows.Err()
}

func uniqueDirectoryUUIDs(values []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil {
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

func scanDirectoryUser(rows *sql.Rows) (*domain.DirectoryUser, error) {
	var item domain.DirectoryUser
	if err := rows.Scan(
		&item.ID,
		&item.ConnectorID,
		&item.ExternalID,
		&item.UserName,
		&item.Email,
		&item.DisplayName,
		&item.Active,
		&item.IdentityID,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &item, nil
}

func scanDirectoryGroup(rows *sql.Rows) (*domain.DirectoryGroup, error) {
	var item domain.DirectoryGroup
	if err := rows.Scan(
		&item.ID,
		&item.ConnectorID,
		&item.ExternalID,
		&item.DisplayName,
		&item.Active,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *DirectoryIdentityRepositoryImpl) DirectoryConnectorSnapshot(ctx context.Context, connectorID uuid.UUID) (*domain.DirectoryConnectorSnapshot, error) {
	var snapshot *domain.DirectoryConnectorSnapshot
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		connector, err := getDirectoryConnectorTx(tx, connectorID)
		if err != nil {
			return err
		}
		if connector == nil {
			return gorm.ErrRecordNotFound
		}
		groups, err := listDirectoryGroupsTx(tx, connectorID)
		if err != nil {
			return err
		}
		users, err := listDirectoryUsersTx(tx, connectorID)
		if err != nil {
			return err
		}
		bindings, err := listDirectoryBindingsTx(tx, connectorID)
		if err != nil {
			return err
		}
		manualMappings, err := listManualDirectoryMappingsTx(tx, connector.ProviderID)
		if err != nil {
			return err
		}
		teams, err := listDirectoryTeamsTx(tx)
		if err != nil {
			return err
		}
		snapshot = &domain.DirectoryConnectorSnapshot{
			Connector:      *connector,
			Groups:         groups,
			Users:          users,
			Bindings:       bindings,
			ManualMappings: manualMappings,
			Teams:          teams,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load directory connector snapshot: %w", err)
	}
	return snapshot, nil
}
