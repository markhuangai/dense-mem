package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lib/pq"
	"hash"
	"slices"

	privacy "github.com/markhuangai/dense-mem/internal/privacy/contract"
	"gorm.io/gorm"
)

const privateMemoryPreflightMappingLimit = 1000

var _ privacy.PrivateMemoryMigrationInspector = (*PrivateMemoryRepositoryImpl)(nil)

func (r *PrivateMemoryRepositoryImpl) InspectPrivateMemoryMigration(ctx context.Context) (*privacy.PrivateMemoryMigrationReport, error) {
	if r == nil || r.db == nil || r.rls == nil {
		return nil, fmt.Errorf("%w: repository is unavailable", privacy.ErrPrivateMemoryInternal)
	}
	var report *privacy.PrivateMemoryMigrationReport
	err := r.rls.WithSystemReadOnlyRepeatableTx(ctx, r.db, func(tx *gorm.DB) error {
		var prepared bool
		if err := tx.Raw(`SELECT to_regclass('public.private_memory_migration_state') IS NOT NULL`).Row().Scan(&prepared); err != nil {
			return err
		}
		if prepared {
			var phase, fingerprint string
			var raw []byte
			if err := tx.Raw(`SELECT phase,report,COALESCE(source_fingerprint,'') FROM private_memory_migration_state WHERE singleton`).Row().Scan(&phase, &raw, &fingerprint); err != nil {
				return err
			}
			if phase != "pending" && (phase != "ready" || fingerprint != "") {
				report = &privacy.PrivateMemoryMigrationReport{PlannerVersion: privacy.PrivateMemoryPlannerVersion, Tables: []privacy.PrivateMemoryMigrationTable{}, Mappings: []privacy.PrivateMemoryWriterMapping{}, Blockers: []string{}}
				if err := json.Unmarshal(raw, report); err != nil {
					return err
				}
				report.Status = phase
				report.SourceFingerprint = fingerprint
				if phase != "complete" {
					report.Blockers = append(report.Blockers, "migration_in_progress:resume_with_the_retained_source_fingerprint")
				}
				return nil
			}
		}
		plan, err := planPrivateMemoryMigration(ctx, tx)
		if err != nil {
			return err
		}
		report = plan.Report
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect private memory migration: %w", err)
	}
	if len(report.Blockers) > 0 {
		return report, privacy.ErrPrivateMemoryMigrationBlocked
	}
	return report, nil
}

func hashPrivateMemoryRows(tx *gorm.DB, fingerprint hash.Hash, table, query string, args ...any) (int64, error) {
	rows, err := tx.Raw(query, args...).Rows()
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	_, _ = fmt.Fprintln(fingerprint, table)
	var count int64
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return 0, err
		}
		_, _ = fmt.Fprintln(fingerprint, digest)
		count++
	}
	return count, rows.Err()
}

func hashPrivateMigrationSchema(tx *gorm.DB, fingerprint hash.Hash, tables map[string]*privateMigrationTable) error {
	for _, name := range sortedMigrationKeys(tables) {
		table := tables[name]
		fmt.Fprintln(fingerprint, "schema:"+name)
		for _, column := range table.Columns {
			if slices.Contains([]string{"remember_attempt_events", "remember_attempt_diagnostics", "remember_failure_artifacts", "semantic_assessments"}, name) && slices.Contains([]string{"space_id", "space_generation"}, column.Name) {
				continue
			}
			fmt.Fprintf(fingerprint, "%s:%s:%t\n", column.Name, column.Type, column.Generated)
		}
		fmt.Fprintln(fingerprint, table.Key)
		for _, fk := range table.ForeignKeys {
			if slices.Contains([]string{"remember_attempt_events", "remember_attempt_diagnostics", "remember_failure_artifacts", "semantic_assessments"}, name) && slices.Contains(fk.Columns, "space_id") {
				continue
			}
			fmt.Fprintf(fingerprint, "%s:%s\n", fk.Name, fk.Definition)
		}
	}
	_, err := hashPrivateMemoryRows(tx, fingerprint, "schema_constraints", `SELECT c.relname||':'||k.conname||':'||pg_get_constraintdef(k.oid) FROM pg_constraint k
      JOIN pg_class c ON c.oid=k.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace
      WHERE n.nspname='public' AND c.relname=ANY(?::text[]) AND k.contype IN ('u','c')
       AND k.conname<>'value_records_canonical_unique' ORDER BY c.relname,k.conname`, pq.Array(privateMemoryMigrationTables()))
	return err
}
