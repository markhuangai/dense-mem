package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/pressly/goose/v3"
)

const (
	topicHybridBackfillVersion int64 = 20261126010001
	topicHybridBackfillFile          = "20261126010001_topic_fts_backfill_and_indexes.sql"
	topicHybridBackfillGoName        = "v2_8/20261126010001_topic_fts_backfill_and_indexes.go"
)

func init() {
	goose.AddNamedMigrationNoTxContext(topicHybridBackfillGoName, topicHybridBackfillUp, topicHybridBackfillDown)
}

func topicHybridBackfillDown(context.Context, *sql.DB) error {
	return errors.New("Topic hybrid backfill requires forward recovery")
}

func topicHybridBackfillUp(ctx context.Context, db *sql.DB) (resultErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, conn.Close()) }()
	var previousLockTimeout string
	if err := conn.QueryRowContext(ctx, "SHOW lock_timeout").Scan(&previousLockTimeout); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var cleanupErr error
		for _, statement := range []string{"RESET app.tx_mode", "RESET lock_timeout"} {
			_, err := conn.ExecContext(cleanupCtx, statement)
			cleanupErr = errors.Join(cleanupErr, err)
		}
		if cleanupErr == nil {
			_, cleanupErr = conn.ExecContext(cleanupCtx, `SELECT set_config('lock_timeout',$1,false)`, previousLockTimeout)
		}
		if cleanupErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		resultErr = errors.Join(resultErr, cleanupErr)
	}()
	for _, statement := range []string{"SET app.tx_mode='migration'", "SET lock_timeout='30s'"} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for _, statement := range []string{topicHybridBackfillTopicsSQL, topicHybridBackfillEquivalenceSQL} {
		for {
			result, err := conn.ExecContext(ctx, statement)
			if err != nil {
				return fmt.Errorf("Topic hybrid backfill: %w", err)
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count == 0 {
				break
			}
		}
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE topic_maintenance_sources ALTER COLUMN equivalence_status SET DEFAULT 'pending'`); err != nil {
		return err
	}
	indexes := []struct{ name, ddl string }{
		{"topics_fts_idx", `CREATE INDEX CONCURRENTLY IF NOT EXISTS topics_fts_idx ON topics USING gin(search_tsv)`},
		{"topic_embeddings_pending_idx", `CREATE INDEX CONCURRENTLY IF NOT EXISTS topic_embeddings_pending_idx ON topics(team_id,shared_space_id,space_generation,topic_id) WHERE NOT retired`},
		{"topic_embeddings_consolidation_pending_idx", `CREATE INDEX CONCURRENTLY IF NOT EXISTS topic_embeddings_consolidation_pending_idx ON topic_embeddings(team_id,shared_space_id,space_generation,topic_id) WHERE consolidation_pending`},
		{"topic_maintenance_sources_equiv_pending_idx", `CREATE INDEX CONCURRENTLY IF NOT EXISTS topic_maintenance_sources_equiv_pending_idx ON topic_maintenance_sources(team_id,shared_space_id,space_generation,source_id) WHERE eligible AND equivalence_status IN ('pending','failed')`},
	}
	for _, index := range indexes {
		var valid bool
		err := conn.QueryRowContext(ctx, `SELECT state.indisvalid FROM pg_index AS state
			JOIN pg_class AS candidate ON candidate.oid=state.indexrelid
			JOIN pg_namespace AS namespace ON namespace.oid=candidate.relnamespace
			WHERE namespace.nspname='public' AND candidate.relname=$1`, index.name).Scan(&valid)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && !valid {
			if _, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY public."+index.name); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, index.ddl); err != nil {
			return fmt.Errorf("Topic hybrid index %s: %w", index.name, err)
		}
	}
	return nil
}

const topicHybridBackfillTopicsSQL = `WITH page AS MATERIALIZED (
 SELECT team_id,shared_space_id,space_generation,topic_id FROM topics
 WHERE search_tsv IS NULL ORDER BY team_id,shared_space_id,space_generation,topic_id LIMIT 1000
)
UPDATE topics AS target SET
 search_tsv=to_tsvector('simple',concat_ws(' ',target.topic_key,target.label,target.description,array_to_string(target.aliases,' '))),
 material_definition_version=target.definition_version,
 manager_authored=EXISTS (
   SELECT 1 FROM topic_record_revisions AS revision JOIN topic_publications AS publication
   USING(team_id,shared_space_id,space_generation,publication_id)
   WHERE (revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id)=
         (target.team_id,target.shared_space_id,target.space_generation,target.topic_id)
     AND revision.kind='topic' AND publication.origin IN ('manager','rollback'))
FROM page WHERE (target.team_id,target.shared_space_id,target.space_generation,target.topic_id)=
                (page.team_id,page.shared_space_id,page.space_generation,page.topic_id)`

const topicHybridBackfillEquivalenceSQL = `WITH page AS MATERIALIZED (
 SELECT team_id,shared_space_id,space_generation,source_kind,source_id FROM topic_maintenance_sources
 WHERE equivalence_status='uninitialized'
 ORDER BY team_id,shared_space_id,space_generation,source_kind,source_id LIMIT 1000
)
UPDATE topic_maintenance_sources AS target SET
 equivalence_status=CASE WHEN target.status IN ('organized','uncategorized') THEN 'completed'
                         WHEN target.status='unavailable' THEN 'unavailable' ELSE 'pending' END,
 equivalence_fingerprint=target.fingerprint
FROM page WHERE (target.team_id,target.shared_space_id,target.space_generation,target.source_kind,target.source_id)=
                (page.team_id,page.shared_space_id,page.space_generation,page.source_kind,page.source_id)`
