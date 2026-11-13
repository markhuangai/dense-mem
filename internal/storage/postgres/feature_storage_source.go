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
	Cursor   json.RawMessage
	Body     []byte
	Complete bool
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

func featureBatchQuery(source featureSource, cursor json.RawMessage) (string, []any, int64, error) {
	var keys, ordered []string
	for _, key := range source.Key {
		name := pq.QuoteIdentifier(key.Name)
		keys = append(keys, name)
		ordered = append(ordered, "source."+name)
	}
	if len(keys) == 0 {
		return "", nil, 0, errors.New("feature_storage_blocked: source table has no primary key")
	}
	where := "true"
	var args []any
	var offset int64
	if len(cursor) > 0 && string(cursor) != "[]" {
		decoder := json.NewDecoder(bytes.NewReader(cursor))
		decoder.UseNumber()
		if err := decoder.Decode(&args); err != nil {
			return "", nil, 0, err
		}
		if len(args) == len(keys)+1 {
			value, ok := args[len(keys)].(json.Number)
			if !ok {
				return "", nil, 0, errors.New("feature_storage_blocked: invalid checkpoint cursor")
			}
			var err error
			offset, err = value.Int64()
			if err != nil || offset <= 0 || offset%featureStorageBatchBytes != 0 {
				return "", nil, 0, errors.New("feature_storage_blocked: invalid checkpoint cursor")
			}
			args = args[:len(keys)]
		}
		if len(args) != len(keys) {
			return "", nil, 0, errors.New("feature storage: checkpoint key width changed")
		}
		var binds []string
		for index, key := range source.Key {
			binds = append(binds, fmt.Sprintf("$%d::%s", index+1, key.Type))
		}
		comparison := ">"
		if offset > 0 {
			comparison = ">="
		}
		where = "(" + strings.Join(keys, ",") + ")" + comparison + "(" + strings.Join(binds, ",") + ")"
	}
	table := pq.QuoteIdentifier(source.Name)
	query := `SELECT jsonb_build_array(` + strings.Join(ordered, ",") + `)::text FROM ` + table + ` source WHERE ` + where + ` ORDER BY ` + strings.Join(keys, ",") + ` LIMIT 100`
	return query, args, offset, nil
}

func readFeatureBatch(ctx context.Context, tx *sql.Tx, source featureSource, cursor json.RawMessage) ([]featureRow, error) {
	query, args, offset, err := featureBatchQuery(source, cursor)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		row, err := readFeatureRow(ctx, tx, source, cursor, offset, featureStorageBatchBytes)
		if err != nil {
			return nil, err
		}
		return []featureRow{row}, nil
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var keys []json.RawMessage
	for rows.Next() {
		var cursor []byte
		if err := rows.Scan(&cursor); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, json.RawMessage(cursor))
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var result []featureRow
	remaining := featureStorageBatchBytes
	for _, key := range keys {
		row, err := readFeatureRow(ctx, tx, source, key, 0, remaining)
		if err != nil {
			return nil, err
		}
		if !row.Complete && len(result) > 0 {
			break
		}
		result = append(result, row)
		remaining -= len(row.Body)
		if remaining == 0 {
			break
		}
	}
	return result, nil
}

func readFeatureRow(ctx context.Context, tx *sql.Tx, source featureSource, cursor json.RawMessage, offset int64, limit int) (featureRow, error) {
	var keys, values []string
	for index, key := range source.Key {
		keys = append(keys, "source."+pq.QuoteIdentifier(key.Name))
		values = append(values, fmt.Sprintf("($1::jsonb->>%d)::%s", index, key.Type))
	}
	query := `WITH payload AS MATERIALIZED (
	 SELECT jsonb_build_array(` + strings.Join(keys, ",") + `) AS row_key,convert_to(to_jsonb(source)::text,'UTF8') AS body
	 FROM ` + pq.QuoteIdentifier(source.Name) + ` source WHERE (` + strings.Join(keys, ",") + `)=(` + strings.Join(values, ",") + `)
	), chunk AS MATERIALIZED (
	 SELECT row_key,octet_length(body) AS payload_bytes,substring(body FROM ($2::int+1) FOR $3::int) AS body FROM payload
	)
	SELECT CASE WHEN $2::bigint+octet_length(body)<payload_bytes THEN row_key||jsonb_build_array($2::bigint+octet_length(body)) ELSE row_key END::text,
	 body,$2::bigint+octet_length(body)>=payload_bytes FROM chunk`
	var row featureRow
	var key []byte
	err := tx.QueryRowContext(ctx, query, string(cursor), offset, limit).Scan(&key, &row.Body, &row.Complete)
	if errors.Is(err, sql.ErrNoRows) {
		return row, errors.New("feature_storage_blocked: source fingerprint mismatch")
	}
	row.Cursor = key
	return row, err
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
