package postgres

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// TelemetryLifecycleReader is the read-only port used by the telemetry
// application service for authoritative Relationship lifecycle counts.
type TelemetryLifecycleReader = operationscontract.TelemetryLifecycleReader
type TelemetryLifecycleFilter = operationscontract.TelemetryLifecycleFilter
type TelemetryLifecycleSnapshot = operationscontract.TelemetryLifecycleSnapshot

type TelemetryLifecycleRepository struct {
	db  *gorm.DB
	rls storagepostgres.RLSHelper
}

var _ operationscontract.TelemetryLifecycleReader = (*TelemetryLifecycleRepository)(nil)

func NewTelemetryLifecycleRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *TelemetryLifecycleRepository {
	return &TelemetryLifecycleRepository{db: db, rls: rls}
}

func (r *TelemetryLifecycleRepository) ReadTelemetryLifecycle(ctx context.Context, filter operationscontract.TelemetryLifecycleFilter, from, to time.Time) (snapshot operationscontract.TelemetryLifecycleSnapshot, err error) {
	snapshot.Transitions = make(map[string]float64)
	snapshot.Current = make(map[string]float64)
	if r == nil || r.db == nil || r.rls == nil {
		return snapshot, fmt.Errorf("telemetry lifecycle reader is unavailable")
	}
	if filter.ProfileID != nil && filter.TeamID == nil {
		return snapshot, fmt.Errorf("telemetry lifecycle profile scope requires a team")
	}

	read := func(tx *gorm.DB) error {
		transitionWhere, transitionArgs := telemetryLifecycleScopeClause(filter, "event")
		transitionQuery := "SELECT event.to_status, count(*) FROM relationship_transition_events AS event WHERE event.created_at >= ? AND event.created_at < ?" + transitionWhere + " AND " + activeSemanticSpaceGenerationSQL("event") + " GROUP BY event.to_status"
		rows, queryErr := tx.WithContext(ctx).Raw(transitionQuery, append([]any{from, to}, transitionArgs...)...).Rows()
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var status string
			var count int64
			if scanErr := rows.Scan(&status, &count); scanErr != nil {
				return scanErr
			}
			snapshot.Transitions[status] = float64(count)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}

		correctionWhere, correctionArgs := telemetryLifecycleScopeClause(filter, "event")
		correctionQuery := "SELECT count(*) FROM relationship_correction_events AS event WHERE event.created_at >= ? AND event.created_at < ?" + correctionWhere + " AND " + activeSemanticSpaceGenerationSQL("event")
		if queryErr := tx.WithContext(ctx).Raw(correctionQuery, append([]any{from, to}, correctionArgs...)...).Scan(&snapshot.Corrections).Error; queryErr != nil {
			return queryErr
		}

		currentWhere, currentArgs := telemetryLifecycleScopeClause(filter, "relationship")
		currentQuery := "SELECT relationship.status, count(*) FROM relationship_records AS relationship WHERE 1=1" + currentWhere + " AND " + activeSemanticSpaceGenerationSQL("relationship") + " GROUP BY relationship.status"
		rows, queryErr = tx.WithContext(ctx).Raw(currentQuery, currentArgs...).Rows()
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var status string
			var count int64
			if scanErr := rows.Scan(&status, &count); scanErr != nil {
				return scanErr
			}
			snapshot.Current[status] = float64(count)
		}
		return rows.Err()
	}

	switch {
	case filter.TeamID == nil:
		err = r.rls.WithSystemReadOnlyRepeatableTx(ctx, r.db, read)
	case filter.ProfileID == nil:
		err = r.rls.WithTeamReadOnlyRepeatableTx(ctx, r.db, filter.TeamID.String(), read)
	default:
		err = r.rls.WithTeamProfileReadOnlyRepeatableTx(ctx, r.db, filter.TeamID.String(), filter.ProfileID.String(), read)
	}
	return snapshot, err
}

func telemetryLifecycleScopeClause(filter operationscontract.TelemetryLifecycleFilter, alias string) (string, []any) {
	if filter.TeamID == nil {
		return "", nil
	}
	if filter.ProfileID == nil {
		return " AND " + alias + ".team_id = ?", []any{filter.TeamID.String()}
	}
	return " AND " + alias + ".team_id = ? AND " + alias + ".owner_profile_id = ?", []any{filter.TeamID.String(), filter.ProfileID.String()}
}

func activeSemanticSpaceGenerationSQL(alias string) string {
	return storagepostgres.ActiveSemanticSpaceGenerationSQL(alias)
}
