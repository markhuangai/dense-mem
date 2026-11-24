//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Historical fixtures write the persisted representation of their release before running the actual upgrade.
func seedHistoricalRecords(t *testing.T, f *ontologyFixture, key string, records ...ontology.Record) map[string]ontology.Record {
	t.Helper()
	catalog := map[string]ontology.Record{}
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.app, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`SELECT r.body FROM ontology_revision_records r JOIN ontology_record_heads h USING(team_id,shared_space_id,space_generation,record_id,version)
			WHERE h.team_id=? AND h.shared_space_id=? AND h.space_generation=?`, f.team, f.space, f.generation).Rows()
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			record, err := decodeRecord(raw)
			if err != nil {
				rows.Close()
				return err
			}
			catalog[record.ID] = record
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		var revision int64
		if err := tx.Raw(`INSERT INTO ontology_catalog_heads(team_id,shared_space_id,space_generation,revision) VALUES(?,?,?,1)
			ON CONFLICT(team_id,shared_space_id,space_generation) DO UPDATE SET revision=ontology_catalog_heads.revision+1 RETURNING revision`, f.team, f.space, f.generation).Row().Scan(&revision); err != nil {
			return err
		}
		id := uuid.NewString()
		if err := tx.Exec(`INSERT INTO ontology_publications(team_id,shared_space_id,space_generation,publication_id,revision,operation_key,request_hash,origin,reason,result)
			VALUES(?,?,?,?,?,?,?,'automatic','Historical migration fixture','{}')`, f.team, f.space, f.generation, id, revision, key, testHash(key)).Error; err != nil {
			return err
		}
		for _, record := range records {
			record.Version = catalog[record.ID].Version + 1
			for _, ref := range ontology.ReferenceIDs(record) {
				record.Dependencies = append(record.Dependencies, ontology.RevisionRef{ID: ref, Version: catalog[ref].Version})
			}
			record.Fingerprint, err = ontology.RecordFingerprintWithAcceptedSources(record, catalog)
			if err != nil {
				return err
			}
			body, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if err := tx.Exec(`SELECT dense_mem_insert_ontology_revision(?,?,?,?,?::jsonb)`, f.team, f.space, f.generation, id, string(body)).Error; err != nil {
				return err
			}
			names := ontology.DefinitionNames(record)
			if names == nil {
				names = []string{}
			}
			if err := tx.Exec(`INSERT INTO ontology_record_heads(team_id,shared_space_id,space_generation,record_id,version,kind,retired,names)
				VALUES(?,?,?,?,?,?,?,?::text[]) ON CONFLICT(team_id,shared_space_id,space_generation,record_id)
				DO UPDATE SET version=EXCLUDED.version,retired=EXCLUDED.retired,names=EXCLUDED.names`, f.team, f.space, f.generation, record.ID, record.Version, record.Kind, record.Retired, pq.Array(names)).Error; err != nil {
				return err
			}
			for _, ref := range record.Dependencies {
				if err := tx.Exec(`INSERT INTO ontology_revision_dependencies(team_id,shared_space_id,space_generation,record_id,record_version,dependency_id,dependency_version)
					VALUES(?,?,?,?,?,?,?)`, f.team, f.space, f.generation, record.ID, record.Version, ref.ID, ref.Version).Error; err != nil {
					return err
				}
			}
			for _, source := range record.Sources {
				if err := tx.Exec(`INSERT INTO ontology_source_dependencies(team_id,shared_space_id,space_generation,record_id,record_version,source_kind,source_id,source_version,fingerprint)
					VALUES(?,?,?,?,?,?,?,?,?)`, f.team, f.space, f.generation, record.ID, record.Version, source.Kind, source.ID, source.Version, source.Fingerprint).Error; err != nil {
					return err
				}
			}
			catalog[record.ID] = record
		}
		return nil
	}))
	return catalog
}
