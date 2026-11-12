package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/pressly/goose/v3"
)

const (
	EvidenceOnlyMigrationInventorySHA256       = "4d4269d72e53f44fa47476c7cf2ed658fa270e65339256c73b2f73faabe494d2"
	evidenceOnlyMigrationVersion         int64 = 20261110010000
	evidenceOnlyMigrationSQLName               = "20261110010000_evidence_only_memory.sql"
	evidenceOnlyFingerprintBoundary            = "-- EVIDENCE_ONLY_REBIND_FINGERPRINTS"
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
	before, after, ok := strings.Cut(up, evidenceOnlyFingerprintBoundary)
	if !ok || strings.Contains(after, evidenceOnlyFingerprintBoundary) {
		return nil, errors.New("evidence-only migration: missing or duplicated fingerprint boundary")
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
			if _, err := tx.ExecContext(ctx, before); err != nil {
				return err
			}
			if err := rebindEvidenceOnlyFingerprints(ctx, tx); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, after)
			return err
		}, Mode: goose.TransactionEnabled},
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, down)
			return err
		}, Mode: goose.TransactionEnabled}), nil
}

func rebindEvidenceOnlyFingerprints(ctx context.Context, tx *sql.Tx) error {
	progress := MigrationProgressFromContext(ctx)
	var loaded, rebound, target int64
	report := func(phase, state, metric string, n, total *int64) {
		if progress != nil {
			progress.Report(phase, "ontology_record_revisions", state, metric, "records", n, total)
		}
	}
	report("fingerprint_load", "started", "counted", nil, nil)
	rows, err := tx.QueryContext(ctx, `SELECT revision.team_id::text,revision.shared_space_id::text,revision.space_generation,revision.body,
		EXISTS(SELECT 1 FROM evidence_only_rebind_records changed WHERE
		 (changed.team_id,changed.shared_space_id,changed.space_generation,changed.record_id,changed.version)=
		 (revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id,revision.version))
		FROM ontology_record_heads head JOIN ontology_record_revisions revision USING(team_id,shared_space_id,space_generation,record_id,version)
		ORDER BY revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id`)
	if err != nil {
		return err
	}
	type scope struct {
		team, space string
		generation  int64
	}
	type retainedRecord struct {
		scope
		record  ontology.Record
		changed bool
	}
	var records []retainedRecord
	catalogs := map[scope]map[string]ontology.Record{}
	for rows.Next() {
		var item retainedRecord
		var body []byte
		if err := rows.Scan(&item.team, &item.space, &item.generation, &body, &item.changed); err != nil {
			return errors.Join(err, rows.Close())
		}
		if err := json.Unmarshal(body, &item.record); err != nil {
			return errors.Join(err, rows.Close())
		}
		if catalogs[item.scope] == nil {
			catalogs[item.scope] = map[string]ontology.Record{}
		}
		catalogs[item.scope][item.record.ID] = item.record
		records = append(records, item)
		loaded++
		if item.changed {
			target++
		}
		report("fingerprint_load", "progress", "counted", &loaded, nil)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	report("fingerprint_load", "completed", "counted", &loaded, &loaded)
	report("fingerprint_rebind", "started", "rebound", nil, &target)
	for _, item := range records {
		if !item.changed {
			continue
		}
		fingerprint, err := ontology.RecordFingerprintWithAcceptedSources(item.record, catalogs[item.scope])
		if err != nil {
			return fmt.Errorf("evidence_only_migration_blocked: retained ontology fingerprint: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ontology_record_revisions SET fingerprint=$1,body=jsonb_set(body,'{fingerprint}',to_jsonb($1::text))
		 WHERE team_id=$2::uuid AND shared_space_id=$3::uuid AND space_generation=$4 AND record_id=$5::uuid AND version=$6`,
			fingerprint, item.team, item.space, item.generation, item.record.ID, item.record.Version); err != nil {
			return err
		}
		rebound++
		report("fingerprint_rebind", "progress", "rebound", &rebound, &target)
	}
	report("fingerprint_rebind", "completed", "rebound", &rebound, &target)
	_, err = tx.ExecContext(ctx, `SELECT set_config('app.evidence_only_rebound','true',true)`)
	return err
}
