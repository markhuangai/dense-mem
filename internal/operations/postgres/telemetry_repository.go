package postgres

import (
	"context"
	"database/sql"
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

type TelemetryLifecycleRepository struct {
	db  *gorm.DB
	rls storagepostgres.RLSHelper
}

var _ operationscontract.TelemetryLifecycleReader = (*TelemetryLifecycleRepository)(nil)

func NewTelemetryLifecycleRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *TelemetryLifecycleRepository {
	return &TelemetryLifecycleRepository{db: db, rls: rls}
}

var _ operationscontract.OperationalTelemetryReader = (*TelemetryLifecycleRepository)(nil)

func (r *TelemetryLifecycleRepository) ReadOperationalTelemetry(ctx context.Context) (snapshot operationscontract.OperationalTelemetrySnapshot, err error) {
	if r == nil || r.db == nil || r.rls == nil {
		return snapshot, fmt.Errorf("operational telemetry reader is unavailable")
	}
	snapshot.DreamRuns = []operationscontract.DreamRunTelemetry{}
	snapshot.Hypotheses = []operationscontract.HypothesisTelemetry{}
	snapshot.Feedback = []operationscontract.WindowedTelemetryCount{}
	snapshot.RememberAttempts = []operationscontract.WindowedTelemetryCount{}
	snapshot.ConfirmedRelationships = []operationscontract.WindowedTelemetryCount{}
	snapshot.RelationshipTransitions = []operationscontract.WindowedTelemetryCount{}
	snapshot.RelationshipCorrections = []operationscontract.WindowedTelemetryCount{}
	snapshot.RelationshipsCurrent = []operationscontract.NamedTelemetryCount{}

	read := func(tx *gorm.DB) error {
		if err := scanOperationalTelemetryRows(ctx, tx, `
			WITH `+operationalTelemetryWindowCTE+`
			SELECT window_bounds.window_key, cycle.lane, cycle.status,
			       count(*)::double precision,
			       COALESCE(sum(cycle.attempt_count), 0)::double precision,
			       COALESCE(sum(cycle.input_count), 0)::double precision,
			       COALESCE(sum(cycle.evidence_targets), 0)::double precision,
			       COALESCE(sum(cycle.evaluated_evidence_targets), 0)::double precision,
			       COALESCE(sum(cycle.provider_proposals), 0)::double precision,
			       COALESCE(sum(cycle.created_hypotheses), 0)::double precision,
			       COALESCE(sum(cycle.rejected_hypotheses), 0)::double precision
			FROM dream_cycle_runs AS cycle
			CROSS JOIN window_bounds
			WHERE cycle.canonical_run_id IS NULL
			  AND cycle.started_at >= window_bounds.starts_at
			  AND `+activeSemanticSpaceGenerationSQL("cycle")+`
			GROUP BY window_bounds.window_key, cycle.lane, cycle.status
		`, func(rows *sql.Rows) error {
			var value operationscontract.DreamRunTelemetry
			if err := rows.Scan(&value.Window, &value.Lane, &value.Status, &value.Runs, &value.Attempts,
				&value.InputTargets, &value.EvidenceTargets, &value.EvaluatedTargets, &value.ProviderProposals,
				&value.CreatedHypotheses, &value.RejectedHypotheses); err != nil {
				return err
			}
			snapshot.DreamRuns = append(snapshot.DreamRuns, value)
			return nil
		}); err != nil {
			return err
		}

		if err := scanOperationalTelemetryRows(ctx, tx, `WITH `+operationalTelemetryActiveSpacesCTE+`
			SELECT hypothesis.lane, hypothesis.status, count(*)::double precision,
			       count(*) FILTER (WHERE hypothesis.status IN ('proposed', 'reinforced'))::double precision,
			       COALESCE(EXTRACT(EPOCH FROM (now() - MIN(hypothesis.created_at) FILTER (
			           WHERE hypothesis.status IN ('proposed', 'reinforced')
				       ))), 0)::double precision
			FROM hypotheses AS hypothesis
			JOIN active_semantic_spaces AS active_space
			  ON active_space.team_id = hypothesis.team_id
			 AND active_space.space_id = hypothesis.space_id
			 AND active_space.generation = hypothesis.space_generation
			WHERE hypothesis.canonical_hypothesis_id IS NULL
			GROUP BY hypothesis.lane, hypothesis.status
		`, func(rows *sql.Rows) error {
			var value operationscontract.HypothesisTelemetry
			if err := rows.Scan(&value.Lane, &value.Status, &value.Count, &value.BacklogCount, &value.OldestBacklogAge); err != nil {
				return err
			}
			snapshot.Hypotheses = append(snapshot.Hypotheses, value)
			return nil
		}); err != nil {
			return err
		}

		if err := scanOperationalTelemetryCounts(ctx, tx, `
			WITH `+operationalTelemetryWindowCTE+`
			SELECT window_bounds.window_key, feedback.decision, count(*)::double precision
			FROM hypothesis_feedback_events AS feedback
			CROSS JOIN window_bounds
			WHERE feedback.created_at >= window_bounds.starts_at
			  AND `+activeSemanticSpaceGenerationSQL("feedback")+`
			GROUP BY window_bounds.window_key, feedback.decision
		`, &snapshot.Feedback); err != nil {
			return err
		}
		if err := scanOperationalTelemetryCounts(ctx, tx, `
			WITH `+operationalTelemetryWindowCTE+`
			SELECT window_bounds.window_key, attempt.outcome, count(*)::double precision
			FROM remember_attempts AS attempt
			CROSS JOIN window_bounds
			WHERE attempt.created_at >= window_bounds.starts_at
			  AND `+activeSemanticSpaceGenerationSQL("attempt")+`
			GROUP BY window_bounds.window_key, attempt.outcome
		`, &snapshot.RememberAttempts); err != nil {
			return err
		}
		if err := scanOperationalTelemetryCounts(ctx, tx, `
			WITH `+operationalTelemetryWindowCTE+`
			SELECT window_bounds.window_key, relationship.status,
			       count(DISTINCT (relationship.team_id, relationship.relationship_id))::double precision
			FROM relationship_records AS relationship
			JOIN relationship_observations AS observation
			  ON observation.team_id = relationship.team_id
			 AND observation.relationship_id = relationship.relationship_id
			 AND observation.space_id = relationship.space_id
			 AND observation.space_generation = relationship.space_generation
			JOIN knowledge_ingests AS ingest
			  ON ingest.team_id = observation.team_id
			 AND ingest.ingest_id = observation.ingest_id
			 AND ingest.space_id = observation.space_id
			 AND ingest.space_generation = observation.space_generation
			 AND ingest.status = 'completed'
			JOIN hypothesis_feedback_events AS feedback
			  ON feedback.team_id = ingest.team_id
			 AND feedback.submitted_ingest_id = ingest.ingest_id
			 AND feedback.space_id = ingest.space_id
			 AND feedback.space_generation = ingest.space_generation
			 AND feedback.decision IN ('confirm_true', 'confirm_false', 'promote_candidate')
			CROSS JOIN window_bounds
			WHERE feedback.created_at >= window_bounds.starts_at
			  AND relationship.identity_alias_of_relationship_id IS NULL
			  AND `+activeSemanticSpaceGenerationSQL("relationship")+`
			GROUP BY window_bounds.window_key, relationship.status
		`, &snapshot.ConfirmedRelationships); err != nil {
			return err
		}
		if err := scanOperationalTelemetryCounts(ctx, tx, `
			WITH `+operationalTelemetryWindowCTE+`
			SELECT window_bounds.window_key, event.to_status, count(*)::double precision
			FROM relationship_transition_events AS event
			CROSS JOIN window_bounds
			WHERE event.created_at >= window_bounds.starts_at
			  AND `+activeSemanticSpaceGenerationSQL("event")+`
			GROUP BY window_bounds.window_key, event.to_status
		`, &snapshot.RelationshipTransitions); err != nil {
			return err
		}
		if err := scanOperationalTelemetryCounts(ctx, tx, `
			WITH `+operationalTelemetryWindowCTE+`
			SELECT window_bounds.window_key, 'corrections', count(*)::double precision
			FROM relationship_correction_events AS event
			CROSS JOIN window_bounds
			WHERE event.created_at >= window_bounds.starts_at
			  AND `+activeSemanticSpaceGenerationSQL("event")+`
			GROUP BY window_bounds.window_key
		`, &snapshot.RelationshipCorrections); err != nil {
			return err
		}
		return scanOperationalTelemetryRows(ctx, tx, `WITH `+operationalTelemetryActiveSpacesCTE+`
			SELECT relationship.status, count(*)::double precision
			FROM relationship_records AS relationship
			JOIN active_semantic_spaces AS active_space
			  ON active_space.team_id = relationship.team_id
			 AND active_space.space_id = relationship.space_id
			 AND active_space.generation = relationship.space_generation
			WHERE relationship.identity_alias_of_relationship_id IS NULL
			GROUP BY relationship.status
		`, func(rows *sql.Rows) error {
			var value operationscontract.NamedTelemetryCount
			if err := rows.Scan(&value.Kind, &value.Count); err != nil {
				return err
			}
			snapshot.RelationshipsCurrent = append(snapshot.RelationshipsCurrent, value)
			return nil
		})
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
