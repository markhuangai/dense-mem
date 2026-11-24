//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func testTopicStorageUpgrade(t *testing.T) {
	ctx := t.Context()
	db, fixture := featureStorageFixture(t, 1)
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, 20261114010000))
	membership := uuid.NewString()
	var topic, publication string
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT record_id::text,publication_id::text FROM ontology_record_revisions WHERE team_id=$1 AND kind='topic' LIMIT 1`, fixture.team).Scan(&topic, &publication); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `SELECT dense_mem_insert_ontology_revision($1,$2,$3,$4,jsonb_build_object(
		    'id',$5::uuid,'version',1,'kind','assignment','retired',false,'fingerprint',$6::text,
		    'assignment',jsonb_build_object('source',jsonb_build_object('kind','evidence','id',$7::uuid,'version',1),'definition_id',$8::uuid)))`,
			fixture.team, fixture.space, fixture.generation, publication, membership, "sha256:"+strings.Repeat("a", 64), fixture.evidence, topic)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ontology_record_heads(team_id,shared_space_id,space_generation,record_id,version,kind,retired)
		    VALUES($1,$2,$3,$4,1,'assignment',false)`, fixture.team, fixture.space, fixture.generation, membership)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO recall_feedback_events(recall_id,team_id,profile_id,space_id,space_generation,result_refs,snapshot_metadata)
		    SELECT 'topic-upgrade-'||n,$1,$2,$3,$4,jsonb_build_array(jsonb_build_object('evidence_id',$5::uuid)),
		    CASE WHEN n%3=0 THEN '{}'::jsonb WHEN n%3=1 THEN '{"decision_stages":null}'::jsonb ELSE '{"decision_stages":[]}'::jsonb END
		    FROM generate_series(1,101) n`, fixture.team, fixture.owner, fixture.space, fixture.generation, fixture.evidence)
		return err
	}))
	before := featureStorageSnapshot(t, db, "evidence_fragments")
	interrupted, cancel := context.WithCancel(ctx)
	defer cancel()
	paused := false
	observed, progress := ObserveMigration(interrupted, func(s MigrationSnapshot) {
		if s.MigrationVersion == TopicStorageDataVersion && s.Phase == "copy" && s.WorkUnit == "recall_capture" && s.State == "batch_committed" {
			paused = true
			cancel()
		}
	})
	require.Error(t, NewMigratorWithDB(db).RunUp(observed))
	progress.Close()
	require.True(t, paused)

	verified, stopVerified := context.WithCancel(ctx)
	defer stopVerified()
	prefixVerified := false
	observed, progress = ObserveMigration(verified, func(s MigrationSnapshot) {
		if s.MigrationVersion == TopicStorageDataVersion && s.Phase == "verify" && s.WorkUnit == "recall_capture" && s.State == "batch_committed" {
			prefixVerified = true
			stopVerified()
		}
	})
	require.Error(t, NewMigratorWithDB(db).RunUp(observed))
	progress.Close()
	require.True(t, prefixVerified)
	updateCapture := func(body string) {
		require.NoError(t, execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE recall_rerank_outcomes SET decision_stages=$1::jsonb WHERE recall_id='topic-upgrade-1'`, body)
			return err
		}))
	}
	updateCapture("[]")
	require.ErrorContains(t, NewMigratorWithDB(db).RunUp(ctx), "recall_capture migrated prefix")
	updateCapture("null")

	require.NoError(t, NewMigratorWithDB(db).RunUp(ctx))
	require.NoError(t, ValidateStartupMigrationState(ctx, db, MigrationsDir()))
	require.Equal(t, before, featureStorageSnapshot(t, db, "evidence_fragments"))
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		var migrated int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM recall_feedback_events f JOIN recall_outcomes o USING(recall_id)
		    LEFT JOIN recall_rerank_outcomes r USING(recall_id)
		    WHERE f.recall_id LIKE 'topic-upgrade-%' AND o.method='legacy' AND o.result_refs#>>'{0,evidence_id}'=$1
		    AND NOT (f.snapshot_metadata ? 'decision_stages') AND
		    (CASE split_part(f.recall_id,'-',3)::int%3 WHEN 0 THEN r.recall_id IS NULL WHEN 1 THEN r.decision_stages='null' ELSE r.decision_stages='[]' END)`, fixture.evidence).Scan(&migrated); err != nil {
			return err
		}
		if migrated != 101 {
			return fmt.Errorf("expected 101 preserved captures, got %d", migrated)
		}
		var known bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM topic_record_heads WHERE team_id=$1 AND shared_space_id=$2 AND space_generation=$3 AND record_id=$4 AND evidence_id=$5 AND topic_id=$6 AND NOT retired)`, fixture.team, fixture.space, fixture.generation, membership, fixture.evidence, topic).Scan(&known); err != nil {
			return err
		}
		require.True(t, known)
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM topic_maintenance_community_work
			WHERE team_id=$1 AND shared_space_id=$2 AND space_generation=$3 AND topic_id=$4 AND topic_version=1 AND ready_at IS NOT NULL)`,
			fixture.team, fixture.space, fixture.generation, topic).Scan(&known); err != nil {
			return err
		}
		require.True(t, known, "populated upgrade must seed unfinished Community placement")
		_, err := tx.ExecContext(ctx, `DELETE FROM recall_feedback_events WHERE recall_id='topic-upgrade-1'`)
		return err
	}))
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM recall_outcomes WHERE recall_id='topic-upgrade-1')+
		   (SELECT count(*) FROM recall_rerank_outcomes WHERE recall_id='topic-upgrade-1')`).Scan(&count); err != nil {
			return err
		}
		require.Zero(t, count)
		return nil
	}))
}
