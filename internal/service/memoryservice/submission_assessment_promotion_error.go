package memoryservice

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func submissionPromotionFailureReason(err error) string {
	const prefix = "atomic_promotion_"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return prefix + "timeout"
	case errors.Is(err, context.Canceled):
		return prefix + "canceled"
	case errors.Is(err, repository.ErrSubmissionLeaseConflict):
		return prefix + "submission_lease_conflict"
	case errors.Is(err, repository.ErrSubmissionConflict):
		return prefix + "submission_conflict"
	case errors.Is(err, repository.ErrPlacementLeaseLost):
		return prefix + "placement_lease_lost"
	case errors.Is(err, repository.ErrPlacementStaleSource):
		return prefix + "placement_stale_source"
	case errors.Is(err, repository.ErrConflictContextStale):
		return prefix + "conflict_context_stale"
	case errors.Is(err, repository.ErrSourceRevisionConflict):
		return prefix + "source_revision_conflict"
	case errors.Is(err, repository.ErrIdempotencyConflict):
		return prefix + "idempotency_conflict"
	case errors.Is(err, repository.ErrSemanticIdempotencyConflict):
		return prefix + "semantic_idempotency_conflict"
	case errors.Is(err, repository.ErrTeamInactive):
		return prefix + "team_inactive"
	}

	var postgresErr *pgconn.PgError
	if errors.As(err, &postgresErr) && validPromotionSQLState(postgresErr.Code) {
		return prefix + "sqlstate_" + postgresErr.Code
	}
	return prefix + "unknown"
}

func validPromotionSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, character := range code {
		if (character < '0' || character > '9') && (character < 'A' || character > 'Z') {
			return false
		}
	}
	return true
}
