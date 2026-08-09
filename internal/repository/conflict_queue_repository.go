package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type ConflictQueueRepository interface {
	ListConflictQueue(context.Context, domain.ConflictQueueQuery) (*domain.ConflictQueuePage, error)
	CollectConflictQueueMetrics(context.Context) (domain.ConflictQueueMetricsSnapshot, error)
}

var _ ConflictQueueRepository = (*LedgerRepositoryImpl)(nil)

func (r *LedgerRepositoryImpl) ListConflictQueue(ctx context.Context, query domain.ConflictQueueQuery) (*domain.ConflictQueuePage, error) {
	if err := validateConflictQueueQuery(query); err != nil {
		return nil, err
	}
	var page *domain.ConflictQueuePage
	err := r.withTeamReadOnlyRepeatableTx(ctx, query.TeamID, func(tx *gorm.DB) error {
		collectedAt, err := transactionTimestamp(ctx, tx)
		if err != nil {
			return err
		}
		summary, err := loadConflictQueueSummary(ctx, tx, query.TeamID, collectedAt)
		if err != nil {
			return err
		}
		records, err := loadConflictQueuePageRecords(ctx, tx, query, query.Limit+1)
		if err != nil {
			return err
		}
		hasNext := len(records) > query.Limit
		if hasNext {
			records = records[:query.Limit]
		}
		items := make([]domain.ConflictQueueItem, 0, len(records))
		for _, record := range records {
			items = append(items, conflictQueueItemFromRecord(record, collectedAt))
		}
		var nextCursor *string
		if hasNext && len(items) > 0 {
			last := items[len(items)-1]
			cursor, cursorErr := domain.EncodeConflictQueueCursor(domain.ConflictQueueCursor{
				Version:      1,
				TeamID:       query.TeamID,
				StatusFilter: query.Status,
				Status:       last.Status,
				NextReviewAt: last.NextReviewAt,
				ConflictID:   last.ConflictID,
			})
			if cursorErr != nil {
				return cursorErr
			}
			nextCursor = &cursor
		}
		page = &domain.ConflictQueuePage{Summary: summary, Items: items, NextCursor: nextCursor}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("conflict queue: list: %w", err)
	}
	return page, nil
}

func (r *LedgerRepositoryImpl) CollectConflictQueueMetrics(ctx context.Context) (domain.ConflictQueueMetricsSnapshot, error) {
	var snapshot domain.ConflictQueueMetricsSnapshot
	err := r.withSystemReadOnlyRepeatableTx(ctx, func(tx *gorm.DB) error {
		collectedAt, err := transactionTimestamp(ctx, tx)
		if err != nil {
			return err
		}
		snapshot.CollectedAt = collectedAt
		teamIDs, err := activeConflictQueueTeams(ctx, tx)
		if err != nil {
			return err
		}
		snapshot.Cases, err = loadConflictQueueCaseMetrics(ctx, tx, teamIDs)
		if err != nil {
			return err
		}
		snapshot.OldestAges, err = loadConflictQueueOldestMetrics(ctx, tx, teamIDs, collectedAt)
		if err != nil {
			return err
		}
		snapshot.Leases, err = loadConflictQueueLeaseMetrics(ctx, tx, teamIDs, collectedAt)
		if err != nil {
			return err
		}
		snapshot.DerivedTasks, err = loadConflictQueueDerivedTaskMetrics(ctx, tx, teamIDs)
		return err
	})
	if err != nil {
		return domain.ConflictQueueMetricsSnapshot{}, fmt.Errorf("conflict queue: collect metrics: %w", err)
	}
	return snapshot, nil
}

func validateConflictQueueQuery(query domain.ConflictQueueQuery) error {
	if _, err := uuid.Parse(strings.TrimSpace(query.TeamID)); err != nil {
		return errors.New("team_id is required")
	}
	if query.Status != "" && query.Status != "open" && query.Status != "overdue" {
		return errors.New("status must be open or overdue")
	}
	if query.Limit < 1 || query.Limit > domain.ConflictQueueMaxLimit {
		return errors.New("limit must be between 1 and 100")
	}
	if query.Cursor != nil {
		if err := query.Cursor.ValidateScope(query.TeamID, query.Status); err != nil {
			return err
		}
	}
	return nil
}

func (r *LedgerRepositoryImpl) withTeamReadOnlyRepeatableTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("conflict queue: database is required")
	}
	if r.rls == nil {
		return errors.New("conflict queue: rls helper is required")
	}
	return r.rls.WithTeamReadOnlyRepeatableTx(ctx, r.db, teamID, fn)
}

func (r *LedgerRepositoryImpl) withSystemReadOnlyRepeatableTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("conflict queue: database is required")
	}
	if r.rls == nil {
		return errors.New("conflict queue: rls helper is required")
	}
	return r.rls.WithSystemReadOnlyRepeatableTx(ctx, r.db, fn)
}

func transactionTimestamp(ctx context.Context, tx *gorm.DB) (time.Time, error) {
	var collectedAt time.Time
	if err := tx.WithContext(ctx).Raw("SELECT transaction_timestamp()").Row().Scan(&collectedAt); err != nil {
		return time.Time{}, err
	}
	return collectedAt.UTC(), nil
}

func loadConflictQueueSummary(ctx context.Context, tx *gorm.DB, teamID string, collectedAt time.Time) (domain.ConflictQueueSummary, error) {
	var summary domain.ConflictQueueSummary
	var oldestOpen, oldestOverdue sql.NullInt64
	err := tx.WithContext(ctx).Raw(`
		SELECT
			COUNT(*) FILTER (WHERE status = 'open')::int,
			COUNT(*) FILTER (WHERE status = 'overdue')::int,
			COUNT(*) FILTER (WHERE lease_until IS NOT NULL AND lease_until > ?)::int,
			COUNT(*) FILTER (WHERE lease_until IS NOT NULL AND lease_until <= ?)::int,
			COALESCE(EXTRACT(EPOCH FROM (? - MIN(created_at) FILTER (WHERE status = 'open')))::bigint, 0),
			COALESCE(EXTRACT(EPOCH FROM (? - MIN(created_at) FILTER (WHERE status = 'overdue')))::bigint, 0)
		FROM relationship_conflict_cases
		WHERE team_id = ?::uuid
		  AND status IN ('open', 'overdue')
	`, collectedAt, collectedAt, collectedAt, collectedAt, teamID).Row().Scan(
		&summary.OpenCount,
		&summary.OverdueCount,
		&summary.ActiveLeaseCount,
		&summary.ExpiredLeaseCount,
		&oldestOpen,
		&oldestOverdue,
	)
	if err != nil {
		return domain.ConflictQueueSummary{}, err
	}
	if oldestOpen.Valid {
		summary.OldestOpenAgeSeconds = max(oldestOpen.Int64, int64(0))
	}
	if oldestOverdue.Valid {
		summary.OldestOverdueAgeSeconds = max(oldestOverdue.Int64, int64(0))
	}
	summary.FailedAssessmentCount24h, err = countFailedConflictAssessments24h(ctx, tx, teamID, collectedAt)
	if err != nil {
		return domain.ConflictQueueSummary{}, err
	}
	summary.LWWResolutionCount24h, err = countLWWResolutions24h(ctx, tx, teamID, collectedAt)
	if err != nil {
		return domain.ConflictQueueSummary{}, err
	}
	summary.PendingDerivedTaskCount, summary.FailedDerivedTaskCount, err = countConflictDerivedTasks(ctx, tx, teamID)
	if err != nil {
		return domain.ConflictQueueSummary{}, err
	}
	summary.CollectedAt = collectedAt
	return summary, nil
}

func countFailedConflictAssessments24h(ctx context.Context, tx *gorm.DB, teamID string, collectedAt time.Time) (int, error) {
	var count int
	err := tx.WithContext(ctx).Raw(`
		SELECT COUNT(*)::int
		FROM relationship_conflict_ai_assessment_events
		WHERE team_id = ?::uuid
		  AND action = 'failed'
		  AND created_at > (?::timestamptz - interval '24 hours')
		  AND created_at <= ?::timestamptz
	`, teamID, collectedAt, collectedAt).Row().Scan(&count)
	return count, err
}

func countLWWResolutions24h(ctx context.Context, tx *gorm.DB, teamID string, collectedAt time.Time) (int, error) {
	var count int
	err := tx.WithContext(ctx).Raw(`
		SELECT COUNT(*)::int
		FROM relationship_conflict_resolution_plans
		WHERE team_id = ?::uuid
		  AND method = 'last_write_wins'
		  AND status = 'applied'
		  AND applied_at > (?::timestamptz - interval '24 hours')
		  AND applied_at <= ?::timestamptz
	`, teamID, collectedAt, collectedAt).Row().Scan(&count)
	return count, err
}

func countConflictDerivedTasks(ctx context.Context, tx *gorm.DB, teamID string) (int, int, error) {
	var pending, failed int
	err := tx.WithContext(ctx).Raw(`
		SELECT
			COUNT(*) FILTER (WHERE NULLIF(btrim(last_failure_class), '') IS NULL)::int,
			COUNT(*) FILTER (WHERE NULLIF(btrim(last_failure_class), '') IS NOT NULL)::int
		FROM relationship_conflict_derived_evidence_tasks
		WHERE team_id = ?::uuid
		  AND status <> 'completed'
	`, teamID).Row().Scan(&pending, &failed)
	return pending, failed, err
}

type conflictQueueCaseRow struct {
	Record       RelationshipConflictCaseRecord
	LeaseUntil   *time.Time
	FailureClass string
}

func loadConflictQueuePageRecords(ctx context.Context, tx *gorm.DB, query domain.ConflictQueueQuery, rowLimit int) ([]conflictQueueCaseRow, error) {
	where := `
		WHERE team_id = ?::uuid
		  AND status IN ('open', 'overdue')
	`
	args := []any{query.TeamID}
	if query.Status != "" {
		where += " AND status = ?"
		args = append(args, query.Status)
	}
	if query.Cursor != nil {
		where += ` AND (
			status < ?
			OR (
				status = ?
				AND (next_review_at > ? OR (next_review_at = ? AND conflict_id > ?::uuid))
			)
		)`
		args = append(args, query.Cursor.Status, query.Cursor.Status, query.Cursor.NextReviewAt, query.Cursor.NextReviewAt, query.Cursor.ConflictID)
	}
	args = append(args, rowLimit)
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, conflict_id::text, semantic_scope_key, kind, status,
		       subject_entity_id::text, predicate_key, predicate_version,
		       relationship_kind, current_cardinality, polarity, COALESCE(scope_key, ''),
		       question, policy_version, review_due_at, next_review_at, review_ttl_days,
		       timezone, COALESCE(preferred_position_id::text, ''),
		       resolved_at, effective_at, effective_time_basis, resolution_reason,
		       version, attempts, created_at, updated_at,
		       lease_until,
		       COALESCE((
			   SELECT attempt.failure_class
			   FROM relationship_conflict_ai_assessment_attempts AS attempt
			   WHERE attempt.team_id = relationship_conflict_cases.team_id
			     AND attempt.conflict_id = relationship_conflict_cases.conflict_id
			     AND attempt.case_version = relationship_conflict_cases.version
			     AND attempt.status = 'failed'
			     AND NULLIF(btrim(attempt.failure_class), '') IS NOT NULL
			   ORDER BY COALESCE(attempt.completed_at, attempt.created_at) DESC, attempt.assessment_attempt_id DESC
			   LIMIT 1
		       ), '')
		FROM relationship_conflict_cases
	`+where+`
		ORDER BY relationship_conflict_cases.status DESC,
		         relationship_conflict_cases.next_review_at,
		         relationship_conflict_cases.conflict_id
		LIMIT ?
	`, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	raw := make([]conflictQueueCaseRow, 0, rowLimit)
	ids := make([]string, 0, rowLimit)
	for rows.Next() {
		var record RelationshipConflictCaseRecord
		var leaseUntil *time.Time
		var failureClass string
		if err := rows.Scan(
			&record.TeamID, &record.ConflictID, &record.SemanticScopeKey, &record.Kind, &record.Status,
			&record.SubjectEntityID, &record.PredicateKey, &record.PredicateVersion,
			&record.RelationshipKind, &record.CurrentCardinality, &record.Polarity, &record.ScopeKey,
			&record.Question, &record.PolicyVersion, &record.ReviewDueAt, &record.NextReviewAt,
			&record.ReviewTTLDays, &record.Timezone, &record.PreferredPositionID,
			&record.ResolvedAt, &record.EffectiveAt, &record.EffectiveTimeBasis, &record.ResolutionReason,
			&record.Version, &record.Attempts, &record.CreatedAt, &record.UpdatedAt,
			&leaseUntil, &failureClass,
		); err != nil {
			return nil, err
		}
		raw = append(raw, conflictQueueCaseRow{Record: record, LeaseUntil: leaseUntil, FailureClass: failureClass})
		ids = append(ids, record.ConflictID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return raw, nil
	}
	loaded, err := loadRelationshipConflictRecordsByIDBounded(ctx, tx, query.TeamID, ids, nil, domain.ConflictQueueMaxPositions, domain.ConflictQueueMaxSupporters)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]RelationshipConflictCaseRecord, len(loaded))
	for _, record := range loaded {
		byID[record.ConflictID] = record
	}
	for i := range raw {
		record, ok := byID[raw[i].Record.ConflictID]
		if !ok {
			return nil, fmt.Errorf("conflict queue: case %s was not available in the team snapshot", raw[i].Record.ConflictID)
		}
		raw[i].Record.Positions = record.Positions
	}
	return raw, nil
}

func conflictQueueItemFromRecord(row conflictQueueCaseRow, collectedAt time.Time) domain.ConflictQueueItem {
	question, questionTruncated := truncateConflictQueueText(row.Record.Question, 512)
	predicateKey, predicateKeyTruncated := truncateConflictQueueText(row.Record.PredicateKey, 256)
	item := domain.ConflictQueueItem{
		ConflictID:            row.Record.ConflictID,
		Version:               row.Record.Version,
		Status:                row.Record.Status,
		Question:              question,
		QuestionTruncated:     questionTruncated,
		PredicateKey:          predicateKey,
		PredicateKeyTruncated: predicateKeyTruncated,
		ReviewDueAt:           row.Record.ReviewDueAt,
		NextReviewAt:          row.Record.NextReviewAt,
		CreatedAt:             row.Record.CreatedAt,
		UpdatedAt:             row.Record.UpdatedAt,
		AttemptCount:          row.Record.Attempts,
		LeaseUntil:            row.LeaseUntil,
		LastFailureClass:      domain.NormalizeConflictQueueFailureClass(row.FailureClass),
		Positions:             make([]domain.ConflictQueuePosition, 0, len(row.Record.Positions)),
	}
	switch {
	case row.LeaseUntil == nil:
		item.LeaseState = "idle"
	case row.LeaseUntil.After(collectedAt):
		item.LeaseState = "active"
	default:
		item.LeaseState = "expired"
	}
	for _, position := range row.Record.Positions {
		positionKey, _ := truncateConflictQueueText(position.PositionKey, 256)
		queuePosition := domain.ConflictQueuePosition{
			PositionID:          position.PositionID,
			PositionKey:         positionKey,
			Disposition:         position.Disposition,
			SupporterCount:      position.SupporterCount,
			SupportersTruncated: position.SupportersTruncated,
			Supporters:          make([]domain.ConflictQueueSupporter, 0, len(position.Supporters)),
		}
		item.PositionsTruncated = item.PositionsTruncated || position.PositionsTruncated
		for _, supporter := range position.Supporters {
			profileName, _ := truncateConflictQueueText(supporter.ProfileName, 256)
			queuePosition.Supporters = append(queuePosition.Supporters, domain.ConflictQueueSupporter{
				ProfileID: supporter.ProfileID, ProfileName: profileName,
				StrongestAuthority: supporter.StrongestAuthority, AcceptedAt: supporter.AcceptedAt,
			})
		}
		item.Positions = append(item.Positions, queuePosition)
	}
	return item
}

func truncateConflictQueueText(value string, maxRunes int) (string, bool) {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value, false
	}
	runes := []rune(value)
	return string(runes[:maxRunes]), true
}

func activeConflictQueueTeams(ctx context.Context, tx *gorm.DB) ([]string, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH relevant_teams AS (
			SELECT team_id
			FROM relationship_conflict_cases
			WHERE status IN ('open', 'overdue')
			UNION
			SELECT team_id
			FROM relationship_conflict_derived_evidence_tasks
			WHERE status <> 'completed'
		)
		SELECT relevant.team_id::text
		FROM relevant_teams AS relevant
		JOIN teams AS team ON team.id = relevant.team_id
		WHERE team.status = 'active'
		  AND team.deleted_at IS NULL
		ORDER BY relevant.team_id
	`).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadConflictQueueCaseMetrics(ctx context.Context, tx *gorm.DB, teamIDs []string) ([]domain.ConflictQueueMetricCase, error) {
	counts := map[string]map[string]float64{}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, status, COUNT(*)::double precision
		FROM relationship_conflict_cases
		WHERE status IN ('open', 'overdue')
		GROUP BY team_id, status
	`).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var teamID, status string
		var count float64
		if err := rows.Scan(&teamID, &status, &count); err != nil {
			return nil, err
		}
		if counts[teamID] == nil {
			counts[teamID] = map[string]float64{}
		}
		counts[teamID][status] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.ConflictQueueMetricCase, 0, len(teamIDs)*2)
	for _, teamID := range teamIDs {
		for _, status := range []string{"open", "overdue"} {
			out = append(out, domain.ConflictQueueMetricCase{TeamID: teamID, Status: status, Value: counts[teamID][status]})
		}
	}
	return out, nil
}

func loadConflictQueueOldestMetrics(ctx context.Context, tx *gorm.DB, teamIDs []string, collectedAt time.Time) ([]domain.ConflictQueueMetricOldestAge, error) {
	values := map[string]map[string]float64{}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, status,
		       COALESCE(EXTRACT(EPOCH FROM (? - MIN(created_at))), 0)::double precision
		FROM relationship_conflict_cases
		WHERE status IN ('open', 'overdue')
		GROUP BY team_id, status
	`, collectedAt).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var teamID, status string
		var age float64
		if err := rows.Scan(&teamID, &status, &age); err != nil {
			return nil, err
		}
		if values[teamID] == nil {
			values[teamID] = map[string]float64{}
		}
		values[teamID][status] = max(age, float64(0))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.ConflictQueueMetricOldestAge, 0, len(teamIDs)*2)
	for _, teamID := range teamIDs {
		for _, status := range []string{"open", "overdue"} {
			out = append(out, domain.ConflictQueueMetricOldestAge{TeamID: teamID, Status: status, Value: values[teamID][status]})
		}
	}
	return out, nil
}

func loadConflictQueueLeaseMetrics(ctx context.Context, tx *gorm.DB, teamIDs []string, collectedAt time.Time) ([]domain.ConflictQueueMetricLease, error) {
	values := map[string]map[string]float64{}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text,
		       CASE
		           WHEN lease_until IS NULL THEN 'idle'
		           WHEN lease_until > ? THEN 'active'
		           ELSE 'expired'
		       END AS lease_state,
		       COUNT(*)::double precision
		FROM relationship_conflict_cases
		WHERE status IN ('open', 'overdue')
		GROUP BY team_id, lease_state
	`, collectedAt).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var teamID, state string
		var count float64
		if err := rows.Scan(&teamID, &state, &count); err != nil {
			return nil, err
		}
		if values[teamID] == nil {
			values[teamID] = map[string]float64{}
		}
		values[teamID][state] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.ConflictQueueMetricLease, 0, len(teamIDs)*3)
	for _, teamID := range teamIDs {
		for _, state := range []string{"idle", "active", "expired"} {
			out = append(out, domain.ConflictQueueMetricLease{TeamID: teamID, State: state, Value: values[teamID][state]})
		}
	}
	return out, nil
}

func loadConflictQueueDerivedTaskMetrics(ctx context.Context, tx *gorm.DB, teamIDs []string) ([]domain.ConflictQueueMetricDerivedTask, error) {
	values := map[string]map[string]float64{}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text,
		       CASE WHEN NULLIF(btrim(last_failure_class), '') IS NULL THEN 'pending' ELSE 'failed' END,
		       COUNT(*)::double precision
		FROM relationship_conflict_derived_evidence_tasks
		WHERE status <> 'completed'
		GROUP BY team_id, 2
	`).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var teamID, status string
		var count float64
		if err := rows.Scan(&teamID, &status, &count); err != nil {
			return nil, err
		}
		if values[teamID] == nil {
			values[teamID] = map[string]float64{}
		}
		values[teamID][status] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.ConflictQueueMetricDerivedTask, 0, len(teamIDs)*2)
	for _, teamID := range teamIDs {
		for _, status := range []string{"pending", "failed"} {
			out = append(out, domain.ConflictQueueMetricDerivedTask{TeamID: teamID, Status: status, Value: values[teamID][status]})
		}
	}
	return out, nil
}
