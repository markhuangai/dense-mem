package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// SkillPackImportRepository persists skill-pack import batches and their
// rollback ledger.
type SkillPackImportRepository interface {
	CreateImport(ctx context.Context, record domain.SkillPackImport) error
	UpdateImportStatus(ctx context.Context, teamID, importID, status string, appliedCount, skippedCount int, summary map[string]any) error
	MarkRolledBack(ctx context.Context, teamID, importID string) error
	GetImport(ctx context.Context, teamID, importID string) (*domain.SkillPackImport, error)
	AppendChange(ctx context.Context, change domain.SkillPackImportChange) error
	ListChanges(ctx context.Context, teamID, importID string) ([]domain.SkillPackImportChange, error)
}

type SkillPackImportRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ SkillPackImportRepository = (*SkillPackImportRepositoryImpl)(nil)

func NewSkillPackImportRepository(db *gorm.DB, rls postgres.RLSHelper) *SkillPackImportRepositoryImpl {
	return &SkillPackImportRepositoryImpl{db: db, rls: rls}
}

func (r *SkillPackImportRepositoryImpl) CreateImport(ctx context.Context, record domain.SkillPackImport) error {
	summaryJSON, err := encodeJSON(record.Summary)
	if err != nil {
		return fmt.Errorf("skill pack import create: encode summary: %w", err)
	}
	err = r.withTeamTx(ctx, record.TeamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO skill_pack_imports (
				import_id, team_id, artifact_hash, source_url, schema_version, name,
				mode, status, item_count, applied_count, skipped_count, summary,
				retention_expires_at, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, $9, $10, $11, $12::jsonb,
				$13, $14, $15
			)
		`,
			record.ImportID,
			record.TeamID,
			record.ArtifactHash,
			record.SourceURL,
			record.SchemaVersion,
			record.Name,
			record.Mode,
			record.Status,
			record.ItemCount,
			record.AppliedCount,
			record.SkippedCount,
			string(summaryJSON),
			record.RetentionExpiresAt,
			record.CreatedAt,
			record.UpdatedAt,
		).Error
	})
	if err != nil {
		return fmt.Errorf("skill pack import create: %w", err)
	}
	return nil
}

func (r *SkillPackImportRepositoryImpl) UpdateImportStatus(ctx context.Context, teamID, importID, status string, appliedCount, skippedCount int, summary map[string]any) error {
	summaryJSON, err := encodeJSON(summary)
	if err != nil {
		return fmt.Errorf("skill pack import update: encode summary: %w", err)
	}
	now := time.Now().UTC()
	err = r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE skill_pack_imports
			SET status = $3::varchar,
			    applied_count = $4,
			    skipped_count = $5,
			    summary = $6::jsonb,
			    completed_at = CASE WHEN $3::text IN ('applied', 'failed', 'needs_review') THEN $7 ELSE completed_at END,
			    updated_at = $7
			WHERE team_id = $1 AND import_id = $2
		`, teamID, importID, status, appliedCount, skippedCount, string(summaryJSON), now).Error
	})
	if err != nil {
		return fmt.Errorf("skill pack import update: %w", err)
	}
	return nil
}

func (r *SkillPackImportRepositoryImpl) MarkRolledBack(ctx context.Context, teamID, importID string) error {
	now := time.Now().UTC()
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE skill_pack_imports
			SET status = 'rolled_back',
			    rolled_back_at = $3,
			    updated_at = $3
			WHERE team_id = $1 AND import_id = $2
		`, teamID, importID, now).Error
	})
	if err != nil {
		return fmt.Errorf("skill pack import rollback mark: %w", err)
	}
	return nil
}

func (r *SkillPackImportRepositoryImpl) GetImport(ctx context.Context, teamID, importID string) (*domain.SkillPackImport, error) {
	var out *domain.SkillPackImport
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		row := tx.Raw(`
			SELECT import_id::text, team_id::text, artifact_hash, source_url,
			       schema_version, name, mode, status, item_count, applied_count,
			       skipped_count, summary::text, retention_expires_at,
			       created_at, updated_at, completed_at, rolled_back_at
			FROM skill_pack_imports
			WHERE team_id = $1 AND import_id = $2
		`, teamID, importID).Row()
		record, err := scanSkillPackImport(row)
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("skill pack import get: %w", err)
	}
	return out, nil
}

func (r *SkillPackImportRepositoryImpl) AppendChange(ctx context.Context, change domain.SkillPackImportChange) error {
	beforeJSON, err := encodeJSON(change.BeforeState)
	if err != nil {
		return fmt.Errorf("skill pack change append: encode before: %w", err)
	}
	afterJSON, err := encodeJSON(change.AfterState)
	if err != nil {
		return fmt.Errorf("skill pack change append: encode after: %w", err)
	}
	err = r.withTeamTx(ctx, change.TeamID, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO skill_pack_import_changes (
				change_id, import_id, team_id, entity_type, entity_id,
				action, before_state, after_state, created_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7::jsonb, $8::jsonb, $9
			)
		`,
			change.ChangeID,
			change.ImportID,
			change.TeamID,
			change.EntityType,
			change.EntityID,
			change.Action,
			string(beforeJSON),
			string(afterJSON),
			change.CreatedAt,
		).Error
	})
	if err != nil {
		return fmt.Errorf("skill pack change append: %w", err)
	}
	return nil
}

func (r *SkillPackImportRepositoryImpl) ListChanges(ctx context.Context, teamID, importID string) ([]domain.SkillPackImportChange, error) {
	var out []domain.SkillPackImportChange
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT change_id::text, import_id::text, team_id::text, entity_type,
			       entity_id, action, before_state::text, after_state::text, created_at
			FROM skill_pack_import_changes
			WHERE team_id = $1 AND import_id = $2
			ORDER BY created_at DESC, change_id DESC
		`, teamID, importID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			change, err := scanSkillPackChange(rows)
			if err != nil {
				return err
			}
			out = append(out, change)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("skill pack changes list: %w", err)
	}
	return out, nil
}

func (r *SkillPackImportRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if r.rls != nil {
		return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
	}
	return r.db.WithContext(ctx).Transaction(fn)
}

func encodeJSON(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSkillPackImport(row rowScanner) (*domain.SkillPackImport, error) {
	var (
		record      domain.SkillPackImport
		summaryRaw  string
		sourceURL   sql.NullString
		completedAt sql.NullTime
		rolledAt    sql.NullTime
	)
	if err := row.Scan(
		&record.ImportID,
		&record.TeamID,
		&record.ArtifactHash,
		&sourceURL,
		&record.SchemaVersion,
		&record.Name,
		&record.Mode,
		&record.Status,
		&record.ItemCount,
		&record.AppliedCount,
		&record.SkippedCount,
		&summaryRaw,
		&record.RetentionExpiresAt,
		&record.CreatedAt,
		&record.UpdatedAt,
		&completedAt,
		&rolledAt,
	); err != nil {
		return nil, err
	}
	if sourceURL.Valid {
		record.SourceURL = sourceURL.String
	}
	if completedAt.Valid {
		t := completedAt.Time
		record.CompletedAt = &t
	}
	if rolledAt.Valid {
		t := rolledAt.Time
		record.RolledBackAt = &t
	}
	record.Summary = map[string]any{}
	if summaryRaw != "" {
		if err := json.Unmarshal([]byte(summaryRaw), &record.Summary); err != nil {
			return nil, err
		}
	}
	return &record, nil
}

func scanSkillPackChange(row rowScanner) (domain.SkillPackImportChange, error) {
	var (
		change    domain.SkillPackImportChange
		beforeRaw string
		afterRaw  string
	)
	if err := row.Scan(
		&change.ChangeID,
		&change.ImportID,
		&change.TeamID,
		&change.EntityType,
		&change.EntityID,
		&change.Action,
		&beforeRaw,
		&afterRaw,
		&change.CreatedAt,
	); err != nil {
		return change, err
	}
	change.BeforeState = map[string]any{}
	if beforeRaw != "" {
		if err := json.Unmarshal([]byte(beforeRaw), &change.BeforeState); err != nil {
			return change, err
		}
	}
	change.AfterState = map[string]any{}
	if afterRaw != "" {
		if err := json.Unmarshal([]byte(afterRaw), &change.AfterState); err != nil {
			return change, err
		}
	}
	return change, nil
}
