package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TelemetryLifecycleReader is the read-only port used by the telemetry
// application service for authoritative Relationship lifecycle counts.
type TelemetryLifecycleReader interface {
	ReadTelemetryLifecycle(ctx context.Context, filter TelemetryLifecycleFilter, from, to time.Time) (TelemetryLifecycleSnapshot, error)
}

type TelemetryLifecycleFilter struct {
	TeamID    *uuid.UUID
	ProfileID *uuid.UUID
}

type TelemetryLifecycleSnapshot struct {
	Transitions map[string]float64
	Corrections float64
	Current     map[string]float64
}

func (r *LedgerRepositoryImpl) ReadTelemetryLifecycle(ctx context.Context, filter TelemetryLifecycleFilter, from, to time.Time) (snapshot TelemetryLifecycleSnapshot, err error) {
	snapshot.Transitions = make(map[string]float64)
	snapshot.Current = make(map[string]float64)
	if r == nil || r.db == nil || r.rls == nil {
		return snapshot, fmt.Errorf("telemetry lifecycle reader is unavailable")
	}
	if filter.ProfileID != nil && filter.TeamID == nil {
		return snapshot, fmt.Errorf("telemetry lifecycle profile scope requires a team")
	}

	read := func(tx *gorm.DB) error {
		where, args := telemetryLifecycleScopeClause(filter)
		transitionQuery := "SELECT to_status, count(*) FROM relationship_transition_events WHERE created_at >= ? AND created_at < ?" + where + " GROUP BY to_status"
		rows, queryErr := tx.WithContext(ctx).Raw(transitionQuery, append([]any{from, to}, args...)...).Rows()
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

		correctionQuery := "SELECT count(*) FROM relationship_correction_events WHERE created_at >= ? AND created_at < ?" + where
		if queryErr := tx.WithContext(ctx).Raw(correctionQuery, append([]any{from, to}, args...)...).Scan(&snapshot.Corrections).Error; queryErr != nil {
			return queryErr
		}

		currentQuery := "SELECT status, count(*) FROM relationship_records WHERE 1=1" + where + " GROUP BY status"
		rows, queryErr = tx.WithContext(ctx).Raw(currentQuery, args...).Rows()
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

func telemetryLifecycleScopeClause(filter TelemetryLifecycleFilter) (string, []any) {
	if filter.TeamID == nil {
		return "", nil
	}
	if filter.ProfileID == nil {
		return " AND team_id = ?", []any{filter.TeamID.String()}
	}
	return " AND team_id = ? AND owner_profile_id = ?", []any{filter.TeamID.String(), filter.ProfileID.String()}
}
