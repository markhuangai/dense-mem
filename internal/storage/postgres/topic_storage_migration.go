package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"

	"github.com/pressly/goose/v3"
)

const (
	TopicStorageLayout               = "topic-storage-v1"
	TopicStorageDataVersion    int64 = 20261124010001
	topicStorageCutoverVersion int64 = 20261124010002
	topicStorageDataFile             = "20261124010001_topic_storage_data.sql"
	topicStorageInventory            = "topic-storage-v1:membership-heads/recall-feedback/topic-marker-dispositions/community-work"
)

type topicCopyUnit struct {
	name, page, copy, mismatch, finish, verifiedMismatch string
}

var topicCopyUnits = []topicCopyUnit{
	{
		name: "membership_heads",
		page: `CREATE TEMP TABLE topic_copy_page ON COMMIT DROP AS
		WITH heads AS MATERIALIZED (
		 SELECT team_id,shared_space_id,space_generation,record_id,version
		 FROM ontology_record_heads WHERE kind='assignment'
		   AND (team_id,shared_space_id,space_generation,record_id)>
		       (COALESCE(($1::jsonb->>0)::uuid,'00000000-0000-0000-0000-000000000000'),COALESCE(($1::jsonb->>1)::uuid,'00000000-0000-0000-0000-000000000000'),COALESCE(($1::jsonb->>2)::bigint,0),COALESCE(($1::jsonb->>3)::uuid,'00000000-0000-0000-0000-000000000000'))
		 ORDER BY team_id,shared_space_id,space_generation,record_id LIMIT 100)
		SELECT h.*, (p.body#>>'{assignment,source,id}')::uuid AS evidence_id,
		       (p.body#>>'{assignment,definition_id}')::uuid AS topic_id,
		       jsonb_build_array(h.team_id,h.shared_space_id,h.space_generation,h.record_id) AS next_cursor,
		       row_number() OVER (ORDER BY h.team_id,h.shared_space_id,h.space_generation,h.record_id) AS ordinal,
		       sha256(convert_to(jsonb_build_array(h.team_id,h.shared_space_id,h.space_generation,h.record_id,h.version,p.body)::text,'UTF8')) AS row_hash
		FROM heads h LEFT JOIN ontology_membership_revision_payloads p USING(team_id,shared_space_id,space_generation,record_id,version)`,
		copy: `UPDATE ontology_record_heads h SET evidence_id=p.evidence_id,topic_id=p.topic_id FROM topic_copy_page p
		     WHERE (h.team_id,h.shared_space_id,h.space_generation,h.record_id,h.version)=(p.team_id,p.shared_space_id,p.space_generation,p.record_id,p.version)`,
		mismatch: `SELECT EXISTS(SELECT 1 FROM topic_copy_page p LEFT JOIN ontology_record_heads h USING(team_id,shared_space_id,space_generation,record_id,version)
		     WHERE p.evidence_id IS NULL OR p.topic_id IS NULL OR (p.evidence_id,p.topic_id) IS DISTINCT FROM (h.evidence_id,h.topic_id))`,
	},
	{
		name: "recall_capture",
		page: `CREATE TEMP TABLE topic_copy_page ON COMMIT DROP AS
		WITH raw AS MATERIALIZED (
		 SELECT e.recall_id,e.snapshot_state,e.result_refs,
		        CASE WHEN e.recall_id<=$2::jsonb->>0 THEN r.recall_id IS NOT NULL
		             ELSE e.snapshot_metadata ? 'decision_stages' END AS has_stages,
		        CASE WHEN e.recall_id<=$2::jsonb->>0 THEN r.decision_stages
		             ELSE e.snapshot_metadata->'decision_stages' END AS stages
		 FROM recall_feedback_events e LEFT JOIN recall_rerank_outcomes r USING(recall_id)
		 WHERE ($1::jsonb->>0 IS NULL OR e.recall_id>$1::jsonb->>0)
		 ORDER BY e.recall_id LIMIT 100),
		sized AS (SELECT raw.*,row_number() OVER (ORDER BY recall_id) AS ordinal,
		    sum(pg_column_size(result_refs)+COALESCE(pg_column_size(stages),0)) OVER (ORDER BY recall_id) AS bytes FROM raw)
		SELECT *,jsonb_build_array(recall_id) AS next_cursor,
		    (snapshot_state='captured' OR result_refs<>'[]'::jsonb OR has_stages) AS captured,
		    sha256(convert_to(jsonb_build_array(recall_id,snapshot_state,result_refs,has_stages,stages)::text,'UTF8')) AS row_hash
		FROM sized WHERE ordinal=1 OR bytes<=8388608`,
		copy: `WITH captures AS (
		    INSERT INTO recall_outcomes(recall_id,result_refs,method)
		    SELECT recall_id,result_refs,'legacy' FROM topic_copy_page WHERE captured
		    ON CONFLICT(recall_id) DO NOTHING RETURNING recall_id)
		    INSERT INTO recall_rerank_outcomes(recall_id,decision_stages,method)
		    SELECT p.recall_id,p.stages,'legacy' FROM topic_copy_page p
		    LEFT JOIN captures c ON c.recall_id=p.recall_id WHERE p.has_stages ON CONFLICT(recall_id) DO NOTHING`,
		mismatch: `SELECT EXISTS(SELECT 1 FROM topic_copy_page p LEFT JOIN recall_outcomes o USING(recall_id)
		    LEFT JOIN recall_rerank_outcomes r USING(recall_id) WHERE p.captured IS DISTINCT FROM (o.recall_id IS NOT NULL)
		      OR (p.captured AND p.result_refs IS DISTINCT FROM o.result_refs)
		      OR p.has_stages IS DISTINCT FROM (r.recall_id IS NOT NULL) OR p.stages IS DISTINCT FROM r.decision_stages)`,
		finish: `UPDATE recall_feedback_events e SET snapshot_metadata=e.snapshot_metadata-'decision_stages'
		    FROM topic_copy_page p WHERE e.recall_id=p.recall_id AND p.has_stages`,
		verifiedMismatch: `SELECT EXISTS(SELECT 1 FROM topic_copy_page p JOIN recall_feedback_events e USING(recall_id)
			WHERE e.snapshot_metadata ? 'decision_stages')`,
	},
	{
		name: "community_work",
		page: `CREATE TEMP TABLE topic_copy_page ON COMMIT DROP AS
		WITH heads AS MATERIALIZED (
		 SELECT h.team_id,h.shared_space_id,h.space_generation,h.record_id,h.version
		 FROM ontology_record_heads h JOIN memory_spaces s ON (s.team_id,s.id,s.generation)=(h.team_id,h.shared_space_id,h.space_generation)
		 WHERE h.kind='topic' AND NOT h.retired AND s.lifecycle_state='active'
		   AND (h.team_id,h.shared_space_id,h.space_generation,h.record_id)>
		       (COALESCE(($1::jsonb->>0)::uuid,'00000000-0000-0000-0000-000000000000'),COALESCE(($1::jsonb->>1)::uuid,'00000000-0000-0000-0000-000000000000'),COALESCE(($1::jsonb->>2)::bigint,0),COALESCE(($1::jsonb->>3)::uuid,'00000000-0000-0000-0000-000000000000'))
		 ORDER BY h.team_id,h.shared_space_id,h.space_generation,h.record_id LIMIT 100)
		SELECT h.*,jsonb_build_array(h.team_id,h.shared_space_id,h.space_generation,h.record_id) AS next_cursor,
		       row_number() OVER (ORDER BY h.team_id,h.shared_space_id,h.space_generation,h.record_id) AS ordinal,
		       sha256(convert_to(to_jsonb(h)::text,'UTF8')) AS row_hash
		FROM heads h`,
		copy: `INSERT INTO topic_maintenance_community_work(team_id,shared_space_id,space_generation,topic_id,topic_version,work_id,ready_at)
		       SELECT team_id,shared_space_id,space_generation,record_id,version,record_id,'-infinity' FROM topic_copy_page
		       ON CONFLICT(team_id,shared_space_id,space_generation,topic_id) DO NOTHING`,
		mismatch: `SELECT EXISTS(SELECT 1 FROM topic_copy_page p LEFT JOIN topic_maintenance_community_work w
		     ON (w.team_id,w.shared_space_id,w.space_generation,w.topic_id)=(p.team_id,p.shared_space_id,p.space_generation,p.record_id)
		     WHERE w.topic_version IS DISTINCT FROM p.version OR w.work_id IS DISTINCT FROM p.record_id
		       OR w.ready_at IS DISTINCT FROM '-infinity'::timestamptz
		       OR w.parent_topic_id IS NOT NULL OR w.parent_topic_version IS NOT NULL)`,
	},
	topicMarkerUnit(true),
	topicMarkerUnit(false),
}

// Old definition markers include manager overrides and retired vocabulary. Only
// a still-active Topic has a successor in the new revision-scoped matching queue.
func topicMarkerUnit(keep bool) topicCopyUnit {
	name := "retired_definition_markers"
	reason := "retired_vocabulary"
	match := "NOT EXISTS"
	action := `DELETE FROM ontology_maintenance_markers m USING archived p`
	resultMismatch := `EXISTS(SELECT 1 FROM ontology_maintenance_markers m WHERE
		(m.team_id,m.shared_space_id,m.space_generation,m.anchor_id)=(p.team_id,p.shared_space_id,p.space_generation,p.anchor_id)
		AND m.anchor_kind IN ('ontology_record_heads','topic_record_heads'))`
	if keep {
		name = "translated_topic_markers"
		reason = "translated_active_topic"
		match = "EXISTS"
		action = `UPDATE ontology_maintenance_markers m SET anchor_kind='topic_record_heads' FROM archived p`
		resultMismatch = `NOT EXISTS(SELECT 1 FROM ontology_maintenance_markers m WHERE
			(m.team_id,m.shared_space_id,m.space_generation,m.anchor_id)=(p.team_id,p.shared_space_id,p.space_generation,p.anchor_id)
			AND m.anchor_kind='topic_record_heads' AND to_jsonb(m)-'anchor_kind'=to_jsonb(p)-ARRAY['anchor_kind','archived','next_cursor','ordinal','row_hash'])`
	}
	return topicCopyUnit{name: name, page: `CREATE TEMP TABLE topic_copy_page ON COMMIT DROP AS
	    WITH original AS (
	        SELECT m.*,false AS archived FROM ontology_maintenance_markers m WHERE m.anchor_kind='ontology_record_heads' AND m.target_kind='definition'
	        AND (m.team_id,m.shared_space_id,m.space_generation,m.anchor_id)>(
	            COALESCE(($1::jsonb->>0)::uuid,'00000000-0000-0000-0000-000000000000'),COALESCE(($1::jsonb->>1)::uuid,'00000000-0000-0000-0000-000000000000'),
	            COALESCE(($1::jsonb->>2)::bigint,0),COALESCE($1::jsonb->>3,''))
	        AND ` + match + ` (SELECT 1 FROM ontology_record_heads h WHERE h.team_id=m.team_id AND h.shared_space_id=m.shared_space_id
	            AND h.space_generation=m.space_generation AND h.record_id=m.target_id::uuid AND h.kind='topic' AND NOT h.retired)
	    ), archived AS (
	        SELECT restored.*,true AS archived
	        FROM historical_topic_marker_dispositions a
	        CROSS JOIN LATERAL jsonb_populate_record(NULL::ontology_maintenance_markers,a.original_marker) restored
	        WHERE a.reason='` + reason + `' AND (a.team_id,a.shared_space_id,a.space_generation,a.anchor_id)>(
	            COALESCE(($1::jsonb->>0)::uuid,'00000000-0000-0000-0000-000000000000'),COALESCE(($1::jsonb->>1)::uuid,'00000000-0000-0000-0000-000000000000'),
	            COALESCE(($1::jsonb->>2)::bigint,0),COALESCE($1::jsonb->>3,''))
	    ), page AS MATERIALIZED (
	        SELECT * FROM (SELECT * FROM original UNION ALL SELECT * FROM archived) all_rows
	        ORDER BY team_id,shared_space_id,space_generation,anchor_id LIMIT 100
	    ) SELECT p.*,jsonb_build_array(team_id,shared_space_id,space_generation,anchor_id) AS next_cursor,
	         row_number() OVER (ORDER BY team_id,shared_space_id,space_generation,anchor_id) AS ordinal,
	         sha256(convert_to((to_jsonb(p)-'archived')::text,'UTF8')) AS row_hash FROM page p`,
		finish: `WITH archived AS (
			INSERT INTO historical_topic_marker_dispositions(team_id,shared_space_id,space_generation,anchor_id,reason,original_marker)
			SELECT team_id,shared_space_id,space_generation,anchor_id,'` + reason + `',to_jsonb(p)-ARRAY['archived','next_cursor','ordinal','row_hash']
			FROM topic_copy_page p RETURNING team_id,shared_space_id,space_generation,anchor_id
		) ` + action + ` WHERE (m.team_id,m.shared_space_id,m.space_generation,m.anchor_id)=
		    (p.team_id,p.shared_space_id,p.space_generation,p.anchor_id) AND m.anchor_kind='ontology_record_heads'`,
		verifiedMismatch: `SELECT EXISTS(SELECT 1 FROM topic_copy_page p WHERE NOT p.archived OR ` + resultMismatch + `)`}
}

func topicStorageDataMigration() *goose.Migration {
	return goose.NewGoMigration(TopicStorageDataVersion,
		&goose.GoFunc{Mode: goose.TransactionDisabled, RunDB: copyTopicStorage},
		&goose.GoFunc{Mode: goose.TransactionDisabled, RunDB: func(context.Context, *sql.DB) error {
			return errors.New("topic storage requires forward recovery")
		}})
}

func copyTopicStorage(ctx context.Context, db *sql.DB) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, DiscardAdvisoryLockConnection(conn)) }()
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(746,20261124)`); err != nil {
		return err
	}
	migration := &featureMigration{conn: conn, progress: MigrationProgressFromContext(ctx)}
	if migration.progress != nil {
		migration.progress.SetMigrationVersion(TopicStorageDataVersion)
	}
	if err = validateTopicStorageResume(ctx, migration); err != nil {
		if migration.progress != nil {
			migration.progress.FeatureStorageDatabaseFailure(err)
			migration.progress.FeatureStorageFailure("failed", "topic_storage_resume")
		}
		return err
	}
	for _, phase := range []string{"copy", "verify"} {
		for _, unit := range topicCopyUnits {
			finished := false
			for !finished {
				var checkpoint featureCheckpoint
				err = migration.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
					var pageErr error
					finished, pageErr = copyTopicPage(ctx, tx, unit, phase, &checkpoint)
					return pageErr
				})
				if err != nil {
					if migration.progress != nil {
						migration.progress.FeatureStorageDatabaseFailure(err)
						migration.progress.FeatureStorageFailure("failed", "topic_storage_"+phase)
					}
					return fmt.Errorf("topic storage %s %s: %w", phase, unit.name, err)
				}
				if migration.progress != nil {
					state := "batch_committed"
					if finished {
						state = "completed"
					}
					migration.progress.FeatureStorageCheckpoint(phase, unit.name, state, checkpoint.Rows, checkpoint.Batches, checkpoint.Fingerprint, "", "", "")
				}
			}
		}
	}
	// The write fence is active. Build under SHARE locks, outside the short rename transaction.
	for _, statement := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS topic_active_membership_pair_idx ON ontology_record_heads(team_id,shared_space_id,space_generation,evidence_id,topic_id) WHERE kind='assignment' AND NOT retired`,
		`CREATE INDEX IF NOT EXISTS topic_membership_peers_idx ON ontology_record_heads(team_id,shared_space_id,space_generation,topic_id,evidence_id) WHERE kind='assignment' AND NOT retired`,
	} {
		if err = runTopicIndexDDL(ctx, conn, statement); err != nil {
			return err
		}
	}
	return runTopicIndexDDL(ctx, conn, `ALTER TABLE ontology_record_heads VALIDATE CONSTRAINT topic_membership_pair_required;
		UPDATE data_migration_runs SET phase='ready',schema_fingerprint=dense_mem_feature_catalog_fingerprint(),
		   updated_at=clock_timestamp() WHERE migration_version=20261124010001 AND phase IN ('copy','ready')`)
}

type topicStorageCheckpoint struct {
	Cursor, Fingerprint string
	Rows                int64
}

func validateTopicStorageResume(ctx context.Context, migration *featureMigration) error {
	var compatible bool
	if err := migration.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM data_migration_runs WHERE migration_version=$1 AND executor_version=$2
			   AND inventory_fingerprint=$3 AND schema_fingerprint=dense_mem_feature_catalog_fingerprint()
			   AND phase IN ('copy','ready'))`, TopicStorageDataVersion, TopicStorageLayout,
			fmt.Sprintf("%x", sha256.Sum256([]byte(topicStorageInventory)))).Scan(&compatible)
	}); err != nil {
		return err
	}
	if !compatible {
		return errors.New("topic storage migration executor, inventory, or schema no longer matches its checkpoint")
	}
	for _, unit := range topicCopyUnits {
		var copied, verified topicStorageCheckpoint
		if err := migration.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
			for i, phase := range []string{"copy", "verify"} {
				p := []*topicStorageCheckpoint{&copied, &verified}[i]
				err := tx.QueryRowContext(ctx, `SELECT cursor::text,fingerprint,row_count FROM data_migration_checkpoints
				    WHERE migration_version=$1 AND phase=$2 AND work_unit=$3`, TopicStorageDataVersion, phase, unit.name).Scan(&p.Cursor, &p.Fingerprint, &p.Rows)
				if errors.Is(err, sql.ErrNoRows) {
					p.Cursor = "null"
				} else if err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if verified.Rows > copied.Rows {
			return fmt.Errorf("topic storage %s verification exceeds its copied prefix", unit.name)
		}
		for i, target := range []topicStorageCheckpoint{copied, verified} {
			cursor, digest, rows := "null", "", int64(0)
			for rows < target.Rows {
				err := migration.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
					if err := loadTopicPage(ctx, tx, unit, cursor, verified.Cursor); err != nil {
						return err
					}
					count, next, nextDigest, err := topicPageFingerprint(ctx, tx, cursor, digest)
					if err != nil {
						return err
					}
					if count == 0 || rows+count > target.Rows {
						return errors.New("migrated row count changed")
					}
					cursor, digest, rows = next, nextDigest, rows+count
					if err := verifyTopicPage(ctx, tx, unit.mismatch); err != nil {
						return err
					}
					if i == 1 {
						return verifyTopicPage(ctx, tx, unit.verifiedMismatch)
					}
					return nil
				})
				if err != nil {
					return fmt.Errorf("topic storage %s migrated prefix: %w", unit.name, err)
				}
			}
			if target.Rows > 0 && (cursor != target.Cursor || digest != target.Fingerprint) {
				return fmt.Errorf("topic storage %s migrated prefix fingerprint changed", unit.name)
			}
		}
	}
	return nil
}

func copyTopicPage(ctx context.Context, tx *sql.Tx, unit topicCopyUnit, phase string, checkpoint *featureCheckpoint) (bool, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO data_migration_checkpoints(migration_version,phase,work_unit,fingerprint)
	   VALUES($1,$2,$3,'') ON CONFLICT DO NOTHING`, TopicStorageDataVersion, phase, unit.name); err != nil {
		return false, err
	}
	var cursor, prior string
	var complete bool
	if err := tx.QueryRowContext(ctx, `SELECT cursor::text,fingerprint,complete,row_count,batch_count FROM data_migration_checkpoints
	    WHERE migration_version=$1 AND phase=$2 AND work_unit=$3 FOR UPDATE`, TopicStorageDataVersion, phase, unit.name).Scan(&cursor, &prior, &complete, &checkpoint.Rows, &checkpoint.Batches); err != nil || complete {
		checkpoint.Fingerprint = prior
		return complete, err
	}
	if err := loadTopicPage(ctx, tx, unit, cursor, "null"); err != nil {
		return false, err
	}
	count, next, digest, err := topicPageFingerprint(ctx, tx, cursor, prior)
	if err != nil {
		return false, err
	}
	if count > 0 {
		if phase == "copy" && unit.copy != "" {
			if _, err := tx.ExecContext(ctx, unit.copy); err != nil {
				return false, err
			}
		}
		if err := verifyTopicPage(ctx, tx, unit.mismatch); err != nil {
			return false, err
		}
		if phase == "verify" && unit.finish != "" {
			if _, err := tx.ExecContext(ctx, unit.finish); err != nil {
				return false, err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE data_migration_checkpoints SET cursor=$4::jsonb,fingerprint=$5,row_count=row_count+$6,
	    batch_count=batch_count+CASE WHEN $6::bigint>0 THEN 1 ELSE 0 END,complete=$7,updated_at=clock_timestamp()
	    WHERE migration_version=$1 AND phase=$2 AND work_unit=$3`, TopicStorageDataVersion, phase, unit.name, next, digest, count, count == 0)
	checkpoint.Fingerprint = digest
	checkpoint.Rows += count
	if count != 0 {
		checkpoint.Batches++
	}
	return count == 0, err
}

func loadTopicPage(ctx context.Context, tx *sql.Tx, unit topicCopyUnit, cursor, verifiedCursor string) error {
	args := []any{cursor}
	if unit.name == "recall_capture" {
		args = append(args, verifiedCursor)
	}
	_, err := tx.ExecContext(ctx, unit.page, args...)
	return err
}

func topicPageFingerprint(ctx context.Context, tx *sql.Tx, cursor, previous string) (count int64, next, digest string, err error) {
	err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE((array_agg(next_cursor ORDER BY ordinal DESC))[1]::text,$1),
		CASE WHEN count(*)=0 THEN $2::text ELSE encode(sha256(convert_to($2::text||string_agg(encode(row_hash,'hex'),'' ORDER BY ordinal),'UTF8')),'hex') END
		FROM topic_copy_page`, cursor, previous).Scan(&count, &next, &digest)
	return
}

func verifyTopicPage(ctx context.Context, tx *sql.Tx, mismatch string) error {
	if mismatch == "" {
		return nil
	}
	var differs bool
	if err := tx.QueryRowContext(ctx, mismatch).Scan(&differs); err != nil {
		return err
	}
	if differs {
		return errors.New("converted topic storage payload differs from its source")
	}
	return nil
}

func runTopicIndexDDL(ctx context.Context, conn *sql.Conn, statement string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.tx_mode','migration',true),set_config('lock_timeout','5s',true)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("build topic membership access paths: %w", err)
	}
	return tx.Commit()
}
