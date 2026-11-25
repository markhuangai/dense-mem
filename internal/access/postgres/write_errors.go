package postgres

import (
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	contract "github.com/markhuangai/dense-mem/internal/access/contract"
)

func uniqueConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return pqErr.Constraint
	}
	return ""
}

func credentialWriteError(err error) error {
	switch uniqueConstraint(err) {
	case "idx_credentials_team_name_unique":
		return errors.Join(contract.ErrCredentialNameConflict, err)
	case "idx_credentials_owner_team_active_unique":
		return errors.Join(contract.ErrCredentialOwnerConflict, err)
	default:
		return err
	}
}

func oauthProviderWriteError(err error) error {
	if uniqueConstraint(err) == "idx_sso_providers_active_oauth_issuer_unique" {
		return errors.Join(contract.ErrOAuthIssuerConflict, err)
	}
	return err
}
