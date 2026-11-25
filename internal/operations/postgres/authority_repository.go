package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type AuthorityRepository struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

func NewAuthorityRepository(db *gorm.DB, rls postgres.RLSHelper) *AuthorityRepository {
	return &AuthorityRepository{db: db, rls: rls}
}

func (r *AuthorityRepository) GetLatestMarker(ctx context.Context) (*domain.CompatibilityMarker, error) {
	var out *domain.CompatibilityMarker
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		row := tx.Raw(`
			SELECT marker_id::text, marker_kind, version, status,
			       COALESCE(run_id::text, ''), corpus_hash, gate_report_hash,
			       metadata::text, created_at
			FROM v2_compatibility_markers
			WHERE marker_kind = ?
			ORDER BY created_at DESC, marker_id DESC
			LIMIT 1
		`, domain.MigrationMarkerKindCutover).Row()
		record, err := scanCompatibilityMarker(row)
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		if isAuthorityRowNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("authority repository: get marker: %w", err)
	}
	return out, nil
}

func (r *AuthorityRepository) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls == nil {
		return r.db.WithContext(ctx).Transaction(fn)
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

type authorityRowScanner interface {
	Scan(dest ...any) error
}

func scanCompatibilityMarker(row authorityRowScanner) (*domain.CompatibilityMarker, error) {
	var record domain.CompatibilityMarker
	var metadataJSON string
	if err := row.Scan(
		&record.MarkerID,
		&record.MarkerKind,
		&record.Version,
		&record.Status,
		&record.RunID,
		&record.CorpusHash,
		&record.GateReportHash,
		&metadataJSON,
		&record.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := unmarshalAuthorityJSON(metadataJSON, &record.Metadata); err != nil {
		return nil, err
	}
	return &record, nil
}

func unmarshalAuthorityJSON(data string, target *map[string]any) error {
	if data == "" {
		*target = map[string]any{}
		return nil
	}
	if err := json.Unmarshal([]byte(data), target); err != nil {
		return fmt.Errorf("authority repository: decode json: %w", err)
	}
	if *target == nil {
		*target = map[string]any{}
	}
	return nil
}

func isAuthorityRowNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}
