package repository

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func lockSSOProtectedResourceProviderSetTx(tx *gorm.DB) error {
	return tx.Exec(
		`SELECT pg_advisory_xact_lock(hashtext(?))`,
		"sso-protected-resource-provider-set",
	).Error
}

func enforceSSOProtectedResourceProfileLimitTx(tx *gorm.DB) error {
	var count int
	if err := tx.Raw(`
		SELECT count(*)
		FROM sso_providers
		WHERE enabled = true
		  AND retired_at IS NULL
		  AND protected_resource_config @> '{"enabled": true}'::jsonb
	`).Row().Scan(&count); err != nil {
		return err
	}
	if count > domain.OAuthProtectedResourceMaximumProfiles {
		return fmt.Errorf("%w: maximum %d", ErrSSOProtectedResourceProfileLimit, domain.OAuthProtectedResourceMaximumProfiles)
	}
	return nil
}
