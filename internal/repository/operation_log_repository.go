package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type OperationLogRepository interface {
	AppendBatch(ctx context.Context, logs []domain.OperationLog) error
	List(ctx context.Context, filter domain.OperationLogFilter) (*domain.OperationLogPage, error)
	PruneBefore(ctx context.Context, cutoff time.Time) error
}

type OperationLogRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ OperationLogRepository = (*OperationLogRepositoryImpl)(nil)

func NewOperationLogRepository(db *gorm.DB, rls postgres.RLSHelper) *OperationLogRepositoryImpl {
	return &OperationLogRepositoryImpl{db: db, rls: rls}
}

func (r *OperationLogRepositoryImpl) AppendBatch(ctx context.Context, logs []domain.OperationLog) error {
	if len(logs) == 0 {
		return nil
	}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		for _, entry := range logs {
			attrs, err := json.Marshal(nonNilMap(entry.Attrs))
			if err != nil {
				return err
			}
			if err := tx.Exec(`
				INSERT INTO operation_logs (
					timestamp, severity, severity_rank, message, source,
					team_id, profile_id, correlation_id, error, attrs
				) VALUES (
					$1, $2, $3, $4, $5,
					$6, $7, $8, $9, $10::jsonb
				)
			`,
				entry.Timestamp.UTC(),
				normalizeOperationLogSeverity(entry.Severity),
				entry.SeverityRank,
				entry.Message,
				entry.Source,
				uuidPtrValue(entry.TeamID),
				uuidPtrValue(entry.ProfileID),
				entry.CorrelationID,
				entry.Error,
				string(attrs),
			).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to append operation logs: %w", err)
	}
	return nil
}

func (r *OperationLogRepositoryImpl) List(ctx context.Context, filter domain.OperationLogFilter) (*domain.OperationLogPage, error) {
	normalized := normalizeOperationLogFilter(filter)
	var page domain.OperationLogPage
	severity := strings.ToUpper(strings.TrimSpace(normalized.Severity))

	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var total int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM operation_logs
			WHERE ($1 = '' OR severity = $1)
		`, severity).Scan(&total).Error; err != nil {
			return err
		}
		page.Total = total

		rows, err := tx.Raw(`
			SELECT
				id::text, timestamp, severity, severity_rank, message, source,
				team_id::text, profile_id::text, correlation_id, error, attrs
			FROM operation_logs
			WHERE ($1 = '' OR severity = $1)
			`+operationLogOrderClause(normalized)+`
			LIMIT $2 OFFSET $3
		`, severity, normalized.Limit, normalized.Offset).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()

		items := make([]domain.OperationLog, 0)
		for rows.Next() {
			entry, err := scanOperationLog(rows)
			if err != nil {
				return err
			}
			items = append(items, entry)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		page.Items = items
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list operation logs: %w", err)
	}
	return &page, nil
}

func (r *OperationLogRepositoryImpl) PruneBefore(ctx context.Context, cutoff time.Time) error {
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.Exec("DELETE FROM operation_logs WHERE timestamp < $1", cutoff.UTC()).Error
	})
	if err != nil {
		return fmt.Errorf("failed to prune operation logs: %w", err)
	}
	return nil
}

func (r *OperationLogRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls != nil {
		return r.rls.WithSystemTx(ctx, r.db, fn)
	}
	return r.db.WithContext(ctx).Transaction(fn)
}

func normalizeOperationLogFilter(filter domain.OperationLogFilter) domain.OperationLogFilter {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	filter.Sort = strings.ToLower(strings.TrimSpace(filter.Sort))
	if filter.Sort != "severity" {
		filter.Sort = "timestamp"
	}
	filter.Direction = strings.ToLower(strings.TrimSpace(filter.Direction))
	if filter.Direction != "asc" {
		filter.Direction = "desc"
	}
	filter.Severity = strings.ToUpper(strings.TrimSpace(filter.Severity))
	return filter
}

func operationLogOrderClause(filter domain.OperationLogFilter) string {
	direction := "DESC"
	if filter.Direction == "asc" {
		direction = "ASC"
	}
	if filter.Sort == "severity" {
		return "ORDER BY severity_rank " + direction + ", timestamp DESC, id DESC"
	}
	return "ORDER BY timestamp " + direction + ", id " + direction
}

func scanOperationLog(rows *sql.Rows) (domain.OperationLog, error) {
	var (
		entry        domain.OperationLog
		idRaw        string
		teamIDRaw    sql.NullString
		profileIDRaw sql.NullString
		attrsRaw     []byte
	)
	if err := rows.Scan(
		&idRaw,
		&entry.Timestamp,
		&entry.Severity,
		&entry.SeverityRank,
		&entry.Message,
		&entry.Source,
		&teamIDRaw,
		&profileIDRaw,
		&entry.CorrelationID,
		&entry.Error,
		&attrsRaw,
	); err != nil {
		return domain.OperationLog{}, err
	}
	parsedID, err := uuid.Parse(idRaw)
	if err != nil {
		return domain.OperationLog{}, fmt.Errorf("invalid operation_logs.id UUID: %w", err)
	}
	entry.ID = parsedID
	entry.TeamID = parseNullableUUID(teamIDRaw)
	entry.ProfileID = parseNullableUUID(profileIDRaw)
	entry.Attrs = map[string]any{}
	if len(attrsRaw) > 0 {
		if err := json.Unmarshal(attrsRaw, &entry.Attrs); err != nil {
			return domain.OperationLog{}, fmt.Errorf("invalid operation_logs.attrs JSON: %w", err)
		}
	}
	return entry, nil
}

func parseNullableUUID(raw sql.NullString) *uuid.UUID {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	parsed, err := uuid.Parse(raw.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func uuidPtrValue(id *uuid.UUID) any {
	if id == nil || *id == uuid.Nil {
		return nil
	}
	return id.String()
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func normalizeOperationLogSeverity(severity string) string {
	severity = strings.ToUpper(strings.TrimSpace(severity))
	if severity == "" {
		return "INFO"
	}
	return severity
}
