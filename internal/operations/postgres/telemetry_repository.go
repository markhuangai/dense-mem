package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"gorm.io/gorm"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type OperationalTelemetrySnapshot = operationscontract.OperationalTelemetrySnapshot

const operationalTelemetryWindowCTE = `window_bounds(window_key, starts_at) AS (
	VALUES
		('15m', now() - interval '15 minutes'),
		('30m', now() - interval '30 minutes'),
		('1h', now() - interval '1 hour'),
		('12h', now() - interval '12 hours'),
		('1d', now() - interval '1 day'),
		('7d', now() - interval '7 days'),
		('30d', now() - interval '30 days')
)`

const operationalTelemetryActiveSpacesCTE = `active_semantic_spaces AS MATERIALIZED (
	SELECT team_id, id AS space_id, generation
	FROM memory_spaces
	WHERE lifecycle_state = 'active'
)`

const operationalTelemetryWindowedCTEs = operationalTelemetryWindowCTE + `,
window_floor AS MATERIALIZED (
	SELECT min(starts_at) AS starts_at
	FROM window_bounds
), ` + operationalTelemetryActiveSpacesCTE

const operationalTelemetryRecentRememberAttemptsCTE = `recent_remember_attempts AS MATERIALIZED (
	SELECT team_id, space_id, space_generation, outcome, created_at
	FROM remember_attempts
	WHERE created_at >= (SELECT starts_at FROM window_floor)
)`

type TelemetryLifecycleRepository struct {
	db  *gorm.DB
	rls storagepostgres.RLSHelper
}

func NewTelemetryLifecycleRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *TelemetryLifecycleRepository {
	return &TelemetryLifecycleRepository{db: db, rls: rls}
}

var _ operationscontract.OperationalTelemetryReader = (*TelemetryLifecycleRepository)(nil)

func (r *TelemetryLifecycleRepository) ReadOperationalTelemetry(ctx context.Context) (snapshot operationscontract.OperationalTelemetrySnapshot, err error) {
	if r == nil || r.db == nil || r.rls == nil {
		return snapshot, fmt.Errorf("operational telemetry reader is unavailable")
	}
	snapshot.RememberAttempts = []operationscontract.WindowedTelemetryCount{}

	read := func(tx *gorm.DB) error {
		return scanOperationalTelemetryCounts(ctx, tx, `
		 WITH `+operationalTelemetryWindowedCTEs+`, `+operationalTelemetryRecentRememberAttemptsCTE+`
		 SELECT window_bounds.window_key, attempt.outcome, count(*)::double precision
		 FROM recent_remember_attempts AS attempt
		 JOIN active_semantic_spaces AS active_space ON active_space.team_id=attempt.team_id
		 AND active_space.space_id=attempt.space_id AND active_space.generation=attempt.space_generation
		 CROSS JOIN window_bounds WHERE attempt.created_at>=window_bounds.starts_at
		 GROUP BY window_bounds.window_key,attempt.outcome`, &snapshot.RememberAttempts)
	}

	err = r.rls.WithSystemReadOnlyRepeatableTx(ctx, r.db, read)
	return snapshot, err
}

func scanOperationalTelemetryRows(ctx context.Context, tx *gorm.DB, query string, scan func(*sql.Rows) error) error {
	rows, err := tx.WithContext(ctx).Raw(query).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func scanOperationalTelemetryCounts(ctx context.Context, tx *gorm.DB, query string, counts *[]operationscontract.WindowedTelemetryCount) error {
	return scanOperationalTelemetryRows(ctx, tx, query, func(rows *sql.Rows) error {
		var value operationscontract.WindowedTelemetryCount
		if err := rows.Scan(&value.Window, &value.Kind, &value.Count); err != nil {
			return err
		}
		*counts = append(*counts, value)
		return nil
	})
}
