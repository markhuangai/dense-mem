package testfixture

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed legacy_feature_storage.sql
var legacyFeatureStorage string

// InstallLegacyOntologyStorage lets current policy seed real pre-cutover fixtures without a runtime storage fallback.
func InstallLegacyOntologyStorage(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, legacyFeatureStorage)
	return err
}
