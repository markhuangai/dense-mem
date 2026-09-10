package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	accesscontract "github.com/markhuangai/dense-mem/internal/access/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func ensureActiveTeamForMutation(ctx context.Context, tx *gorm.DB, teamID string) error {
	return storagepostgres.EnsureActiveTeamForMutation(ctx, tx, teamID)
}

func isPostgresUniqueConstraint(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

var (
	ErrDirectoryIdentityNotProvisioned  = accesscontract.ErrDirectoryIdentityNotProvisioned
	ErrDirectoryManagedMapping          = accesscontract.ErrDirectoryManagedMapping
	ErrDirectoryResourceConflict        = accesscontract.ErrDirectoryResourceConflict
	ErrDirectoryInvalidValue            = accesscontract.ErrDirectoryInvalidValue
	ErrDirectoryReconcileStale          = accesscontract.ErrDirectoryReconcileStale
	ErrSSOIdentityConflict              = accesscontract.ErrSSOIdentityConflict
	ErrSSOProtectedResourceProfileLimit = accesscontract.ErrSSOProtectedResourceProfileLimit
)
