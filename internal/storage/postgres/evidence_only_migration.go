package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/pressly/goose/v3"
)

const (
	EvidenceOnlyMigrationInventorySHA256       = "b0d41120350071869ff9c3d77c6ddbb562341c41c06422f173b30cb4958df754"
	evidenceOnlyMigrationVersion         int64 = 20261110010000
	evidenceOnlyMigrationSQLName               = "20261110010000_evidence_only_memory.sql"
)

func newEvidenceOnlyMigration(source []byte) (*goose.Migration, error) {
	_, sections, ok := strings.Cut(string(source), "-- +goose Up")
	if !ok {
		return nil, errors.New("evidence-only migration: missing Up section")
	}
	up, down, ok := strings.Cut(sections, "-- +goose Down")
	if !ok {
		return nil, errors.New("evidence-only migration: missing Down section")
	}
	return goose.NewGoMigration(evidenceOnlyMigrationVersion,
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) (err error) {
			progress := MigrationProgressFromContext(ctx)
			if progress != nil {
				if err = progress.beginTransaction(ctx, tx); err != nil {
					return err
				}
				defer func() {
					if err != nil {
						s := progress.Snapshot()
						progress.Report(s.Phase, s.WorkUnit, "rolling_back", s.Metric, s.Unit, s.Completed, s.Total)
					}
				}()
			}
			analyzed := int64(1)
			if progress != nil {
				progress.Report("legal_hold_statistics", "private_memory_legal_holds", "started", "completed", "tables", nil, &analyzed)
			}
			// Unknown empty-table statistics can make every inventory row expand private lineage unnecessarily.
			if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '5s'; ANALYZE public.private_memory_legal_holds;`); err != nil {
				return fmt.Errorf("prepare Evidence-only legal-hold statistics: %w", err)
			}
			if progress != nil {
				progress.Report("legal_hold_statistics", "private_memory_legal_holds", "completed", "completed", "tables", &analyzed, &analyzed)
			}
			_, err = tx.ExecContext(ctx, up)
			return err
		}, Mode: goose.TransactionEnabled},
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, down)
			return err
		}, Mode: goose.TransactionEnabled}), nil
}
