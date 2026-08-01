package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func directoryConnectorProvisioningTx(tx *gorm.DB, connectorID uuid.UUID, forUpdate ...bool) (uuid.UUID, domain.DirectoryConnectorStatus, error) {
	lockClause := ""
	if len(forUpdate) > 0 && forUpdate[0] {
		lockClause = " FOR UPDATE"
	}
	var providerID uuid.UUID
	var status string
	if err := tx.Raw(`SELECT provider_id, status FROM sso_directory_connectors WHERE id = $1`+lockClause, connectorID).Row().Scan(&providerID, &status); err != nil {
		return uuid.Nil, "", err
	}
	return providerID, domain.DirectoryConnectorStatus(status), nil
}

func bumpDirectoryConnectorReconcileVersionTx(tx *gorm.DB, connectorID uuid.UUID, now time.Time) error {
	res := tx.Exec(`
		UPDATE sso_directory_connectors
		SET reconcile_version = reconcile_version + 1,
		    updated_at = $1
		WHERE id = $2
	`, now, connectorID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func upsertDirectoryIdentityTx(tx *gorm.DB, providerID uuid.UUID, externalID, subject, email, displayName string, active, synchronizeActive bool) (uuid.UUID, error) {
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
		return uuid.Nil, fmt.Errorf("%w: subject conflicts with a different external identity", ErrDirectoryResourceConflict)
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
		`, id, providerID, subject, externalID, email, displayName, active && synchronizeActive, now).Error; err != nil {
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
		    active = CASE WHEN $5 THEN $6 ELSE active END,
		    updated_at = $7
		WHERE id = $8 AND provider_id = $9
	`, subject, externalID, email, displayName, synchronizeActive, active, now, id, providerID).Error; err != nil {
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

const directoryIdentityPageMaxResults = 100

func normalizeDirectoryPageRequest(request domain.DirectoryPageRequest) (domain.DirectoryPageRequest, error) {
	if request.Offset < 0 || request.Limit < 0 || request.Limit > directoryIdentityPageMaxResults {
		return domain.DirectoryPageRequest{}, fmt.Errorf("%w: directory page bounds are invalid", ErrDirectoryInvalidValue)
	}
	request.FilterField = strings.TrimSpace(request.FilterField)
	request.FilterValue = strings.TrimSpace(request.FilterValue)
	return request, nil
}

func directoryUserPageFilter(request domain.DirectoryPageRequest) (string, []any, error) {
	switch request.FilterField {
	case "":
		return "", nil, nil
	case "userName":
		return " AND lower(user_name) = lower($2)", []any{request.FilterValue}, nil
	case "externalId":
		return " AND external_id = $2", []any{request.FilterValue}, nil
	case "id":
		id, err := uuid.Parse(request.FilterValue)
		if err != nil {
			return "", nil, fmt.Errorf("%w: directory user id filter is invalid", ErrDirectoryInvalidValue)
		}
		return " AND id = $2", []any{id}, nil
	default:
		return "", nil, fmt.Errorf("%w: directory user filter is invalid", ErrDirectoryInvalidValue)
	}
}

func directoryGroupPageFilter(request domain.DirectoryPageRequest) (string, []any, error) {
	switch request.FilterField {
	case "":
		return "", nil, nil
	case "displayName":
		return " AND lower(display_name) = lower($2)", []any{request.FilterValue}, nil
	case "externalId":
		return " AND external_id = $2", []any{request.FilterValue}, nil
	case "id":
		id, err := uuid.Parse(request.FilterValue)
		if err != nil {
			return "", nil, fmt.Errorf("%w: directory group id filter is invalid", ErrDirectoryInvalidValue)
		}
		return " AND id = $2", []any{id}, nil
	default:
		return "", nil, fmt.Errorf("%w: directory group filter is invalid", ErrDirectoryInvalidValue)
	}
}

func translateDirectoryUserWriteError(err error) error {
	if isPostgresUniqueConstraint(err, "idx_sso_directory_users_connector_external_id_unique") ||
		isPostgresUniqueConstraint(err, "idx_sso_directory_users_connector_username_unique") {
		return fmt.Errorf("%w: directory user already exists", ErrDirectoryResourceConflict)
	}
	return err
}

func translateDirectoryGroupWriteError(err error) error {
	if isPostgresUniqueConstraint(err, "idx_sso_directory_groups_connector_external_id_unique") {
		return fmt.Errorf("%w: directory group already exists", ErrDirectoryResourceConflict)
	}
	return err
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
