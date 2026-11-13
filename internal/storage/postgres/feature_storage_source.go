package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// The inventory excludes only executor-owned state and destination payloads.
const featureSourceInventory = "all public application tables with primary keys; exclude goose_db_version,data_migration_runs,data_migration_checkpoints,ontology_draft_envelopes,ontology_{topic,membership,equivalence,override}_{revision,draft}_payloads"

type featureColumn struct{ Name, Type string }
type featureSource struct {
	Name         string
	Columns, Key []featureColumn
}
type featureRow struct {
	Cursor json.RawMessage
	Body   []byte
}

func featureSources(ctx context.Context, tx *sql.Tx) ([]featureSource, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.relname,a.attname,format_type(a.atttypid,a.atttypmod),COALESCE(array_position(i.indkey::smallint[],a.attnum),-1)
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
		LEFT JOIN pg_index i ON i.indrelid=c.oid AND i.indisprimary
		WHERE n.nspname='public' AND c.relkind IN ('r','p')
		AND c.relname NOT IN ('goose_db_version','data_migration_runs','data_migration_checkpoints','ontology_draft_envelopes',
		'ontology_topic_revision_payloads','ontology_membership_revision_payloads','ontology_equivalence_revision_payloads','ontology_override_revision_payloads',
		'ontology_topic_draft_payloads','ontology_membership_draft_payloads','ontology_equivalence_draft_payloads','ontology_override_draft_payloads')
		ORDER BY c.relname,a.attnum`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []featureSource
	positions := map[string]map[int]featureColumn{}
	for rows.Next() {
		var table string
		var column featureColumn
		var position int
		if err := rows.Scan(&table, &column.Name, &column.Type, &position); err != nil {
			return nil, err
		}
		if len(result) == 0 || result[len(result)-1].Name != table {
			result = append(result, featureSource{Name: table})
			positions[table] = map[int]featureColumn{}
		}
		result[len(result)-1].Columns = append(result[len(result)-1].Columns, column)
		if position >= 0 {
			positions[table][position] = column
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		keys := positions[result[index].Name]
		if len(keys) == 0 {
			return nil, fmt.Errorf("feature_storage_blocked: source table has no primary key: %s", result[index].Name)
		}
		// PostgreSQL int2vector arrays use zero-based subscripts.
		for position := 0; position < len(result[index].Columns); position++ {
			if key, ok := keys[position]; ok {
				result[index].Key = append(result[index].Key, key)
			}
		}
	}
	return result, nil
}

func featureBatchQuery(source featureSource, cursor json.RawMessage) (string, []any, error) {
	var keys, ordered, join []string
	for _, key := range source.Key {
		name := pq.QuoteIdentifier(key.Name)
		keys = append(keys, name)
		ordered = append(ordered, "source."+name)
		join = append(join, name)
	}
	if len(keys) == 0 {
		return "", nil, errors.New("feature_storage_blocked: source table has no primary key")
	}
	where := "true"
	var args []any
	if len(cursor) > 0 && string(cursor) != "[]" {
		decoder := json.NewDecoder(bytes.NewReader(cursor))
		decoder.UseNumber()
		if err := decoder.Decode(&args); err != nil {
			return "", nil, err
		}
		if len(args) != len(keys) {
			return "", nil, errors.New("feature storage: checkpoint key width changed")
		}
		var binds []string
		for index, key := range source.Key {
			binds = append(binds, fmt.Sprintf("$%d::%s", index+1, key.Type))
		}
		where = "(" + strings.Join(keys, ",") + ")>(" + strings.Join(binds, ",") + ")"
	}
	table := pq.QuoteIdentifier(source.Name)
	query := `WITH candidates AS MATERIALIZED (SELECT ` + strings.Join(keys, ",") + `,octet_length(to_jsonb(source)::text) AS payload_bytes FROM ` + table + ` source WHERE ` + where + ` ORDER BY ` + strings.Join(keys, ",") + ` LIMIT 100),
	bounded AS (SELECT *,sum(payload_bytes) OVER (ORDER BY ` + strings.Join(keys, ",") + `) AS cumulative_bytes,row_number() OVER (ORDER BY ` + strings.Join(keys, ",") + `) AS position FROM candidates)
	SELECT jsonb_build_array(` + strings.Join(ordered, ",") + `)::text,to_jsonb(source)::text FROM bounded JOIN ` + table + ` source USING(` + strings.Join(join, ",") + `)
	WHERE cumulative_bytes<=8388608 OR position=1 ORDER BY ` + strings.Join(ordered, ",")
	return query, args, nil
}

func readFeatureBatch(ctx context.Context, tx *sql.Tx, source featureSource, cursor json.RawMessage) ([]featureRow, error) {
	query, args, err := featureBatchQuery(source, cursor)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var result []featureRow
	size := 0
	for rows.Next() {
		var row featureRow
		if err := rows.Scan(&row.Cursor, &row.Body); err != nil {
			rows.Close()
			return nil, err
		}
		size += len(row.Body)
		if size > featureStorageBatchBytes {
			rows.Close()
			return nil, errors.New("feature_storage_blocked: source row exceeds batch byte bound")
		}
		result = append(result, row)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return result, nil
}

func advanceFeatureFingerprint(previous string, body []byte) string {
	hash := sha256.New()
	hash.Write([]byte(previous))
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(body)))
	hash.Write(length[:])
	hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))
}

func readFeatureCheckpoint(ctx context.Context, tx *sql.Tx, phase, work string) (featureCheckpoint, error) {
	var cp featureCheckpoint
	err := tx.QueryRowContext(ctx, `SELECT cursor,row_count,batch_count,fingerprint,complete FROM data_migration_checkpoints WHERE migration_version=$1 AND phase=$2 AND work_unit=$3`, FeatureStorageDataVersion, phase, work).Scan(&cp.Cursor, &cp.Rows, &cp.Batches, &cp.Fingerprint, &cp.Complete)
	if errors.Is(err, sql.ErrNoRows) {
		return featureCheckpoint{Cursor: json.RawMessage(`[]`)}, nil
	}
	return cp, err
}

func saveFeatureCheckpoint(ctx context.Context, tx *sql.Tx, phase, work string, cp featureCheckpoint) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO data_migration_checkpoints(migration_version,phase,work_unit,cursor,row_count,batch_count,fingerprint,complete)
		VALUES($1,$2,$3,$4::jsonb,$5,$6,$7,$8) ON CONFLICT(migration_version,phase,work_unit) DO UPDATE
		SET cursor=EXCLUDED.cursor,row_count=EXCLUDED.row_count,batch_count=EXCLUDED.batch_count,fingerprint=EXCLUDED.fingerprint,complete=EXCLUDED.complete,updated_at=clock_timestamp()`, FeatureStorageDataVersion, phase, work, string(cp.Cursor), cp.Rows, cp.Batches, cp.Fingerprint, cp.Complete)
	return err
}
