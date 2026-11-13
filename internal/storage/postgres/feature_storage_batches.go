package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

func (m *featureMigration) scan(ctx context.Context, phase string, source featureSource, copyPayload bool) error {
	var cp featureCheckpoint
	if err := m.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		cp, err = readFeatureCheckpoint(ctx, tx, phase, source.Name)
		return err
	}); err != nil {
		return err
	}
	m.report(phase, source.Name, "started", cp)
	if cp.Rows > 0 {
		if err := m.verifyPrefix(ctx, source, cp, phase == "verify"); err != nil {
			return err
		}
		m.report(phase, source.Name, "resumed", cp)
	}
	if cp.Complete {
		return m.verifyScan(ctx, phase, source, cp)
	}
	for {
		err := m.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
			rows, err := readFeatureBatch(ctx, tx, source, cp.Cursor)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if copyPayload {
					if err := copyFeaturePayload(ctx, tx, source.Name, row.Body); err != nil {
						return err
					}
				}
				if phase == "verify" {
					if err := verifyFeaturePayload(ctx, tx, source.Name, row.Body); err != nil {
						return err
					}
				}
				cp.Fingerprint = advanceFeatureFingerprint(cp.Fingerprint, row.Body)
				cp.Cursor = row.Cursor
				cp.Rows++
			}
			cp.Batches++
			cp.Complete = len(rows) == 0
			if phase == "verify" && cp.Complete {
				if err := compareFeatureSnapshot(ctx, tx, source.Name, cp); err != nil {
					return err
				}
			}
			return saveFeatureCheckpoint(ctx, tx, phase, source.Name, cp)
		})
		if err != nil {
			return err
		}
		state := "batch_committed"
		if cp.Complete {
			state = "completed"
		}
		m.report(phase, source.Name, state, cp)
		if cp.Complete {
			return nil
		}
	}
}

func (m *featureMigration) verifyPrefix(ctx context.Context, source featureSource, expected featureCheckpoint, verifyPayload bool) error {
	current := featureCheckpoint{Cursor: json.RawMessage(`[]`)}
	for current.Rows < expected.Rows {
		err := m.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
			rows, err := readFeatureBatch(ctx, tx, source, current.Cursor)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return errors.New("feature_storage_blocked: source fingerprint mismatch")
			}
			for _, row := range rows {
				if current.Rows == expected.Rows {
					break
				}
				if verifyPayload {
					if err := verifyFeaturePayload(ctx, tx, source.Name, row.Body); err != nil {
						return err
					}
				}
				current.Fingerprint = advanceFeatureFingerprint(current.Fingerprint, row.Body)
				current.Cursor = row.Cursor
				current.Rows++
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if current.Fingerprint != expected.Fingerprint || !bytes.Equal(current.Cursor, expected.Cursor) {
		if m.progress != nil {
			m.progress.FeatureFingerprintMismatch(expected.Fingerprint, current.Fingerprint)
		}
		return errors.New("feature_storage_blocked: source fingerprint mismatch")
	}
	return nil
}

func (m *featureMigration) verifyScan(ctx context.Context, phase string, source featureSource, cp featureCheckpoint) error {
	return m.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := readFeatureBatch(ctx, tx, source, cp.Cursor)
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			return errors.New("feature_storage_blocked: source fingerprint mismatch")
		}
		if phase == "verify" {
			return compareFeatureSnapshot(ctx, tx, source.Name, cp)
		}
		return nil
	})
}

func compareFeatureSnapshot(ctx context.Context, tx *sql.Tx, table string, actual featureCheckpoint) error {
	expected, err := readFeatureCheckpoint(ctx, tx, "snapshot", table)
	if err != nil {
		return err
	}
	if !expected.Complete || expected.Rows != actual.Rows || expected.Fingerprint != actual.Fingerprint {
		if p := MigrationProgressFromContext(ctx); p != nil {
			p.FeatureFingerprintMismatch(expected.Fingerprint, actual.Fingerprint)
		}
		return errors.New("feature_storage_blocked: source fingerprint mismatch")
	}
	return nil
}

type featurePayloadRow struct {
	Team       string          `json:"team_id"`
	Space      string          `json:"shared_space_id"`
	Generation int64           `json:"space_generation"`
	Owner      string          `json:"owner_profile_id"`
	Record     string          `json:"record_id"`
	Draft      string          `json:"draft_id"`
	Version    int64           `json:"version"`
	Kind       string          `json:"kind"`
	Body       json.RawMessage `json:"body"`
}

func copyFeaturePayload(ctx context.Context, tx *sql.Tx, table string, body []byte) error {
	var row featurePayloadRow
	if err := json.Unmarshal(body, &row); err != nil {
		return err
	}
	switch table {
	case "ontology_record_revisions":
		_, err := tx.ExecContext(ctx, `SELECT dense_mem_store_feature_revision($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7::jsonb)`, row.Team, row.Space, row.Generation, row.Record, row.Version, row.Kind, string(row.Body))
		return err
	case "ontology_draft_revisions":
		_, err := tx.ExecContext(ctx, `SELECT dense_mem_store_feature_draft($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6,$7::jsonb)`, row.Team, row.Space, row.Generation, row.Owner, row.Draft, row.Version, string(row.Body))
		return err
	default:
		return fmt.Errorf("feature storage: unsupported copy work unit %s", table)
	}
}

func verifyFeaturePayload(ctx context.Context, tx *sql.Tx, table string, body []byte) error {
	if table != "ontology_record_revisions" && table != "ontology_draft_revisions" && table != "ontology_draft_heads" {
		return nil
	}
	var row featurePayloadRow
	if err := json.Unmarshal(body, &row); err != nil {
		return err
	}
	var same bool
	if table == "ontology_record_revisions" {
		if err := tx.QueryRowContext(ctx, `SELECT count(*)=1 AND COALESCE(bool_and(kind=$6 AND body=$7::jsonb),false) FROM ontology_revision_payloads WHERE (team_id,shared_space_id,space_generation,record_id,version)=($1::uuid,$2::uuid,$3,$4::uuid,$5)`, row.Team, row.Space, row.Generation, row.Record, row.Version, row.Kind, string(row.Body)).Scan(&same); err != nil {
			return err
		}
	} else {
		if err := tx.QueryRowContext(ctx, `SELECT dense_mem_feature_draft_body($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6)=$7::jsonb`, row.Team, row.Space, row.Generation, row.Owner, row.Draft, row.Version, string(row.Body)).Scan(&same); err != nil {
			return err
		}
	}
	if !same {
		return errors.New("feature_storage_blocked: payload verification failed")
	}
	return nil
}
